//go:build unit

package service

import (
	"context"
	"math"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func requireMarketReason(t *testing.T, err error, reason string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, infraerrors.FromError(err).Reason, err.Error())
}

func TestMarketTierRateStoredPositive(t *testing.T) {
	for _, tc := range []struct {
		rate float64
		want bool
	}{
		{0, false}, {-1, false}, {math.NaN(), false}, {math.Inf(1), false},
		{0.00001, false}, {0.00004, false}, {0.00005, true}, {0.0001, true}, {1, true},
	} {
		require.Equal(t, tc.want, MarketTierRateStoredPositive(tc.rate), "rate %v", tc.rate)
	}
}

func TestGroupValidateMarketTier(t *testing.T) {
	ok := func() *Group {
		return &Group{MarketTier: true, Platform: PlatformAnthropic, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1.2}
	}
	require.NoError(t, ok().ValidateMarketTier())
	require.NoError(t, (&Group{Platform: PlatformOpenAI}).ValidateMarketTier(), "not a tier")
	for name, mutate := range map[string]func(*Group){
		"openai":       func(g *Group) { g.Platform = PlatformOpenAI },
		"subscription": func(g *Group) { g.SubscriptionType = SubscriptionTypeSubscription },
		"hosted":       func(g *Group) { g.HostedUsageEnabled = true },
		"zero rate":    func(g *Group) { g.RateMultiplier = 0 },
		"tiny rate":    func(g *Group) { g.RateMultiplier = 0.00001 },
	} {
		g := ok()
		mutate(g)
		require.Error(t, g.ValidateMarketTier(), name)
		requireMarketReason(t, g.ValidateMarketTier(), "INVALID_MARKET_TIER")
	}
	g := ok()
	fb := int64(9)
	g.FallbackGroupID = &fb
	requireMarketReason(t, g.ValidateMarketTier(), "MARKET_TIER_FALLBACK")
}

// marketTierGroupRepo adds the tier guard to the fallback-chain stub.
type marketTierGroupRepo struct {
	groupRepoStubForFallbackCycle
	referrers   map[int64][]int64
	updated     *Group
	tierLookups int
}

