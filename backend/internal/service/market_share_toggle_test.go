//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// 共享 / 自用 are exclusive (migration 138 revision of P3a): a listed account
// never serves its owner's hosted group; a stale "listed" view never blocks
// self-use.

func requireMarketInvalidated(t *testing.T, err error, invalidated bool) {
	t.Helper()
	var failover *UpstreamFailoverError
	require.True(t, errors.As(err, &failover), "fails over: %v", err)
	require.Equal(t, invalidated, failover.MarketInvalidated, "MarketInvalidated")
	require.False(t, failover.ForceCacheBilling)
	require.ErrorIs(t, err, ErrMarketAdmissionRejected)
}

func listedFacts(base func(*MarketAdmissionRequest) *MarketAuthoritativeFacts) func(*MarketAdmissionRequest) *MarketAuthoritativeFacts {
	return func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		f := base(req)
		selected := mktTier
		f.Listing = &MarketListing{AccountID: req.AccountID, OwnerUserID: mktOwner, Plan: mktPlan(), SelectedGroupID: &selected,
			Status: MarketListingStatusListed, Version: 4}
		return f
	}
}

func TestMarketSharedAccountNeverServesOwnerGroup(t *testing.T) {
	env, upstream, acc, dev := ownerForwardEnv(t, true)
	env.store.facts = listedFacts(env.store.facts)
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, _ := ownerForwardCtx(c.Request.Context(), ownerKey(mktOwner, mktHosted))
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	requireMarketInvalidated(t, err, true)
	require.Zero(t, upstream.calls)
	require.Empty(t, dev.classes, "refused before the device registration")
	require.Len(t, env.store.requests, 1)
	require.Equal(t, marketRefusalShared, EvaluateMarketAdmission(
		&MarketSelection{Class: MarketClassOwner, AccountID: 1, GroupID: mktHosted, OwnerUserID: mktOwner},
		&MarketAdmissionRequest{Class: MarketClassOwner, AccountID: 1, GroupID: mktHosted, AuthUserID: mktOwner},
		env.store.facts(&MarketAdmissionRequest{AccountID: 1})))
}

// Opposite timing 2: the unlisting committed, the cached copy and the
// registry still say listed. The owner candidate is checked in the database
// and served (self-use is never blocked by a stale view).
func TestMarketStaleListedViewServesSelfUse(t *testing.T) {
	env, upstream, acc, dev := ownerForwardEnv(t, true)
	env.rt.SetState(&MarketDispatchState{Enabled: true, ListedAccountIDs: []int64{acc.ID}})
	require.NotNil(t, acc.MarketListing, "cached copy still carries the listing")
	unlisted := env.store.facts
	env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		f := unlisted(req)
		f.Listing = &MarketListing{AccountID: req.AccountID, OwnerUserID: mktOwner, Plan: mktPlan(), Status: MarketListingStatusUnlisted, Version: 5}
		return f
	}
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, _ := ownerForwardCtx(c.Request.Context(), ownerKey(mktOwner, mktHosted))
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	require.NoError(t, err)
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, []string{MarketDeviceClassOwner}, dev.classes)
	require.Len(t, env.store.requests, 2, "checked before the device registration and before the send")
}

// The registry never loaded (database down at start): every owned account
// looks listed. The owner still gets self-use after the database check; a
// database error refuses temporarily, never as "shared".
func TestMarketFirstLoadMissingOwnerPath(t *testing.T) {
	env, upstream, acc, _ := ownerForwardEnv(t, true)
	env.svc.market = NewMarketRuntime(env.store) // never loaded
	acc.MarketListing, acc.MarketGroupIDs = nil, nil
	require.True(t, env.svc.marketAccountListed(acc))
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, _ := ownerForwardCtx(c.Request.Context(), ownerKey(mktOwner, mktHosted))
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	require.NoError(t, err)
	require.Equal(t, 1, upstream.calls)

	env.store.admitErr = errors.New("db down")
	c, _, parsed = claudeCodeCacheSafeRetryContext(t)
	ctx, _ = ownerForwardCtx(c.Request.Context(), ownerKey(mktOwner, mktHosted))
	_, err = env.svc.Forward(ctx, c, acc, parsed)
	requireMarketInvalidated(t, err, false)
	require.Equal(t, 1, upstream.calls)
}

