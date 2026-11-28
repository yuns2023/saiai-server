package service

import (
	"context"
	"slices"
	"time"
)

// MarketListingService serves the admin and owner listing endpoints. The
// lifecycle writes are atomic in the repository; this layer reads settings,
// shapes views and supplies the account checks run under the row lock.
type MarketListingService struct {
	repo        MarketListingRepository
	settingRepo SettingRepository
	// listings is told about a listing before its transaction starts, so
	// this process treats the account as listed even before the registry
	// poll (or a stale cached copy) catches up.
	listings MarketListingObserver
	now      func() time.Time
}

// MarketListingObserver is implemented by MarketRuntime.
type MarketListingObserver interface {
	// BeginListing returns a token that finalizes exactly this listing.
	BeginListing(accountID int64) uint64
	// AbortListing: this listing's transaction rolled back before its commit.
	AbortListing(accountID int64, token uint64)
	// EndListing: this listing's transaction ended (committed or unknown).
	EndListing(accountID int64, token uint64)
}

// SetListingObserver wires the dispatch registry (P3a part 2).
func (s *MarketListingService) SetListingObserver(o MarketListingObserver) {
	if s != nil {
		s.listings = o
	}
}

func NewMarketListingService(repo MarketListingRepository, settingRepo SettingRepository) *MarketListingService {
	return &MarketListingService{repo: repo, settingRepo: settingRepo, now: time.Now}
}

// AdminMarketListingView is the body of GET/PUT/DELETE
// /api/v1/admin/accounts/:id/market-listing.
type AdminMarketListingView struct {
	AccountID   int64  `json:"account_id"`
	OwnerUserID *int64 `json:"owner_user_id"`
	// Listing is null when the account was never configured; listing.plan
	// is the account's plan (null = none).
	Listing *MarketListing `json:"listing"`
	// AllowedTiers: the tiers derived from the plan.
	AllowedTiers []MarketTierOption `json:"allowed_tiers"`
	// IneligibleReason: why the account type/config cannot be listed ("" = ok).
	IneligibleReason string `json:"ineligible_reason"`
	MarketEnabled    bool   `json:"market_enabled"`
}

// OwnerMarketView is the body of GET/PUT /api/v1/hosted/owned-accounts/:id/market.
type OwnerMarketView struct {
	AccountID     int64 `json:"account_id"`
	MarketEnabled bool  `json:"market_enabled"`
	// Status: not_configured | unlisted | listed | revoked.
	Status string `json:"status"`
	// Mode: shared ⇔ status listed, else self_use.
	Mode string `json:"mode"`
	// Plan: the account's plan (null = none).
	Plan            *string            `json:"plan"`
	SelectedGroupID *int64             `json:"selected_group_id"`
	Version         int64              `json:"version"`
	AllowedTiers    []MarketTierOption `json:"allowed_tiers"`
	// CooldownHours: market_mode_switch_cooldown_hours (0 = no cooldown).
	CooldownHours int `json:"cooldown_hours"`
	// NextSwitchAt: when the owner may switch again (database time, rounded
	// up to the second); null = may switch now.
	NextSwitchAt *time.Time `json:"next_switch_at"`
	// UpstreamWindows: the account's upstream 5h / 7d usage windows, the
	// same objects as the owned-accounts list (empty without a sample).
	UpstreamWindows []HostedQuotaWindow `json:"upstream_windows"`
	// UnavailableReasons: why switching to 共享 is not possible now (empty =
	// can list).
	UnavailableReasons []string `json:"unavailable_reasons"`
	// CanList: may switch to 共享 (or another tier) now.
	CanList bool `json:"can_list"`
	// CanUnlist: may switch back to 自用 now.
	CanUnlist bool `json:"can_unlist"`
}

func (s *MarketListingService) settings(ctx context.Context) (MarketSettings, error) {
	values, err := s.settingRepo.GetMultiple(ctx, MarketSettingKeys())
	if err != nil {
		return MarketSettings{}, err
	}
	return ParseMarketSettings(values), nil
}

