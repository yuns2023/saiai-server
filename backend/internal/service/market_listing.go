package service

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

// Hosted sharing market, P3a (schema, listing lifecycle, contracts).
//
// An admin marks groups as sharing tiers (groups.market_tier) and gives each
// tier a plan (groups.market_plan, e.g. "max"). An owned account gets a plan
// once (self-onboard approval or the admin sharing settings,
// account_market_listings.plan); the tiers it may sell in are the live,
// active tiers of that plan (derived on every read and write, never stored
// as a permission). The owner toggles 共享 (shared: listed in one tier,
// buyers only) / 自用 (self-use: not on the market), at most once per
// market_mode_switch_cooldown_hours. Listing writes the account_groups row
// with source='market'. Every lifecycle write runs in one transaction with
// the account row locked and writes the scheduler outbox in that
// transaction. There are no owner reservations.
//
// Market memberships are dispatchable only through the admission
// classification of service/market_dispatch.go (buyer path of the listed
// tier; a listed account never serves its owner's hosted group).

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
	// MarketListingActionOwnerPaused is history only: since migration 138 a
	// pause keeps the listing (the paused account serves nobody).
	MarketListingActionOwnerPaused = "owner_paused"
	// MarketListingActionOwnerWithdraw: the automatic unlist of an account
	// withdrawal. actor_role is owner, but it is not an owner switch (the
	// cooldown filters by action, never by role).
	MarketListingActionOwnerWithdraw  = "owner_withdraw"
	MarketListingActionAccountDeleted = "account_deleted"
	MarketListingActionGroupDeleted   = "group_deleted"
	MarketListingActionGroupDemoted   = "group_demoted"
	// MarketListingActionGroupReplanned: the tier's plan changed and no
	// longer matches the listing's plan (same semantics as a demotion).
	MarketListingActionGroupReplanned = "group_replanned"

	// DefaultMarketModeSwitchCooldownHours / MaxMarketModeSwitchCooldownHours
	// bound market_mode_switch_cooldown_hours (0 = no cooldown).
	DefaultMarketModeSwitchCooldownHours = 24
	MaxMarketModeSwitchCooldownHours     = 168
)

// MarketOwnerSwitchActions are the effective owner-initiated switches the
// cooldown counts (pause, withdrawal, admin and system changes never count).
var MarketOwnerSwitchActions = []string{MarketListingActionOwnerList, MarketListingActionOwnerUnlist, MarketListingActionOwnerSwitch}

// Owner view modes.
const (
	MarketModeSelfUse = "self_use"
	MarketModeShared  = "shared"
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
	// MarketReasonProxyUnavailable: a self-onboarded account without a live
	// active proxy (it is not schedulable either).
	MarketReasonProxyUnavailable = "proxy_unavailable"
	MarketReasonMaxSessions      = "max_sessions_enabled"
	// MarketReasonProtectionInvalid: the account has no finite concurrency
	// (0 = unlimited slots), which a shared account needs.
	MarketReasonProtectionInvalid = "protection_invalid"
	// MarketReasonNoPlan: the listing exists but has no plan (an admin must
	// set one before the account can be shared).
	MarketReasonNoPlan = "no_plan"
	// MarketReasonSwitchCooldown: the last owner switch is younger than the
	// cooldown.
	MarketReasonSwitchCooldown = "switch_cooldown"
)

