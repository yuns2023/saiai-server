//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// ---- fakes ----

type fakeMarketStore struct {
	mu         sync.Mutex
	state      *MarketDispatchState
	facts      func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts
	admitErr   error
	confirm    *MarketAdmission
	confirmErr error
	requests   []*MarketAdmissionRequest
	inserted   []*MarketAdmission
	members    int
}

func (f *fakeMarketStore) LoadMarketDispatchState(context.Context) (*MarketDispatchState, error) {
	return f.state, nil
}

func (f *fakeMarketStore) AdmitMarketCandidate(_ context.Context, req *MarketAdmissionRequest, evaluate func(*MarketAuthoritativeFacts) string) (*MarketAdmissionVerdict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copyReq := *req
	f.requests = append(f.requests, &copyReq)
	if f.admitErr != nil {
		return nil, f.admitErr
	}
	var facts *MarketAuthoritativeFacts
	if f.facts != nil {
		facts = f.facts(req)
	} else {
		// Default: a platform admin member of the requested tier.
		acc := mktPlatformAccount(req.AccountID, req.GroupID)
		facts = &MarketAuthoritativeFacts{AccountFound: true, Account: &acc, MembershipSource: AccountGroupSourceAdmin,
			GroupFound: true, GroupMarketTier: true, GroupActive: true, GroupPlatform: PlatformAnthropic}
	}
	if reason := evaluate(facts); reason != "" {
		return &MarketAdmissionVerdict{Reason: reason, Facts: facts}, nil
	}
	v := &MarketAdmissionVerdict{Admitted: true, Facts: facts}
	if req.Class == MarketClassBuyer {
		a := &MarketAdmission{ID: int64(len(f.inserted) + 100), AttemptID: req.AttemptID, RequestRef: req.RequestRef,
			AccountID: req.AccountID, GroupID: req.GroupID, OwnerUserID: *facts.Account.OwnerUserID,
			OwnerEventID: facts.LatestOwnerEventID, ListingVersion: facts.Listing.Version,
			BuyerUserID: req.AuthUserID, APIKeyID: req.APIKeyID, AdmittedAt: time.Now()}
		f.inserted = append(f.inserted, a)
		v.Admission = a
	}
	return v, nil
}

func (f *fakeMarketStore) GetMarketAdmissionByAttempt(_ context.Context, attemptID string) (*MarketAdmission, error) {
	if f.confirmErr != nil {
		return nil, f.confirmErr
	}
	if f.confirm != nil && f.confirm.AttemptID == attemptID {
		return f.confirm, nil
	}
	return nil, ErrMarketAdmissionNotFound
}

func (f *fakeMarketStore) CountMarketTierPlatformMembers(context.Context, int64) (int, error) {
	return f.members, nil
}

type fakeMarketUsage struct {
	mu   sync.Mutex
	obs  map[int64]*MarketUsageObservations
	gets int
}

func (u *fakeMarketUsage) RecordMarketUsageObservations(_ context.Context, id int64, obs MarketUsageObservations) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.obs == nil {
		u.obs = map[int64]*MarketUsageObservations{}
	}
	o := obs
	u.obs[id] = &o
	return nil
}

func (u *fakeMarketUsage) GetMarketUsageObservations(_ context.Context, ids []int64) (map[int64]*MarketUsageObservations, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.gets++
	out := map[int64]*MarketUsageObservations{}
	for _, id := range ids {
		out[id] = u.obs[id]
	}
	return out, nil
}

// dualSlotCache is an in-memory total + buyer slot store with the Lua
// semantics (the Redis implementation is covered by integration tests).
type dualSlotCache struct {
	*mockConcurrencyCache
	mu            sync.Mutex
	total, buyer  map[int64]map[string]bool
	plainAcquires int
	buyerAcquires int
}

func newDualSlotCache() *dualSlotCache {
	return &dualSlotCache{mockConcurrencyCache: &mockConcurrencyCache{}, total: map[int64]map[string]bool{}, buyer: map[int64]map[string]bool{}}
}

func (c *dualSlotCache) set(m map[int64]map[string]bool, id int64) map[string]bool {
	if m[id] == nil {
		m[id] = map[string]bool{}
	}
	return m[id]
}

func (c *dualSlotCache) AcquireAccountSlot(_ context.Context, id int64, max int, req string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.plainAcquires++
	t := c.set(c.total, id)
	if len(t) >= max {
		return false, nil
	}
	t[req] = true
	return true, nil
}

func (c *dualSlotCache) ReleaseAccountSlot(_ context.Context, id int64, req string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.set(c.total, id), req)
	return nil
}

func (c *dualSlotCache) AcquireAccountBuyerSlot(_ context.Context, id int64, totalMax, buyerMax int, req string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buyerAcquires++
	t, b := c.set(c.total, id), c.set(c.buyer, id)
	if totalMax < 1 || buyerMax < 1 || len(t) >= totalMax || len(b) >= buyerMax {
		return false, nil
	}
	t[req], b[req] = true, true
	return true, nil
}

func (c *dualSlotCache) ReleaseAccountBuyerSlot(_ context.Context, id int64, req string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.set(c.total, id), req)
	delete(c.set(c.buyer, id), req)
	return nil
}

func (c *dualSlotCache) RenewAccountBuyerSlot(_ context.Context, id int64, req string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.set(c.total, id)[req] && c.set(c.buyer, id)[req], nil
}

func (c *dualSlotCache) GetAccountBuyerConcurrencyBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[int64]int{}
	for _, id := range ids {
		out[id] = len(c.buyer[id])
	}
	return out, nil
}

func (c *dualSlotCache) SlotTTLSeconds() int { return 900 }

func (c *dualSlotCache) GetAccountsLoadBatch(_ context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[int64]*AccountLoadInfo{}
	for _, a := range accounts {
		cur := len(c.total[a.ID])
		rate := 0
		if a.MaxConcurrency > 0 {
			rate = cur * 100 / a.MaxConcurrency
		}
		out[a.ID] = &AccountLoadInfo{AccountID: a.ID, CurrentConcurrency: cur, LoadRate: rate}
	}
	return out, nil
}

func (c *dualSlotCache) counts(id int64) (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.total[id]), len(c.buyer[id])
}

// ---- fixture ----

const (
	mktTier   = int64(30)
	mktPlain  = int64(31)
	mktHosted = int64(32)
	mktOwner  = int64(7)
	mktBuyer  = int64(9)
	mktKey    = int64(77)
)

func mktProtection() MarketProtectionParams {
	return MarketProtectionParams{ReservedConcurrency: 1, ReservedDevices: 2, ReservedAdmissions: 1, Cap5hPct: 70, Cap7dPct: 60, SampleMaxAgeMinutes: 30}
}