// A plain attempt (selected while the account looked unlisted) that sees a
// listed view at the dispatch boundary: in the owner's hosted group it is
// upgraded to an owner attempt (database decides); elsewhere it reselects
// without the account (MarketInvalidated).
func TestMarketGateUpgradesPlainOwnerAttempt(t *testing.T) {
	env, upstream, acc, dev := ownerForwardEnv(t, true)
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	hosted := mktGroups()[mktHosted]
	key := ownerKey(mktOwner, mktHosted)
	key.Group = hosted
	ctx := WithMarketDispatch(WithAccountUserID(c.Request.Context(), mktOwner), key)
	ctx, attempt := WithMarketAttempt(ctx, nil)
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	require.NoError(t, err)
	require.Equal(t, MarketClassOwner, attempt.Selection.Class)
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, []string{MarketDeviceClassOwner}, dev.classes)
	require.Len(t, env.store.requests, 2)

	// Shared in the database: refused (no upstream, no device).
	env.store.facts = listedFacts(env.store.facts)
	ctx, _ = WithMarketAttempt(WithMarketDispatch(WithAccountUserID(c.Request.Context(), mktOwner), key), nil)
	_, err = env.svc.Forward(ctx, c, acc, parsed)
	requireMarketInvalidated(t, err, true)
	require.Equal(t, 1, upstream.calls)

	// Another group (or another user): never the owner path.
	plain := mktPlain
	other := &APIKey{ID: 8, UserID: mktOwner, GroupID: &plain, Group: mktGroups()[mktPlain], User: &User{ID: mktOwner}}
	ctx, _ = WithMarketAttempt(WithMarketDispatch(WithAccountUserID(c.Request.Context(), mktOwner), other), nil)
	_, err = env.svc.Forward(ctx, c, acc, parsed)
	requireMarketInvalidated(t, err, true)
	require.Equal(t, 1, upstream.calls)
}

// Opposite timing 1: this process started a listing that has not committed
// (the database still says unlisted): the owner path avoids the account.
func TestMarketPendingListingAvoidedByOwnerPath(t *testing.T) {
	acc := mktListedAccount(1)
	acc.MarketListing, acc.MarketGroupIDs = nil, nil
	acc.GroupIDs = []int64{mktHosted}
	acc.AccountGroups = []AccountGroup{{GroupID: mktHosted, Group: mktGroups()[mktHosted]}}
	env := newMarketEnv(t, []Account{acc})
	env.rt.SetState(&MarketDispatchState{Enabled: true})
	ctx := ownerSelectionCtx()

	res, err := env.svc.SelectAccountWithLoadAwareness(ctx, ptr64(mktHosted), "", "claude-fable-5", nil, "")
	require.NoError(t, err)
	require.Nil(t, res.Market, "unlisted: the plain path")
	res.ReleaseFunc()

	token := env.rt.BeginListing(acc.ID)
	_, err = env.svc.SelectAccountWithLoadAwareness(ctx, ptr64(mktHosted), "", "claude-fable-5", nil, "")
	require.ErrorIs(t, err, ErrNoAvailableAccounts, "the pending listing is avoided")
	env.rt.AbortListing(acc.ID, token)
	res, err = env.svc.SelectAccountWithLoadAwareness(ctx, ptr64(mktHosted), "", "claude-fable-5", nil, "")
	require.NoError(t, err, "a rolled-back listing is usable again at once")
	res.ReleaseFunc()
}

func ownerSelectionCtx() context.Context {
	hosted := mktHosted
	key := &APIKey{ID: 6, UserID: mktOwner, GroupID: &hosted, Group: mktGroups()[mktHosted], User: &User{ID: mktOwner, Status: StatusActive}}
	return WithMarketDispatch(WithAccountUserID(context.Background(), mktOwner), key)
}

