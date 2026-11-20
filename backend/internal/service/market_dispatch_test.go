//go:build unit

package service

import (
	"context"
	"errors"
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

// dualSlotCache is an in-memory total + buyer slot store with the Lua
// semantics (the Redis implementation is covered by integration tests).
type dualSlotCache struct {
	*mockConcurrencyCache
	mu            sync.Mutex
	total, buyer  map[int64]map[string]bool
	plainAcquires int
	buyerAcquires int
	onAcquire     func(id int64)
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
	if c.onAcquire != nil {
		c.onAcquire(id)
	}
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

func (c *dualSlotCache) AccountBuyerLeaseHeld(_ context.Context, id int64, req string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total[id][req] && c.buyer[id][req], nil
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
	return MarketProtectionParams{ReservedConcurrency: 1, ReservedDevices: 2, ReservedAdmissions: 1}
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
	all := mktGroups()
	for _, g := range groups {
		ags = append(ags, AccountGroup{GroupID: g, Group: all[g]})
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

type marketEnv struct {
	svc   *GatewayService
	slots *dualSlotCache
	store *fakeMarketStore
	rt    *MarketRuntime
}

func newMarketEnv(t *testing.T, accounts []Account) *marketEnv {
	t.Helper()
	byID := map[int64]*Account{}
	for i := range accounts {
		byID[accounts[i].ID] = &accounts[i]
	}
	slots := newDualSlotCache()
	store := &fakeMarketStore{}
	rt := NewMarketRuntime(store)
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
	return &marketEnv{svc: svc, slots: slots, store: store, rt: rt}
}

func buyerCtx(groupID int64) context.Context {
	gid := groupID
	key := &APIKey{ID: mktKey, UserID: mktBuyer, GroupID: &gid, Group: mktGroups()[groupID], User: &User{ID: mktBuyer, Status: StatusActive}}
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
	listed := mktListedAccount(1)
	platform := mktPlatformAccount(2, mktTier)
	owned := mktPlatformAccount(3, mktTier)
	owned.OwnerUserID = ptr64(mktOwner)
	plain := mktPlatformAccount(4, mktPlain)
	listedInPlain := mktListedAccount(5)
	listedInPlain.GroupIDs = []int64{mktPlain}
	listedInHosted := mktListedAccount(6)
	listedInHosted.GroupIDs = []int64{mktHosted}
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
	ownerKey := &APIKey{ID: 6, UserID: mktOwner, GroupID: ptr64(mktHosted), Group: groups[mktHosted], User: &User{ID: mktOwner}}
	ownerCtx := at(WithMarketDispatch(WithAccountUserID(context.Background(), mktOwner), ownerKey), mktHosted)
	c, sel := svc.classifyMarketCandidate(ownerCtx, &listedInHosted)
	require.Equal(t, MarketClassOwner, c)
	require.Equal(t, mktOwner, sel.OwnerUserID)
	require.Equal(t, MarketClassExcluded, class(at(WithAccountUserID(context.Background(), mktOwner), mktHosted), listedInHosted),
		"no final admission on the path (count_tokens, Ops): a listed account is never used, not even by its owner")
	otherUser := at(WithMarketDispatch(WithAccountUserID(context.Background(), 99), ownerKey), mktHosted)
	require.Equal(t, MarketClassExcluded, class(otherUser, listedInHosted), "a trusted co-user of the hosted group is not the owner")

	// Self purchase stays on the buyer path (no reservation).
	self := at(WithMarketDispatch(WithAccountUserID(context.Background(), mktOwner), &APIKey{ID: 5, UserID: mktOwner, GroupID: ptr64(mktTier), Group: groups[mktTier], User: &User{ID: mktOwner}}), mktTier)
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
	base.concurrencyService = env.svc.concurrencyService
	ok, err := env.slots.AcquireAccountBuyerSlot(context.Background(), 1, 4, 3, "lease-1")
	require.NoError(t, err)
	require.True(t, ok)
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
			Listing:   &MarketListing{AccountID: 1, OwnerUserID: mktOwner, SelectedGroupID: &selected, Status: MarketListingStatusListed, Version: 3, Protection: mktProtection()},
			GroupRate: 1.2}
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
	key := &APIKey{ID: mktKey, UserID: mktBuyer, GroupID: &gid, Group: mktGroups()[mktTier], User: &User{ID: mktBuyer, Status: StatusActive}}
	ctx := WithAccountUserID(base, mktBuyer)
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "client-req-1")
	ctx = context.WithValue(ctx, ctxkey.Group, &Group{ID: mktTier, Platform: PlatformAnthropic, Status: StatusActive, RateMultiplier: 1.2, MarketTier: true, Hydrated: true, ClaudeOAuthRequestGateDisabled: false})
	ctx = WithMarketDispatch(ctx, key)
	sel := &MarketSelection{Class: MarketClassBuyer, AccountID: 1, GroupID: mktTier, OwnerUserID: mktOwner, ListingVersion: 3,
		Concurrency: 4, BuyerMaxConcurrency: 3, ReservedDevices: 2, ReservedAdmissions: 1, Protection: mktProtection(), DeviceLimit: 5,
		LeaseID: "lease-1"}
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
		// A zero user rate override on a tier is allowed (free self-use);
		// set while the request waited (the request-start price used a
		// cached positive rate), the snapshot re-read refuses the sale.
		{"buyer rate override set to zero while waiting", func(env *marketEnv, _ *marketDeviceStub) {
			inner := env.store.facts
			env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
				f := inner(req)
				zero := 0.0
				f.BuyerRateOverride = &zero
				return f
			}
		}},
		{"temp-unschedulable set while waiting", func(env *marketEnv, _ *marketDeviceStub) {
			inner := env.store.facts
			env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
				f := inner(req)
				until := time.Now().Add(time.Hour)
				f.Account.TempUnschedulableUntil = &until
				return f
			}
		}},
		{"allowlist tightened while waiting", func(env *marketEnv, _ *marketDeviceStub) {
			inner := env.store.facts
			env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
				f := inner(req)
				f.Account.Extra["claude_oauth_carpool_users_only"] = true
				f.Account.CarpoolUserIDs = []int64{12345}
				return f
			}
		}},
		{"lease expired and taken by someone else", func(env *marketEnv, _ *marketDeviceStub) {
			require.NoError(t, env.slots.ReleaseAccountBuyerSlot(context.Background(), 1, "lease-1"))
			ok, err := env.slots.AcquireAccountBuyerSlot(context.Background(), 1, 4, 3, "someone-else")
			require.NoError(t, err)
			require.True(t, ok)
		}},
		// The final price check reads the group rates of the admission
		// snapshot, not the group captured when the request started.
		{"model rate set to zero while waiting", func(env *marketEnv, _ *marketDeviceStub) {
			inner := env.store.facts
			env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
				f := inner(req)
				f.GroupModelRates = map[string]float64{"claude-*": 0}
				return f
			}
		}},
		{"group rate unreadable in the snapshot", func(env *marketEnv, _ *marketDeviceStub) {
			inner := env.store.facts
			env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
				f := inner(req)
				f.GroupRate = 0
				return f
			}
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

	t.Run("snapshot model rate changed but positive dispatches", func(t *testing.T) {
		env, upstream, acc := marketForwardEnv(t)
		inner := env.store.facts
		env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
			f := inner(req)
			f.GroupModelRates = map[string]float64{"claude-*": 0.5}
			return f
		}
		c, _, parsed := claudeCodeCacheSafeRetryContext(t)
		ctx, attempt := buyerForwardCtx(t, c.Request.Context())
		_, err := env.svc.Forward(ctx, c, acc, parsed)
		require.NoError(t, err)
		require.Equal(t, 1, upstream.calls)
		// Sellability only: the price snapshot (and the charge) keep the
		// request-start group's factors.
		require.NotNil(t, attempt.Price)
		require.InDelta(t, 1.2, attempt.Price.GroupRate, 1e-9)
	})

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
	rt := NewMarketRuntime(store)
	require.True(t, rt.Refresh(context.Background()))
	t5 := rt.BeginListing(5)
	require.True(t, rt.IsListed(5), "listed locally before the transaction commits")
	require.True(t, rt.Refresh(context.Background()))
	require.True(t, rt.IsListed(5), "a poll during the transaction keeps the mark")
	rt.EndListing(5, t5)
	time.Sleep(2 * time.Millisecond)
	store.state = &MarketDispatchState{Enabled: true, ListedAccountIDs: []int64{5}}
	require.True(t, rt.Refresh(context.Background()))
	require.True(t, rt.IsListed(5))
	// A rolled-back listing: dropped by the first poll after it ended.
	t6 := rt.BeginListing(6)
	rt.EndListing(6, t6)
	time.Sleep(2 * time.Millisecond)
	require.True(t, rt.IsListed(6))
	require.True(t, rt.Refresh(context.Background()))
	require.False(t, rt.IsListed(6))
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