func mktListedAccount(id int64) Account {
	owner := mktOwner
	return Account{
		ID: id, Name: "listed", Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive, Schedulable: true,
		Concurrency: 4, OwnerUserID: &owner,
		Credentials:    map[string]any{"access_token": "setup-token"},
		Extra:          map[string]any{"claude_oauth_mode": ClaudeOAuthModeCarpool, "claude_oauth_carpool_device_limit": 5},
		MarketGroupIDs: []int64{mktTier},
		MarketListing:  &AccountMarketListing{GroupID: mktTier, OwnerUserID: mktOwner, Version: 3, Protection: mktProtection()},
	}
}

func mktPlatformAccount(id int64, groups ...int64) Account {
	ags := make([]AccountGroup, 0, len(groups))
	for _, g := range groups {
		ags = append(ags, AccountGroup{GroupID: g})
	}
	return Account{ID: id, Name: "platform", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Concurrency: 4, GroupIDs: groups, AccountGroups: ags, Credentials: map[string]any{"api_key": "k"}}
}

func mktGroups() map[int64]*Group {
	return map[int64]*Group{
		mktTier:   {ID: mktTier, Name: "T1", Platform: PlatformAnthropic, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1.2, MarketTier: true, Hydrated: true},
		mktPlain:  {ID: mktPlain, Name: "plain", Platform: PlatformAnthropic, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1, Hydrated: true},
		mktHosted: {ID: mktHosted, Name: "hosted", Platform: PlatformAnthropic, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1, HostedUsageEnabled: true, IsExclusive: true, Hydrated: true},
	}
}

func freshObservations() *MarketUsageObservations {
	now := time.Now()
	return &MarketUsageObservations{
		FiveHour: &MarketUsageObservation{Utilization: 0.2, ResetAt: now.Add(2 * time.Hour).Unix(), ObservedAt: now.Unix()},
		SevenDay: &MarketUsageObservation{Utilization: 0.3, ResetAt: now.Add(72 * time.Hour).Unix(), ObservedAt: now.Unix()},
	}
}

type marketEnv struct {
	svc   *GatewayService
	slots *dualSlotCache
	store *fakeMarketStore
	usage *fakeMarketUsage
	rt    *MarketRuntime
}

