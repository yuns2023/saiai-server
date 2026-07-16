package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func highFiveHourClaudeAccount(id int64) *Account {
	resetAt := time.Now().Add(2 * time.Hour)
	return &Account{
		ID:               id,
		Name:             "high-five-hour",
		Platform:         PlatformAnthropic,
		Type:             AccountTypeOAuth,
		Status:           StatusActive,
		Schedulable:      true,
		Concurrency:      1,
		SessionWindowEnd: &resetAt,
		Extra:            map[string]any{"session_window_utilization": 0.95},
	}
}

func TestGatewayService_FreshAdmissionSnapshotRejectsNewButKeepsExistingBinding(t *testing.T) {
	stale := highFiveHourClaudeAccount(7101)
	stale.Extra = map[string]any{"session_window_utilization": 0.10}
	fresh := highFiveHourClaudeAccount(7101)
	snapshotCache := &openAISnapshotCacheStub{accountsByID: map[int64]*Account{fresh.ID: fresh}}
	svc := &GatewayService{schedulerSnapshot: &SchedulerSnapshotService{cache: snapshotCache}}

	require.Nil(t, svc.resolveFreshAccountForSessionAdmission(context.Background(), stale, false))
	resolved := svc.resolveFreshAccountForSessionAdmission(context.Background(), stale, true)
	require.NotNil(t, resolved)
	require.Equal(t, fresh.ID, resolved.ID)

	staleHigh := highFiveHourClaudeAccount(7101)
	freshLow := highFiveHourClaudeAccount(7101)
	freshLow.Extra = map[string]any{"session_window_utilization": 0.10}
	snapshotCache.accountsByID[freshLow.ID] = freshLow
	resolved = svc.resolveFreshAccountForSessionAdmission(context.Background(), staleHigh, false)
	require.NotNil(t, resolved, "fresh recovered usage must override a stale high bucket value")
}

func TestGatewayService_HighFiveHourUsageKeepsConfirmedAndPendingSticky(t *testing.T) {
	account := highFiveHourClaudeAccount(7151)
	repo := &oauthStickyAccountRepoStub{accounts: map[int64]*Account{account.ID: account}}

	for _, tt := range []struct {
		name  string
		cache *oauthStickyGatewayCacheStub
	}{
		{name: "confirmed", cache: &oauthStickyGatewayCacheStub{binding: account.ID}},
		{name: "pending", cache: &oauthStickyGatewayCacheStub{pendingBinding: account.ID}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := &GatewayService{accountRepo: repo, cache: tt.cache}
			selected, err := svc.SelectAccountForModelWithExclusions(
				context.Background(),
				nil,
				"sticky-high-five-hour",
				"",
				nil,
			)
			require.NoError(t, err)
			require.NotNil(t, selected)
			require.Equal(t, account.ID, selected.ID)
		})
	}
}

func TestGatewayService_PinnedHighFiveHourUsageKeepsBoundDevice(t *testing.T) {
	account := highFiveHourClaudeAccount(7201)
	groupID := int64(72)
	svc := &GatewayService{identityService: &IdentityService{}}
	candidates := []accountWithLoad{{
		account:  account,
		loadInfo: &AccountLoadInfo{AccountID: account.ID, LoadRate: 0},
	}}
	deviceBindings := map[int64]*PinnedDeviceBinding{account.ID: {AccountID: account.ID}}

	result, handled, err := svc.trySelectPinnedWithLoad(
		context.Background(),
		&groupID,
		"device-user",
		"claude-fable-5",
		candidates,
		deviceBindings,
		nil,
	)
	require.NoError(t, err)
	require.True(t, handled)
	require.NotNil(t, result)
	require.Equal(t, account.ID, result.Account.ID)
	require.True(t, result.Acquired)
}

func TestGatewayService_PinnedHighFiveHourUsageRejectsNewDeviceIncludingWaitPlan(t *testing.T) {
	account := highFiveHourClaudeAccount(7301)
	groupID := int64(73)
	svc := &GatewayService{identityService: &IdentityService{}}
	candidates := []accountWithLoad{{
		account:  account,
		loadInfo: &AccountLoadInfo{AccountID: account.ID, LoadRate: 100},
	}}

	result, handled, err := svc.trySelectPinnedWithLoad(
		context.Background(),
		&groupID,
		"new-device-user",
		"claude-fable-5",
		candidates,
		nil,
		nil,
	)
	require.Nil(t, result)
	require.True(t, handled)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.NotErrorIs(t, err, ErrClaudeOAuthPinnedDevicesFull)
}

func TestSelectRandomAccountByMinPriorityIgnoresLastUsedAndAccountType(t *testing.T) {
	old := time.Now().Add(-24 * time.Hour)
	recent := time.Now()
	accounts := []*Account{
		{ID: 1, Priority: 0, Type: AccountTypeAPIKey, LastUsedAt: &recent},
		{ID: 2, Priority: 0, Type: AccountTypeOAuth, LastUsedAt: &old},
		{ID: 3, Priority: 10, Type: AccountTypeOAuth},
	}
	seen := map[int64]bool{}
	for i := 0; i < 200; i++ {
		selected := selectRandomAccountByMinPriority(accounts)
		require.NotNil(t, selected)
		require.NotEqual(t, int64(3), selected.ID)
		seen[selected.ID] = true
	}
	require.True(t, seen[1])
	require.True(t, seen[2])
}

func TestSelectRandomAccountByMinPriorityIgnoresGroupMembershipPriority(t *testing.T) {
	accounts := []*Account{
		{ID: 1, Priority: 5, AccountGroups: []AccountGroup{{GroupID: 9, Priority: -100}}},
		{ID: 2, Priority: 0, AccountGroups: []AccountGroup{{GroupID: 9, Priority: 100}}},
	}
	for i := 0; i < 50; i++ {
		selected := selectRandomAccountByMinPriority(accounts)
		require.NotNil(t, selected)
		require.Equal(t, int64(2), selected.ID)
	}
}

func TestSelectRandomAccountWithLoadDoesNotSoftSortByQuotaOrLastUsed(t *testing.T) {
	old := time.Now().Add(-24 * time.Hour)
	recent := time.Now()
	candidates := []accountWithLoad{
		{
			account: &Account{ID: 1, Priority: 0, LastUsedAt: &recent, Extra: map[string]any{
				"session_window_utilization":   0.79,
				"passive_usage_7d_utilization": 0.99,
			}},
			loadInfo: &AccountLoadInfo{AccountID: 1, LoadRate: 20},
		},
		{
			account: &Account{ID: 2, Priority: 0, LastUsedAt: &old, Extra: map[string]any{
				"session_window_utilization":   0.10,
				"passive_usage_7d_utilization": 0.10,
			}},
			loadInfo: &AccountLoadInfo{AccountID: 2, LoadRate: 20},
		},
	}
	seen := map[int64]bool{}
	for i := 0; i < 200; i++ {
		selected := selectRandomAccountWithLoad(candidates, false)
		require.NotNil(t, selected)
		seen[selected.account.ID] = true
	}
	require.True(t, seen[1])
	require.True(t, seen[2])
}