var (
	ErrMarketListingNotFound = infraerrors.NotFound("MARKET_LISTING_NOT_FOUND", "market listing not found")
	ErrMarketDisabled        = infraerrors.Conflict("MARKET_DISABLED", "the sharing market is not enabled")
	ErrMarketAccountNoOwner  = infraerrors.Conflict("MARKET_ACCOUNT_NO_OWNER", "only an account with an owner can be shared")
	ErrMarketGroupNotAllowed = infraerrors.BadRequest("MARKET_GROUP_NOT_ALLOWED", "the tier is not a live, active tier of the account's plan")
	// ErrMarketTierRequired: the owner omitted group_id while several tiers
	// match the plan.
	ErrMarketTierRequired = infraerrors.BadRequest("MARKET_TIER_REQUIRED", "several tiers match the account's plan; group_id is required")
	// ErrMarketPlanUnknown: the plan is not a valid slug or no live, active
	// market tier has it (approval and admin sharing settings).
	ErrMarketPlanUnknown = infraerrors.BadRequest("MARKET_PLAN_UNKNOWN", "no live, active market tier has this plan")
	// ErrMarketPlanInvalid: a group plan that is not a valid slug.
	ErrMarketPlanInvalid = infraerrors.BadRequest("MARKET_PLAN_INVALID", "market_plan must match ^[a-z0-9][a-z0-9_-]{0,31}$")
	// ErrMarketPlanRequiresTier: a plan on a group that is not a market tier.
	ErrMarketPlanRequiresTier = infraerrors.BadRequest("MARKET_PLAN_REQUIRES_TIER", "market_plan can only be set on a market tier")
	// ErrMarketAdminMembershipConflict: the owner tries to list into a tier
	// the account is already an admin member of (one row per pair).
	ErrMarketAdminMembershipConflict = infraerrors.Conflict("MARKET_ADMIN_MEMBERSHIP_CONFLICT", "the account is already an admin member of this tier")
	// ErrMarketMembershipConflict: an admin binding would replace a market
	// listing of the same (account, group) pair; revoke the listing first.
	ErrMarketMembershipConflict = infraerrors.Conflict("MARKET_MEMBERSHIP_CONFLICT", "the account is listed in this group by its owner; revoke the market listing first")
	ErrMarketListingRevoked     = infraerrors.Conflict("MARKET_LISTING_REVOKED", "sharing was revoked by an administrator")

	ErrMarketTierInvalid      = infraerrors.BadRequest("INVALID_MARKET_TIER", "Market tiers must be standard (balance) anthropic groups without hosted usage and with a rate multiplier > 0")
	ErrMarketTierFallback     = infraerrors.BadRequest("MARKET_TIER_FALLBACK", "Fallback chains may not enter or leave a market tier")
	ErrMarketTierRateOverride = infraerrors.BadRequest("MARKET_TIER_RATE_OVERRIDE", "User rate overrides on a market tier may not be negative")
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

// NewMarketSwitchCooldownError is the 429 of an owner switch inside the
// cooldown: metadata next_switch_at (RFC3339, database time) and
// retry_after_seconds (the handler copies it into Retry-After).
func NewMarketSwitchCooldownError(nextSwitchAt time.Time, retryAfterSeconds int64) error {
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	at := ceilToSecond(nextSwitchAt).Format(time.RFC3339)
	return infraerrors.TooManyRequests("MARKET_SWITCH_COOLDOWN", "the sharing mode can be switched again at "+at).
		WithMetadata(map[string]string{
			"next_switch_at":      at,
			"retry_after_seconds": strconv.FormatInt(retryAfterSeconds, 10),
		})
}

var marketPlanPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ValidMarketPlan reports a plan slug (migration 138 CHECK).
func ValidMarketPlan(plan string) bool {
	return marketPlanPattern.MatchString(plan)
}

// NormalizeMarketPlanInput trims an API plan value; "" = no plan.
func NormalizeMarketPlanInput(plan string) string {
	return strings.TrimSpace(plan)
}

// MarketListing is one account_market_listings row. AllowedGroupIDs is not
// stored as a permission: views fill it with the tiers derived from Plan.
// The reserved_* columns are never read (reservations are always 0).
type MarketListing struct {
	AccountID       int64      `json:"account_id"`
	OwnerUserID     int64      `json:"owner_user_id"`
	Plan            *string    `json:"plan"`
	AllowedGroupIDs []int64    `json:"allowed_group_ids"`
	SelectedGroupID *int64     `json:"selected_group_id"`
	Status          string     `json:"status"`
	Version         int64      `json:"version"`
	ListedAt        *time.Time `json:"listed_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// MarketTierOption is a tier group as shown to admins and owners.
type MarketTierOption struct {
	GroupID        int64   `json:"group_id"`
	Name           string  `json:"name"`
	RateMultiplier float64 `json:"rate_multiplier"`
	// Plan: the tier's plan (the account's plan in per-account views).
	Plan *string `json:"plan"`
	// Available: still a live, active market tier.
	Available bool `json:"available"`
	// AdminMember: the account is already an admin member of this tier, so
	// it cannot be listed there (set in per-account views).
	AdminMember bool `json:"admin_member"`
}

// MarketAdminConfigureParams is the admin PUT (set or change the plan),
// applied under the account row lock.
type MarketAdminConfigureParams struct {
	AccountID   int64
	AdminUserID int64
	Plan        string
	// Validate runs on the locked account.
	Validate func(account *Account) error
}

// MarketOwnerSetParams is the owner PUT, applied under the account row lock.
type MarketOwnerSetParams struct {
	OwnerUserID int64
	AccountID   int64
	// GroupID 0 = the only live, active tier of the plan.
	GroupID int64
	Listed  bool
	// CooldownHours: market_mode_switch_cooldown_hours read for this request
	// (0 = none).
	CooldownHours int
	// Validate runs on the locked account before listing (not on unlist).
	Validate func(account *Account) error
	// BeforeListWrite runs once every check passed, right before the listing
	// writes; ListingRolledBack runs when the transaction is then rolled
	// back without reaching its commit (the dispatch registry marks / clears
	// the account locally).
	BeforeListWrite   func()
	ListingRolledBack func()
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
	// ListPlanTierOptions: the live market tiers of the platform with the
	// plan (Available = also active), plus extraGroupID when it is a live
	// tier (the selected tier after a re-plan or deactivation).
	ListPlanTierOptions(ctx context.Context, plan, platform string, extraGroupID *int64) ([]MarketTierOption, error)
	// HasAdminMembership reports an admin account_groups row for the pair.
	HasAdminMembership(ctx context.Context, accountID, groupID int64) (bool, error)
	// ListAdminMemberGroupIDs: which of groupIDs the account is an admin
	// member of (those tiers cannot be listed into).
	ListAdminMemberGroupIDs(ctx context.Context, accountID int64, groupIDs []int64) ([]int64, error)
	// NextOwnerSwitchAt: when the owner may switch again (database time),
	// nil when allowed now (no owner switch, cooldown 0 or elapsed).
	NextOwnerSwitchAt(ctx context.Context, accountID int64, cooldownHours int) (*time.Time, error)
	AdminConfigure(ctx context.Context, p MarketAdminConfigureParams) (*MarketListing, error)
	// AdminRevoke returns ErrMarketListingNotFound when there is no listing.
	AdminRevoke(ctx context.Context, accountID, adminUserID int64) (*MarketListing, error)
	// OwnerSet checks market_enabled inside the transaction when listing,
	// and the switch cooldown (except a take-back while the market is off).
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
	// The owner reservation covers concurrency slots, not sessions: a listed
	// account must not use the account-level session limit.
	if a.GetMaxSessions() > 0 {
		return MarketReasonMaxSessions
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
	if a.SelfOnboarded && !a.hasUsableProxy() {
		return MarketReasonProxyUnavailable
	}
	if a.Status != StatusActive || !a.Schedulable {
		return MarketReasonAccountInactive
	}
	return ""
}

// marketCapacityReason: a shared account needs a finite concurrency (0 =
// unlimited slots, which the buyer slot cap cannot express).
func marketCapacityReason(a *Account) string {
	if a == nil || a.Concurrency <= 0 {
		return MarketReasonProtectionInvalid
	}
	return ""
}

// ValidateMarketTier checks the group-level tier rules (also a DB CHECK) and
// that a tier never leaves through a fallback. A plan needs a tier; its
// format is checked here too.
func (g *Group) ValidateMarketTier() error {
	if g == nil {
		return nil
	}
	if g.MarketPlan != nil {
		if !ValidMarketPlan(*g.MarketPlan) {
			return ErrMarketPlanInvalid
		}
		if !g.MarketTier {
			return ErrMarketPlanRequiresTier
		}
	}
	if !g.MarketTier {
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

// MarketTierOverrideStoredAllowed reports a finite user rate override that
// is >= 0 after rounding to the DECIMAL(10,4) storage precision. A zero
// override on a tier is allowed: the buyer path is then unpriced and never
// selects a listed account.
func MarketTierOverrideStoredAllowed(rate float64) bool {
	if math.IsNaN(rate) || math.IsInf(rate, 0) {
		return false
	}
	return !decimal.NewFromFloat(rate).Round(4).IsNegative()
}

// MarketTierGroupGuard is implemented by the group repository: live groups
// whose claude_code_only fallback or invalid-request fallback points at
// groupID (inbound references block promotion to a tier).
type MarketTierGroupGuard interface {
	ListFallbackReferrers(ctx context.Context, groupID int64) ([]int64, error)
}

// MarketTierLookup is implemented by the group repository: which of the
// given live groups are market tiers (one query, no edges loaded).
type MarketTierLookup interface {
	ListMarketTierIDs(ctx context.Context, groupIDs []int64) ([]int64, error)
}

// MarketTierRateGuard is implemented by the user-group rate repository:
// users whose stored override on groupID is negative (or NaN).
type MarketTierRateGuard interface {
	ListNegativeRateUsers(ctx context.Context, groupID int64) ([]int64, error)
}