func newMarketEnv(t *testing.T, accounts []Account) *marketEnv {
	t.Helper()
	byID := map[int64]*Account{}
	for i := range accounts {
		byID[accounts[i].ID] = &accounts[i]
	}
	slots := newDualSlotCache()
	usage := &fakeMarketUsage{obs: map[int64]*MarketUsageObservations{}}
	for _, a := range accounts {
		if a.MarketListing != nil {
			usage.obs[a.ID] = freshObservations()
		}
	}
	store := &fakeMarketStore{}
	rt := NewMarketRuntime(store, usage)
	listed := []int64{}
	for _, a := range accounts {
		if a.MarketListing != nil {
			listed = append(listed, a.ID)
		}
	}
	rt.SetState(&MarketDispatchState{Enabled: true, ListedAccountIDs: listed})
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc := &GatewayService{
		accountRepo:        &mockAccountRepoForPlatform{accounts: accounts, accountsByID: byID},
		groupRepo:          &mockGroupRepoForGateway{groups: mktGroups()},
		cache:              &oauthStickyGatewayCacheStub{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(slots),
		billingService:     NewBillingService(&config.Config{}, nil),
		market:             rt,
	}
	return &marketEnv{svc: svc, slots: slots, store: store, usage: usage, rt: rt}
}

func buyerCtx(groupID int64) context.Context {
	gid := groupID
	key := &APIKey{ID: mktKey, UserID: mktBuyer, GroupID: &gid, User: &User{ID: mktBuyer, Status: StatusActive}}
	ctx := WithAccountUserID(context.Background(), mktBuyer)
	ctx = WithAccountAPIKeyID(ctx, mktKey)
	return WithMarketDispatch(ctx, key)
}

func ptr64(v int64) *int64 { return &v }

// ---- classification ----

func TestMarketClassification(t *testing.T) {
	env := newMarketEnv(t, nil)
	svc := env.svc
	groups := mktGroups()
	at := func(ctx context.Context, groupID int64) context.Context {
		if groupID > 0 {
			ctx = context.WithValue(ctx, ctxkey.Group, groups[groupID])
			return withMarketRequestedModel(withMarketSelectionGroup(ctx, ptr64(groupID)), "claude-fable-5")
		}
		return withMarketRequestedModel(withMarketSelectionGroup(ctx, nil), "claude-fable-5")
	}
	env.usage.obs[1] = freshObservations()
	listed := mktListedAccount(1)
	platform := mktPlatformAccount(2, mktTier)
	owned := mktPlatformAccount(3, mktTier)
	owned.OwnerUserID = ptr64(mktOwner)
	plain := mktPlatformAccount(4, mktPlain)
	listedInPlain := mktListedAccount(5)
	listedInPlain.GroupIDs = []int64{mktPlain}
	listedInHosted := mktListedAccount(6)
	listedInHosted.GroupIDs = []int64{mktHosted}
	env.usage.obs[6] = freshObservations()
	pinned := mktPlatformAccount(8, mktTier)
	pinned.Type = AccountTypeSetupToken
	pinned.Extra = map[string]any{"claude_oauth_mode": ClaudeOAuthModePinned}
	antigravity := mktPlatformAccount(10, mktTier)
	antigravity.Platform = PlatformAntigravity

	class := func(ctx context.Context, a Account) string {
		c, _ := svc.classifyMarketCandidate(ctx, &a)
		return c
	}
	buyer := at(buyerCtx(mktTier), mktTier)
	require.Equal(t, MarketClassBuyer, class(buyer, listed))
	require.Equal(t, MarketClassPlatform, class(buyer, platform))
	require.Equal(t, MarketClassExcluded, class(buyer, owned), "an admin tier member with an owner is no longer platform")
	require.Equal(t, MarketClassExcluded, class(buyer, plain), "not a member of the tier")
	require.Equal(t, MarketClassExcluded, class(buyer, pinned), "pinned mode is not supported in tiers")
	require.Equal(t, MarketClassExcluded, class(buyer, antigravity))

	// No final admission on this path (count_tokens, ops retry, gemini): no buyer.
	noDispatch := at(WithAccountUserID(context.Background(), mktBuyer), mktTier)
	require.Equal(t, MarketClassExcluded, class(noDispatch, listed))
	require.Equal(t, MarketClassPlatform, class(noDispatch, platform))

	// Outside the tier.
	inPlain := at(buyerCtx(mktPlain), mktPlain)
	require.Equal(t, MarketClassNone, class(inPlain, plain), "non-market accounts keep the old path")
	require.Equal(t, MarketClassExcluded, class(inPlain, listedInPlain), "a listed account never serves another group")
	ungrouped := at(buyerCtx(0), 0)
	require.Equal(t, MarketClassExcluded, class(ungrouped, listed))
	require.Equal(t, MarketClassNone, class(ungrouped, mktPlatformAccount(11)))

	// Owner path: the owner's own exclusive hosted group only.
	ownerCtx := at(WithAccountUserID(context.Background(), mktOwner), mktHosted)
	c, sel := svc.classifyMarketCandidate(ownerCtx, &listedInHosted)
	require.Equal(t, MarketClassOwner, c)
	require.Equal(t, mktOwner, sel.OwnerUserID)
	otherUser := at(WithAccountUserID(context.Background(), 99), mktHosted)
	require.Equal(t, MarketClassExcluded, class(otherUser, listedInHosted), "a trusted co-user of the hosted group is not the owner")

	// Self purchase stays on the buyer path (no reservation).
	self := at(WithMarketDispatch(WithAccountUserID(context.Background(), mktOwner), &APIKey{ID: 5, UserID: mktOwner, User: &User{ID: mktOwner}}), mktTier)
	c, sel = svc.classifyMarketCandidate(self, &listed)
	require.Equal(t, MarketClassBuyer, c)
	require.True(t, sel.SelfPurchase)
	require.Equal(t, 3, sel.BuyerMaxConcurrency)

	// Buyer effective cap 0 is excluded, never "unlimited".
	full := mktListedAccount(1)
	full.MarketListing.Protection.ReservedConcurrency = 4
	require.Equal(t, MarketClassExcluded, class(buyer, full))
	// Account-level session limit is not allowed on a listed account.
	sessions := mktListedAccount(1)
	sessions.Extra["max_sessions"] = 2
	require.Equal(t, MarketClassExcluded, class(buyer, sessions))
	// Market switched off: no new buyer selections.
	env.rt.SetState(&MarketDispatchState{Enabled: false, ListedAccountIDs: []int64{1}})
	require.Equal(t, MarketClassExcluded, class(buyer, listed))
	env.rt.SetState(&MarketDispatchState{Enabled: true, ListedAccountIDs: []int64{1}})
	// Simple mode: listed accounts never selected.
	svc.cfg.RunMode = config.RunModeSimple
	require.Equal(t, MarketClassExcluded, class(buyer, listed))
	require.Equal(t, MarketClassNone, class(buyer, plain))
	svc.cfg.RunMode = config.RunModeStandard
}

// The cached copy of a listed account was written back without its market
// fields (old snapshot): the registry still marks it listed.
func TestMarketStaleSnapshotWriteBackNeverDispatches(t *testing.T) {
	stale := mktPlatformAccount(1, mktPlain) // what an old snapshot says
	stale.Type = AccountTypeSetupToken
	stale.OwnerUserID = ptr64(mktOwner)
	other := mktPlatformAccount(2, mktPlain)
	env := newMarketEnv(t, []Account{stale, other})
	env.rt.SetState(&MarketDispatchState{Enabled: true, ListedAccountIDs: []int64{1}})

	for _, groupID := range []*int64{ptr64(mktPlain), nil} {
		for i := 0; i < 20; i++ {
			res, err := env.svc.SelectAccountWithLoadAwareness(buyerCtx(mktPlain), groupID, "", "claude-fable-5", nil, "")
			require.NoError(t, err)
			require.Equal(t, int64(2), res.Account.ID, "group %v", groupID)
			res.ReleaseFunc()
		}
	}
	// Even if something hands it to Forward without a market selection.
	upstream := &queuedClaudeCodeUpstream{responses: []func() *http.Response{okClaudeStreamResponse}}
	env.svc.httpUpstream = upstream
	c, _, parsed := gatewayHTTPRetryRequest(t)
	_, err := env.svc.Forward(c.Request.Context(), c, &stale, parsed)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.ErrorIs(t, err, ErrMarketAdmissionRejected)
	require.Zero(t, upstream.calls)
}

// ---- leases ----

func TestMarketBuyerLeaseThroughSelection(t *testing.T) {
	env := newMarketEnv(t, []Account{mktListedAccount(1)})
	ctx := buyerCtx(mktTier)
	var releases []func()
	for i := 0; i < 3; i++ {
		res, err := env.svc.SelectAccountWithLoadAwareness(ctx, ptr64(mktTier), "", "claude-fable-5", nil, "")
		require.NoError(t, err)
		require.True(t, res.Acquired, "buyer %d", i)
		require.True(t, res.Market.IsBuyer())
		require.Equal(t, 3, res.Market.BuyerMaxConcurrency)
		releases = append(releases, res.ReleaseFunc)
	}
	total, buyers := env.slots.counts(1)
	require.Equal(t, 3, total)
	require.Equal(t, 3, buyers)
	// The owner is idle, yet the 4th buyer gets a buyer wait plan, never a lease.
	res, err := env.svc.SelectAccountWithLoadAwareness(ctx, ptr64(mktTier), "", "claude-fable-5", nil, "")
	require.NoError(t, err)
	require.False(t, res.Acquired)
	require.NotNil(t, res.WaitPlan)
	require.True(t, res.WaitPlan.Market.IsBuyer(), "the wait keeps the buyer class")
	require.Equal(t, 4, res.WaitPlan.MaxConcurrency)
	// The reserved slot is still there for the owner (total < C).
	ownerLease, err := env.svc.concurrencyService.AcquireAccountSlot(ctx, 1, 4)
	require.NoError(t, err)
	require.True(t, ownerLease.Acquired)
	// Initial-serial release + reacquire inherits the class.
	plan := NewFallbackWaitPlan(res, time.Second, 5)
	require.True(t, plan.Market.IsBuyer())
	require.Zero(t, env.slots.plainAcquires-1, "buyers never took a plain lease")
	ownerLease.ReleaseFunc()
	for _, r := range releases {
		r()
	}
	total, buyers = env.slots.counts(1)
	require.Zero(t, total)
	require.Zero(t, buyers)
}

func TestMarketBuyerLoadUsesTighterLimit(t *testing.T) {
	env := newMarketEnv(t, []Account{mktListedAccount(1), mktPlatformAccount(2, mktTier)})
	ctx := withMarketRequestedModel(withMarketSelectionGroup(context.WithValue(buyerCtx(mktTier), ctxkey.Group, mktGroups()[mktTier]), ptr64(mktTier)), "claude-fable-5")
	for i := 0; i < 3; i++ {
		ok, err := env.slots.AcquireAccountBuyerSlot(ctx, 1, 4, 3, string(rune('a'+i)))
		require.NoError(t, err)
		require.True(t, ok)
	}
	listed, platform := mktListedAccount(1), mktPlatformAccount(2, mktTier)
	loads := map[int64]*AccountLoadInfo{1: {AccountID: 1, CurrentConcurrency: 3, LoadRate: 75}, 2: {AccountID: 2, LoadRate: 0}}
	env.svc.adjustMarketBuyerLoads(ctx, []*Account{&listed, &platform}, loads)
	require.Equal(t, 100, loads[1].LoadRate, "buyers full at C-R although the total has room")
	require.Equal(t, 0, loads[2].LoadRate)
}

// ---- share caps ----

func TestEvaluateMarketShareCaps(t *testing.T) {
	now := time.Now()
	p := mktProtection()
	w := func(u float64, resetIn, age time.Duration) *MarketUsageObservation {
		return &MarketUsageObservation{Utilization: u, ResetAt: now.Add(resetIn).Unix(), ObservedAt: now.Add(-age).Unix()}
	}
	ok7 := w(0.3, 72*time.Hour, time.Minute)
	ok5 := w(0.2, 2*time.Hour, time.Minute)
	cases := []struct {
		name string
		obs  *MarketUsageObservations
		want string
	}{
		{"fresh below caps", &MarketUsageObservations{FiveHour: ok5, SevenDay: ok7}, ""},
		{"unknown", nil, "sample_unknown"},
		{"no 7d", &MarketUsageObservations{FiveHour: ok5}, "7d_unknown"},
		{"no 5h", &MarketUsageObservations{SevenDay: ok7}, "5h_unknown"},
		{"stale 7d", &MarketUsageObservations{FiveHour: ok5, SevenDay: w(0.3, 72*time.Hour, 31*time.Minute)}, "7d_stale"},
		{"future 5h", &MarketUsageObservations{SevenDay: ok7, FiveHour: w(0.2, 2*time.Hour, -10*time.Minute)}, "5h_stale"},
		{"NaN", &MarketUsageObservations{SevenDay: ok7, FiveHour: w(math.NaN(), 2*time.Hour, time.Minute)}, "5h_invalid"},
		{"Inf", &MarketUsageObservations{SevenDay: w(math.Inf(1), 72*time.Hour, time.Minute), FiveHour: ok5}, "7d_invalid"},
		{"7d at cap", &MarketUsageObservations{FiveHour: ok5, SevenDay: w(0.60, 72*time.Hour, time.Minute)}, "7d_cap"},
		{"5h at cap", &MarketUsageObservations{SevenDay: ok7, FiveHour: w(0.70, 2*time.Hour, time.Minute)}, "5h_cap"},
		{"5h just below", &MarketUsageObservations{SevenDay: ok7, FiveHour: w(0.6999, 2*time.Hour, time.Minute)}, ""},
		{"observed a few seconds in the future", &MarketUsageObservations{SevenDay: ok7, FiveHour: w(0.2, 2*time.Hour, -5*time.Second)}, "5h_stale"},
		{"ultra flag in the same second as a 5h sample", &MarketUsageObservations{SevenDay: ok7, FiveHour: w(0.9, 2*time.Hour, time.Minute),
			FiveHourAbsent: &MarketFiveHourAbsence{Absent: true, ObservedAt: now.Add(-time.Minute).Unix()}}, "5h_cap"},
		{"window rolled over", &MarketUsageObservations{SevenDay: ok7, FiveHour: w(0.95, -time.Minute, 2*time.Minute)}, ""},
		{"ultra: no 5h needed", &MarketUsageObservations{SevenDay: ok7, FiveHourAbsent: &MarketFiveHourAbsence{Absent: true, ObservedAt: now.Unix()}}, ""},
		{"ultra flag older than a 5h sample", &MarketUsageObservations{SevenDay: ok7, FiveHour: w(0.9, 2*time.Hour, time.Minute),
			FiveHourAbsent: &MarketFiveHourAbsence{Absent: true, ObservedAt: now.Add(-5 * time.Minute).Unix()}}, "5h_cap"},
		{"stale ultra flag", &MarketUsageObservations{SevenDay: ok7, FiveHourAbsent: &MarketFiveHourAbsence{Absent: true, ObservedAt: now.Add(-time.Hour).Unix()}}, "5h_unknown"},
		{"ultra still capped on 7d", &MarketUsageObservations{SevenDay: w(0.61, 72*time.Hour, time.Minute), FiveHourAbsent: &MarketFiveHourAbsence{Absent: true, ObservedAt: now.Unix()}}, "7d_cap"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, evaluateMarketShareCaps(tc.obs, p, now), tc.name)
	}
	p29 := p
	p29.Cap5hPct = 29
	require.Equal(t, "5h_cap", evaluateMarketShareCaps(&MarketUsageObservations{SevenDay: ok7, FiveHour: w(0.29, 2*time.Hour, time.Minute)}, p29, now))
}

func TestBuildMarketUsageObservations(t *testing.T) {
	now := time.Now()
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	h.Set("anthropic-ratelimit-unified-5h-utilization", "0.42")
	h.Set("anthropic-ratelimit-unified-5h-reset", itoa(now.Add(2*time.Hour).Unix()))
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.31")
	h.Set("anthropic-ratelimit-unified-7d-reset", itoa(now.Add(70*time.Hour).Unix()))
	obs := buildMarketUsageObservations(h, sampleAnthropicPassiveUsageHeaders(h, now), now, true)
	require.InDelta(t, 0.42, obs.FiveHour.Utilization, 1e-9)
	require.InDelta(t, 0.31, obs.SevenDay.Utilization, 1e-9)
	require.False(t, obs.FiveHourAbsent.Absent)

	ultra := http.Header{}
	ultra.Set("anthropic-ratelimit-unified-7d-utilization", "0.5")
	ultra.Set("anthropic-ratelimit-unified-7d-reset", itoa(now.Add(70*time.Hour).Unix()))
	obs = buildMarketUsageObservations(ultra, sampleAnthropicPassiveUsageHeaders(ultra, now), now, true)
	require.Nil(t, obs.FiveHour)
	require.True(t, obs.FiveHourAbsent.Absent)
	obs = buildMarketUsageObservations(ultra, sampleAnthropicPassiveUsageHeaders(ultra, now), now, false)
	require.Nil(t, obs.FiveHourAbsent, "a 429 never decides the 5h absence")

	noReset := http.Header{}
	noReset.Set("anthropic-ratelimit-unified-7d-utilization", "0.5")
	obs = buildMarketUsageObservations(noReset, sampleAnthropicPassiveUsageHeaders(noReset, now), now, true)
	require.Nil(t, obs.SevenDay, "utilization without a window end is not an observation")
	require.Nil(t, obs.FiveHourAbsent, "no Ultra flag without a complete 7d observation of its own")
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// ---- authoritative evaluation ----

func TestEvaluateMarketAdmission(t *testing.T) {
	sel := &MarketSelection{Class: MarketClassBuyer, AccountID: 1, GroupID: mktTier, OwnerUserID: mktOwner, ListingVersion: 3, Concurrency: 4, BuyerMaxConcurrency: 3,
		ReservedDevices: 2, ReservedAdmissions: 1, DeviceLimit: 5}
	req := &MarketAdmissionRequest{Class: MarketClassBuyer, AccountID: 1, GroupID: mktTier, AuthUserID: mktBuyer}
	base := func() *MarketAuthoritativeFacts {
		acc := mktListedAccount(1)
		selected := mktTier
		owner := mktOwner
		return &MarketAuthoritativeFacts{
			AccountFound: true, Account: &acc, MembershipSource: AccountGroupSourceMarket,
			GroupFound: true, GroupMarketTier: true, GroupActive: true, GroupPlatform: PlatformAnthropic,
			OwnerUserStatus: StatusActive, LatestOwnerEventID: 50, LatestOwnerEventOwner: &owner, MarketEnabled: true,
			Listing: &MarketListing{AccountID: 1, OwnerUserID: mktOwner, SelectedGroupID: &selected, Status: MarketListingStatusListed, Version: 3, Protection: mktProtection()},
		}
	}
	require.Equal(t, "", EvaluateMarketAdmission(sel, req, base()))
	mut := []struct {
		reason string
		edit   func(f *MarketAuthoritativeFacts)
	}{
		{"market_disabled", func(f *MarketAuthoritativeFacts) { f.MarketEnabled = false }},
		{"not_listed", func(f *MarketAuthoritativeFacts) { f.Listing.Status = MarketListingStatusUnlisted }},
		{"listing_changed", func(f *MarketAuthoritativeFacts) { f.Listing.Version = 4 }},
		{"owner_changed", func(f *MarketAuthoritativeFacts) { other := int64(8); f.Account.OwnerUserID = &other }},
		{"owner_inactive", func(f *MarketAuthoritativeFacts) { f.OwnerUserStatus = StatusDisabled }},
		{"owner_version", func(f *MarketAuthoritativeFacts) { f.LatestOwnerEventID = 0 }},
		{"not_market_member", func(f *MarketAuthoritativeFacts) { f.MembershipSource = "" }},
		{"account_inactive", func(f *MarketAuthoritativeFacts) { f.Account.Schedulable = false }},
		{"account_inactive", func(f *MarketAuthoritativeFacts) { f.OwnerPaused = true }},
		{"group_inactive", func(f *MarketAuthoritativeFacts) { f.GroupActive = false }},
		{"capacity_changed", func(f *MarketAuthoritativeFacts) { f.Listing.Protection.ReservedConcurrency = 2 }},
		{"capacity_changed", func(f *MarketAuthoritativeFacts) { f.Account.Concurrency = 6 }},
		{"ineligible_" + MarketReasonMaxSessions, func(f *MarketAuthoritativeFacts) { f.Account.Extra["max_sessions"] = 3 }},
		{"account_missing", func(f *MarketAuthoritativeFacts) { f.AccountFound = false }},
		{"reservation_changed", func(f *MarketAuthoritativeFacts) { f.Listing.Protection.ReservedDevices = 1 }},
		{"device_policy_changed", func(f *MarketAuthoritativeFacts) { f.Account.Extra["claude_oauth_carpool_device_limit"] = 8 }},
	}
	for _, m := range mut {
		f := base()
		m.edit(f)
		require.Equal(t, m.reason, EvaluateMarketAdmission(sel, req, f))
	}

	platformSel := &MarketSelection{Class: MarketClassPlatform, AccountID: 2, GroupID: mktTier}
	platformReq := &MarketAdmissionRequest{Class: MarketClassPlatform, AccountID: 2, GroupID: mktTier}
	pf := func() *MarketAuthoritativeFacts {
		acc := mktPlatformAccount(2, mktTier)
		return &MarketAuthoritativeFacts{AccountFound: true, Account: &acc, MembershipSource: AccountGroupSourceAdmin,
			GroupFound: true, GroupMarketTier: true, GroupActive: true, GroupPlatform: PlatformAnthropic}
	}
	require.Equal(t, "", EvaluateMarketAdmission(platformSel, platformReq, pf()))
	f := pf()
	f.Account.OwnerUserID = ptr64(5)
	require.Equal(t, "has_owner", EvaluateMarketAdmission(platformSel, platformReq, f))
	f = pf()
	f.MembershipSource = ""
	require.Equal(t, "not_admin_member", EvaluateMarketAdmission(platformSel, platformReq, f))
	f = pf()
	f.Listing = &MarketListing{Status: MarketListingStatusListed}
	require.Equal(t, "listed", EvaluateMarketAdmission(platformSel, platformReq, f))
}

// ---- dispatch boundary ----

func marketForwardEnv(t *testing.T) (*marketEnv, *queuedClaudeCodeUpstream, *Account) {
	t.Helper()
	acc := claudeCodeCacheSafeRetryAccount()
	acc.ID = 1
	acc.Concurrency = 4
	acc.OwnerUserID = ptr64(mktOwner)
	acc.Extra["claude_oauth_carpool_device_limit"] = 5
	acc.MarketGroupIDs = []int64{mktTier}
	acc.MarketListing = &AccountMarketListing{GroupID: mktTier, OwnerUserID: mktOwner, Version: 3, Protection: mktProtection()}
	env := newMarketEnv(t, []Account{*acc})
	upstream := &queuedClaudeCodeUpstream{responses: []func() *http.Response{okClaudeStreamResponse}}
	base := claudeCodeCacheSafeRetryService(upstream)
	base.market = env.rt
	base.billingService = env.svc.billingService
	base.groupRepo = env.svc.groupRepo
	base.identityService = NewIdentityService(&marketDeviceStub{}, strings.Repeat("x", 32))
	env.svc = base
	env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		a := *acc
		a.Extra = map[string]any{"claude_oauth_mode": ClaudeOAuthModeCarpool, "claude_oauth_carpool_device_limit": 5}
		selected, owner := mktTier, mktOwner
		return &MarketAuthoritativeFacts{AccountFound: true, Account: &a, MembershipSource: AccountGroupSourceMarket,
			GroupFound: true, GroupMarketTier: true, GroupActive: true, GroupPlatform: PlatformAnthropic,
			OwnerUserStatus: StatusActive, LatestOwnerEventID: 50, LatestOwnerEventOwner: &owner, MarketEnabled: true,
			Listing: &MarketListing{AccountID: 1, OwnerUserID: mktOwner, SelectedGroupID: &selected, Status: MarketListingStatusListed, Version: 3, Protection: mktProtection()}}
	}
	return env, upstream, acc
}

type marketDeviceStub struct {
	identityCacheStub
	mu      sync.Mutex
	classes []string
	err     error
}

func (s *marketDeviceStub) GetOrCreateCarpoolDeviceMarket(_ context.Context, _ int64, device string, _ ClientHints, _ int, now int64, _ *CarpoolAdmissionPolicy, adm MarketDeviceAdmission) (*CarpoolDeviceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.classes = append(s.classes, adm.Class)
	if s.err != nil {
		return nil, s.err
	}
	return &CarpoolDeviceRecord{OriginalDeviceID: device, CreatedAt: now, Class: adm.Class}, nil
}

func buyerForwardCtx(t *testing.T, base context.Context) (context.Context, *MarketAttempt) {
	t.Helper()
	gid := mktTier
	key := &APIKey{ID: mktKey, UserID: mktBuyer, GroupID: &gid, User: &User{ID: mktBuyer, Status: StatusActive}}
	ctx := WithAccountUserID(base, mktBuyer)
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "client-req-1")
	ctx = context.WithValue(ctx, ctxkey.Group, &Group{ID: mktTier, Platform: PlatformAnthropic, Status: StatusActive, RateMultiplier: 1.2, MarketTier: true, Hydrated: true, ClaudeOAuthRequestGateDisabled: false})
	ctx = WithMarketDispatch(ctx, key)
	sel := &MarketSelection{Class: MarketClassBuyer, AccountID: 1, GroupID: mktTier, OwnerUserID: mktOwner, ListingVersion: 3,
		Concurrency: 4, BuyerMaxConcurrency: 3, ReservedDevices: 2, ReservedAdmissions: 1, Protection: mktProtection(), DeviceLimit: 5}
	return WithMarketAttempt(ctx, sel)
}