func (s *marketTierGroupRepo) ListMarketTierIDs(_ context.Context, ids []int64) ([]int64, error) {
	s.tierLookups++
	var out []int64
	for _, id := range ids {
		if g, ok := s.groups[id]; ok && g.MarketTier {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *marketTierGroupRepo) Update(_ context.Context, g *Group) error {
	s.updated = g
	return nil
}

func (s *marketTierGroupRepo) ListFallbackReferrers(_ context.Context, groupID int64) ([]int64, error) {
	return s.referrers[groupID], nil
}

type marketTierRateRepo struct {
	userGroupRateRepoStubForGroupRate
	negative map[int64][]int64
}

func (s *marketTierRateRepo) ListNegativeRateUsers(_ context.Context, groupID int64) ([]int64, error) {
	return s.negative[groupID], nil
}

func newMarketTierRepos() (*marketTierGroupRepo, *marketTierRateRepo) {
	groups := map[int64]*Group{
		1: {ID: 1, Name: "tier", Platform: PlatformAnthropic, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1.2, Status: StatusActive, MarketTier: true},
		2: {ID: 2, Name: "plain", Platform: PlatformAnthropic, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1, Status: StatusActive},
		3: {ID: 3, Name: "via", Platform: PlatformAnthropic, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1, Status: StatusActive},
	}
	tierPtr := int64(1)
	groups[3].FallbackGroupID = &tierPtr // 3 → 1 (stored before 1 became a tier)
	return &marketTierGroupRepo{groupRepoStubForFallbackCycle: groupRepoStubForFallbackCycle{groups: groups}, referrers: map[int64][]int64{}},
		&marketTierRateRepo{negative: map[int64][]int64{}}
}

func TestFallbackChainsMayNotEnterMarketTier(t *testing.T) {
	groups, rates := newMarketTierRepos()
	svc := &adminServiceImpl{groupRepo: groups, userGroupRateRepo: rates}
	ctx := context.Background()
	// Direct and through a middle node (2 → 3 → 1).
	requireMarketReason(t, svc.validateFallbackGroup(ctx, 2, 1), "MARKET_TIER_FALLBACK")
	requireMarketReason(t, svc.validateFallbackGroup(ctx, 2, 3), "MARKET_TIER_FALLBACK")
	requireMarketReason(t, svc.validateFallbackGroupOnInvalidRequest(ctx, 2, PlatformAnthropic, SubscriptionTypeStandard, 1), "MARKET_TIER_FALLBACK")
	require.NoError(t, svc.validateFallbackGroupOnInvalidRequest(ctx, 3, PlatformAnthropic, SubscriptionTypeStandard, 2))
}

func TestUpdateGroupMarketTierPromotionGuards(t *testing.T) {
	ctx := context.Background()
	promote := func(groups *marketTierGroupRepo, rates UserGroupRateRepository, id int64) error {
		svc := &adminServiceImpl{groupRepo: groups, userGroupRateRepo: rates}
		on := true
		_, err := svc.UpdateGroup(ctx, id, &UpdateGroupInput{MarketTier: &on})
		return err
	}

	groups, rates := newMarketTierRepos()
	require.NoError(t, promote(groups, rates, 2))
	require.True(t, groups.updated.MarketTier)

	// Inbound fallback reference (claude_code_only or invalid-request).
	groups, rates = newMarketTierRepos()
	groups.referrers[2] = []int64{7}
	requireMarketReason(t, promote(groups, rates, 2), "MARKET_TIER_FALLBACK")
	require.Nil(t, groups.updated)
	// Middle-of-chain node: has its own fallback.
	groups, rates = newMarketTierRepos()
	groups.groups[3].FallbackGroupID = new(int64)
	*groups.groups[3].FallbackGroupID = 2
	requireMarketReason(t, promote(groups, rates, 3), "MARKET_TIER_FALLBACK")
	// Existing negative user override (a zero override is allowed: the
	// repository lists only negative / NaN ones).
	groups, rates = newMarketTierRepos()
	rates.negative[2] = []int64{42}
	requireMarketReason(t, promote(groups, rates, 2), "MARKET_TIER_RATE_OVERRIDE")
	require.Nil(t, groups.updated)
	// Zero group rate.
	groups, rates = newMarketTierRepos()
	groups.groups[2].RateMultiplier = 0
	requireMarketReason(t, promote(groups, rates, 2), "INVALID_MARKET_TIER")
	// A repository without the guard refuses instead of skipping.
	groups, _ = newMarketTierRepos()
	requireMarketReason(t, promote(groups, &userGroupRateRepoStubForGroupRate{}, 2), "MARKET_TIER_GUARD_UNAVAILABLE")
}

func TestMarketTierRateOverrideSavePaths(t *testing.T) {
	ctx := context.Background()
	groups, rates := newMarketTierRepos()
	svc := &adminServiceImpl{groupRepo: groups, userGroupRateRepo: rates}

	// Group-side bulk save: a negative override on a tier is refused.
	err := svc.BatchSetGroupRateMultipliers(ctx, 1, []GroupRateMultiplierInput{{UserID: 5, RateMultiplier: 1.1}, {UserID: 6, RateMultiplier: -0.001}})
	requireMarketReason(t, err, "MARKET_TIER_RATE_OVERRIDE")
	require.Zero(t, rates.syncedGroupID, "nothing written")
	// Zero (and rounds-to-zero) overrides on a tier are allowed (free self-use).
	require.NoError(t, svc.BatchSetGroupRateMultipliers(ctx, 1, []GroupRateMultiplierInput{{UserID: 5, RateMultiplier: 0}, {UserID: 6, RateMultiplier: 0.00001}}))
	require.Equal(t, int64(1), rates.syncedGroupID)
	require.NoError(t, svc.BatchSetGroupRateMultipliers(ctx, 2, []GroupRateMultiplierInput{{UserID: 5, RateMultiplier: 0}}), "non-tier groups unchanged")

	// User-side save: refused before anything is written (Update panics).
	users := &userRepoStub{user: &User{ID: 5, Role: RoleUser, Status: StatusActive}}
	svc = &adminServiceImpl{groupRepo: groups, userGroupRateRepo: rates, userRepo: users}
	zero := 0.0
	negative := -1.0
	_, err = svc.UpdateUser(ctx, 5, &UpdateUserInput{GroupRates: map[int64]*float64{2: &zero, 1: &negative}})
	requireMarketReason(t, err, "MARKET_TIER_RATE_OVERRIDE")
	_, err = svc.UpdateUser(ctx, 5, &UpdateUserInput{GroupRates: map[int64]*float64{1: &negative}})
	requireMarketReason(t, err, "MARKET_TIER_RATE_OVERRIDE")
	require.Empty(t, userGroupRatesToCheck(map[int64]*float64{1: nil}), "deleting an override is always allowed")

	// Values >= 0 at storage need no lookup at all (zero on a tier too); the
	// rest one batch.
	groups.tierLookups = 0
	one, tiny, negTiny, negSmall := 1.5, 0.00001, -0.00001, -0.0001
	require.NoError(t, svc.validateMarketTierRateOverrides(ctx, userGroupRatesToCheck(map[int64]*float64{1: &zero, 2: &one, 3: &tiny})))
	require.NoError(t, svc.validateMarketTierRateOverrides(ctx, userGroupRatesToCheck(map[int64]*float64{1: &negTiny})), "rounds to 0 at storage")
	require.Zero(t, groups.tierLookups)
	require.NoError(t, svc.validateMarketTierRateOverrides(ctx, userGroupRatesToCheck(map[int64]*float64{2: &negSmall, 3: &negative})))
	require.Equal(t, 1, groups.tierLookups)
	requireMarketReason(t, svc.validateMarketTierRateOverrides(ctx, userGroupRatesToCheck(map[int64]*float64{1: &negSmall})), "MARKET_TIER_RATE_OVERRIDE")
}

func TestMarketAccountIneligibleReason(t *testing.T) {
	carpool := func() *Account {
		return &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Extra: map[string]any{"claude_oauth_carpool_device_limit": 5}}
	}
	require.Empty(t, MarketAccountIneligibleReason(carpool()))
	oauth := carpool()
	oauth.Type = AccountTypeOAuth
	require.Equal(t, MarketReasonNotSetupToken, MarketAccountIneligibleReason(oauth))
	openai := carpool()
	openai.Platform = PlatformOpenAI
	require.Equal(t, MarketReasonNotAnthropic, MarketAccountIneligibleReason(openai))
	pinned := carpool()
	pinned.Extra["claude_oauth_mode"] = ClaudeOAuthModePinned
	require.Equal(t, MarketReasonNotCarpool, MarketAccountIneligibleReason(pinned))
	unlimited := carpool()
	unlimited.Extra["claude_oauth_carpool_unlimited_devices"] = true
	require.Equal(t, MarketReasonUnlimitedDevices, MarketAccountIneligibleReason(unlimited))
	sessions := carpool()
	sessions.Extra["max_sessions"] = 2
	require.Equal(t, MarketReasonMaxSessions, MarketAccountIneligibleReason(sessions))

	owner := int64(7)
	self := carpool()
	self.OwnerUserID, self.Status, self.Schedulable, self.SelfOnboarded = &owner, StatusActive, true, true
	require.Equal(t, MarketReasonProxyUnavailable, marketListBlockedReason(self))
	proxyID := int64(3)
	self.ProxyID, self.Proxy = &proxyID, &Proxy{ID: 3, Status: StatusActive, Host: "203.0.113.5"}
	require.Empty(t, marketListBlockedReason(self))
	self.Proxy.Status = "disabled"
	require.Equal(t, MarketReasonProxyUnavailable, marketListBlockedReason(self))
}

func TestValidateMarketProtectionForAccount(t *testing.T) {
	unlimited := &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Concurrency: 0, Extra: map[string]any{}}
	requireMarketReason(t, validateMarketProtectionForAccount(unlimited, MarketProtectionParams{}), "MARKET_INVALID_PARAMS")
	acc := &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Concurrency: 3,
		Extra: map[string]any{"claude_oauth_carpool_device_limit": 5, "claude_oauth_carpool_maintenance_admissions_per_cycle": 2}}
	p := MarketProtectionParams{ReservedConcurrency: 2, ReservedDevices: 4, ReservedAdmissions: 2}
	require.NoError(t, validateMarketProtectionForAccount(acc, p))
	for name, mutate := range map[string]func(*MarketProtectionParams){
		"concurrency": func(p *MarketProtectionParams) { p.ReservedConcurrency = 3 },
		"devices":     func(p *MarketProtectionParams) { p.ReservedDevices = 5 },
		"admissions":  func(p *MarketProtectionParams) { p.ReservedAdmissions = 3 },
		"negative":    func(p *MarketProtectionParams) { p.ReservedDevices = -1 },
	} {
		q := p
		mutate(&q)
		require.Error(t, validateMarketProtectionForAccount(acc, q), name)
		requireMarketReason(t, validateMarketProtectionForAccount(acc, q), "MARKET_INVALID_PARAMS")
	}
}

