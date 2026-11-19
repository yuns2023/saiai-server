package service

import (
	"context"
	"slices"
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
}

// MarketListingObserver is implemented by MarketRuntime.
type MarketListingObserver interface {
	BeginListing(accountID int64)
	EndListing(accountID int64)
}

// SetListingObserver wires the dispatch registry (P3a part 2).
func (s *MarketListingService) SetListingObserver(o MarketListingObserver) {
	if s != nil {
		s.listings = o
	}
}

func NewMarketListingService(repo MarketListingRepository, settingRepo SettingRepository) *MarketListingService {
	return &MarketListingService{repo: repo, settingRepo: settingRepo}
}

// AdminMarketListingView is the body of GET/PUT/DELETE
// /api/v1/admin/accounts/:id/market-listing.
type AdminMarketListingView struct {
	AccountID   int64  `json:"account_id"`
	OwnerUserID *int64 `json:"owner_user_id"`
	// Listing is null when the account was never configured.
	Listing *MarketListing `json:"listing"`
	// AllowedTiers describes Listing.AllowedGroupIDs.
	AllowedTiers []MarketTierOption `json:"allowed_tiers"`
	// IneligibleReason: why the account type/config cannot be listed ("" = ok).
	IneligibleReason string `json:"ineligible_reason"`
	// DefaultProtection: the site defaults used when a listing is created.
	DefaultProtection MarketProtectionParams `json:"default_protection"`
	MarketEnabled     bool                   `json:"market_enabled"`
}

// OwnerMarketView is the body of GET/PUT /api/v1/hosted/owned-accounts/:id/market.
type OwnerMarketView struct {
	AccountID     int64 `json:"account_id"`
	MarketEnabled bool  `json:"market_enabled"`
	// Status: not_configured | unlisted | listed | revoked.
	Status          string             `json:"status"`
	SelectedGroupID *int64             `json:"selected_group_id"`
	Version         int64              `json:"version"`
	AllowedTiers    []MarketTierOption `json:"allowed_tiers"`
	// Protection is read-only for owners (admin-managed); null when not configured.
	Protection *MarketProtectionParams `json:"protection"`
	// UnavailableReasons: why listing is not possible now (empty = can list).
	UnavailableReasons []string `json:"unavailable_reasons"`
	CanList            bool     `json:"can_list"`
}

// AdminMarketListingInput is the admin PUT body (protection fields optional:
// omitted keeps the stored value, or the site default on create).
type AdminMarketListingInput struct {
	AllowedGroupIDs     []int64
	ReservedConcurrency *int
	ReservedDevices     *int
	ReservedAdmissions  *int
	Cap5hPct            *int
	Cap7dPct            *int
	SampleMaxAgeMinutes *int
}

func (in AdminMarketListingInput) merge(base MarketProtectionParams) MarketProtectionParams {
	set := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	set(&base.ReservedConcurrency, in.ReservedConcurrency)
	set(&base.ReservedDevices, in.ReservedDevices)
	set(&base.ReservedAdmissions, in.ReservedAdmissions)
	set(&base.Cap5hPct, in.Cap5hPct)
	set(&base.Cap7dPct, in.Cap7dPct)
	set(&base.SampleMaxAgeMinutes, in.SampleMaxAgeMinutes)
	return base
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
		AccountID:         account.ID,
		OwnerUserID:       account.OwnerUserID,
		Listing:           listing,
		AllowedTiers:      []MarketTierOption{},
		IneligibleReason:  MarketAccountIneligibleReason(account),
		DefaultProtection: cfg.DefaultProtection,
		MarketEnabled:     cfg.Enabled,
	}
	if listing != nil && len(listing.AllowedGroupIDs) > 0 {
		tiers, err := s.accountTierOptions(ctx, account.ID, listing.AllowedGroupIDs)
		if err != nil {
			return nil, err
		}
		view.AllowedTiers = tiers
	}
	return view, nil
}

