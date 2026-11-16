//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Hosted rows carry the hosting service fee in actual_cost. Balance revenue
// must include it (zero for free hosted groups); standard usage is unchanged.
func TestHostedFeeCountsAsBalanceRevenueAndOwnerFee(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	logs := newUsageLogRepositoryWithSQL(client, integrationDB)
	owners := &hostedOwnerRepository{db: integrationDB, accounts: newAccountRepositoryWithSQL(client, integrationDB, nil)}

	suffix := uuid.NewString()
	owner := mustCreateUser(t, client, &service.User{Email: "fee-owner-" + suffix + "@example.invalid"})
	ownerKey := mustCreateApiKey(t, client, &service.APIKey{UserID: owner.ID, Key: "sk-fee-" + suffix, Name: "fee"})
	account := mustCreateAccount(t, client, &service.Account{Name: "fee-" + suffix, Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM usage_logs WHERE api_key_id = $1", ownerKey.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM account_owner_events WHERE account_id = $1", account.ID)
	})
	_, err := owners.SetAccountOwner(ctx, account.ID, &owner.ID, 0)
	require.NoError(t, err)

	now := time.Now().UTC()
	newLog := func(total, actual float64, metered *float64) *service.UsageLog {
		return &service.UsageLog{UserID: owner.ID, APIKeyID: ownerKey.ID, AccountID: account.ID, RequestID: uuid.NewString(),
			Model: "claude-sonnet-5", InputTokens: 1, TotalCost: total, ActualCost: actual, MeteredCost: metered,
			BillingType: service.BillingTypeBalance, CreatedAt: now.Add(-time.Minute)}
	}
	hostedStd, ordinaryStd := 10.0, 4.0
	for _, log := range []*service.UsageLog{
		newLog(hostedStd, 1.0, &hostedStd), // hosted with a 0.10 fee
		newLog(hostedStd, 0, &hostedStd),   // free hosted group
		newLog(ordinaryStd, 2.0, nil),      // ordinary balance usage of the same account
	} {
		_, err := logs.createSingle(ctx, integrationDB, log)
		require.NoError(t, err)
	}

	stats, err := logs.GetAccountWindowStats(ctx, account.ID, now.Add(-time.Hour))
	require.NoError(t, err)
	require.InDelta(t, 3.0, stats.BalanceCost, 1e-9, "balance revenue = ordinary 2.0 + hosted fee 1.0")
	require.Equal(t, int64(2), stats.HostedRequests)
	require.InDelta(t, 20.0, stats.HostedCost, 1e-9, "hosted standard usage is unchanged")

	_, last7d, err := owners.SummarizeOwnedUsage(ctx, owner.ID, []int64{account.ID}, now.Add(-7*24*time.Hour), now.Add(-24*time.Hour), now.Add(time.Minute))
	require.NoError(t, err)
	require.InDelta(t, 1.0, last7d[account.ID].SelfServiceFee, 1e-9, "only hosted rows count as service fee")
	require.Equal(t, int64(3), last7d[account.ID].SelfRequests)
}