// ---- review round 2 ----

func TestMarketRegistryColdStartIsConservative(t *testing.T) {
	rt := NewMarketRuntime(&fakeMarketStore{}) // never loaded
	owned := mktPlatformAccount(1, mktPlain)
	owned.OwnerUserID = ptr64(mktOwner)
	require.True(t, rt.marketAccountListed(&owned), "before the first load any owned account may be listed")
	require.False(t, rt.marketAccountListed(ptr(mktPlatformAccount(2, mktPlain))))
	rt.SetState(&MarketDispatchState{})
	require.False(t, rt.marketAccountListed(&owned))
}

// hookedMarketRepo runs the listing hooks like the repository does.
type hookedMarketRepo struct {
	*fakeMarketRepo
	failAfterWrite bool
	afterRollback  func()
}

func (r *hookedMarketRepo) OwnerSet(ctx context.Context, p MarketOwnerSetParams) (*MarketListing, error) {
	if _, err := r.GetOwnedAccountFacts(ctx, p.OwnerUserID, p.AccountID); err != nil {
		return nil, err
	}
	if p.Listed && p.Validate != nil {
		if err := p.Validate(r.account, MarketProtectionParams{}); err != nil {
			return nil, err
		}
	}
	if p.Listed && p.BeforeListWrite != nil {
		p.BeforeListWrite()
		if r.failAfterWrite {
			p.ListingRolledBack()
			if r.afterRollback != nil {
				r.afterRollback()
			}
			return nil, errors.New("outbox insert failed")
		}
	}
	return r.listing, nil
}