func TestParseMarketSettings(t *testing.T) {
	def := ParseMarketSettings(nil)
	require.False(t, def.Enabled)
	require.True(t, def.PlatformCutRate.Equal(decimal.RequireFromString("0.2")))
	require.Equal(t, 7, def.EarningFreezeDays)
	require.True(t, def.MinConvertUSD.Equal(decimal.NewFromInt(1)))
	require.Equal(t, MarketProtectionParams{ReservedConcurrency: 1, ReservedDevices: 2, ReservedAdmissions: 1}, def.DefaultProtection)

	for raw, want := range map[string]string{
		"0":       "0", // explicit 0 kept, never the default
		"1":       "1",
		"0.1234":  "0.1234",
		"1.00001": "0.2",
		"-0.1":    "0.2",
		"0.12345": "0.2",
		"abc":     "0.2",
	} {
		got := ParseMarketSettings(map[string]string{SettingKeyMarketPlatformCutRate: raw})
		require.True(t, got.PlatformCutRate.Equal(decimal.RequireFromString(want)), "raw %q → %s", raw, got.PlatformCutRate)
	}
	zero := ParseMarketSettings(map[string]string{SettingKeyMarketEarningFreezeDays: "0", SettingKeyMarketDefaultReservedConcurrency: "0"})
	require.Equal(t, 0, zero.EarningFreezeDays)
	require.Equal(t, 0, zero.DefaultProtection.ReservedConcurrency)
	require.Equal(t, 7, ParseMarketSettings(map[string]string{SettingKeyMarketEarningFreezeDays: "91"}).EarningFreezeDays)
	require.True(t, ParseMarketSettings(map[string]string{SettingKeyMarketMinConvertUSD: "0.001"}).MinConvertUSD.Equal(decimal.NewFromInt(1)))

	// Round trip through UpdateSettings keeps an explicit 0; a nil block writes nothing.
	updates := map[string]string{}
	s := ParseMarketSettings(nil)
	s.PlatformCutRate = decimal.Zero
	marketSettingUpdates(&SystemSettings{Market: &s}, updates)
	require.Equal(t, "0", updates[SettingKeyMarketPlatformCutRate])
	empty := map[string]string{}
	marketSettingUpdates(&SystemSettings{}, empty)
	require.Empty(t, empty)

	for _, tc := range []struct {
		v  float64
		ok bool
	}{{0, true}, {1, true}, {0.2, true}, {0.1234, true}, {1.00001, false}, {-0.0001, false}, {0.12345, false}, {math.NaN(), false}} {
		d, finite := MarketDecimalFromFloat(tc.v)
		require.Equal(t, tc.ok, finite && ValidMarketPlatformCutRate(d), "cut %v", tc.v)
	}
}

