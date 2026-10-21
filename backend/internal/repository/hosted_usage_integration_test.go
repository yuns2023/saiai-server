//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestHostedUsagePersistsReferenceAndDeduplicatesKeyLimits(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	g := &service.Group{Name: "hosted-" + uuid.NewString(), Platform: service.PlatformOpenAI,
		IsExclusive: true, HostedUsageEnabled: true, SubscriptionType: service.SubscriptionTypeStandard, Status: service.StatusActive}
	groups := NewGroupRepository(client, integrationDB)
	require.NoError(t, groups.Create(ctx, g))
	loaded, err := groups.GetByIDLite(ctx, g.ID)
	require.NoError(t, err)
	require.True(t, loaded.UsesHostedUsage())
	user := mustCreateUser(t, client, &service.User{Email: "hosted-" + uuid.NewString() + "@example.invalid", PasswordHash: "TEST_ONLY", Balance: 0})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "TEST_ONLY_" + uuid.NewString(), Name: "hosted", Quota: .08})
	_, err = integrationDB.ExecContext(ctx, "UPDATE api_keys SET rate_limit_5h=.08, group_id=$1 WHERE id=$2", g.ID, key.ID)
	require.NoError(t, err)
	key.GroupID, key.Group, key.RateLimit5h = &g.ID, loaded, .08
	account := mustCreateAccount(t, client, &service.Account{Name: "hosted-" + uuid.NewString(), Type: service.AccountTypeOAuth, Platform: service.PlatformOpenAI})
	keys := NewAPIKeyRepository(client, integrationDB)
	cache := service.NewBillingCacheService(nil, nil, nil, keys, &config.Config{})
	t.Cleanup(cache.Stop)
	logs := NewUsageLogRepository(client, integrationDB)
	svc := service.NewOpenAIGatewayService(nil, logs, NewUsageBillingRepository(client, integrationDB), nil, nil, nil, nil, &config.Config{}, nil, nil, nil, nil, cache, nil, &service.DeferredService{}, nil)
	input := &service.OpenAIChatGPTTurnUsageInput{RequestID: "TEST_ONLY_" + uuid.NewString(), BasePriceUSD: .05,
		APIKey: key, User: user, Account: account, APIKeyService: nativeChatQuotaUpdater{}}
	require.NoError(t, cache.CheckBillingEligibility(ctx, user, key, loaded, nil), "an empty wallet must not block explicitly free hosted traffic")
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- svc.RecordChatGPTTurnUsage(ctx, input) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.NoError(t, svc.RecordChatGPTTurnUsage(ctx, input), "late replay must remain idempotent")
	var balance, quota, window float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", user.ID).Scan(&balance))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used,usage_5h FROM api_keys WHERE id=$1", key.ID).Scan(&quota, &window))
	require.Zero(t, balance)
	require.InDelta(t, .05, quota, 1e-8)
	require.InDelta(t, .05, window, 1e-8)
	var rowID int64
	require.Eventually(t, func() bool {
		return integrationDB.QueryRowContext(ctx, "SELECT id FROM usage_logs WHERE api_key_id=$1 AND request_id=$2", key.ID, input.RequestID).Scan(&rowID) == nil
	}, 3*time.Second, 10*time.Millisecond)
	log, err := logs.GetByID(ctx, rowID)
	require.NoError(t, err)
	require.Equal(t, .05, *log.MeteredCost)
	require.Zero(t, log.ActualCost)
	require.Zero(t, *log.AccountRateMultiplier)
	stats, err := logs.GetBatchAPIKeyUsageStats(ctx, []int64{key.ID}, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.InDelta(t, .05, stats[key.ID].TotalUsageCost, 1e-8)
	require.Zero(t, stats[key.ID].TotalActualCost)
	second := *input
	second.RequestID = "TEST_ONLY_" + uuid.NewString()
	require.NoError(t, svc.RecordChatGPTTurnUsage(ctx, &second))
	require.ErrorIs(t, cache.CheckBillingEligibility(ctx, user, key, loaded, nil), service.ErrAPIKeyRateLimit5hExceeded)

	// The same upstream account serving a paid group still contributes its
	// ordinary wallet charge and platform account cost.
	paidGroup := &service.Group{Name: "paid-" + uuid.NewString(), Platform: service.PlatformOpenAI,
		RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeStandard, Status: service.StatusActive}
	require.NoError(t, groups.Create(ctx, paidGroup))
	paidUser := mustCreateUser(t, client, &service.User{Email: "paid-" + uuid.NewString() + "@example.invalid", PasswordHash: "TEST_ONLY", Balance: 1})
	paidKey := mustCreateApiKey(t, client, &service.APIKey{UserID: paidUser.ID, Key: "TEST_ONLY_" + uuid.NewString(), Name: "paid"})
	paidKey.GroupID, paidKey.Group = &paidGroup.ID, paidGroup
	paid := *input
	paid.RequestID, paid.User, paid.APIKey = "TEST_ONLY_"+uuid.NewString(), paidUser, paidKey
	require.NoError(t, svc.RecordChatGPTTurnUsage(ctx, &paid))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", paidUser.ID).Scan(&balance))
	require.InDelta(t, .95, balance, 1e-8)
	var platformCost float64
	require.Eventually(t, func() bool {
		if err := integrationDB.QueryRowContext(ctx, "SELECT COALESCE(SUM(total_cost * COALESCE(account_rate_multiplier,1)),0) FROM usage_logs WHERE account_id=$1", account.ID).Scan(&platformCost); err != nil {
			return false
		}
		return platformCost == .05
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT rate_multiplier FROM accounts WHERE id=$1", account.ID).Scan(&platformCost))
	require.Equal(t, 1.0, platformCost)

	// An invalid partial group edit cannot turn hosted mode into paid mode.
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET rate_multiplier=1 WHERE id=$1", g.ID)
	require.Error(t, err)
}
