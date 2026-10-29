package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// groupedGatewayCache keys bindings by group like the Redis cache, so a Key
// moving between groups sees separate group bindings.
type groupedGatewayCache struct {
	bindings map[string]int64
	deleted  map[string]int
}

func groupedKey(groupID int64, key string) string { return fmt.Sprintf("%d|%s", groupID, key) }

func (c *groupedGatewayCache) GetSessionAccountID(_ context.Context, groupID int64, key string) (int64, error) {
	if id, ok := c.bindings[groupedKey(groupID, key)]; ok {
		return id, nil
	}
	return 0, errors.New("not found")
}

func (c *groupedGatewayCache) SetSessionAccountID(_ context.Context, groupID int64, key string, accountID int64, _ time.Duration) error {
	c.bindings[groupedKey(groupID, key)] = accountID
	return nil
}

func (c *groupedGatewayCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (c *groupedGatewayCache) DeleteSessionAccountID(_ context.Context, groupID int64, key string) error {
	c.deleted[groupedKey(groupID, key)]++
	delete(c.bindings, groupedKey(groupID, key))
	return nil
}

const (
	userStickyUserID = int64(7)
	userStickyHash   = "user-sticky-session"
	userStickyOld    = int64(1)
	userStickyNew    = int64(2)
)

func userStickyKey(userID int64) string {
	return groupedKey(0, openAIUserStickyCacheKey(userID, userStickyHash))
}

func groupStickyKey(groupID int64) string { return groupedKey(groupID, "openai:"+userStickyHash) }

// shared is in both groups; preferred is only in the new group and wins load
// balancing, so selecting shared proves the user-scoped binding was used.
func newUserStickyFixture(t *testing.T, sharedGroups ...int64) (*OpenAIGatewayService, *groupedGatewayCache, Account, Account) {
	t.Helper()
	var memberships []AccountGroup
	for _, id := range sharedGroups {
		memberships = append(memberships, AccountGroup{GroupID: id})
	}
	shared := Account{ID: 501, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
		Concurrency: 1, Priority: 10, AccountGroups: memberships}
	preferred := Account{ID: 502, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
		Concurrency: 1, Priority: 0, AccountGroups: []AccountGroup{{GroupID: userStickyNew}}}
	cache := &groupedGatewayCache{bindings: map[string]int64{}, deleted: map[string]int{}}
	svc := &OpenAIGatewayService{
		accountRepo:        groupFilteredOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: []Account{shared, preferred}}},
		cache:              cache,
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}
	return svc, cache, shared, preferred
}