func TestMarketListingMarksOnlyCheckedListings(t *testing.T) {
	rt := NewMarketRuntime(&fakeMarketStore{state: &MarketDispatchState{}})
	rt.SetState(&MarketDispatchState{})
	repo := &hookedMarketRepo{fakeMarketRepo: &fakeMarketRepo{account: marketTestAccount(7)}}
	svc := NewMarketListingService(repo, &marketSettingRepo{values: map[string]string{}})
	svc.SetListingObserver(rt)

	_, _ = svc.OwnerSet(context.Background(), 8, 11, 3, true)
	require.False(t, rt.IsListed(11), "someone else's request never changes scheduling")

	repo.failAfterWrite = true
	_, err := svc.OwnerSet(context.Background(), 7, 11, 3, true)
	require.Error(t, err)
	require.False(t, rt.IsListed(11), "a rolled-back listing is cleared at once")

	repo.failAfterWrite = false
	_, _ = svc.OwnerSet(context.Background(), 7, 11, 3, true)
	require.True(t, rt.IsListed(11), "a committed listing stays marked until a later poll")
	time.Sleep(2 * time.Millisecond)
	require.True(t, rt.Refresh(context.Background()))
	require.False(t, rt.IsListed(11), "the poll (not listed in this fake) is authoritative afterwards")
}

func TestMarketForwardCancelledDuringAdmissionNeverDispatches(t *testing.T) {
	env, upstream, acc := marketForwardEnv(t)
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	base, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	ctx, attempt := buyerForwardCtx(t, base)
	inner := env.store.facts
	env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		cancel() // the client goes away while the admission runs
		return inner(req)
	}
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	require.ErrorIs(t, err, context.Canceled)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover), "a cancelled request ends, it does not reselect")
	require.Zero(t, upstream.calls)
	_, proof := attempt.SettlementFields()
	require.Nil(t, proof)
}