func TestMarketForwardBuyerAdmissionAndProof(t *testing.T) {
	env, upstream, acc := marketForwardEnv(t)
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, attempt := buyerForwardCtx(t, c.Request.Context())
	result, err := env.svc.Forward(ctx, c, acc, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, upstream.calls)
	require.Len(t, env.store.requests, 1)
	req := env.store.requests[0]
	require.Equal(t, "client-req-1", req.RequestRef, "request_ref = the server request id")
	require.Equal(t, mktKey, req.APIKeyID)
	require.NotEmpty(t, req.AttemptID)
	dev, ok := env.svc.identityService.cache.(*marketDeviceStub)
	require.True(t, ok)
	require.Equal(t, []string{MarketDeviceClassBuyer}, dev.classes)

	marketPath, proof := attempt.SettlementFields()
	require.True(t, marketPath)
	require.NotNil(t, proof)
	require.Equal(t, env.store.inserted[0].ID, proof.AdmissionID)
	require.Equal(t, req.AttemptID, proof.AttemptID)
	require.Equal(t, "client-req-1", proof.RequestRef)
	require.Equal(t, mktBuyer, proof.BuyerUserID)
	require.Equal(t, mktTier, proof.GroupID)
	require.NotNil(t, attempt.Price)
	require.InDelta(t, 1.2, attempt.Price.GroupRate, 1e-9)
}

