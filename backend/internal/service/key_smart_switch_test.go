//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

type smartKeyRepo struct {
	APIKeyRepository
	key *APIKey
}

func (r smartKeyRepo) GetByID(context.Context, int64) (*APIKey, error) { return r.key, nil }

type smartUserRepo struct {
	UserRepository
	user *User
}

func (r smartUserRepo) GetByID(context.Context, int64) (*User, error) { return r.user, nil }

type smartGroupRepo struct {
	GroupRepository
	groups map[int64]*Group
}

func (r smartGroupRepo) GetByID(_ context.Context, id int64) (*Group, error) {
	g := r.groups[id]
	if g == nil {
		return nil, ErrGroupNotFound
	}
	return g, nil
}

func (r smartGroupRepo) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	return r.GetByID(ctx, id)
}

type smartSubRepo struct {
	UserSubscriptionRepository
	sub *UserSubscription
	err error
}

func (r *smartSubRepo) GetActiveByUserIDAndGroupID(context.Context, int64, int64) (*UserSubscription, error) {
	if r.err != nil {
		return nil, r.err
	}
	copy := *r.sub
	return &copy, nil
}

func smartFixture() (*APIKeyService, *APIKey, *smartSubRepo) {
	id := int64(1)
	limit := 10.0
	now := time.Now()
	sub := &UserSubscription{ID: 1, UserID: 1, GroupID: 1, Status: SubscriptionStatusActive, ExpiresAt: now.Add(24 * time.Hour), DailyWindowStart: &now, WeeklyWindowStart: &now, MonthlyWindowStart: &now, FiveHourWindowStart: &now}
	user := &User{ID: 1, Status: StatusActive, Balance: 100}
	groups := map[int64]*Group{1: {ID: 1, Platform: PlatformAnthropic, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription, DailyLimitUSD: &limit}, 2: {ID: 2, Platform: PlatformAnthropic, Status: StatusActive}}
	key := &APIKey{ID: 1, UserID: 1, Status: StatusActive, User: user, GroupID: &id, Group: groups[1], SmartSwitch: &domain.KeySmartSwitchPolicy{Enabled: true, GroupIDs: []int64{1, 2}, PaygConfirmed: true}}
	subs := &smartSubRepo{sub: sub}
	svc := &APIKeyService{apiKeyRepo: smartKeyRepo{key: key}, userRepo: smartUserRepo{user: user}, groupRepo: smartGroupRepo{groups: groups}, userSubRepo: subs}
	return svc, key, subs
}

func TestSmartSwitchSubscriptionFallbackAndRecovery(t *testing.T) {
	svc, key, subs := smartFixture()
	selectGroup := func() *SmartSwitchSelection {
		result, err := svc.SelectSmartSwitchGroup(context.Background(), key, "claude-sonnet", nil, &SubscriptionService{})
		require.NoError(t, err)
		return result
	}
	require.Equal(t, int64(1), *selectGroup().Key.GroupID)
	subs.sub.DailyUsageUSD = 10
	fallback := selectGroup()
	require.Equal(t, int64(2), *fallback.Key.GroupID)
	require.Nil(t, fallback.Subscription)
	require.Equal(t, "subscription_limit_or_expiry", fallback.Key.SmartSwitchReason)
	require.Equal(t, int64(1), *key.GroupID, "cached binding must not change")
	require.Equal(t, int64(1), key.Group.ID)
	fallback.Key.SmartSwitch.GroupIDs[0] = 99
	require.Equal(t, int64(1), key.SmartSwitch.GroupIDs[0])
	subs.sub.DailyUsageUSD = 0
	require.Equal(t, int64(1), *selectGroup().Key.GroupID)
}

func TestSmartSwitchFailsClosedOnDependencyError(t *testing.T) {
	svc, key, subs := smartFixture()
	subs.err = errors.New("database unavailable")
	result, err := svc.SelectSmartSwitchGroup(context.Background(), key, "", nil, &SubscriptionService{})
	require.ErrorIs(t, err, subs.err)
	require.Nil(t, result)
}

func TestSmartSwitchGlobalQuotaAndPaidBudget(t *testing.T) {
	svc, key, subs := smartFixture()
	key.Quota = 1
	key.QuotaUsed = 1
	_, err := svc.SelectSmartSwitchGroup(context.Background(), key, "", nil, &SubscriptionService{})
	require.ErrorIs(t, err, ErrAPIKeyQuotaExhausted)
	key.QuotaUsed = 0
	subs.sub.DailyUsageUSD = 10
	zero := 0.0
	key.SmartSwitch.PaygBudgetUSD = &zero
	_, err = svc.SelectSmartSwitchGroup(context.Background(), key, "", nil, &SubscriptionService{})
	require.ErrorIs(t, err, ErrSmartSwitchUnavailable)
	var exhausted *SmartSwitchExhaustedError
	require.ErrorAs(t, err, &exhausted)
	require.Equal(t, []string{"subscription_limit_or_expiry", "payg_budget_exhausted"}, exhausted.Reasons)
	require.True(t, exhausted.BusinessLimited())
	subs.sub.DailyUsageUSD = 0
	result, err := svc.SelectSmartSwitchGroup(context.Background(), key, "", nil, &SubscriptionService{})
	require.NoError(t, err)
	require.Equal(t, int64(1), *result.Key.GroupID)
}