func TestMarketOpsPinnedRetryRechecksTierPlatform(t *testing.T) {
	platform := mktPlatformAccount(2, mktTier)
	env := newMarketEnv(t, []Account{platform})
	env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		acc := mktPlatformAccount(req.AccountID, mktTier)
		acc.OwnerUserID = ptr64(mktOwner) // got an owner after the read
		return &MarketAuthoritativeFacts{AccountFound: true, Account: &acc, MembershipSource: AccountGroupSourceAdmin,
			GroupFound: true, GroupMarketTier: true, GroupActive: true, GroupPlatform: PlatformAnthropic}
	}
	upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{{response: gatewayHTTPSuccessResponse}}}
	env.svc.httpUpstream = upstream
	env.svc.responseHeaderFilter = compileResponseHeaderFilter(env.svc.cfg)
	env.svc.rateLimitService = &RateLimitService{}
	env.svc.deferredService = &DeferredService{}
	ops := &OpsService{gatewayService: env.svc, accountRepo: env.svc.accountRepo}
	errorLog := &OpsErrorLogDetail{OpsErrorLog: OpsErrorLog{GroupID: ptr64(mktTier), RequestPath: "/v1/messages"}}
	body := []byte(`{"model":"claude-fable-5","stream":false,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	exec := ops.executePinnedRetry(context.Background(), opsRetryTypeMessages, errorLog, body, 2)
	require.Equal(t, opsRetryStatusFailed, exec.status)
	require.Zero(t, upstream.calls)
	require.Len(t, env.store.requests, 1)
	require.Equal(t, MarketClassPlatform, env.store.requests[0].Class)
}

// ---- review round 3 ----

func TestMarketPriceUsesTheBilledKeyGroup(t *testing.T) {
	env := newMarketEnv(t, []Account{mktListedAccount(1)})
	gid := mktTier
	noGroup := WithMarketDispatch(WithAccountUserID(context.Background(), mktBuyer), &APIKey{ID: mktKey, UserID: mktBuyer, GroupID: &gid, User: &User{ID: mktBuyer}})
	_, err := env.svc.SelectAccountWithLoadAwareness(noGroup, ptr64(mktTier), "", "claude-fable-5", nil, "")
	require.ErrorIs(t, err, ErrNoAvailableAccounts, "no billed group object: not a sale")
	zero := *mktGroups()[mktTier]
	zero.ModelRateMultipliers = map[string]float64{"claude-fable-5": 0}
	zeroRate := WithMarketDispatch(WithAccountUserID(context.Background(), mktBuyer), &APIKey{ID: mktKey, UserID: mktBuyer, GroupID: &gid, Group: &zero, User: &User{ID: mktBuyer}})
	_, err = env.svc.SelectAccountWithLoadAwareness(zeroRate, ptr64(mktTier), "", "claude-fable-5", nil, "")
	require.ErrorIs(t, err, ErrNoAvailableAccounts, "zero model rate: not sellable")
}

// ---- independent review round 1 ----

func TestMarketOpsPinnedRetryOrdinaryGroupUnchanged(t *testing.T) {
	// The group is unknown to the group repository (deleted / read error):
	// the pinned retry behaves as before (no group lookup).
	acc := mktPlatformAccount(2, 99)
	env := newMarketEnv(t, []Account{acc})
	groups, ok := env.svc.groupRepo.(*mockGroupRepoForGateway)
	require.True(t, ok)
	upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{{response: gatewayHTTPSuccessResponse}}}
	env.svc.httpUpstream = upstream
	env.svc.responseHeaderFilter = compileResponseHeaderFilter(env.svc.cfg)
	env.svc.rateLimitService = &RateLimitService{}
	env.svc.deferredService = &DeferredService{}
	ops := &OpsService{gatewayService: env.svc, accountRepo: env.svc.accountRepo}
	errorLog := &OpsErrorLogDetail{OpsErrorLog: OpsErrorLog{GroupID: ptr64(99), RequestPath: "/v1/chat/completions"}}
	body := []byte(`{"model":"claude-fable-5","stream":false,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	exec := ops.executePinnedRetry(context.Background(), opsRetryTypeMessages, errorLog, body, 2)
	require.Equal(t, opsRetryStatusSucceeded, exec.status, exec.errorMessage)
	require.Equal(t, 1, upstream.calls)
	require.Zero(t, groups.getByIDLiteCalls+groups.getByIDCalls, "no extra group read for ordinary groups")
	require.Empty(t, env.store.requests)
}

func TestMarketOwnerPathDeviceErrorFailsOpenAsOwner(t *testing.T) {
	env, upstream, acc := marketForwardEnv(t)
	dev, ok := env.svc.identityService.cache.(*marketDeviceStub)
	require.True(t, ok)
	dev.err = errors.New("redis down")
	hosted := mktHosted
	acc.GroupIDs = []int64{mktHosted}
	env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		a := *acc
		a.Extra = map[string]any{"claude_oauth_mode": ClaudeOAuthModeCarpool, "claude_oauth_carpool_device_limit": 5}
		return &MarketAuthoritativeFacts{AccountFound: true, Account: &a, MembershipSource: AccountGroupSourceAdmin,
			GroupFound: true, GroupActive: true, GroupPlatform: PlatformAnthropic, GroupHosted: true, GroupExclusive: true,
			OwnerUserStatus: StatusActive, OwnerGroupQualified: true}
	}
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx := WithAccountUserID(c.Request.Context(), mktOwner)
	ctx = WithMarketDispatch(ctx, &APIKey{ID: 6, UserID: mktOwner, GroupID: &hosted, User: &User{ID: mktOwner}})
	ctx, attempt := WithMarketAttempt(ctx, &MarketSelection{Class: MarketClassOwner, AccountID: acc.ID, GroupID: hosted, OwnerUserID: mktOwner, Concurrency: 4})
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	require.NoError(t, err, "owner path fails open on a registry error, like plain carpool")
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, []string{MarketDeviceClassOwner}, dev.classes, "never registered as a buyer")
	marketPath, proof := attempt.SettlementFields()
	require.False(t, marketPath)
	require.Nil(t, proof)
	require.Empty(t, env.store.inserted, "no admission row on the owner path")
}