// Every selection path that can hand a listed account to the owner's hosted
// Key (load-aware layers, direct sticky hit) attaches the owner class, so
// the dispatch boundary always runs the database check.
func TestMarketListedAccountSelectedOnlyAsOwnerCandidate(t *testing.T) {
	acc := mktListedAccount(1)
	acc.GroupIDs = []int64{mktHosted}
	acc.AccountGroups = []AccountGroup{{GroupID: mktHosted, Group: mktGroups()[mktHosted]}}
	env := newMarketEnv(t, []Account{acc})
	ctx := ownerSelectionCtx()

	res, err := env.svc.SelectAccountWithLoadAwareness(ctx, ptr64(mktHosted), "", "claude-fable-5", nil, "")
	require.NoError(t, err)
	require.NotNil(t, res.Market)
	require.Equal(t, MarketClassOwner, res.Market.Class)
	res.ReleaseFunc()

	cache, ok := env.svc.cache.(*oauthStickyGatewayCacheStub)
	require.True(t, ok)
	cache.binding = acc.ID
	res, err = env.svc.SelectAccountWithLoadAwareness(ctx, ptr64(mktHosted), "session-1", "claude-fable-5", nil, "")
	require.NoError(t, err)
	require.Equal(t, acc.ID, res.Account.ID)
	require.NotNil(t, res.Market, "direct sticky hit")
	require.Equal(t, MarketClassOwner, res.Market.Class)
	if res.ReleaseFunc != nil {
		res.ReleaseFunc()
	}
	// The wait plan of an initial-serial release keeps the owner class.
	require.Equal(t, MarketClassOwner, NewFallbackWaitPlan(res, time.Second, 3).Market.Class)

	// Another user of the group (trusted co-user): excluded.
	hosted := mktHosted
	coUser := WithMarketDispatch(WithAccountUserID(context.Background(), 99),
		&APIKey{ID: 9, UserID: 99, GroupID: &hosted, Group: mktGroups()[mktHosted], User: &User{ID: 99}})
	cache.binding = 0
	_, err = env.svc.SelectAccountWithLoadAwareness(coUser, ptr64(mktHosted), "", "claude-fable-5", nil, "")
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
}

func TestMarketSelectionChangedOwnerAndPlainAreSelfUse(t *testing.T) {
	env := newMarketEnv(t, nil)
	ctx := ownerSelectionCtx()
	ctx = context.WithValue(ctx, ctxkey.Group, mktGroups()[mktHosted])
	ctx = withMarketRequestedModel(withMarketSelectionGroup(ctx, ptr64(mktHosted)), "claude-fable-5")
	plain := mktListedAccount(1)
	plain.MarketListing, plain.MarketGroupIDs = nil, nil
	plain.GroupIDs = []int64{mktHosted}
	stale := plain
	stale.MarketListing = &AccountMarketListing{GroupID: mktTier, OwnerUserID: mktOwner, Version: 3}
	env.rt.SetState(&MarketDispatchState{Enabled: true})
	require.False(t, env.svc.marketSelectionChanged(ctx, &plain, &stale), "unlisted → stale listed fresh copy: decided at the boundary")
	class, _ := env.svc.classifyMarketCandidate(ctx, &plain)
	require.Equal(t, MarketClassNone, class)
	require.False(t, env.svc.marketSelectionChanged(ctx, &stale, &plain), "listed copy → fresh unlisted copy: still self-use")
	class, sel := env.svc.classifyMarketCandidate(ctx, &plain)
	require.Equal(t, MarketClassOwner, class, "the listed evidence of the selected copy is kept: the owner check still runs")
	require.Equal(t, MarketClassOwner, sel.Class)
	token := env.rt.BeginListing(1)
	require.True(t, env.svc.marketSelectionChanged(ctx, &plain, &plain), "a listing write in progress drops the candidate")
	env.rt.EndListing(1, token)
	require.False(t, env.rt.marketListingPending(1), "ended: the database check decides from now on")
	require.True(t, env.rt.IsListed(1), "but the account stays possibly listed until a later poll")
}

// The bucket copy says listed, the fresh copy (written back without the
// market fields) and the registry say unlisted: the selection keeps the
// owner class for the fresh copy, and the dispatch boundary refuses it when
// the database says listed (and serves it when it says unlisted).
func TestMarketOwnerToPlainFreshCopyKeepsDatabaseCheck(t *testing.T) {
	env, upstream, acc, dev := ownerForwardEnv(t, true)
	env.rt.SetState(&MarketDispatchState{Enabled: true})
	bucket := *acc
	fresh := *acc
	fresh.MarketListing, fresh.MarketGroupIDs = nil, nil
	env.store.facts = listedFacts(env.store.facts)

	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	hosted := mktGroups()[mktHosted]
	key := ownerKey(mktOwner, mktHosted)
	key.Group = hosted
	ctx := WithMarketDispatch(WithAccountUserID(c.Request.Context(), mktOwner), key)
	sctx := withMarketRequestedModel(withMarketSelectionGroup(context.WithValue(ctx, ctxkey.Group, hosted), ptr64(mktHosted)), "claude-fable-5")
	require.False(t, env.svc.marketSelectionChanged(sctx, &bucket, &fresh))
	res, err := env.svc.attachMarketSelection(sctx, &AccountSelectionResult{Account: &fresh, Acquired: true, ReleaseFunc: func() {}})
	require.NoError(t, err)
	require.NotNil(t, res.Market)
	require.Equal(t, MarketClassOwner, res.Market.Class)

	fctx, _ := WithMarketAttempt(ctx, res.Market)
	_, err = env.svc.Forward(fctx, c, &fresh, parsed)
	requireMarketInvalidated(t, err, true)
	require.Zero(t, upstream.calls)
	require.Empty(t, dev.classes)
	require.Len(t, env.store.requests, 1, "the database decided")
}