// AdminGet returns the listing state of a live account.
func (s *MarketListingService) AdminGet(ctx context.Context, accountID int64) (*AdminMarketListingView, error) {
	account, err := s.repo.GetAccountFacts(ctx, accountID)
	if err != nil {
		return nil, err
	}
	listing, err := s.repo.GetListing(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return s.adminView(ctx, account, listing)
}

func (s *MarketListingService) adminView(ctx context.Context, account *Account, listing *MarketListing) (*AdminMarketListingView, error) {
	cfg, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}
	view := &AdminMarketListingView{
		AccountID:        account.ID,
		OwnerUserID:      account.OwnerUserID,
		Listing:          listing,
		AllowedTiers:     []MarketTierOption{},
		IneligibleReason: MarketAccountIneligibleReason(account),
		MarketEnabled:    cfg.Enabled,
	}
	if listing != nil {
		tiers, err := s.planTierOptions(ctx, account, listing)
		if err != nil {
			return nil, err
		}
		view.AllowedTiers = tiers
	}
	return view, nil
}

// planTierOptions: the tiers derived from the listing's plan (plus its
// selected tier when that no longer matches), with the admin-member marks.
// Also fills listing.AllowedGroupIDs with the available ones.
func (s *MarketListingService) planTierOptions(ctx context.Context, account *Account, listing *MarketListing) ([]MarketTierOption, error) {
	listing.AllowedGroupIDs = []int64{}
	plan := ""
	if listing.Plan != nil {
		plan = *listing.Plan
	}
	if plan == "" && listing.SelectedGroupID == nil {
		return []MarketTierOption{}, nil
	}
	tiers, err := s.repo.ListPlanTierOptions(ctx, plan, account.Platform, listing.SelectedGroupID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(tiers))
	for _, tier := range tiers {
		ids = append(ids, tier.GroupID)
	}
	members, err := s.repo.ListAdminMemberGroupIDs(ctx, account.ID, ids)
	if err != nil {
		return nil, err
	}
	for i := range tiers {
		tiers[i].AdminMember = slices.Contains(members, tiers[i].GroupID)
		if tiers[i].Available {
			listing.AllowedGroupIDs = append(listing.AllowedGroupIDs, tiers[i].GroupID)
		}
	}
	return tiers, nil
}