func TestMarketForwardNeverDispatchesWithoutAdmission(t *testing.T) {
	cases := []struct {
		name  string
		setup func(env *marketEnv, dev *marketDeviceStub)
	}{
		{"db timeout, nothing committed", func(env *marketEnv, _ *marketDeviceStub) {
			env.store.admitErr = context.DeadlineExceeded
		}},
		{"unknown commit, confirm read fails", func(env *marketEnv, _ *marketDeviceStub) {
			env.store.admitErr = errors.New("connection reset during commit")
			env.store.confirmErr = errors.New("db down")
		}},
		{"listing changed while waiting", func(env *marketEnv, _ *marketDeviceStub) {
			inner := env.store.facts
			env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
				f := inner(req)
				f.Listing.Version = 4
				return f
			}
		}},
		{"unlisted while waiting", func(env *marketEnv, _ *marketDeviceStub) {
			inner := env.store.facts
			env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
				f := inner(req)
				f.Listing.Status = MarketListingStatusUnlisted
				f.MembershipSource = ""
				return f
			}
		}},
		{"seller disabled while waiting", func(env *marketEnv, _ *marketDeviceStub) {
			inner := env.store.facts
			env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
				f := inner(req)
				f.OwnerUserStatus = StatusDisabled
				return f
			}
		}},
		{"share cap reached while waiting", func(env *marketEnv, _ *marketDeviceStub) {
			obs := freshObservations()
			obs.SevenDay.Utilization = 0.95
			env.usage.obs[1] = obs
		}},
		{"device registry error", func(_ *marketEnv, dev *marketDeviceStub) {
			dev.err = errors.New("redis down")
		}},
		{"device unidentified", func(_ *marketEnv, dev *marketDeviceStub) {
			dev.err = ErrMarketDeviceUnidentified
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, upstream, acc := marketForwardEnv(t)
			dev, ok := env.svc.identityService.cache.(*marketDeviceStub)
			require.True(t, ok)
			tc.setup(env, dev)
			c, _, parsed := claudeCodeCacheSafeRetryContext(t)
			ctx, attempt := buyerForwardCtx(t, c.Request.Context())
			_, err := env.svc.Forward(ctx, c, acc, parsed)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover, "the handler releases the lease and reselects")
			require.Zero(t, upstream.calls, "zero upstream sends")
			marketPath, proof := attempt.SettlementFields()
			require.False(t, marketPath)
			require.Nil(t, proof)
		})
	}

	t.Run("unknown commit confirmed by attempt id dispatches", func(t *testing.T) {
		env, upstream, acc := marketForwardEnv(t)
		env.store.admitErr = errors.New("commit result unknown")
		c, _, parsed := claudeCodeCacheSafeRetryContext(t)
		ctx, attempt := buyerForwardCtx(t, c.Request.Context())
		// The row did commit: the confirm read finds it by attempt id.
		env.store.facts = nil
		confirmStore := &confirmingStore{fakeMarketStore: env.store}
		env.rt.store = confirmStore
		_, err := env.svc.Forward(ctx, c, acc, parsed)
		require.NoError(t, err)
		require.Equal(t, 1, upstream.calls)
		_, proof := attempt.SettlementFields()
		require.NotNil(t, proof)
		require.Equal(t, int64(555), proof.AdmissionID)
	})
}