func TestMarketListedAccountNeverServesCountTokens(t *testing.T) {
	env, upstream, acc := marketForwardEnv(t)
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx := WithAccountUserID(c.Request.Context(), mktOwner) // its owner, no market attempt
	err := env.svc.ForwardCountTokens(ctx, c, acc, parsed)
	require.Error(t, err)
	require.Zero(t, upstream.calls)
}

func TestMarketRecordUsageFreezesBillingRequestID(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	gid := mktTier
	proof := &MarketSettlementProof{AdmissionID: 5, AttemptID: "a", RequestRef: "client-req-1", AccountID: 1, GroupID: gid, BuyerUserID: mktBuyer, APIKeyID: mktKey}
	usageRepo.lastLog = nil
	input := func(market bool, frozen string) *RecordUsageInput {
		in := &RecordUsageInput{
			Result:        &ForwardResult{RequestID: "msg_upstream_id", Model: "claude-sonnet-4", Duration: time.Second, Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 5}},
			APIKey:        &APIKey{ID: mktKey, GroupID: &gid, Group: mktGroups()[mktTier]},
			User:          &User{ID: mktBuyer},
			Account:       &Account{ID: 1},
			APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
		}
		if market {
			in.MarketPath, in.Market, in.BillingRequestID = true, proof, frozen
		}
		return in
	}
	// Worker context without the client request id: the frozen id wins.
	require.NoError(t, svc.RecordUsage(context.Background(), input(true, "client:client-req-1")))
	require.Equal(t, "client:"+proof.RequestRef, billingRepo.lastCmd.RequestID)
	require.True(t, billingRepo.lastCmd.MarketPath)
	require.Equal(t, "client:"+proof.RequestRef, usageRepo.lastLog.RequestID, "same identity on the usage log")
	// A tier sale served by a platform account is not a market command: a
	// captured id (set by mistake) never replaces main's identity.
	platformSale := input(false, "")
	platformSale.BillingRequestID = "client:client-req-1"
	require.NoError(t, svc.RecordUsage(context.Background(), platformSale))
	require.Equal(t, "msg_upstream_id", billingRepo.lastCmd.RequestID)
	require.Equal(t, "msg_upstream_id", usageRepo.lastLog.RequestID)
	require.False(t, billingRepo.lastCmd.MarketPath)
	// Empty = the original derivation (ordinary sales never set it).
	require.NoError(t, svc.RecordUsage(context.Background(), input(false, "")))
	require.Equal(t, "msg_upstream_id", billingRepo.lastCmd.RequestID)
}

func TestMarketSelectionChangeRetriesTheGroup(t *testing.T) {
	first := mktPlatformAccount(2, mktTier)
	first.Priority = 0
	second := mktPlatformAccount(3, mktTier)
	second.Priority = 50
	env := newMarketEnv(t, []Account{first, second})
	// Account 2 gets listed between its lease and the end of the selection.
	env.slots.onAcquire = func(id int64) {
		if id == 2 {
			env.rt.SetState(&MarketDispatchState{Enabled: true, ListedAccountIDs: []int64{2}})
		}
	}
	res, err := env.svc.SelectAccountWithLoadAwareness(buyerCtx(mktTier), ptr64(mktTier), "", "claude-fable-5", nil, "")
	require.NoError(t, err)
	require.Equal(t, int64(3), res.Account.ID, "the group is retried without the changed account")
	res.ReleaseFunc()
	total, _ := env.slots.counts(2)
	require.Zero(t, total, "the changed account's lease was released")
}

