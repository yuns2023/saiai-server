//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTrustedUserAccountScope(t *testing.T) {
	for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey} {
		for _, mode := range []string{"carpool", "shared", "pinned", "single_device"} {
			t.Run(typ+"/"+mode, func(t *testing.T) {
				a := &Account{Platform: PlatformAnthropic, Type: typ, Extra: map[string]any{"claude_oauth_mode": mode, "claude_oauth_carpool_trusted_only": true}}
				restricted := mode == "carpool" && typ != AccountTypeAPIKey
				require.Equal(t, restricted, a.IsTrustedUserOnly())
				require.Equal(t, !restricted, accountAllowsUser(context.Background(), a))
				require.True(t, accountAllowsUser(WithTrustedUser(context.Background(), true), a))
			})
		}
	}
	require.False(t, (*Account)(nil).IsTrustedUserOnly())
	require.False(t, (&Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}).IsTrustedUserOnly())
}

func TestTrustedUserSelection(t *testing.T) {
	// Exercise both selectors, native and mixed platform paths, model routes,
	// confirmed/pending/prefetched sticky, and removal of the only fallback.
	for _, load := range []bool{false, true} {
		for _, mixed := range []bool{false, true} {
			for _, route := range []bool{false, true} {
				for _, binding := range []string{"none", "confirmed", "pending", "prefetched"} {
					for _, trusted := range []bool{false, true} {
						for _, fallback := range []bool{false, true} {
							name := fmt.Sprintf("load=%v/mixed=%v/route=%v/binding=%s/trusted=%v/fallback=%v", load, mixed, route, binding, trusted, fallback)
							t.Run(name, func(t *testing.T) {
								groupID := int64(7)
								restricted := Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 5, Priority: 1, AccountGroups: []AccountGroup{{GroupID: groupID}}, Extra: map[string]any{"claude_oauth_mode": "carpool", "claude_oauth_carpool_trusted_only": true}}
								normal := restricted
								normal.ID = 2
								normal.Priority = 2
								normal.Extra = nil
								repo := &mockAccountRepoForPlatform{accounts: []Account{restricted}, accountsByID: map[int64]*Account{1: &restricted, 2: &normal}}
								if fallback {
									repo.accounts = append(repo.accounts, normal)
								}
								cache := &oauthStickyGatewayCacheStub{}
								session := ""
								if binding != "none" {
									session = "existing-session"
								}
								if binding == "confirmed" || binding == "prefetched" {
									cache.binding = 1
								}
								if binding == "pending" {
									cache.pendingBinding = 1
								}
								cfg := testConfig()
								cfg.Gateway.Scheduling.LoadBatchEnabled = load
								group := &Group{ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true, ModelRoutingEnabled: route, ModelRouting: map[string][]int64{"claude-test": {1}}}
								svc := &GatewayService{accountRepo: repo, cache: cache, cfg: cfg, groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}}}
								if load {
									svc.concurrencyService = NewConcurrencyService(stubConcurrencyCache{})
								}
								ctx := WithTrustedUser(context.Background(), trusted)
								if !mixed {
									ctx = context.WithValue(ctx, ctxkey.ForcePlatform, PlatformAnthropic)
								}
								if binding == "prefetched" {
									ctx = WithPrefetchedStickySession(ctx, 1, groupID, false)
								}
								result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, session, "claude-test", nil, "")
								if !trusted && !fallback {
									require.ErrorIs(t, err, ErrNoAvailableAccounts)
									require.Nil(t, result)
									return
								}
								require.NoError(t, err)
								expected := int64(2)
								if trusted {
									expected = 1
								}
								require.Equal(t, expected, result.Account.ID)
								if result.ReleaseFunc != nil {
									result.ReleaseFunc()
								}
								if !trusted {
									require.NotEqual(t, int64(1), cache.pendingSetAccount, "must never create a restricted pending binding")
								}
							})
						}
					}
				}
			}
		}
	}
}

func TestTrustedUserRevocationAndAccountToggle(t *testing.T) {
	account := Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 5, Extra: map[string]any{"claude_oauth_carpool_trusted_only": true}}
	repo := &mockAccountRepoForPlatform{accounts: []Account{account}, accountsByID: map[int64]*Account{1: &account}}
	cache := &oauthStickyGatewayCacheStub{binding: 1}
	svc := &GatewayService{accountRepo: repo, cache: cache, cfg: testConfig()}
	_, err := svc.SelectAccount(WithTrustedUser(context.Background(), true), nil, "session")
	require.NoError(t, err)
	_, err = svc.SelectAccount(context.Background(), nil, "session")
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	account.Extra["claude_oauth_carpool_trusted_only"] = false
	_, err = svc.SelectAccount(context.Background(), nil, "session")
	require.NoError(t, err)
	account.Extra["claude_oauth_carpool_trusted_only"] = true
	_, err = svc.SelectAccount(context.Background(), nil, "session")
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	stats := svc.collectSelectionFailureStats(context.Background(), repo.accounts, "", PlatformAnthropic, nil, true)
	require.Equal(t, 1, stats.TrustedUserRequired)
}

