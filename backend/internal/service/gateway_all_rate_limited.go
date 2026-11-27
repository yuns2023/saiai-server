package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// allRateLimitedListPageSize bounds the group read behind
// AllAccountsRateLimitedUntil (the repository caps a page at 100). A larger
// group keeps the generic "no available accounts" answer.
const allRateLimitedListPageSize = 100

// AllAccountsRateLimitedUntil explains a failed account selection for a
// group: it reports the earliest time one of the group's candidate accounts
// becomes schedulable again, when every candidate is blocked only by a
// time-bound limit with a stored end (account rate limit, overload,
// temporary unschedulable, model-scoped rate limit). It returns ok=false as
// soon as one candidate is out for a reason without a known end (error or
// disabled status, paused scheduling, expiry, owner pause, quota or window
// cost), is schedulable by its stored state (the selection then failed for
// something else: concurrency, RPM, carpool devices, pinned bindings, a stale
// snapshot), or when the group cannot be read; callers then keep the generic
// answer. It never invents a time.
//
// Candidates are the group's members on the platform selection uses (with
// mixed Antigravity accounts), allowed for the user, supporting the model.
// Ungrouped Keys, simple mode and market tiers are out of scope (ok=false).
//
// The result is a lower bound: it is when the first candidate's time-bound
// limit ends. Admission applied at that time (pinned/carpool devices,
// sticky bindings, RPM, concurrency) can still reject the retry, which then
// gets a fresh answer; the time is never later than a stored end.
//
// The block ends are timestamps stored in the database by whichever gateway
// replica saw the upstream limit; they are compared with this process's
// clock, the same clock account selection uses for them.
func (s *GatewayService) AllAccountsRateLimitedUntil(ctx context.Context, groupID *int64, requestedModel string) (time.Time, bool) {
	if s == nil || s.accountRepo == nil || groupID == nil || *groupID <= 0 || s.isRunModeSimple() {
		return time.Time{}, false
	}
	group, resolvedID, err := s.checkClaudeCodeRestriction(ctx, groupID)
	if err != nil || resolvedID == nil || *resolvedID <= 0 {
		return time.Time{}, false
	}
	// A forced platform resolves no group; without it the group's kind
	// (market tier or not) is unknown.
	if group == nil || group.MarketTier {
		return time.Time{}, false
	}
	ctx = s.withGroupContext(ctx, group)
	platform, hasForcePlatform, err := s.resolvePlatform(ctx, resolvedID, group)
	if err != nil || platform != PlatformAnthropic {
		return time.Time{}, false
	}
	useMixed := !hasForcePlatform

	accounts, page, err := s.accountRepo.ListWithFilters(ctx,
		pagination.PaginationParams{Page: 1, PageSize: allRateLimitedListPageSize},
		"", "", "", "", *resolvedID, AccountListSort{})
	// Count and page are two reads: a full page may hide a member added in
	// between, so only a short page that matches the count is complete.
	if err != nil || page == nil || len(accounts) >= allRateLimitedListPageSize || page.Total != int64(len(accounts)) {
		return time.Time{}, false
	}
	// Drop members selection would not offer to this request (market
	// listings outside their tier and the like), as listSchedulableAccounts.
	accounts = s.marketFilterCandidates(ctx, accounts, resolvedID)
	return s.earliestTimeBoundRelease(ctx, accounts, platform, useMixed, requestedModel, time.Now())
}

// AllAccountsRateLimitedMessage is the client-facing text of the 429 for a
// group whose accounts are all rate limited until resetAt.
func AllAccountsRateLimitedMessage(resetAt time.Time) string {
	return "No available accounts: all accounts in this group are rate limited, resets at " + formatLimitResetTime(resetAt)
}

// earliestTimeBoundRelease is the decision behind AllAccountsRateLimitedUntil
// for an already loaded member list.
func (s *GatewayService) earliestTimeBoundRelease(ctx context.Context, accounts []Account, platform string, useMixed bool, requestedModel string, now time.Time) (time.Time, bool) {
	var earliest time.Time
	candidates := 0
	for i := range accounts {
		acc := &accounts[i]
		if !s.isAccountAllowedForPlatform(acc, platform, useMixed) ||
			!accountAllowsUser(ctx, acc) ||
			s.excludedInSimpleMode(acc) ||
			(requestedModel != "" && !s.isModelSupportedByAccountWithContext(ctx, acc, requestedModel)) {
			continue // could never serve this request
		}
		until, ok := s.timeBoundBlockedUntil(ctx, acc, requestedModel, now)
		if !ok {
			return time.Time{}, false
		}
		candidates++
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	if candidates == 0 {
		return time.Time{}, false
	}
	return earliest, true
}

// timeBoundBlockedUntil reports when acc becomes schedulable again for
// requestedModel, if it is blocked now and only by limits with a stored end;
// the latest of those ends, since all of them must pass.
func (s *GatewayService) timeBoundBlockedUntil(ctx context.Context, acc *Account, requestedModel string, now time.Time) (time.Time, bool) {
	if acc == nil || !acc.IsActive() || !acc.Schedulable {
		return time.Time{}, false
	}
	if acc.IsOwnerOrProxyBlocked() {
		return time.Time{}, false
	}
	// Blocks without a stored end that would still hold after the reset; a
	// window cost that could not be read is unknown, not clear.
	if !s.isAccountSchedulableForQuota(acc) {
		return time.Time{}, false
	}
	if schedulable, known := s.windowCostSchedulability(ctx, acc, false); !schedulable || !known {
		return time.Time{}, false
	}

	var until time.Time
	for _, t := range []*time.Time{acc.RateLimitResetAt, acc.OverloadUntil, acc.TempUnschedulableUntil} {
		if t != nil && now.Before(*t) && t.After(until) {
			until = *t
		}
	}
	overagesAvailable := acc.Platform == PlatformAntigravity && acc.IsOveragesEnabled() && !acc.isCreditsExhausted()
	if !overagesAvailable && acc.isModelRateLimitedWithContext(ctx, requestedModel) {
		modelUntil := time.Time{}
		for _, key := range acc.modelRateLimitKeysForRequest(ctx, requestedModel) {
			if t := acc.modelRateLimitResetAt(key); t != nil && now.Before(*t) && t.After(modelUntil) {
				modelUntil = *t
			}
		}
		if modelUntil.IsZero() {
			return time.Time{}, false
		}
		if modelUntil.After(until) {
			until = modelUntil
		}
	}
	if until.IsZero() {
		// Schedulable by its stored state: the selection failed for another
		// reason (concurrency, RPM, devices, bindings, stale snapshot).
		return time.Time{}, false
	}
	// Expiry and the OAuth token's no-refresh window are checked at the
	// reset, not now: an account that will be paused by then has no end.
	if acc.AutoPauseOnExpired && acc.ExpiresAt != nil && !until.Before(*acc.ExpiresAt) {
		return time.Time{}, false
	}
	if acc.IsClaudeOAuthTokenSchedulingDisabled(until) {
		return time.Time{}, false
	}
	return until, true
}