// ---- independent review round 2 ----

func TestMarketListingRollbackNeverClearsAnotherListing(t *testing.T) {
	rt := NewMarketRuntime(&fakeMarketStore{state: &MarketDispatchState{}})
	rt.SetState(&MarketDispatchState{})
	repo := &hookedMarketRepo{fakeMarketRepo: &fakeMarketRepo{account: marketTestAccount(7)}, failAfterWrite: true}
	svc := NewMarketListingService(repo, &marketSettingRepo{values: map[string]string{}})
	svc.SetListingObserver(rt)
	var tokenB uint64
	// A rolls back; B (same account) begins before A's service returns.
	repo.afterRollback = func() { tokenB = rt.BeginListing(11) }
	_, err := svc.OwnerSet(context.Background(), 7, 11, 3, true)
	require.Error(t, err)
	require.True(t, rt.IsListed(11), "A's end never touches B's mark")
	rt.EndListing(11, tokenB)
	require.True(t, rt.IsListed(11), "B ended: kept until a later poll")
	time.Sleep(2 * time.Millisecond)
	require.True(t, rt.Refresh(context.Background()))
	require.False(t, rt.IsListed(11))
	// Finalizing twice is harmless.
	rt.EndListing(11, tokenB)
	rt.AbortListing(11, tokenB)
	require.False(t, rt.IsListed(11))
}

func TestMarketForwardLeaseLostDuringAdmission(t *testing.T) {
	env, upstream, acc := marketForwardEnv(t)
	inner := env.store.facts
	env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		// The lease expires while the admission transaction runs and its
		// capacity is taken by someone else.
		require.NoError(t, env.slots.ReleaseAccountBuyerSlot(context.Background(), 1, "lease-1"))
		ok, err := env.slots.AcquireAccountBuyerSlot(context.Background(), 1, 4, 3, "someone-else")
		require.NoError(t, err)
		require.True(t, ok)
		return inner(req)
	}
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, attempt := buyerForwardCtx(t, c.Request.Context())
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Zero(t, upstream.calls)
	_, proof := attempt.SettlementFields()
	require.Nil(t, proof)
}

func TestMarketTierCountTokensFiltersIncapableBeforeRanking(t *testing.T) {
	bedrock := mktPlatformAccount(2, mktTier)
	bedrock.Type, bedrock.Priority = AccountTypeBedrock, 0
	good := mktPlatformAccount(3, mktTier)
	good.Priority = 50
	env := newMarketEnv(t, []Account{bedrock, good})
	ctx := context.WithValue(WithAccountUserID(context.Background(), mktBuyer), ctxkey.Group, mktGroups()[mktTier])
	acc, err := env.svc.SelectMarketTierCountTokensAccount(ctx, ptr64(mktTier), "claude-fable-5")
	require.NoError(t, err)
	require.Equal(t, int64(3), acc.ID)
	require.Len(t, env.store.requests, 1, "no attempt was spent on the Bedrock account")
	// The fresh row became incapable (e.g. switched to Bedrock): refused.
	env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		a := mktPlatformAccount(req.AccountID, mktTier)
		a.Type = AccountTypeBedrock
		return &MarketAuthoritativeFacts{AccountFound: true, Account: &a, MembershipSource: AccountGroupSourceAdmin,
			GroupFound: true, GroupMarketTier: true, GroupActive: true, GroupPlatform: PlatformAnthropic}
	}
	env.store.members = 1
	_, err = env.svc.SelectMarketTierCountTokensAccount(ctx, ptr64(mktTier), "claude-fable-5")
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
}