// confirmingStore fails the admission call but "finds" the row it would
// have committed (same attempt id and identity).
type confirmingStore struct {
	*fakeMarketStore
	last *MarketAdmissionRequest
}

func (s *confirmingStore) AdmitMarketCandidate(_ context.Context, req *MarketAdmissionRequest, _ func(*MarketAuthoritativeFacts) string) (*MarketAdmissionVerdict, error) {
	r := *req
	s.last = &r
	return nil, errors.New("commit result unknown")
}

func (s *confirmingStore) GetMarketAdmissionByAttempt(_ context.Context, attemptID string) (*MarketAdmission, error) {
	if s.last == nil || s.last.AttemptID != attemptID {
		return nil, ErrMarketAdmissionNotFound
	}
	return &MarketAdmission{ID: 555, AttemptID: attemptID, RequestRef: s.last.RequestRef, AccountID: s.last.AccountID,
		GroupID: s.last.GroupID, OwnerUserID: mktOwner, BuyerUserID: s.last.AuthUserID, APIKeyID: s.last.APIKeyID}, nil
}

func TestMarketForwardSelectionMismatch(t *testing.T) {
	env, upstream, acc := marketForwardEnv(t)
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	// A listed account without a market selection (path without admission).
	_, err := env.svc.Forward(c.Request.Context(), c, acc, parsed)
	require.ErrorIs(t, err, ErrMarketAdmissionRejected)
	// A selection for another account.
	ctx, _ := WithMarketAttempt(c.Request.Context(), &MarketSelection{Class: MarketClassBuyer, AccountID: 99})
	_, err = env.svc.Forward(ctx, c, acc, parsed)
	require.ErrorIs(t, err, ErrMarketAdmissionRejected)
	require.Zero(t, upstream.calls)
}