func selectForUser(t *testing.T, svc *OpenAIGatewayService, groupID, userID int64, excluded map[int64]struct{}) (*Account, OpenAIAccountScheduleDecision) {
	t.Helper()
	selection, decision, err := svc.SelectAccountWithSchedulerForUser(context.Background(), &groupID, userID, "", userStickyHash, "gpt-5.1", excluded, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
	return selection.Account, decision
}

func TestOpenAIUserStickyKeepsAccountAfterGroupChange(t *testing.T) {
	svc, cache, shared, _ := newUserStickyFixture(t, userStickyOld, userStickyNew)
	require.NoError(t, svc.BindStickySessionForUser(context.Background(), ptrInt64(userStickyOld), userStickyUserID, userStickyHash, shared.ID))
	require.Equal(t, shared.ID, cache.bindings[userStickyKey(userStickyUserID)])

	got, decision := selectForUser(t, svc, userStickyNew, userStickyUserID, nil)
	require.Equal(t, shared.ID, got.ID)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
	require.True(t, decision.StickyUserFallbackHit)
	require.Equal(t, shared.ID, cache.bindings[groupStickyKey(userStickyNew)], "the new group's binding is filled in")
	require.EqualValues(t, 1, svc.SnapshotOpenAIAccountSchedulerMetrics().StickyUserFallbackTotal)
}

func TestOpenAIUserStickyNeverLeavesTheRequestGroup(t *testing.T) {
	svc, cache, shared, preferred := newUserStickyFixture(t, userStickyOld)
	require.NoError(t, svc.BindStickySessionForUser(context.Background(), ptrInt64(userStickyOld), userStickyUserID, userStickyHash, shared.ID))

	got, decision := selectForUser(t, svc, userStickyNew, userStickyUserID, nil)
	require.Equal(t, preferred.ID, got.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.False(t, decision.StickyUserFallbackHit)
	require.Zero(t, cache.deleted[userStickyKey(userStickyUserID)], "the user binding may still be valid in the old group")
	require.Equal(t, preferred.ID, cache.bindings[userStickyKey(userStickyUserID)], "the new selection becomes the user's preference")
	require.Equal(t, shared.ID, cache.bindings[groupStickyKey(userStickyOld)], "switching back finds the old group's own binding")
}

func TestOpenAIUserStickyIsScopedToUserAndResponses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		userScope bool
		userID    int64
	}{
		{"other user", true, userStickyUserID + 1},
		{"non-Responses selection", false, userStickyUserID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, shared, preferred := newUserStickyFixture(t, userStickyOld, userStickyNew)
			require.NoError(t, svc.BindStickySessionForUser(context.Background(), ptrInt64(userStickyOld), userStickyUserID, userStickyHash, shared.ID))
			groupID := userStickyNew
			var (
				selection *AccountSelectionResult
				decision  OpenAIAccountScheduleDecision
				err       error
			)
			if tc.userScope {
				selection, decision, err = svc.SelectAccountWithSchedulerForUser(context.Background(), &groupID, tc.userID, "", userStickyHash, "gpt-5.1", nil, OpenAIUpstreamTransportAny)
			} else {
				selection, decision, err = svc.SelectAccountWithScheduler(context.Background(), &groupID, "", userStickyHash, "gpt-5.1", nil, OpenAIUpstreamTransportAny)
			}
			require.NoError(t, err)
			require.Equal(t, preferred.ID, selection.Account.ID)
			require.False(t, decision.StickyUserFallbackHit)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

func TestOpenAIUserStickyBacksUpAnIneligibleGroupBinding(t *testing.T) {
	svc, cache, shared, preferred := newUserStickyFixture(t, userStickyOld, userStickyNew)
	stale := Account{ID: 503, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
		Concurrency: 1, AccountGroups: []AccountGroup{{GroupID: 99}}}
	svc.accountRepo = groupFilteredOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: []Account{shared, preferred, stale}}}
	cache.bindings[groupStickyKey(userStickyNew)] = stale.ID
	cache.bindings[userStickyKey(userStickyUserID)] = shared.ID

	got, decision := selectForUser(t, svc, userStickyNew, userStickyUserID, nil)
	require.Equal(t, shared.ID, got.ID)
	require.True(t, decision.StickyUserFallbackHit)
	require.Equal(t, 1, cache.deleted[groupStickyKey(userStickyNew)], "an ineligible group binding is still cleared")
	require.Equal(t, shared.ID, cache.bindings[groupStickyKey(userStickyNew)])
}

func TestOpenAIUserStickySkipsExcludedAccount(t *testing.T) {
	svc, cache, shared, preferred := newUserStickyFixture(t, userStickyOld, userStickyNew)
	cache.bindings[userStickyKey(userStickyUserID)] = shared.ID

	got, decision := selectForUser(t, svc, userStickyNew, userStickyUserID, map[int64]struct{}{shared.ID: {}})
	require.Equal(t, preferred.ID, got.ID)
	require.False(t, decision.StickyUserFallbackHit)
}

func TestOpenAIGroupStickyHitBackfillsUserBinding(t *testing.T) {
	svc, cache, shared, _ := newUserStickyFixture(t, userStickyOld, userStickyNew)
	cache.bindings[groupStickyKey(userStickyNew)] = shared.ID

	got, decision := selectForUser(t, svc, userStickyNew, userStickyUserID, nil)
	require.Equal(t, shared.ID, got.ID)
	require.True(t, decision.StickySessionHit)
	require.False(t, decision.StickyUserFallbackHit)
	require.Equal(t, shared.ID, cache.bindings[userStickyKey(userStickyUserID)], "sessions bound before rollout gain a user binding")
}

func TestOpenAIUserStickyRequiresAuthenticatedUser(t *testing.T) {
	svc, cache, shared, _ := newUserStickyFixture(t, userStickyOld)
	require.NoError(t, svc.BindStickySessionForUser(context.Background(), ptrInt64(userStickyOld), 0, userStickyHash, shared.ID))
	require.Equal(t, shared.ID, cache.bindings[groupStickyKey(userStickyOld)])
	require.Len(t, cache.bindings, 1, "no shared user-0 binding")
	require.Empty(t, openAIUserStickyCacheKey(0, userStickyHash))
	require.Empty(t, openAIUserStickyCacheKey(userStickyUserID, " "))
}

func highFiveHourUsage() map[string]any {
	return map[string]any{
		"codex_5h_used_percent": 99.0,
		"codex_5h_reset_at":     time.Now().Add(2 * time.Hour).Format(time.RFC3339),
	}
}

func TestOpenAIUserStickyKeepsHighUsageAccountButLoadBalancingDoesNot(t *testing.T) {
	svc, cache, shared, preferred := newUserStickyFixture(t, userStickyOld, userStickyNew)
	shared.Extra, shared.Priority = highFiveHourUsage(), 0
	preferred.Priority = 10
	svc.accountRepo = groupFilteredOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: []Account{shared, preferred}}}

	got, decision := selectForUser(t, svc, userStickyNew, userStickyUserID, nil)
	require.Equal(t, preferred.ID, got.ID, "without any binding the high-usage account takes no new session")
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)

	cache.bindings = map[string]int64{userStickyKey(userStickyUserID): shared.ID}
	got, decision = selectForUser(t, svc, userStickyNew, userStickyUserID, nil)
	require.Equal(t, shared.ID, got.ID, "historical affinity keeps the session on its account")
	require.True(t, decision.StickyUserFallbackHit)
}