func TestMarketPlatformCandidateListedWhileWaiting(t *testing.T) {
	env, upstream, acc := marketForwardEnv(t)
	acc.MarketGroupIDs, acc.MarketListing, acc.OwnerUserID = nil, nil, nil // cached as a tier platform account
	env.rt.SetState(&MarketDispatchState{Enabled: true, ListedAccountIDs: []int64{acc.ID}})
	c, rec, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, _ := WithMarketAttempt(c.Request.Context(), &MarketSelection{Class: MarketClassPlatform, AccountID: acc.ID, GroupID: mktTier, Concurrency: 4})
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover, "Messages reselects, never a client error")
	var clientErr *ClientRequestError
	require.False(t, errors.As(err, &clientErr))
	require.Zero(t, upstream.calls)
	require.NotEqual(t, 400, rec.Code)

	c2, rec2, parsed2 := claudeCodeCacheSafeRetryContext(t)
	err = env.svc.ForwardCountTokens(c2.Request.Context(), c2, acc, parsed2)
	require.Error(t, err)
	require.False(t, errors.As(err, &clientErr))
	require.Equal(t, 503, rec2.Code, "count_tokens: temporarily unavailable")
	require.Zero(t, upstream.calls)
}

// ---- independent review round 3 ----

// listingDuringRegistrationStub lists the account (registry) while the plain
// carpool device registration of an ordinary attempt is in progress.
type listingDuringRegistrationStub struct {
	identityCacheStub
	onRegister func()
}

func (s *listingDuringRegistrationStub) GetOrCreateCarpoolDevice(_ context.Context, _ int64, device string, _ ClientHints, _ int, now int64) (*CarpoolDeviceRecord, error) {
	if s.onRegister != nil {
		s.onRegister()
	}
	return &CarpoolDeviceRecord{OriginalDeviceID: device, CreatedAt: now}, nil
}

func TestMarketListedDuringDeviceRegistrationNeverSends(t *testing.T) {
	newEnv := func(t *testing.T) (*marketEnv, *queuedClaudeCodeUpstream, *Account, *bool) {
		env, upstream, acc := marketForwardEnv(t)
		// An ordinary owner-group carpool account, not listed yet.
		acc.MarketGroupIDs, acc.MarketListing = nil, nil
		env.rt.SetState(&MarketDispatchState{Enabled: true})
		registered := false
		env.svc.identityService = NewIdentityService(&listingDuringRegistrationStub{onRegister: func() {
			registered = true
			env.rt.SetState(&MarketDispatchState{Enabled: true, ListedAccountIDs: []int64{acc.ID}})
		}}, strings.Repeat("x", 32))
		return env, upstream, acc, &registered
	}

	t.Run("messages fails over", func(t *testing.T) {
		env, upstream, acc, registered := newEnv(t)
		c, rec, parsed := claudeCodeCacheSafeRetryContext(t)
		_, err := env.svc.Forward(c.Request.Context(), c, acc, parsed)
		require.True(t, *registered, "the listing happened during device registration")
		var failover *UpstreamFailoverError
		require.ErrorAs(t, err, &failover)
		require.Zero(t, upstream.calls)
		require.NotEqual(t, 400, rec.Code)
	})
	t.Run("count_tokens answers 503 once", func(t *testing.T) {
		env, upstream, acc, registered := newEnv(t)
		c, rec, parsed := claudeCodeCacheSafeRetryContext(t)
		err := env.svc.ForwardCountTokens(c.Request.Context(), c, acc, parsed)
		require.True(t, *registered)
		require.Error(t, err)
		require.Zero(t, upstream.calls)
		require.Equal(t, 503, rec.Code)
		require.Equal(t, 1, strings.Count(rec.Body.String(), "temporarily unavailable"), "written once")
	})
}

func TestMarketTierCountTokensSkipsManyIncapableAccounts(t *testing.T) {
	var accounts []Account
	for i := int64(10); i < 15; i++ {
		b := mktPlatformAccount(i, mktTier)
		b.Type, b.Priority = AccountTypeBedrock, 0
		b.Credentials = map[string]any{"auth_mode": "apikey", "api_key": "k", "aws_region": "us-east-1"}
		accounts = append(accounts, b)
	}
	good := mktPlatformAccount(3, mktTier)
	good.Priority = 50
	accounts = append(accounts, good)
	env := newMarketEnv(t, accounts)
	require.True(t, env.svc.isModelSupportedByAccount(&accounts[0], "claude-sonnet-4-5"), "the Bedrock accounts do support the model")
	ctx := context.WithValue(WithAccountUserID(context.Background(), mktBuyer), ctxkey.Group, mktGroups()[mktTier])
	acc, err := env.svc.SelectMarketTierCountTokensAccount(ctx, ptr64(mktTier), "claude-sonnet-4-5")
	require.NoError(t, err, "five higher-priority Bedrock accounts never exhaust the attempts")
	require.Equal(t, int64(3), acc.ID)
	require.Len(t, env.store.requests, 1)
}