// ---- count_tokens ----

func TestMarketTierCountTokensUsesPlatformAccountsOnly(t *testing.T) {
	sticky := &oauthStickyGatewayCacheStub{binding: 1}
	env := newMarketEnv(t, []Account{mktListedAccount(1), mktPlatformAccount(2, mktTier)})
	env.svc.cache = sticky
	ctx := context.WithValue(WithAccountUserID(context.Background(), mktBuyer), ctxkey.Group, mktGroups()[mktTier])
	for i := 0; i < 10; i++ {
		acc, err := env.svc.SelectMarketTierCountTokensAccount(ctx, ptr64(mktTier), "claude-fable-5")
		require.NoError(t, err)
		require.Equal(t, int64(2), acc.ID, "never a market account")
	}
	require.Zero(t, sticky.setCalls+sticky.deleteCalls+sticky.refreshCalls+sticky.pendingSetCalls+sticky.pendingDeleteCalls, "never touches the Messages sticky binding")

	only := newMarketEnv(t, []Account{mktListedAccount(1)})
	_, err := only.svc.SelectMarketTierCountTokensAccount(ctx, ptr64(mktTier), "claude-fable-5")
	require.ErrorIs(t, err, ErrMarketCountTokensUnsupported, "no platform account configured: 404")

	only.store.members = 1 // configured but unavailable right now
	_, err = only.svc.SelectMarketTierCountTokensAccount(ctx, ptr64(mktTier), "claude-fable-5")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrMarketCountTokensUnsupported, "503, not 404")
}

// ---- settlement plumbing ----

func TestMarketProofReachesSettlementInputOnlyWhenSet(t *testing.T) {
	gid := mktTier
	key := &APIKey{ID: mktKey, GroupID: &gid, Group: mktGroups()[mktTier]}
	p := &postUsageBillingParams{Cost: &CostBreakdown{TotalCost: 1, ActualCost: 1.2}, APIKey: key, User: &User{ID: mktBuyer}, Account: &Account{ID: 1}}
	cmd := buildUsageBillingCommand("req-1", nil, p)
	require.False(t, cmd.MarketPath)
	require.Nil(t, cmd.Settlement.Market)
	require.Nil(t, cmd.Settlement.MarketPrice)

	proof := &MarketSettlementProof{AdmissionID: 5, AttemptID: "a", RequestRef: "r", AccountID: 1, GroupID: mktTier, BuyerUserID: mktBuyer, APIKeyID: mktKey}
	price := &MarketPriceSnapshot{Model: "claude-fable-5", PriceVersion: "v1", GroupRate: 1.2, ModelRate: 1, UserDiscount: 1}
	p.MarketPath, p.Market, p.MarketPrice = true, proof, price
	cmd = buildUsageBillingCommand("req-1", nil, p)
	require.True(t, cmd.MarketPath, "the market flag is on the top-level command")
	require.Equal(t, *proof, *cmd.Settlement.Market)
	require.NotSame(t, proof, cmd.Settlement.Market)
	require.Equal(t, *price, *cmd.Settlement.MarketPrice)

	// A rejected attempt has no proof; a fresh attempt is created per candidate.
	_, rejected := WithMarketAttempt(context.Background(), &MarketSelection{Class: MarketClassBuyer, AccountID: 1})
	mp, pr := rejected.SettlementFields()
	require.False(t, mp)
	require.Nil(t, pr)
	var none *MarketAttempt
	mp, pr = none.SettlementFields()
	require.False(t, mp)
	require.Nil(t, pr)
}

// ---- ops retry ----