// accountTierOptions describes the tiers and marks those the account is an
// admin member of.
func (s *MarketListingService) accountTierOptions(ctx context.Context, accountID int64, groupIDs []int64) ([]MarketTierOption, error) {
	tiers, err := s.repo.ListTierOptions(ctx, groupIDs)
	if err != nil {
		return nil, err
	}
	members, err := s.repo.ListAdminMemberGroupIDs(ctx, accountID, groupIDs)
	if err != nil {
		return nil, err
	}
	for i := range tiers {
		tiers[i].AdminMember = slices.Contains(members, tiers[i].GroupID)
	}
	return tiers, nil
}

// AdminConfigure creates or updates the listing (allowed tiers + protection).
// Removing the selected tier unlists the account in the same transaction; a
// revoked listing becomes unlisted again.
func (s *MarketListingService) AdminConfigure(ctx context.Context, adminUserID, accountID int64, in AdminMarketListingInput) (*AdminMarketListingView, error) {
	allowed, err := NormalizeMarketAllowedGroupIDs(in.AllowedGroupIDs)
	if err != nil {
		return nil, err
	}
	cfg, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}
	listing, err := s.repo.AdminConfigure(ctx, MarketAdminConfigureParams{
		AccountID:       accountID,
		AdminUserID:     adminUserID,
		AllowedGroupIDs: allowed,
		Defaults:        cfg.DefaultProtection,
		Merge:           in.merge,
		Validate: func(account *Account, p MarketProtectionParams) error {
			if account.OwnerUserID == nil {
				return ErrMarketAccountNoOwner
			}
			if reason := MarketAccountIneligibleReason(account); reason != "" {
				return NewMarketAccountNotEligibleError(reason)
			}
			return validateMarketProtectionForAccount(account, p)
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
// membership); the history stays.
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

// OwnerSet lists the account into one allowed tier (listed=true; requires
// market_enabled) or stops sharing (listed=false; always allowed).
func (s *MarketListingService) OwnerSet(ctx context.Context, ownerUserID, accountID, groupID int64, listed bool) (*OwnerMarketView, error) {
	if listed && s.listings != nil {
		s.listings.BeginListing(accountID)
		defer s.listings.EndListing(accountID)
	}
	if _, err := s.repo.OwnerSet(ctx, MarketOwnerSetParams{
		OwnerUserID: ownerUserID,
		AccountID:   accountID,
		GroupID:     groupID,
		Listed:      listed,
		Validate: func(account *Account, protection MarketProtectionParams) error {
			if reason := marketListBlockedReason(account); reason != "" {
				return NewMarketAccountNotEligibleError(reason)
			}
			// The account may have been reconfigured since the admin allowed it.
			return validateMarketProtectionForAccount(account, protection)
		},
	}); err != nil {
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
	view := &OwnerMarketView{
		AccountID:          account.ID,
		MarketEnabled:      cfg.Enabled,
		Status:             MarketListingStatusNotConfigured,
		AllowedTiers:       []MarketTierOption{},
		UnavailableReasons: []string{},
	}
	if listing != nil {
		view.Status = listing.Status
		view.SelectedGroupID = listing.SelectedGroupID
		view.Version = listing.Version
		protection := listing.Protection
		view.Protection = &protection
		if len(listing.AllowedGroupIDs) > 0 {
			tiers, err := s.accountTierOptions(ctx, account.ID, listing.AllowedGroupIDs)
			if err != nil {
				return nil, err
			}
			view.AllowedTiers = tiers
		}
	}
	reasons := view.UnavailableReasons
	if listing != nil {
		if err := validateMarketProtectionForAccount(account, listing.Protection); err != nil {
			reasons = append(reasons, MarketReasonProtectionInvalid)
		}
	}
	if !cfg.Enabled {
		reasons = append(reasons, MarketReasonMarketDisabled)
	}
	switch {
	case listing == nil:
		reasons = append(reasons, MarketReasonNotConfigured)
	case listing.Status == MarketListingStatusRevoked:
		reasons = append(reasons, MarketReasonNotConfigured)
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
	if reason := marketListBlockedReason(account); reason != "" {
		reasons = append(reasons, reason)
	}
	view.UnavailableReasons = reasons
	view.CanList = len(reasons) == 0
	return view, nil
}