// A listing of this process starts after the owner candidate was selected:
// the owner admission refuses (before the device registration and before
// the send); a resend after an upstream error re-checks the database.
func TestMarketOwnerAttemptRefusedWhileListingRuns(t *testing.T) {
	env, upstream, acc, dev := ownerForwardEnv(t, true)
	token := env.rt.BeginListing(acc.ID)
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, _ := ownerForwardCtx(c.Request.Context(), ownerKey(mktOwner, mktHosted))
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	requireMarketInvalidated(t, err, true)
	require.Zero(t, upstream.calls)
	require.Empty(t, dev.classes)
	require.Empty(t, env.store.requests, "refused before any database read")
	env.rt.AbortListing(acc.ID, token)

	// Owner resend after a 5xx: the listing committed meanwhile is seen.
	calls := 0
	unlisted := env.store.facts
	env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		calls++
		if calls <= 2 {
			return unlisted(req)
		}
		return listedFacts(unlisted)(req)
	}
	require.True(t, env.svc.marketOwnerResendRefused(func() context.Context {
		c2, _, _ := claudeCodeCacheSafeRetryContext(t)
		x, _ := ownerForwardCtx(c2.Request.Context(), ownerKey(mktOwner, mktHosted))
		calls = 2
		return x
	}(), acc), "the resend re-check reads the database and refuses")
	plainCtx, _ := WithMarketAttempt(context.Background(), nil)
	require.False(t, env.svc.marketOwnerResendRefused(plainCtx, acc), "non-owner attempts keep their admission")
}

func TestMarketRefusalInvalidationSet(t *testing.T) {
	for _, r := range []string{marketRefusalShared, marketRefusalPending, "not_listed", "not_market_member", "listing_changed",
		"plan_mismatch", "owner_changed", "owner_version", "market_disabled"} {
		require.True(t, marketRefusalInvalidatesListing(r), r)
	}
	for _, r := range []string{"account_inactive", "account_missing", "group_inactive", "not_priced", "not_allowed", "owner_inactive",
		"capacity_changed", "device_policy_changed", "not_owner", "not_owner_group", "ineligible_max_sessions_enabled", "rejected"} {
		require.False(t, marketRefusalInvalidatesListing(r), r)
	}
}

func TestMarketBuyerRefusalAfterOwnerTakeBackIsInvalidated(t *testing.T) {
	env, upstream, acc := marketForwardEnv(t)
	listed := env.store.facts
	env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		f := listed(req)
		f.Listing.Status = MarketListingStatusUnlisted // the owner switched to self-use while the buyer waited
		f.MembershipSource = ""
		return f
	}
	c, _, parsed := claudeCodeCacheSafeRetryContext(t)
	ctx, attempt := buyerForwardCtx(t, c.Request.Context())
	_, err := env.svc.Forward(ctx, c, acc, parsed)
	requireMarketInvalidated(t, err, true)
	require.Zero(t, upstream.calls)
	_, proof := attempt.SettlementFields()
	require.Nil(t, proof)

	// An account-health refusal keeps the generic failover.
	env.store.facts = func(req *MarketAdmissionRequest) *MarketAuthoritativeFacts {
		f := listed(req)
		f.Account.Schedulable = false
		return f
	}
	c, _, parsed = claudeCodeCacheSafeRetryContext(t)
	ctx, _ = buyerForwardCtx(t, c.Request.Context())
	_, err = env.svc.Forward(ctx, c, acc, parsed)
	requireMarketInvalidated(t, err, false)
}