func TestSmartSwitchUnavailableGroupAndPermissionRevocation(t *testing.T) {
	svc, key, _ := smartFixture()
	result, err := svc.SelectSmartSwitchGroup(context.Background(), key, "", map[int64]bool{1: true}, &SubscriptionService{})
	require.NoError(t, err)
	require.Equal(t, int64(2), *result.Key.GroupID)
	group := svc.groupRepo.(smartGroupRepo).groups[2]
	group.IsExclusive = true
	_, err = svc.SelectSmartSwitchGroup(context.Background(), key, "", map[int64]bool{1: true}, &SubscriptionService{})
	require.ErrorIs(t, err, ErrSmartSwitchUnavailable)
}

func TestSmartSwitchExhaustedReasons(t *testing.T) {
	selectErr := func(t *testing.T, mutate func(*APIKeyService, *APIKey, *smartSubRepo), excluded map[int64]bool) *SmartSwitchExhaustedError {
		svc, key, subs := smartFixture()
		mutate(svc, key, subs)
		_, err := svc.SelectSmartSwitchGroup(context.Background(), key, "claude-sonnet", excluded, &SubscriptionService{})
		require.ErrorIs(t, err, ErrSmartSwitchUnavailable)
		var exhausted *SmartSwitchExhaustedError
		require.ErrorAs(t, err, &exhausted)
		return exhausted
	}
	t.Run("subscription_limit_and_no_balance", func(t *testing.T) {
		exhausted := selectErr(t, func(_ *APIKeyService, key *APIKey, subs *smartSubRepo) {
			subs.sub.DailyUsageUSD = 10
			key.User.Balance = 0
		}, nil)
		require.Equal(t, []string{"subscription_limit_or_expiry", "insufficient_balance"}, exhausted.Reasons)
		require.True(t, exhausted.BusinessLimited())
		require.True(t, exhausted.Has("insufficient_balance"))
	})
	t.Run("missing_subscription_is_business", func(t *testing.T) {
		exhausted := selectErr(t, func(_ *APIKeyService, key *APIKey, subs *smartSubRepo) {
			subs.err = ErrSubscriptionNotFound
			key.User.Balance = 0
		}, nil)
		require.Equal(t, []string{"subscription_unavailable", "insufficient_balance"}, exhausted.Reasons)
		require.True(t, exhausted.BusinessLimited())
	})
	t.Run("excluded_group_keeps_service_error", func(t *testing.T) {
		exhausted := selectErr(t, func(_ *APIKeyService, key *APIKey, _ *smartSubRepo) {
			key.User.Balance = 0
		}, map[int64]bool{1: true})
		require.Equal(t, []string{"group_unavailable", "insufficient_balance"}, exhausted.Reasons)
		require.False(t, exhausted.BusinessLimited())
	})
	t.Run("blocked_model_keeps_service_error", func(t *testing.T) {
		exhausted := selectErr(t, func(svc *APIKeyService, key *APIKey, _ *smartSubRepo) {
			svc.groupRepo.(smartGroupRepo).groups[1].BlockedModelPatterns = []string{"claude-*"}
			key.User.Balance = 0
		}, nil)
		require.Equal(t, []string{"model_unavailable", "insufficient_balance"}, exhausted.Reasons)
		require.False(t, exhausted.BusinessLimited())
	})
	t.Run("dependency_error_after_business_skip_is_returned", func(t *testing.T) {
		svc, key, subs := smartFixture()
		key.SmartSwitch.GroupIDs = []int64{2, 1}
		key.User.Balance = 0
		subs.err = errors.New("database unavailable")
		_, err := svc.SelectSmartSwitchGroup(context.Background(), key, "claude-sonnet", nil, &SubscriptionService{})
		require.ErrorIs(t, err, subs.err)
		var exhausted *SmartSwitchExhaustedError
		require.False(t, errors.As(err, &exhausted))
	})
	duplicate := &SmartSwitchExhaustedError{Reasons: []string{"insufficient_balance", "subscription_unavailable", "insufficient_balance"}}
	require.Equal(t, []string{"insufficient_balance", "subscription_unavailable"}, duplicate.UniqueReasons())
	require.False(t, (&SmartSwitchExhaustedError{}).BusinessLimited())
}

