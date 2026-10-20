//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Ownership stub deliberately retains legacy bindings so tests exercise stale
// prefetches and the scheduler rather than simply returning a chosen account.
type stickyOwnershipStub struct {
	*mockGatewayCacheForPlatform
	token      string
	claimError error
	raceToken  string
	claims     int
}

func (c *stickyOwnershipStub) GetStickyOwnership(context.Context, int64, string) (string, error) {
	return c.token, nil
}
func (c *stickyOwnershipStub) ClaimStickyOwnership(_ context.Context, _ int64, session, previous, token string, account int64, _ time.Duration) (bool, error) {
	c.claims++
	if c.claimError != nil {
		return false, c.claimError
	}
	if c.raceToken != "" {
		c.token = c.raceToken
		c.raceToken = ""
		return false, nil
	}
	if c.token != previous {
		return false, nil
	}
	c.token = token
	c.sessionBindings[session] = account
	return true, nil
}
func (c *stickyOwnershipStub) ConfirmStickyOwnership(_ context.Context, _ int64, session, token string, account int64, _ time.Duration) (bool, error) {
	if token != c.token {
		return false, nil
	}
	c.sessionBindings[session] = account
	return true, nil
}

func capacityTestService(t *testing.T, batch, routed bool) (*GatewayService, *stickyOwnershipStub, *mockConcurrencyCache, *int64) {
	t.Helper()
	groupID := int64(20)
	repo := &mockAccountRepoForPlatform{accounts: []Account{
		{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2, AccountGroups: []AccountGroup{{GroupID: groupID}}},
		{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Priority: 2, Status: StatusActive, Schedulable: true, Concurrency: 2, AccountGroups: []AccountGroup{{GroupID: groupID}}},
	}, accountsByID: map[int64]*Account{}}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	cache := &stickyOwnershipStub{mockGatewayCacheForPlatform: &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"session": 1}}}
	group := &Group{ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true, ModelRoutingEnabled: routed, ModelRouting: map[string][]int64{"claude-test": {1, 2}}}
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = batch
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 120 * time.Second
	cc := &mockConcurrencyCache{acquireResults: map[int64]bool{1: false, 2: true}, waitCounts: map[int64]int{1: 3}}
	svc := &GatewayService{accountRepo: repo, groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}}, cache: cache, cfg: cfg, concurrencyService: NewConcurrencyService(cc)}
	return svc, cache, cc, &groupID
}

func TestClaudeStickyCapacityNeverSpillsToIdleAccount(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, routed := range []bool{false, true} {
			t.Run(fmt.Sprintf("batch=%v/routed=%v", batch, routed), func(t *testing.T) {
				svc, cache, cc, group := capacityTestService(t, batch, routed)
				ctx := SetClaudeCodeClient(context.Background(), true)
				for _, waiting := range []int{0, 3, 8} {
					cc.waitCounts[1] = waiting
					result, err := svc.SelectAccountWithLoadAwareness(ctx, group, "session", "claude-test", nil, "")
					require.NoError(t, err)
					require.Equal(t, int64(1), result.Account.ID)
					require.False(t, result.Acquired)
					require.NotNil(t, result.WaitPlan)
					require.Equal(t, 3, result.WaitPlan.MaxWaiting)
					require.Equal(t, 120*time.Second, result.WaitPlan.Timeout)
					require.NotEmpty(t, result.StickyOwnership)
				}
				require.Equal(t, int64(1), cache.sessionBindings["session"])
			})
		}
	}
}