// ---- service views (fake repository) ----

type fakeMarketRepo struct {
	account      *Account
	listing      *MarketListing
	tiers        []MarketTierOption
	adminMembers []int64
	lastCfg      *MarketAdminConfigureParams
}

func (f *fakeMarketRepo) GetListing(context.Context, int64) (*MarketListing, error) {
	return f.listing, nil
}
func (f *fakeMarketRepo) GetAccountFacts(context.Context, int64) (*Account, error) {
	return f.account, nil
}
func (f *fakeMarketRepo) GetOwnedAccountFacts(_ context.Context, owner, _ int64) (*Account, error) {
	if f.account.OwnerUserID == nil || *f.account.OwnerUserID != owner {
		return nil, ErrHostedOwnedAccountNotFound
	}
	return f.account, nil
}
func (f *fakeMarketRepo) ListTierOptions(context.Context, []int64) ([]MarketTierOption, error) {
	return f.tiers, nil
}
func (f *fakeMarketRepo) HasAdminMembership(context.Context, int64, int64) (bool, error) {
	return false, nil
}
func (f *fakeMarketRepo) ListAdminMemberGroupIDs(context.Context, int64, []int64) ([]int64, error) {
	return f.adminMembers, nil
}
func (f *fakeMarketRepo) AdminConfigure(_ context.Context, p MarketAdminConfigureParams) (*MarketListing, error) {
	f.lastCfg = &p
	params := p.Defaults
	if p.Merge != nil {
		params = p.Merge(params)
	}
	if err := p.Validate(f.account, params); err != nil {
		return nil, err
	}
	f.listing = &MarketListing{AccountID: f.account.ID, OwnerUserID: *f.account.OwnerUserID, AllowedGroupIDs: p.AllowedGroupIDs, Status: MarketListingStatusUnlisted, Version: 1, Protection: params}
	return f.listing, nil
}
func (f *fakeMarketRepo) AdminRevoke(context.Context, int64, int64) (*MarketListing, error) {
	return f.listing, nil
}
func (f *fakeMarketRepo) OwnerSet(_ context.Context, p MarketOwnerSetParams) (*MarketListing, error) {
	if _, err := f.GetOwnedAccountFacts(context.Background(), p.OwnerUserID, p.AccountID); err != nil {
		return nil, err
	}
	if p.Listed && p.Validate != nil {
		protection := MarketProtectionParams{}
		if f.listing != nil {
			protection = f.listing.Protection
		}
		if err := p.Validate(f.account, protection); err != nil {
			return nil, err
		}
	}
	return f.listing, nil
}