func TestMarketHostedAccountSharedBlocked(t *testing.T) {
	env := newMarketEnv(t, []Account{mktListedAccount(1)})
	hosted := mktHosted
	key := &APIKey{ID: 6, UserID: mktOwner, GroupID: &hosted, Group: mktGroups()[mktHosted], User: &User{ID: mktOwner}}
	ctx := context.Background()

	env.store.sharedTotal, env.store.shared = 1, []int64{1}
	require.True(t, env.svc.HostedAccountSharedBlocked(ctx, key))
	reads := env.store.sharedReads

	// Not every account is shared (another self-use account, e.g. rate
	// limited or device full): the existing error stays.
	env.store.sharedTotal = 2
	require.False(t, env.svc.HostedAccountSharedBlocked(ctx, key))
	env.store.sharedTotal, env.store.shared = 0, nil
	require.False(t, env.svc.HostedAccountSharedBlocked(ctx, key), "no account at all")
	// Database failure: never reported as shared.
	env.store.sharedTotal, env.store.shared, env.store.sharedErr = 1, []int64{1}, errors.New("db down")
	require.False(t, env.svc.HostedAccountSharedBlocked(ctx, key))
	env.store.sharedErr = nil
	// Not an owner hosted group: no read at all.
	reads = env.store.sharedReads
	tier := mktTier
	require.False(t, env.svc.HostedAccountSharedBlocked(ctx, &APIKey{ID: 7, UserID: mktOwner, GroupID: &tier, Group: mktGroups()[mktTier]}))
	plain := mktPlain
	require.False(t, env.svc.HostedAccountSharedBlocked(ctx, &APIKey{ID: 7, UserID: mktOwner, GroupID: &plain, Group: mktGroups()[mktPlain]}))
	require.False(t, env.svc.HostedAccountSharedBlocked(ctx, &APIKey{ID: 7, UserID: mktOwner}))
	require.Equal(t, reads, env.store.sharedReads)
	// Nothing listed anywhere (saiai.top with the market off): no read.
	env.rt.SetState(&MarketDispatchState{})
	require.False(t, env.svc.HostedAccountSharedBlocked(ctx, key))
	require.Equal(t, reads, env.store.sharedReads)
	require.False(t, (&GatewayService{}).HostedAccountSharedBlocked(ctx, key), "no market runtime")
}

func TestMarketHostedSharedLogThrottle(t *testing.T) {
	rt := NewMarketRuntime(&fakeMarketStore{})
	require.True(t, rt.hostedSharedLogDue(1))
	require.False(t, rt.hostedSharedLogDue(1), "once per hour per account")
	require.True(t, rt.hostedSharedLogDue(2))
	rt.sharedLogged[1] = time.Now().Add(-61 * time.Minute)
	require.True(t, rt.hostedSharedLogDue(1))
}

// Reservations are always 0: an old snapshot (or row) with protection
// values gives buyers the whole account.
func TestMarketLegacyReservationsIgnored(t *testing.T) {
	var snap AccountMarketListing
	require.NoError(t, json.Unmarshal([]byte(`{"group_id":30,"owner_user_id":7,"version":3,
		"protection":{"reserved_concurrency":3,"reserved_devices":4,"reserved_admissions":1}}`), &snap))
	acc := mktListedAccount(1)
	acc.MarketListing = &snap
	env := newMarketEnv(t, []Account{acc})
	ctx := withMarketRequestedModel(withMarketSelectionGroup(context.WithValue(buyerCtx(mktTier), ctxkey.Group, mktGroups()[mktTier]), ptr64(mktTier)), "claude-fable-5")
	class, sel := env.svc.classifyMarketCandidate(ctx, &acc)
	require.Equal(t, MarketClassBuyer, class)
	require.Equal(t, acc.Concurrency, sel.BuyerMaxConcurrency)
	require.Equal(t, &MarketDeviceAdmission{Class: MarketDeviceClassBuyer},
		marketDeviceAdmissionFor(func() context.Context { c, _ := WithMarketAttempt(ctx, sel); return c }(), &acc))
}

// A paused account serves nobody while it keeps its 共享 mode.
func TestMarketPausedListedAccountServesNobody(t *testing.T) {
	env := newMarketEnv(t, nil)
	paused := mktListedAccount(1)
	now := time.Now()
	paused.OwnerPausedAt = &now
	ctx := withMarketRequestedModel(withMarketSelectionGroup(context.WithValue(buyerCtx(mktTier), ctxkey.Group, mktGroups()[mktTier]), ptr64(mktTier)), "claude-fable-5")
	class, _ := env.svc.classifyMarketCandidate(ctx, &paused)
	require.Equal(t, MarketClassExcluded, class, "buyers excluded at selection")
	require.True(t, shouldClearStickySession(&paused, "claude-fable-5"), "sticky bindings are cleared")
	f := &MarketAuthoritativeFacts{AccountFound: true, Account: &paused, OwnerPaused: true}
	require.Equal(t, "account_inactive", EvaluateMarketAdmission(&MarketSelection{Class: MarketClassBuyer}, &MarketAdmissionRequest{}, f))
	require.Equal(t, "account_inactive", EvaluateMarketAdmission(&MarketSelection{Class: MarketClassOwner}, &MarketAdmissionRequest{}, f))
}