func TestClaudeStickyMigrationFencesLateSuccessAndStalePrefetch(t *testing.T) {
	svc, cache, cc, group := capacityTestService(t, true, false)
	cc.acquireResults[1] = true
	ctx := SetClaudeCodeClient(context.Background(), true)
	a, err := svc.SelectAccountWithLoadAwareness(ctx, group, "session", "claude-test", nil, "")
	require.NoError(t, err)
	defer a.ReleaseFunc()
	require.NoError(t, svc.BindStickySessionForAccount(WithStickyOwnership(ctx, a.StickyOwnership), group, "session", a.Account))
	b, err := svc.SelectAccountWithLoadAwareness(ctx, group, "session", "claude-test", map[int64]struct{}{1: {}}, "")
	require.NoError(t, err)
	defer b.ReleaseFunc()
	require.Equal(t, int64(2), b.Account.ID)
	require.NotEqual(t, a.StickyOwnership, b.StickyOwnership)
	current, err := svc.StickyOwnershipCurrent(ctx, group, "session", a.StickyOwnership)
	require.NoError(t, err)
	require.False(t, current, "a request waiting on the old account must not be forwarded")
	current, err = svc.StickyOwnershipCurrent(ctx, group, "session", b.StickyOwnership)
	require.NoError(t, err)
	require.True(t, current)

	require.NoError(t, svc.BindStickySessionForAccount(WithStickyOwnership(ctx, b.StickyOwnership), group, "session", b.Account))
	require.NoError(t, svc.BindStickySessionForAccount(WithStickyOwnership(ctx, a.StickyOwnership), group, "session", a.Account))
	require.NoError(t, svc.ClearStickySessionForAccount(WithStickyOwnership(ctx, a.StickyOwnership), group, "session", a.Account))
	require.Equal(t, int64(2), cache.sessionBindings["session"])
	stale := WithPrefetchedStickySession(ctx, 1, *group, true)
	next, err := svc.SelectAccountWithLoadAwareness(stale, group, "session", "claude-test", nil, "")
	require.NoError(t, err)
	defer next.ReleaseFunc()
	require.Equal(t, int64(2), next.Account.ID)
}

func TestClaudeStickyOwnershipRaceReselectsWinner(t *testing.T) {
	svc, cache, cc, group := capacityTestService(t, true, false)
	cc.acquireResults[1] = true
	cache.raceToken = "2:other-process"
	result, err := svc.SelectAccountWithLoadAwareness(SetClaudeCodeClient(context.Background(), true), group, "session", "claude-test", nil, "")
	require.NoError(t, err)
	defer result.ReleaseFunc()
	require.Equal(t, int64(2), result.Account.ID)
	require.Equal(t, "2:other-process", result.StickyOwnership)
	require.Equal(t, 2, cache.claims)
}

func TestClaudeStickyOwnershipCacheFailureStopsSelection(t *testing.T) {
	svc, cache, _, group := capacityTestService(t, true, false)
	cache.claimError = errors.New("cache unavailable")
	result, err := svc.SelectAccountWithLoadAwareness(SetClaudeCodeClient(context.Background(), true), group, "session", "claude-test", nil, "")
	require.ErrorContains(t, err, "reserve session ownership")
	require.Nil(t, result)
}

func (c *stickyOwnershipStub) RenewStickyOwnership(_ context.Context, _ int64, _, token string, _ time.Duration) (bool, error) {
	return c.token == token, nil
}

func TestClaudeStickyLegacySelectionUsesOwnerWithoutPendingBinding(t *testing.T) {
	svc, cache, cc, group := capacityTestService(t, false, false)
	cache.token = "2:active-long-request"
	cache.sessionBindings = map[string]int64{} // the two-minute pending key expired
	cc.acquireResults[2] = false
	cc.waitCounts[2] = 9
	result, err := svc.SelectAccountWithLoadAwareness(SetClaudeCodeClient(context.Background(), true), group, "session", "claude-test", nil, "")
	require.NoError(t, err)
	require.Equal(t, int64(2), result.Account.ID)
	require.NotNil(t, result.WaitPlan)
	require.Equal(t, 3, result.WaitPlan.MaxWaiting)
}

func TestClaudeStickyOAuthCapacityAcrossModes(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		for _, mode := range []string{ClaudeOAuthModeCarpool, ClaudeOAuthModeShared, ClaudeOAuthModeSingleDevice} {
			for _, batch := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/batch=%v", accountType, mode, batch), func(t *testing.T) {
					svc, _, _, group := capacityTestService(t, batch, false)
					repo := svc.accountRepo.(*mockAccountRepoForPlatform)
					for i := range repo.accounts {
						repo.accounts[i].Type = accountType
						repo.accounts[i].Extra = map[string]any{"claude_oauth_mode": mode}
					}
					ctx := WithClaudeOAuthGroupMode(SetClaudeCodeClient(context.Background(), true), *group, mode)
					result, err := svc.SelectAccountWithLoadAwareness(ctx, group, "session", "claude-test", nil, "")
					require.NoError(t, err)
					require.Equal(t, int64(1), result.Account.ID)
					require.NotNil(t, result.WaitPlan)
					require.Equal(t, 3, result.WaitPlan.MaxWaiting)
				})
			}
		}
	}
}