// AdminConfigure creates the listing (自用) or changes its plan. A revoked
// listing is re-granted. If the selected tier no longer matches the plan,
// the account is unlisted like a deleted tier, in the same transaction.
func (s *MarketListingService) AdminConfigure(ctx context.Context, adminUserID, accountID int64, plan string) (*AdminMarketListingView, error) {
	if !ValidMarketPlan(plan) {
		return nil, ErrMarketPlanInvalid
	}
	listing, err := s.repo.AdminConfigure(ctx, MarketAdminConfigureParams{
		AccountID:   accountID,
		AdminUserID: adminUserID,
		Plan:        plan,
		Validate: func(account *Account) error {
			if account.OwnerUserID == nil {
				return ErrMarketAccountNoOwner
			}
			if reason := MarketAccountIneligibleReason(account); reason != "" {
				return NewMarketAccountNotEligibleError(reason)
			}
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	account, err := s.repo.GetAccountFacts(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return s.adminView(ctx, account, listing)
}

// AdminRevoke withdraws the sharing permission (removes the market
// membership and the plan); the history stays.
func (s *MarketListingService) AdminRevoke(ctx context.Context, adminUserID, accountID int64) (*AdminMarketListingView, error) {
	listing, err := s.repo.AdminRevoke(ctx, accountID, adminUserID)
	if err != nil {
		return nil, err
	}
	account, err := s.repo.GetAccountFacts(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return s.adminView(ctx, account, listing)
}

// OwnerGet: only the current owner of a live account (else 404).
func (s *MarketListingService) OwnerGet(ctx context.Context, ownerUserID, accountID int64) (*OwnerMarketView, error) {
	account, err := s.repo.GetOwnedAccountFacts(ctx, ownerUserID, accountID)
	if err != nil {
		return nil, err
	}
	return s.ownerView(ctx, account)
}

// OwnerSet switches the account to 共享 (listed=true, requires
// market_enabled; groupID 0 = the plan's only tier) or back to 自用
// (listed=false). At most one effective switch per cooldown; a take-back
// while the market is off is always allowed; a request for the current
// state is an idempotent no-op.
func (s *MarketListingService) OwnerSet(ctx context.Context, ownerUserID, accountID, groupID int64, listed bool) (*OwnerMarketView, error) {
	var beforeWrite, rolledBack, ended func()
	if listed && s.listings != nil {
		// Marked locally only once every check passed (inside the locked
		// transaction, right before the writes); cleared at once on a
		// rollback; otherwise kept until a registry poll that started after
		// the transaction ended.
		var (
			token            uint64
			marked, finished bool
		)
		beforeWrite = func() { token, marked = s.listings.BeginListing(accountID), true }
		rolledBack = func() {
			if marked && !finished {
				finished = true
				s.listings.AbortListing(accountID, token)
			}
		}
		ended = func() {
			if marked && !finished {
				finished = true
				s.listings.EndListing(accountID, token)
			}
		}
		defer ended()
	}
	_, err := s.repo.OwnerSet(ctx, MarketOwnerSetParams{
		BeforeListWrite:   beforeWrite,
		ListingRolledBack: rolledBack,
		OwnerUserID:       ownerUserID,
		AccountID:         accountID,
		GroupID:           groupID,
		Listed:            listed,
		Validate: func(account *Account) error {
			if reason := marketListBlockedReason(account); reason != "" {
				return NewMarketAccountNotEligibleError(reason)
			}
			if reason := marketCapacityReason(account); reason != "" {
				return NewMarketAccountNotEligibleError(reason)
			}
			return nil
		},
	})
	if ended != nil {
		// The transaction is over: from now on the owner path's database
		// check decides (no avoidance while the view is read back).
		ended()
	}
	if err != nil {
		return nil, err
	}
	return s.OwnerGet(ctx, ownerUserID, accountID)
}

func (s *MarketListingService) ownerView(ctx context.Context, account *Account) (*OwnerMarketView, error) {
	cfg, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}
	listing, err := s.repo.GetListing(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	windows := hostedAccountSnapshot(account, now).Windows
	if windows == nil {
		windows = []HostedQuotaWindow{}
	}
	view := &OwnerMarketView{
		AccountID:          account.ID,
		MarketEnabled:      cfg.Enabled,
		Status:             MarketListingStatusNotConfigured,
		Mode:               MarketModeSelfUse,
		AllowedTiers:       []MarketTierOption{},
		CooldownHours:      cfg.ModeSwitchCooldownHours,
		UpstreamWindows:    windows,
		UnavailableReasons: []string{},
	}
	if listing != nil {
		view.Status = listing.Status
		view.Plan = listing.Plan
		view.SelectedGroupID = listing.SelectedGroupID
		view.Version = listing.Version
		if listing.Status == MarketListingStatusListed {
			view.Mode = MarketModeShared
		}
		if listing.Status != MarketListingStatusRevoked {
			tiers, err := s.planTierOptions(ctx, account, listing)
			if err != nil {
				return nil, err
			}
			view.AllowedTiers = tiers
		}
		next, err := s.repo.NextOwnerSwitchAt(ctx, account.ID, cfg.ModeSwitchCooldownHours)
		if err != nil {
			return nil, err
		}
		// With the market off a shared account can be taken back now: one
		// decision, no cooldown shown.
		if next != nil && (listing.Status != MarketListingStatusListed || cfg.Enabled) {
			at := ceilToSecond(*next)
			view.NextSwitchAt = &at
		}
	}
	reasons := view.UnavailableReasons
	if !cfg.Enabled {
		reasons = append(reasons, MarketReasonMarketDisabled)
	}
	switch {
	case listing == nil, listing.Status == MarketListingStatusRevoked:
		reasons = append(reasons, MarketReasonNotConfigured)
	case listing.Plan == nil:
		reasons = append(reasons, MarketReasonNoPlan)
	default:
		available := false
		for _, tier := range view.AllowedTiers {
			if tier.Available && !tier.AdminMember {
				available = true
				break
			}
		}
		if !available {
			reasons = append(reasons, MarketReasonNoAllowedTier)
		}
	}
	if view.NextSwitchAt != nil {
		reasons = append(reasons, MarketReasonSwitchCooldown)
	}
	if reason := marketListBlockedReason(account); reason != "" {
		reasons = append(reasons, reason)
	}
	if reason := marketCapacityReason(account); reason != "" {
		reasons = append(reasons, reason)
	}
	view.UnavailableReasons = reasons
	view.CanList = len(reasons) == 0
	view.CanUnlist = view.Mode == MarketModeShared && (view.NextSwitchAt == nil || !cfg.Enabled)
	return view, nil
}

// ceilToSecond rounds up to a whole second, so a client that waits until
// the reported time is never refused for a sub-second remainder.
func ceilToSecond(t time.Time) time.Time {
	if r := t.Truncate(time.Second); !r.Equal(t) {
		return r.Add(time.Second).UTC()
	}
	return t.UTC()
}
