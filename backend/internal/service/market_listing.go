package service

import (
	"context"
	"math"
	"slices"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

// Hosted sharing market, P3a part 1 (schema, listing lifecycle, contracts).
//
// An admin marks groups as sharing tiers (groups.market_tier) and allows an
// owned account into some of them (account_market_listings.allowed_group_ids
// plus owner-protection parameters). The owner picks one allowed tier and
// toggles sharing; listing writes the account_groups row with
// source='market'. Every lifecycle write runs in one transaction with the
// account row locked and writes the scheduler outbox in that transaction.
//
// In this part market memberships are NEVER dispatchable: every scheduling
// read of account_groups goes through the repository predicate
// dispatchableAccountGroupSources (admin rows only). The selection part (BE2)
// replaces that predicate with the three-way admission classification.

const (
	AccountGroupSourceAdmin  = "admin"
	AccountGroupSourceMarket = "market"

	MarketListingStatusUnlisted = "unlisted"
	MarketListingStatusListed   = "listed"
	MarketListingStatusRevoked  = "revoked"
	// MarketListingStatusNotConfigured is a view-only status: no listing row.
	MarketListingStatusNotConfigured = "not_configured"

	MarketListingActorAdmin  = "admin"
	MarketListingActorOwner  = "owner"
	MarketListingActorSystem = "system"

	MarketListingActionAdminConfigure = "admin_configure"
	MarketListingActionAdminRevoke    = "admin_revoke"
	MarketListingActionOwnerList      = "owner_list"
	MarketListingActionOwnerSwitch    = "owner_switch"
	MarketListingActionOwnerUnlist    = "owner_unlist"
	MarketListingActionOwnerCleared   = "owner_cleared"
	MarketListingActionOwnerPaused    = "owner_paused"
	MarketListingActionOwnerWithdraw  = "owner_withdraw"
	MarketListingActionAccountDeleted = "account_deleted"
	MarketListingActionGroupDeleted   = "group_deleted"
	MarketListingActionGroupDemoted   = "group_demoted"

	// MaxMarketAllowedGroups matches the migration 134 cardinality CHECK.
	MaxMarketAllowedGroups = 32
)

// Reasons an account cannot be listed now (owner/admin views and errors).
const (
	MarketReasonMarketDisabled    = "market_disabled"
	MarketReasonNotConfigured     = "not_configured"
	MarketReasonNoAllowedTier     = "no_allowed_tier"
	MarketReasonNoOwner           = "no_owner"
	MarketReasonNotAnthropic      = "platform_not_anthropic"
	MarketReasonNotSetupToken     = "not_setup_token"
	MarketReasonNotCarpool        = "not_carpool"
	MarketReasonUnlimitedDevices  = "unlimited_devices"
	MarketReasonOwnerPaused       = "owner_paused"
	MarketReasonWithdrawRequested = "withdraw_requested"
	MarketReasonAccountInactive   = "account_inactive"
)

var (
	ErrMarketListingNotFound = infraerrors.NotFound("MARKET_LISTING_NOT_FOUND", "market listing not found")
	ErrMarketDisabled        = infraerrors.Conflict("MARKET_DISABLED", "the sharing market is not enabled")
	ErrMarketAccountNoOwner  = infraerrors.Conflict("MARKET_ACCOUNT_NO_OWNER", "only an account with an owner can be shared")
	ErrMarketGroupNotAllowed = infraerrors.BadRequest("MARKET_GROUP_NOT_ALLOWED", "the tier is not allowed for this account")
	ErrMarketInvalidTier     = infraerrors.BadRequest("MARKET_INVALID_TIER", "allowed groups must be live market tiers of the account platform")
	ErrMarketInvalidParams   = infraerrors.BadRequest("MARKET_INVALID_PARAMS", "invalid market protection parameters")
	// ErrMarketAdminMembershipConflict: the owner tries to list into a tier
	// the account is already an admin member of (one row per pair).
	ErrMarketAdminMembershipConflict = infraerrors.Conflict("MARKET_ADMIN_MEMBERSHIP_CONFLICT", "the account is already an admin member of this tier")
	// ErrMarketMembershipConflict: an admin binding would replace a market
	// listing of the same (account, group) pair; revoke the listing first.
	ErrMarketMembershipConflict = infraerrors.Conflict("MARKET_MEMBERSHIP_CONFLICT", "the account is listed in this group by its owner; revoke the market listing first")
	ErrMarketListingRevoked     = infraerrors.Conflict("MARKET_LISTING_REVOKED", "sharing was revoked by an administrator")

	ErrMarketTierInvalid      = infraerrors.BadRequest("INVALID_MARKET_TIER", "Market tiers must be standard (balance) anthropic groups without hosted usage and with a rate multiplier > 0")
	ErrMarketTierFallback     = infraerrors.BadRequest("MARKET_TIER_FALLBACK", "Fallback chains may not enter or leave a market tier")
	ErrMarketTierRateOverride = infraerrors.BadRequest("MARKET_TIER_RATE_OVERRIDE", "User rate overrides on a market tier must be greater than 0 at 4 decimal places")
	ErrMarketTierGuardMissing = infraerrors.ServiceUnavailable("MARKET_TIER_GUARD_UNAVAILABLE", "market tier checks are unavailable")
	// ErrMarketEarningRecordImmutable: market_earning history rows are never
	// deleted or expired.
	ErrMarketEarningRecordImmutable = infraerrors.BadRequest("MARKET_EARNING_RECORD_IMMUTABLE", "market earning records cannot be deleted or expired")
)

// NewMarketAccountNotEligibleError carries the reason code in metadata.
func NewMarketAccountNotEligibleError(reason string) error {
	return infraerrors.Conflict("MARKET_ACCOUNT_NOT_ELIGIBLE", "the account cannot be shared: "+reason).
		WithMetadata(map[string]string{"reason": reason})
}

// MarketProtectionParams are the per-listing owner protections (admin-set).
type MarketProtectionParams struct {
	ReservedConcurrency int `json:"reserved_concurrency"`
	ReservedDevices     int `json:"reserved_devices"`
	ReservedAdmissions  int `json:"reserved_admissions"`
	Cap5hPct            int `json:"cap_5h_pct"`
	Cap7dPct            int `json:"cap_7d_pct"`
	SampleMaxAgeMinutes int `json:"sample_max_age_minutes"`
}

// Bounds of the protection parameters (migration 134 CHECK).
const (
	MaxMarketReservedConcurrency = 100
	MaxMarketReservedDevices     = 32
	MaxMarketReservedAdmissions  = 32
	MaxMarketSampleMaxAgeMinutes = 1440
)

// ValidRange checks the static bounds only.
func (p MarketProtectionParams) ValidRange() bool {
	return p.ReservedConcurrency >= 0 && p.ReservedConcurrency <= MaxMarketReservedConcurrency &&
		p.ReservedDevices >= 0 && p.ReservedDevices <= MaxMarketReservedDevices &&
		p.ReservedAdmissions >= 0 && p.ReservedAdmissions <= MaxMarketReservedAdmissions &&
		p.Cap5hPct >= 1 && p.Cap5hPct <= 100 &&
		p.Cap7dPct >= 1 && p.Cap7dPct <= 100 &&
		p.SampleMaxAgeMinutes >= 1 && p.SampleMaxAgeMinutes <= MaxMarketSampleMaxAgeMinutes
}

// MarketListing is one account_market_listings row.
type MarketListing struct {
	AccountID       int64                  `json:"account_id"`
	OwnerUserID     int64                  `json:"owner_user_id"`
	AllowedGroupIDs []int64                `json:"allowed_group_ids"`
	SelectedGroupID *int64                 `json:"selected_group_id"`
	Status          string                 `json:"status"`
	Version         int64                  `json:"version"`
	Protection      MarketProtectionParams `json:"protection"`
	ListedAt        *time.Time             `json:"listed_at"`
	CreatedAt       time.Time              `json:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at"`
}

// MarketTierOption is a tier group as shown to admins and owners.
type MarketTierOption struct {
	GroupID        int64   `json:"group_id"`
	Name           string  `json:"name"`
	RateMultiplier float64 `json:"rate_multiplier"`
	// Available: still a live, active market tier.
	Available bool `json:"available"`
}

// MarketAdminConfigureParams is the admin PUT, applied under the account row
// lock. The final protection = Merge(stored values, or Defaults on create);
// a nil Merge keeps them unchanged.
type MarketAdminConfigureParams struct {
	AccountID       int64
	AdminUserID     int64
	AllowedGroupIDs []int64
	Defaults        MarketProtectionParams
	Merge           func(base MarketProtectionParams) MarketProtectionParams
	// Validate runs on the locked account with the final parameters.
	Validate func(account *Account, p MarketProtectionParams) error
}

// MarketOwnerSetParams is the owner PUT, applied under the account row lock.
type MarketOwnerSetParams struct {
	OwnerUserID int64
	AccountID   int64
	GroupID     int64
	Listed      bool
	// Validate runs on the locked account before listing (not on unlist).
	Validate func(account *Account) error
}

// MarketListingRepository: every write is one transaction that locks the
// account row (FOR NO KEY UPDATE, same lock as owner changes), writes the
// listing, its history row, the market account_groups rows and the scheduler
// outbox rows; an error rolls all of it back.
type MarketListingRepository interface {
	// GetListing returns nil, nil when the account has no listing row.
	GetListing(ctx context.Context, accountID int64) (*MarketListing, error)
	// GetAccountFacts reads a live account (ErrAccountNotFound otherwise).
	GetAccountFacts(ctx context.Context, accountID int64) (*Account, error)
	// GetOwnedAccountFacts reads a live account whose CURRENT owner is
	// ownerUserID (ErrMarketListingNotFound otherwise).
	GetOwnedAccountFacts(ctx context.Context, ownerUserID, accountID int64) (*Account, error)
	// ListTierOptions describes the given group ids (missing ids are left out).
	ListTierOptions(ctx context.Context, groupIDs []int64) ([]MarketTierOption, error)
	// HasAdminMembership reports an admin account_groups row for the pair.
	HasAdminMembership(ctx context.Context, accountID, groupID int64) (bool, error)
	AdminConfigure(ctx context.Context, p MarketAdminConfigureParams) (*MarketListing, error)
	// AdminRevoke returns ErrMarketListingNotFound when there is no listing.
	AdminRevoke(ctx context.Context, accountID, adminUserID int64) (*MarketListing, error)
	// OwnerSet checks market_enabled inside the transaction when listing.
	OwnerSet(ctx context.Context, p MarketOwnerSetParams) (*MarketListing, error)
}

// MarketAccountIneligibleReason returns "" when the account can be listed by
// configuration (type and carpool settings), else a MarketReason* code.
// Pilot scope: Anthropic setup-token carpool accounts with a finite device
// limit.
func MarketAccountIneligibleReason(a *Account) string {
	if a == nil || a.Platform != PlatformAnthropic {
		return MarketReasonNotAnthropic
	}
	if a.Type != AccountTypeSetupToken {
		return MarketReasonNotSetupToken
	}
	if a.GetClaudeOAuthMode() != ClaudeOAuthModeCarpool {
		return MarketReasonNotCarpool
	}
	if a.IsClaudeOAuthCarpoolUnlimitedDevices() {
		return MarketReasonUnlimitedDevices
	}
	return ""
}

// marketListBlockedReason adds the runtime owner/account state that blocks a
// new listing (pause, withdraw, inactive).
func marketListBlockedReason(a *Account) string {
	if reason := MarketAccountIneligibleReason(a); reason != "" {
		return reason
	}
	if a.OwnerUserID == nil {
		return MarketReasonNoOwner
	}
	if a.OwnerPausedAt != nil {
		return MarketReasonOwnerPaused
	}
	if a.OwnerWithdrawRequestedAt != nil {
		return MarketReasonWithdrawRequested
	}
	if a.Status != StatusActive || !a.Schedulable {
		return MarketReasonAccountInactive
	}
	return ""
}

// validateMarketProtectionForAccount checks the parameters against the
// account configuration: reservations must leave at least one unit for
// buyers (concurrency, devices) and cannot exceed the per-cycle admissions.
func validateMarketProtectionForAccount(a *Account, p MarketProtectionParams) error {
	if !p.ValidRange() {
		return ErrMarketInvalidParams
	}
	if a.Concurrency > 0 && p.ReservedConcurrency > a.Concurrency-1 {
		return infraerrors.BadRequest("MARKET_INVALID_PARAMS", "reserved_concurrency must be at most the account concurrency - 1")
	}
	if limit := a.GetClaudeOAuthCarpoolDeviceLimit(); limit > 0 && p.ReservedDevices > limit-1 {
		return infraerrors.BadRequest("MARKET_INVALID_PARAMS", "reserved_devices must be at most the device limit - 1")
	}
	if q := a.GetCarpoolAdmissionsPerCycle(); q > 0 && p.ReservedAdmissions > q {
		return infraerrors.BadRequest("MARKET_INVALID_PARAMS", "reserved_admissions must be at most the admissions per cycle")
	}
	return nil
}

// NormalizeMarketAllowedGroupIDs dedupes, sorts and bounds the id list.
func NormalizeMarketAllowedGroupIDs(ids []int64) ([]int64, error) {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, ErrMarketInvalidTier
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	if len(out) > MaxMarketAllowedGroups {
		return nil, ErrMarketInvalidTier
	}
	slices.Sort(out)
	return out, nil
}

// ValidateMarketTier checks the group-level tier rules (also a DB CHECK) and
// that a tier never leaves through a fallback.
func (g *Group) ValidateMarketTier() error {
	if g == nil || !g.MarketTier {
		return nil
	}
	if g.Platform != PlatformAnthropic || g.SubscriptionType != SubscriptionTypeStandard ||
		g.HostedUsageEnabled || !MarketTierRateStoredPositive(g.RateMultiplier) {
		return ErrMarketTierInvalid
	}
	if g.FallbackGroupID != nil {
		return ErrMarketTierFallback
	}
	return nil
}

// MarketTierRateStoredPositive reports a finite rate that stays > 0 after
// rounding to the DECIMAL(10,4) storage precision (0.00004 → 0.0000).
func MarketTierRateStoredPositive(rate float64) bool {
	if math.IsNaN(rate) || math.IsInf(rate, 0) {
		return false
	}
	return decimal.NewFromFloat(rate).Round(4).IsPositive()
}

// MarketTierGroupGuard is implemented by the group repository: live groups
// whose claude_code_only fallback or invalid-request fallback points at
// groupID (inbound references block promotion to a tier).
type MarketTierGroupGuard interface {
	ListFallbackReferrers(ctx context.Context, groupID int64) ([]int64, error)
}

// MarketTierRateGuard is implemented by the user-group rate repository:
// users whose stored override on groupID is <= 0.
type MarketTierRateGuard interface {
	ListNonPositiveRateUsers(ctx context.Context, groupID int64) ([]int64, error)
}