func TestTrustedUserForwardGuard(t *testing.T) {
	a := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"claude_oauth_carpool_trusted_only": true}}
	svc := &GatewayService{}
	_, err := svc.Forward(context.Background(), nil, a, &ParsedRequest{})
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	err = svc.ForwardCountTokens(context.Background(), c, a, &ParsedRequest{})
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestTrustedUserAuthSnapshot(t *testing.T) {
	svc := &APIKeyService{}
	for _, trusted := range []bool{false, true} {
		original := &APIKey{ID: 1, UserID: 7, User: &User{ID: 7, IsTrusted: trusted}}
		data, err := json.Marshal(svc.snapshotFromAPIKey(original))
		require.NoError(t, err)
		var snapshot APIKeyAuthSnapshot
		require.NoError(t, json.Unmarshal(data, &snapshot))
		require.Equal(t, trusted, svc.snapshotToAPIKey("test-key", &snapshot).User.IsTrusted)
	}
	var old APIKeyAuthSnapshot
	require.NoError(t, json.Unmarshal([]byte(`{"user":{"id":7}}`), &old))
	require.False(t, svc.snapshotToAPIKey("test-key", &old).User.IsTrusted)
}

func TestAdminService_UpdateUserTrustInvalidatesCache(t *testing.T) {
	repo := &balanceUserRepoStub{userRepoStub: &userRepoStub{user: &User{ID: 7, Status: StatusActive}}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{userRepo: repo, authCacheInvalidator: invalidator}
	for _, trusted := range []bool{true, false} {
		updated, err := svc.UpdateUser(context.Background(), 7, &UpdateUserInput{IsTrusted: &trusted})
		require.NoError(t, err)
		require.Equal(t, trusted, updated.IsTrusted)
	}
	require.Equal(t, []int64{7, 7}, invalidator.userIDs)
	_, err := svc.UpdateUser(context.Background(), 7, &UpdateUserInput{})
	require.NoError(t, err)
	require.Len(t, invalidator.userIDs, 2)
}

func TestSpecifiedUserSelection(t *testing.T) {
	// Exercise both selectors, native and mixed platform paths, model routes,
	// confirmed/pending/prefetched sticky, and removal of the only fallback.
	for _, load := range []bool{false, true} {
		for _, mixed := range []bool{false, true} {
			for _, route := range []bool{false, true} {
				for _, binding := range []string{"none", "confirmed", "pending", "prefetched"} {
					for _, trusted := range []bool{false, true} {
						for _, fallback := range []bool{false, true} {
							name := fmt.Sprintf("load=%v/mixed=%v/route=%v/binding=%s/trusted=%v/fallback=%v", load, mixed, route, binding, trusted, fallback)
							t.Run(name, func(t *testing.T) {
								groupID := int64(7)
								restricted := Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 5, Priority: 1, AccountGroups: []AccountGroup{{GroupID: groupID}}, Extra: map[string]any{"claude_oauth_mode": "carpool", "claude_oauth_carpool_users_only": true}}
								restricted.CarpoolUserIDs = []int64{7}
								normal := restricted
								normal.ID = 2
								normal.Priority = 2
								normal.Extra = nil
								repo := &mockAccountRepoForPlatform{accounts: []Account{restricted}, accountsByID: map[int64]*Account{1: &restricted, 2: &normal}}
								if fallback {
									repo.accounts = append(repo.accounts, normal)
								}
								cache := &oauthStickyGatewayCacheStub{}
								session := ""
								if binding != "none" {
									session = "existing-session"
								}
								if binding == "confirmed" || binding == "prefetched" {
									cache.binding = 1
								}
								if binding == "pending" {
									cache.pendingBinding = 1
								}
								cfg := testConfig()
								cfg.Gateway.Scheduling.LoadBatchEnabled = load
								group := &Group{ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true, ModelRoutingEnabled: route, ModelRouting: map[string][]int64{"claude-test": {1}}}
								svc := &GatewayService{accountRepo: repo, cache: cache, cfg: cfg, groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}}}
								if load {
									svc.concurrencyService = NewConcurrencyService(stubConcurrencyCache{})
								}
								id := int64(8)
								if trusted {
									id = 7
								}
								ctx := WithAccountUserID(context.Background(), id)
								if !mixed {
									ctx = context.WithValue(ctx, ctxkey.ForcePlatform, PlatformAnthropic)
								}
								if binding == "prefetched" {
									ctx = WithPrefetchedStickySession(ctx, 1, groupID, false)
								}
								result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, session, "claude-test", nil, "")
								if !trusted && !fallback {
									require.ErrorIs(t, err, ErrNoAvailableAccounts)
									require.Nil(t, result)
									return
								}
								require.NoError(t, err)
								expected := int64(2)
								if trusted {
									expected = 1
								}
								require.Equal(t, expected, result.Account.ID)
								if result.ReleaseFunc != nil {
									result.ReleaseFunc()
								}
								if !trusted {
									require.NotEqual(t, int64(1), cache.pendingSetAccount, "must never create a restricted pending binding")
								}
							})
						}
					}
				}
			}
		}
	}
}

func TestCarpoolUserConditionsIntersection(t *testing.T) {
	for _, trustSwitch := range []bool{false, true} {
		for _, listSwitch := range []bool{false, true} {
			for _, trusted := range []bool{false, true} {
				for _, member := range []bool{false, true} {
					a := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, CarpoolUserIDs: []int64{7}, Extra: map[string]any{"claude_oauth_mode": "carpool", "claude_oauth_carpool_trusted_only": trustSwitch, "claude_oauth_carpool_users_only": listSwitch}}
					id := int64(8)
					if member {
						id = 7
					}
					ctx := WithAccountUserID(WithTrustedUser(context.Background(), trusted), id)
					require.Equal(t, (!trustSwitch || trusted) && (!listSwitch || member), accountAllowsUser(ctx, a))
					if listSwitch {
						a.CarpoolUserIDs = nil
						require.False(t, accountAllowsUser(ctx, a))
						require.False(t, accountAllowsUser(context.Background(), a))
					}
				}
			}
		}
	}
}