// The listed account also has an admin binding in the plain group; an Ops
// client-mode retry from that group never runs on it.
func TestMarketOpsClientRetrySkipsListedAccount(t *testing.T) {
	listed := mktListedAccount(1)
	listed.GroupIDs = []int64{mktPlain}
	listed.AccountGroups = []AccountGroup{{GroupID: mktPlain}}
	listed.Priority = 0
	other := mktPlatformAccount(2, mktPlain)
	other.Priority = 50
	body := []byte(`{"model":"claude-fable-5","stream":false,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)

	for _, stale := range []bool{false, true} {
		acc := listed
		if stale {
			// Old snapshot without market fields; the registry still knows.
			acc.MarketGroupIDs, acc.MarketListing = nil, nil
		}
		env := newMarketEnv(t, []Account{acc, other})
		env.rt.SetState(&MarketDispatchState{Enabled: true, ListedAccountIDs: []int64{1}})
		upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{{response: gatewayHTTPSuccessResponse}}}
		env.svc.httpUpstream = upstream
		env.svc.responseHeaderFilter = compileResponseHeaderFilter(env.svc.cfg)
		env.svc.rateLimitService = &RateLimitService{}
		env.svc.deferredService = &DeferredService{}
		ops := &OpsService{gatewayService: env.svc, accountRepo: env.svc.accountRepo}
		errorLog := &OpsErrorLogDetail{OpsErrorLog: OpsErrorLog{GroupID: ptr64(mktPlain), RequestPath: "/v1/messages"}}
		exec := ops.executeClientRetry(context.Background(), opsRetryTypeMessages, errorLog, body)
		require.NotNil(t, exec)
		require.NotNil(t, exec.usedAccountID, "stale=%v %s", stale, exec.errorMessage)
		require.Equal(t, int64(2), *exec.usedAccountID, "stale=%v", stale)
		total, _ := env.slots.counts(1)
		require.Zero(t, total, "no lease left on the listed account")

		// Pinned retry refuses it outright.
		exec = ops.executePinnedRetry(context.Background(), opsRetryTypeMessages, errorLog, body, 1)
		require.Equal(t, opsRetryStatusFailed, exec.status)
		require.Contains(t, exec.errorMessage, "listed")
		require.Equal(t, 1, upstream.calls, "only the platform account was sent upstream")
	}
}

// ---- review round 1 ----

func TestMarketRegistryPendingListing(t *testing.T) {
	store := &fakeMarketStore{state: &MarketDispatchState{Enabled: true}}
	rt := NewMarketRuntime(store, nil)
	require.True(t, rt.Refresh(context.Background()))
	rt.BeginListing(5)
	require.True(t, rt.IsListed(5), "listed locally before the transaction commits")
	require.True(t, rt.Refresh(context.Background()))
	require.True(t, rt.IsListed(5), "a poll during the transaction keeps the mark")
	rt.EndListing(5)
	time.Sleep(2 * time.Millisecond)
	store.state = &MarketDispatchState{Enabled: true, ListedAccountIDs: []int64{5}}
	require.True(t, rt.Refresh(context.Background()))
	require.True(t, rt.IsListed(5))
	// A rolled-back listing: dropped by the first poll after it ended.
	rt.BeginListing(6)
	rt.EndListing(6)
	time.Sleep(2 * time.Millisecond)
	require.True(t, rt.IsListed(6))
	require.True(t, rt.Refresh(context.Background()))
	require.False(t, rt.IsListed(6))
}

func TestMarketUsageWriteFailureStopsSelling(t *testing.T) {
	env := newMarketEnv(t, []Account{mktListedAccount(1)})
	ctx := buyerCtx(mktTier)
	res, err := env.svc.SelectAccountWithLoadAwareness(ctx, ptr64(mktTier), "", "claude-fable-5", nil, "")
	require.NoError(t, err)
	res.ReleaseFunc()
	failing := &failingUsageCache{fakeMarketUsage: env.usage}
	env.rt.usage = failing
	rl := &RateLimitService{market: env.rt}
	acc := mktListedAccount(1)
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.9")
	h.Set("anthropic-ratelimit-unified-7d-reset", itoa(time.Now().Add(48*time.Hour).Unix()))
	rl.recordMarketUsageObservations(context.Background(), &acc, h, sampleAnthropicPassiveUsageHeaders(h, time.Now()), time.Now(), true)
	_, err = env.svc.SelectAccountWithLoadAwareness(buyerCtx(mktTier), ptr64(mktTier), "", "claude-fable-5", nil, "")
	require.ErrorIs(t, err, ErrNoAvailableAccounts, "a lost (possibly higher) observation stops selling")
	failing.fail = false
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.1")
	rl.recordMarketUsageObservations(context.Background(), &acc, h, sampleAnthropicPassiveUsageHeaders(h, time.Now()), time.Now(), true)
	res, err = env.svc.SelectAccountWithLoadAwareness(buyerCtx(mktTier), ptr64(mktTier), "", "claude-fable-5", nil, "")
	require.NoError(t, err)
	res.ReleaseFunc()
}

type failingUsageCache struct {
	*fakeMarketUsage
	fail bool
}

func (f *failingUsageCache) RecordMarketUsageObservations(ctx context.Context, id int64, obs MarketUsageObservations) error {
	if f.fail || f.fakeMarketUsage == nil {
		return errors.New("redis down")
	}
	return f.fakeMarketUsage.RecordMarketUsageObservations(ctx, id, obs)
}

func TestMarketForwardRejectsBillingGroupMismatch(t *testing.T) {
	env, upstream, acc := marketForwardEnv(t)
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, attempt := buyerForwardCtx(t, c.Request.Context())
	other := mktPlain
	ctx = WithMarketDispatch(ctx, &APIKey{ID: mktKey, UserID: mktBuyer, GroupID: &other, User: &User{ID: mktBuyer}})
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	require.ErrorIs(t, err, ErrMarketAdmissionRejected)
	require.Zero(t, upstream.calls)
	_, proof := attempt.SettlementFields()
	require.Nil(t, proof)
	require.Empty(t, env.store.requests)
}

func TestMarketTierCountTokensRechecksPlatformInDatabase(t *testing.T) {
	stale := mktPlatformAccount(2, mktTier) // cached "no owner"
	good := mktPlatformAccount(3, mktTier)
	stale.Priority, good.Priority = 0, 50
	env := newMarketEnv(t, []Account{stale, good})
	env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		acc := mktPlatformAccount(req.AccountID, mktTier)
		if req.AccountID == 2 {
			acc.OwnerUserID = ptr64(mktOwner) // just got an owner
		}
		return &MarketAuthoritativeFacts{AccountFound: true, Account: &acc, MembershipSource: AccountGroupSourceAdmin,
			GroupFound: true, GroupMarketTier: true, GroupActive: true, GroupPlatform: PlatformAnthropic}
	}
	ctx := context.WithValue(WithAccountUserID(context.Background(), mktBuyer), ctxkey.Group, mktGroups()[mktTier])
	for i := 0; i < 5; i++ {
		acc, err := env.svc.SelectMarketTierCountTokensAccount(ctx, ptr64(mktTier), "claude-fable-5")
		require.NoError(t, err)
		require.Equal(t, int64(3), acc.ID)
	}
	env.store.members = 2
	only := newMarketEnv(t, []Account{stale})
	only.store.facts = env.store.facts
	only.store.members = 1
	_, err := only.svc.SelectMarketTierCountTokensAccount(ctx, ptr64(mktTier), "claude-fable-5")
	require.ErrorIs(t, err, ErrNoAvailableAccounts, "configured but unavailable: 503")
}