type marketSettingRepo struct {
	SettingRepository
	values map[string]string
}

func (s *marketSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := s.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func marketTestAccount(owner int64) *Account {
	return &Account{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Concurrency: 3, Status: StatusActive, Schedulable: true,
		OwnerUserID: &owner, Extra: map[string]any{"claude_oauth_carpool_device_limit": 5}}
}

func TestMarketListingServiceViewsAndChecks(t *testing.T) {
	ctx := context.Background()
	repo := &fakeMarketRepo{account: marketTestAccount(7)}
	settings := &marketSettingRepo{values: map[string]string{}}
	svc := NewMarketListingService(repo, settings)

	view, err := svc.OwnerGet(ctx, 7, 11)
	require.NoError(t, err)
	require.Equal(t, MarketListingStatusNotConfigured, view.Status)
	require.Equal(t, []string{MarketReasonMarketDisabled, MarketReasonNotConfigured}, view.UnavailableReasons)
	require.False(t, view.CanList)
	_, err = svc.OwnerGet(ctx, 8, 11)
	requireMarketReason(t, err, "HOSTED_ACCOUNT_NOT_FOUND")

	// Admin: defaults + partial override; validation against the account.
	three := 3
	admin, err := svc.AdminConfigure(ctx, 1, 11, AdminMarketListingInput{AllowedGroupIDs: []int64{4, 3, 4}, ReservedDevices: &three})
	require.NoError(t, err)
	require.Equal(t, []int64{3, 4}, repo.lastCfg.AllowedGroupIDs, "deduped and sorted")
	require.Equal(t, 3, admin.Listing.Protection.ReservedDevices)
	require.Equal(t, 1, admin.Listing.Protection.ReservedConcurrency)
	tooMany := 3
	_, err = svc.AdminConfigure(ctx, 1, 11, AdminMarketListingInput{AllowedGroupIDs: []int64{3}, ReservedConcurrency: &tooMany})
	requireMarketReason(t, err, "MARKET_INVALID_PARAMS")
	_, err = svc.AdminConfigure(ctx, 1, 11, AdminMarketListingInput{AllowedGroupIDs: []int64{0}})
	requireMarketReason(t, err, "MARKET_INVALID_TIER")
	noOwner := &fakeMarketRepo{account: &Account{ID: 12, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}}
	_, err = NewMarketListingService(noOwner, settings).AdminConfigure(ctx, 1, 12, AdminMarketListingInput{AllowedGroupIDs: []int64{3}})
	requireMarketReason(t, err, "MARKET_ACCOUNT_NO_OWNER")

	settings.values[SettingKeyMarketEnabled] = "true"
	repo.tiers = []MarketTierOption{{GroupID: 3, Name: "T1", RateMultiplier: 1.2, Available: true}}
	view, err = svc.OwnerGet(ctx, 7, 11)
	require.NoError(t, err)
	require.True(t, view.CanList, view.UnavailableReasons)
	require.NotNil(t, view.Protection)

	// The only tier already has the account as an admin member: not listable.
	repo.adminMembers = []int64{3}
	view, err = svc.OwnerGet(ctx, 7, 11)
	require.NoError(t, err)
	require.True(t, view.AllowedTiers[0].AdminMember)
	require.Equal(t, []string{MarketReasonNoAllowedTier}, view.UnavailableReasons)
	repo.adminMembers = nil
	// Concurrency lowered below the stored reservation since the admin allowed it.
	repo.listing.Protection.ReservedConcurrency = 2
	repo.account.Concurrency = 2
	view, err = svc.OwnerGet(ctx, 7, 11)
	require.NoError(t, err)
	require.Equal(t, []string{MarketReasonProtectionInvalid}, view.UnavailableReasons)
	_, err = svc.OwnerSet(ctx, 7, 11, 3, true)
	requireMarketReason(t, err, "MARKET_INVALID_PARAMS")
	repo.account.Concurrency = 3

	// Paused owner: listing refused with the reason; unlisting allowed.
	now := repo.account.CreatedAt
	repo.account.OwnerPausedAt = &now
	view, err = svc.OwnerGet(ctx, 7, 11)
	require.NoError(t, err)
	require.Equal(t, []string{MarketReasonOwnerPaused}, view.UnavailableReasons)
	_, err = svc.OwnerSet(ctx, 7, 11, 3, true)
	requireMarketReason(t, err, "MARKET_ACCOUNT_NOT_ELIGIBLE")
	_, err = svc.OwnerSet(ctx, 7, 11, 0, false)
	require.NoError(t, err)
}

func TestResolveGatewayGroupNeverEntersMarketTier(t *testing.T) {
	groups, _ := newMarketTierRepos()
	tier := int64(1)
	// Group 4 (stale config) falls back into the tier: refused at runtime.
	groups.groups[4] = &Group{ID: 4, Platform: PlatformAnthropic, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1, Status: StatusActive,
		ClaudeCodeOnly: true, FallbackGroupID: &tier}
	svc := &GatewayService{groupRepo: groups}
	id := int64(4)
	_, _, err := svc.resolveGatewayGroup(context.Background(), &id)
	require.ErrorIs(t, err, ErrClaudeCodeOnly)
	plain := int64(2)
	groups.groups[4].FallbackGroupID = &plain
	g, _, err := svc.resolveGatewayGroup(context.Background(), &id)
	require.NoError(t, err)
	require.Equal(t, int64(2), g.ID)
}

func TestMarketSettingUpdatesOnlyProvidedKeys(t *testing.T) {
	s := ParseMarketSettings(nil)
	s.Enabled = true
	updates := map[string]string{}
	marketSettingUpdates(&SystemSettings{Market: &s, MarketFields: []string{SettingKeyMarketEarningFreezeDays}}, updates)
	require.Equal(t, map[string]string{SettingKeyMarketEarningFreezeDays: "7"}, updates, "the stale switch is not written back")
}

func TestIsAccountInGroupIgnoresMarketMemberships(t *testing.T) {
	svc := &GatewayService{}
	tier := int64(5)
	listed := &Account{ID: 1, MarketGroupIDs: []int64{tier}}
	require.False(t, svc.isAccountInGroup(listed, &tier), "market membership is not dispatchable")
	require.False(t, svc.isAccountInGroup(listed, nil), "a listed account is not ungrouped")
	plain := &Account{ID: 2}
	require.True(t, svc.isAccountInGroup(plain, nil))
	admin := &Account{ID: 3, AccountGroups: []AccountGroup{{GroupID: tier}}, GroupIDs: []int64{tier}}
	require.True(t, svc.isAccountInGroup(admin, &tier))

	// Window-cost disclosure: a listed account is not used only by its owner.
	owned := &Account{GroupIDs: []int64{9}}
	require.True(t, hostedAccountUsedOnlyBy(owned, 9, 1))
	owned.MarketGroupIDs = []int64{tier}
	require.False(t, hostedAccountUsedOnlyBy(owned, 9, 1))
}

type marketUserRepo struct {
	userRepoStub
	updates int
}

func (r *marketUserRepo) Update(_ context.Context, _ *User) error {
	r.updates++
	return nil
}

type rejectingRateRepo struct {
	marketTierRateRepo
}

func (r *rejectingRateRepo) SyncUserGroupRates(context.Context, int64, map[int64]*float64) error {
	return ErrMarketTierRateOverride
}

func TestUpdateUserReportsRaceRejectedOverrideAfterFinishingCommit(t *testing.T) {
	groups, _ := newMarketTierRepos()
	users := &marketUserRepo{userRepoStub: userRepoStub{user: &User{ID: 5, Role: RoleUser, Status: StatusActive}}}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{groupRepo: groups, userGroupRateRepo: &rejectingRateRepo{}, userRepo: users, authCacheInvalidator: invalidator}
	rate := -1.0
	// The pre-check passes (group 2 is not a tier, a negative override is
	// not refused there); the database then rejects it (promoted meanwhile).
	_, err := svc.UpdateUser(context.Background(), 5, &UpdateUserInput{Status: StatusDisabled, GroupRates: map[int64]*float64{2: &rate}})
	requireMarketReason(t, err, "MARKET_TIER_RATE_OVERRIDE")
	require.Equal(t, 1, users.updates, "the user change was committed")
	require.Equal(t, []int64{5}, invalidator.userIDs, "and its cache invalidation still ran")
}
