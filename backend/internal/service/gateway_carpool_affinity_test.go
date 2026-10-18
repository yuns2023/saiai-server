//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const affinityMetadata = `{"device_id":"device-a","account_uuid":"user-a","session_id":"session-a"}`

type affinityCacheStub struct {
	IdentityCache
	membership map[int64]bool
	err        error
	calls      int
	ids        []int64
	device     string
}

func (c *affinityCacheStub) GetCarpoolDeviceMembership(_ context.Context, ids []int64, device string) (map[int64]bool, error) {
	c.calls++
	c.ids = append(c.ids, ids...)
	c.device = device
	return c.membership, c.err
}

func TestCarpoolAffinitySelection(t *testing.T) {
	for _, path := range []string{"load", "legacy", "load-error", "routed", "routed-legacy"} {
		for _, scenario := range []string{"reuse", "priority", "sticky", "pending", "excluded", "rate-limited", "high-usage", "cache-error", "full-load", "slot-race", "all-busy", "route-boundary"} {
			t.Run(path+"/"+scenario, func(t *testing.T) {
				accounts := []Account{
					{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 10},
					{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 10},
				}
				cache := &affinityCacheStub{membership: map[int64]bool{1: true}}
				concurrency := &mockConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{
					1: {AccountID: 1, LoadRate: 70}, 2: {AccountID: 2, LoadRate: 0},
				}}
				sticky := &oauthStickyGatewayCacheStub{}
				cfg := testConfig()
				cfg.Gateway.Scheduling.LoadBatchEnabled = path != "legacy" && path != "routed-legacy"
				if path == "load-error" {
					concurrency.loadBatchErr = errors.New("unavailable")
				}
				var excluded map[int64]struct{}
				want := int64(1)
				switch scenario {
				case "priority":
					accounts[0].Priority = 10
					want = 2
				case "sticky":
					sticky.binding = 2
					want = 2
				case "pending":
					sticky.pendingBinding = 2
					want = 2
				case "excluded":
					excluded = map[int64]struct{}{1: {}}
					want = 2
				case "rate-limited":
					until := time.Now().Add(time.Hour)
					accounts[0].RateLimitResetAt = &until
					want = 2
				case "high-usage":
					accounts[0].Extra = map[string]any{"session_window_utilization": 0.95}
					until := time.Now().Add(time.Hour)
					accounts[0].SessionWindowEnd = &until
					want = 2
				case "cache-error":
					cache.err = errors.New("unavailable")
					if path != "load" && path != "routed" {
						accounts[0].Priority = 10
					}
					want = 2
				case "full-load":
					if path != "load" && path != "routed" {
						t.Skip("load-aware path only")
					}
					concurrency.loadMap[1].LoadRate = 100
					want = 2
				case "slot-race":

					concurrency.acquireResults = map[int64]bool{1: false, 2: true}
					want = 2
				case "all-busy":
					concurrency.acquireResults = map[int64]bool{1: false, 2: false}
					// Both records exist, so exhaustion must retain a wait plan.
					cache.membership[2] = true
					accounts[1].Priority = 10
				case "route-boundary":
					if path != "routed" && path != "routed-legacy" {
						t.Skip("routing paths only")
					}
					want = 2
				}
				repo := &mockAccountRepoForPlatform{accounts: accounts, accountsByID: map[int64]*Account{1: &accounts[0], 2: &accounts[1]}}
				svc := &GatewayService{accountRepo: repo, cache: sticky, cfg: cfg,
					identityService: &IdentityService{cache: cache}, concurrencyService: NewConcurrencyService(concurrency)}
				var groupID *int64
				if path == "routed" || path == "routed-legacy" {
					id := int64(9)
					groupID = &id
					routes := []int64{1, 2}
					if scenario == "route-boundary" {
						routes = []int64{2}
					}
					group := &Group{ID: id, Platform: PlatformAnthropic, Status: StatusActive, ModelRoutingEnabled: true, ModelRouting: map[string][]int64{"claude-sonnet-4-5": routes}}
					svc.groupRepo = &mockGroupRepoForGateway{groups: map[int64]*Group{id: group}}
					for i := range accounts {
						accounts[i].AccountGroups = []AccountGroup{{GroupID: id}}
					}
				}
				result, err := svc.SelectAccountWithLoadAwareness(context.Background(), groupID, "session", "claude-sonnet-4-5", excluded, affinityMetadata)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, want, result.Account.ID)
				if scenario == "all-busy" {
					require.NotNil(t, result.WaitPlan)
				}
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
				if scenario == "sticky" || scenario == "pending" {
					require.Zero(t, cache.calls)
				}
			})
		}
	}
}