func TestSmartSwitchPolicyValidation(t *testing.T) {
	svc, key, _ := smartFixture()
	require.NoError(t, svc.validateSmartSwitch(context.Background(), key.User, key.GroupID, key.SmartSwitch))
	for _, ids := range [][]int64{{}, {1, 1}, {0}, {-1}} {
		policy := key.SmartSwitch.Clone()
		policy.GroupIDs = ids
		require.ErrorIs(t, svc.validateSmartSwitch(context.Background(), key.User, key.GroupID, policy), ErrSmartSwitchInvalid)
	}
	policy := key.SmartSwitch.Clone()
	policy.PaygConfirmed = false
	require.ErrorIs(t, svc.validateSmartSwitch(context.Background(), key.User, key.GroupID, policy), ErrSmartSwitchInvalid)
	svc.groupRepo.(smartGroupRepo).groups[2].Platform = PlatformOpenAI
	require.ErrorIs(t, svc.validateSmartSwitch(context.Background(), key.User, key.GroupID, key.SmartSwitch), ErrSmartSwitchInvalid)
}

func TestSmartSwitchRecordsActualGroupAndPaidBudget(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	first, second := int64(1), int64(2)
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{RequestID: "smart-budget", Model: "claude-sonnet-4", Duration: time.Second, Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 500}},
		APIKey: &APIKey{ID: 501, GroupID: &second, Group: &Group{ID: second, RateMultiplier: 2}, SmartSwitchOriginalGroupID: &first, SmartSwitchReason: "subscription_limit_or_expiry", SmartSwitchVersion: 3},
		User:   &User{ID: 601}, Account: &Account{ID: 701}, APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
	})
	require.NoError(t, err)
	require.Positive(t, billingRepo.lastCmd.BalanceCost)
	require.Equal(t, billingRepo.lastCmd.BalanceCost, billingRepo.lastCmd.SmartSwitchPaygCost)
	require.Equal(t, second, *usageRepo.lastLog.GroupID)
	require.Nil(t, usageRepo.lastLog.SubscriptionID)
	require.Equal(t, first, usageRepo.lastLog.SmartSwitch.OriginalGroupID)
	require.Equal(t, int64(3), usageRepo.lastLog.SmartSwitch.PolicyVersion)
}

func TestSmartSwitchOldAuthSnapshotIsReloaded(t *testing.T) {
	svc := &APIKeyService{}
	_, used, err := svc.applyAuthCacheEntry("TEST_ONLY", &APIKeyAuthCacheEntry{Snapshot: &APIKeyAuthSnapshot{APIKeyID: 1}})
	require.NoError(t, err)
	require.False(t, used)
}

func TestSmartSwitchBillingRecheckRecognizesRecoveredDailyWindow(t *testing.T) {
	_, key, subs := smartFixture()
	original := *key.GroupID
	key.SmartSwitchOriginalGroupID = &original
	past := time.Now().Add(-25 * time.Hour)
	subs.sub.DailyWindowStart = &past
	subs.sub.DailyUsageUSD = 20
	billing := NewBillingCacheService(nil, nil, subs, nil, &config.Config{RunMode: config.RunModeStandard})
	t.Cleanup(billing.Stop)
	require.NoError(t, billing.CheckBillingEligibility(context.Background(), key.User, key, key.Group, subs.sub))
	now := time.Now()
	subs.sub.DailyWindowStart = &now
	require.ErrorIs(t, billing.CheckBillingEligibility(context.Background(), key.User, key, key.Group, subs.sub), ErrDailyLimitExceeded)
}

func TestSmartSwitchBillingRecheckRejectsReplacedSubscription(t *testing.T) {
	_, key, subs := smartFixture()
	original := *key.GroupID
	key.SmartSwitchOriginalGroupID = &original
	selected := *subs.sub
	subs.sub.ID = selected.ID + 1 // deleted and re-subscribed after selection
	billing := NewBillingCacheService(nil, nil, subs, nil, &config.Config{RunMode: config.RunModeStandard})
	t.Cleanup(billing.Stop)
	require.ErrorIs(t, billing.CheckBillingEligibility(context.Background(), key.User, key, key.Group, &selected), ErrSubscriptionNotFound)
}

type smartUpdateKeyRepo struct {
	smartKeyRepo
	saved *APIKey
}

func (r *smartUpdateKeyRepo) Update(_ context.Context, key *APIKey) error {
	r.saved = key
	return nil
}

func TestSmartSwitchCanBeDisabledWhenOriginalGroupIsUnavailable(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		svc, key, _ := smartFixture()
		key.SmartSwitchVersion = 2
		if deleted {
			// Group deletion clears the binding but keeps the policy.
			key.GroupID = nil
			delete(svc.groupRepo.(smartGroupRepo).groups, 1)
		} else {
			svc.groupRepo.(smartGroupRepo).groups[1].Status = StatusDisabled
		}
		repo := &smartUpdateKeyRepo{smartKeyRepo: smartKeyRepo{key: key}}
		svc.apiKeyRepo = repo
		policy := key.SmartSwitch.Clone()
		policy.Enabled = false
		policy.Version = 2
		_, err := svc.Update(context.Background(), key.ID, key.UserID, UpdateAPIKeyRequest{GroupID: key.GroupID, SmartSwitch: policy})
		require.NoError(t, err)
		require.NotNil(t, repo.saved)
		require.False(t, repo.saved.SmartSwitch.Enabled)
		require.Equal(t, int64(3), repo.saved.SmartSwitch.Version)
	}
}