// The scheduler re-reads the single-account snapshot after ranking and
// returns that copy: it must itself be count_tokens capable.
func TestMarketTierCountTokensChecksTheReturnedCopy(t *testing.T) {
	listed := mktPlatformAccount(2, mktTier) // what the bucket says
	listed.Priority = 0
	freshBedrock := listed
	freshBedrock.Type = AccountTypeBedrock
	freshBedrock.Credentials = map[string]any{"auth_mode": "apikey", "api_key": "k", "aws_region": "us-east-1"}
	good := mktPlatformAccount(3, mktTier)
	good.Priority = 50
	env := newMarketEnv(t, []Account{listed, good})
	env.svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{&listed, &good},
		accountsByID:     map[int64]*Account{2: &freshBedrock, 3: &good},
	}}
	ctx := context.WithValue(WithAccountUserID(context.Background(), mktBuyer), ctxkey.Group, mktGroups()[mktTier])
	for i := 0; i < 5; i++ {
		acc, err := env.svc.SelectMarketTierCountTokensAccount(ctx, ptr64(mktTier), "claude-sonnet-4-5")
		require.NoError(t, err)
		require.Equal(t, int64(3), acc.ID, "a returned Bedrock copy is excluded and the loop continues")
	}
}

// ---- independent review round 4 ----

// The account gets listed between a same-account replay's response and the
// replay send: the round ends with that response's own outcome (the
// cache-safe client-retryable 502), never a failover to another account.
func TestMarketListedDuringCacheSafeReplayKeepsClientRetryable(t *testing.T) {
	c, rec, parsed := claudeCodeCacheSafeRetryContext(t)
	acc := claudeCodeCacheSafeRetryAccount() // ordinary, not listed yet
	rt := NewMarketRuntime(&fakeMarketStore{})
	rt.SetState(&MarketDispatchState{Enabled: true})
	upstream := &queuedClaudeCodeUpstream{responses: []func() *http.Response{
		func() *http.Response {
			// Listed while the cache-safe backoff runs.
			rt.SetState(&MarketDispatchState{Enabled: true, ListedAccountIDs: []int64{acc.ID}})
			return transient502UnexpectedEOFResponse()
		},
		okClaudeStreamResponse,
	}}
	svc := claudeCodeCacheSafeRetryService(upstream)
	svc.market = rt
	_, err := svc.Forward(c.Request.Context(), c, acc, parsed)
	require.Equal(t, 1, upstream.calls, "no replay through the now-listed account")
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "never replayed on another account")
	var clientRetryErr *ClientRetryableUpstreamError
	require.ErrorAs(t, err, &clientRetryErr)
	require.Equal(t, http.StatusBadGateway, rec.Code)
}

// Same for the generic same-account 5xx replay: one send, then that
// response's normal (no-replay) outcome.
func TestMarketListedDuringSameAccount5xxReplaySendsOnce(t *testing.T) {
	c, _, parsed := gatewayHTTPRetryRequest(t)
	acc := gatewayHTTPRetryAccount()
	rt := NewMarketRuntime(&fakeMarketStore{})
	rt.SetState(&MarketDispatchState{Enabled: true})
	upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{
		{response: func() *http.Response {
			rt.SetState(&MarketDispatchState{Enabled: true, ListedAccountIDs: []int64{acc.ID}})
			return gatewayHTTPErrorResponse(http.StatusServiceUnavailable)
		}},
		{response: gatewayHTTPSuccessResponse},
	}}
	svc := claudeCodeCacheSafeRetryService(upstream)
	svc.market = rt
	_, err := svc.Forward(c.Request.Context(), c, acc, parsed)
	require.Error(t, err)
	require.Equal(t, 1, upstream.calls, "no replay through the now-listed account")
	require.NotErrorIs(t, err, ErrMarketAdmissionRejected, "the 503's own outcome, not the market refusal")
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, http.StatusServiceUnavailable, failover.StatusCode)
	require.JSONEq(t, `{"type":"error","error":{"message":"temporary upstream failure"}}`, string(failover.ResponseBody),
		"the original response body survives the rebuild")
}