func TestCarpoolAffinityScopeAndMemoization(t *testing.T) {
	cache := &affinityCacheStub{membership: map[int64]bool{1: true, 2: true, 3: true, 4: true}}
	svc := &GatewayService{identityService: &IdentityService{cache: cache}}
	accounts := []*Account{
		{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
		{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"claude_oauth_mode": ClaudeOAuthModeShared}},
		{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"claude_oauth_carpool_unlimited_devices": true}},
		{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
		{ID: 5, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"claude_oauth_mode": ClaudeOAuthModePinned}},
		{ID: 6, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"claude_oauth_mode": ClaudeOAuthModeSingleDevice}},
	}
	ctx := withCarpoolAffinity(context.Background(), affinityMetadata)
	membership := svc.carpoolMembership(ctx, accounts)
	require.Equal(t, map[int64]bool{1: true}, membership)
	require.Equal(t, []int64{1}, cache.ids)
	require.Equal(t, "device-a", cache.device)
	svc.carpoolMembership(ctx, accounts)
	require.Equal(t, 1, cache.calls)
	cache.membership = nil
	require.False(t, svc.carpoolMembership(withCarpoolAffinity(context.Background(), affinityMetadata), accounts)[1])
	require.Equal(t, 2, cache.calls)
	for _, metadata := range []string{"", "invalid", `{"session_id":"session"}`} {
		require.Nil(t, svc.carpoolMembership(withCarpoolAffinity(context.Background(), metadata), accounts))
	}
	require.Equal(t, 2, cache.calls)
}

func TestCarpoolAffinityLoadAndRandomTies(t *testing.T) {
	cache := &affinityCacheStub{membership: map[int64]bool{1: true, 2: true, 3: true}}
	svc := &GatewayService{identityService: &IdentityService{cache: cache}}
	candidates := []accountWithLoad{
		{account: &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
		{account: &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
		{account: &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth}, loadInfo: &AccountLoadInfo{LoadRate: 80}},
		{account: &Account{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeOAuth}, loadInfo: &AccountLoadInfo{LoadRate: 0}},
	}
	ctx := withCarpoolAffinity(context.Background(), affinityMetadata)
	seen := map[int64]bool{}
	for i := 0; i < 200; i++ {
		pool := filterByMinLoadRate(svc.preferCarpoolWithLoad(ctx, filterByMinPriority(candidates)))
		selected := selectRandomAccountWithLoad(pool, false)
		require.Contains(t, []int64{1, 2}, selected.account.ID)
		seen[selected.account.ID] = true
	}
	require.Len(t, seen, 2)
	require.Equal(t, 1, cache.calls)
}

func TestCarpoolAffinityFullRegistryReadOnly(t *testing.T) {
	cache := &identityCacheStub{
		carpoolDevices: map[string]*CarpoolDeviceRecord{
			carpoolDeviceCacheKey(1, "device-a"):     {OriginalDeviceID: "device-a", LastSeenAt: 10},
			carpoolDeviceCacheKey(2, "other-device"): {OriginalDeviceID: "other-device", LastSeenAt: 20},
		},
		carpoolOverflow: map[string]*CarpoolOverflowRecord{
			carpoolDeviceCacheKey(2, "device-a"): {OriginalDeviceID: "device-a"},
		},
	}
	ctx := context.Background()
	svc := &GatewayService{identityService: &IdentityService{cache: cache}}
	accounts := []*Account{
		{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
		{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeSetupToken},
	}
	membership := svc.carpoolMembership(withCarpoolAffinity(ctx, affinityMetadata), accounts)
	require.True(t, membership[1], "a full registry still permits the admitted device")
	require.False(t, membership[2], "overflow does not establish affinity")
	require.Len(t, cache.carpoolDevices, 2)
	require.Equal(t, int64(10), cache.carpoolDevices[carpoolDeviceCacheKey(1, "device-a")].LastSeenAt)
}