func TestOpenAIUserStickySkipsUnschedulableAccountWithoutDeleting(t *testing.T) {
	svc, cache, shared, preferred := newUserStickyFixture(t, userStickyOld, userStickyNew)
	shared.Schedulable = false
	svc.accountRepo = groupFilteredOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: []Account{shared, preferred}}}
	cache.bindings[userStickyKey(userStickyUserID)] = shared.ID

	got, decision := selectForUser(t, svc, userStickyNew, userStickyUserID, nil)
	require.Equal(t, preferred.ID, got.ID)
	require.False(t, decision.StickyUserFallbackHit)
	require.Zero(t, cache.deleted[userStickyKey(userStickyUserID)])
}

func TestOpenAIGroupStickyWinsOverUserBinding(t *testing.T) {
	svc, cache, shared, preferred := newUserStickyFixture(t, userStickyOld, userStickyNew)
	shared.Priority, preferred.Priority = 0, 10
	svc.accountRepo = groupFilteredOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: []Account{shared, preferred}}}
	cache.bindings[groupStickyKey(userStickyNew)] = preferred.ID
	cache.bindings[userStickyKey(userStickyUserID)] = shared.ID

	got, decision := selectForUser(t, svc, userStickyNew, userStickyUserID, nil)
	require.Equal(t, preferred.ID, got.ID)
	require.True(t, decision.StickySessionHit)
	require.False(t, decision.StickyUserFallbackHit)
	require.Equal(t, preferred.ID, cache.bindings[userStickyKey(userStickyUserID)])
}

type failingGroupWriteCache struct{ *groupedGatewayCache }

func (c failingGroupWriteCache) SetSessionAccountID(ctx context.Context, groupID int64, key string, accountID int64, ttl time.Duration) error {
	if groupID != 0 {
		return errors.New("group write failed")
	}
	return c.groupedGatewayCache.SetSessionAccountID(ctx, groupID, key, accountID, ttl)
}

func TestOpenAIUserStickyWriteIsIndependentOfGroupWrite(t *testing.T) {
	svc, cache, shared, _ := newUserStickyFixture(t, userStickyOld)
	svc.cache = failingGroupWriteCache{cache}
	err := svc.BindStickySessionForUser(context.Background(), ptrInt64(userStickyOld), userStickyUserID, userStickyHash, shared.ID)
	require.ErrorContains(t, err, "group write failed")
	require.Equal(t, shared.ID, cache.bindings[userStickyKey(userStickyUserID)])
}
