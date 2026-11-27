package service

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"golang.org/x/sync/singleflight"
)

// allRateLimitedListPageSize bounds the group read behind
// AllAccountsRateLimitedUntil (the repository caps a page at 100). A larger
// group keeps the generic "no available accounts" answer.
const allRateLimitedListPageSize = 100

const (
	// allRateLimitedCacheTTL bounds how long a computed answer (429 reset or
	// "unknown", i.e. 503) is reused for the same Key, group and model, so a
	// retry storm costs one group read per TTL per replica. Staleness is at
	// most the TTL, like the existing snapshot/DB read race.
	allRateLimitedCacheTTL = 10 * time.Second
	// allRateLimitedCacheMaxEntries caps the per-process cache; it is simply
	// swept of expired entries when full, and emptied if still full.
	allRateLimitedCacheMaxEntries = 4096
	// allRateLimitedMaxModelLen bounds the request model kept in a cache key
	// (the model string is client input); longer ones skip the diagnosis.
	allRateLimitedMaxModelLen = 256
	// allRateLimitedComputeTimeout bounds the shared computation, which runs
	// detached from the first caller's cancellation (singleflight).
	allRateLimitedComputeTimeout = 5 * time.Second
)

type allRateLimitedResult struct {
	resetAt time.Time
	ok      bool
}

type allRateLimitedEntry struct {
	result    allRateLimitedResult
	expiresAt time.Time
}

// allRateLimitedCache is the per-process cache behind
// AllAccountsRateLimitedUntil; the zero value is ready to use.
type allRateLimitedCache struct {
	mu      sync.Mutex
	entries map[string]allRateLimitedEntry
	group   singleflight.Group
}

func (c *allRateLimitedCache) get(key string, now time.Time) (allRateLimitedResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !now.Before(e.expiresAt) {
		return allRateLimitedResult{}, false
	}
	return e.result, true
}

// put stores result until now+TTL; a reset answer never outlives its reset,
// so a client returning after Retry-After gets a fresh answer.
func (c *allRateLimitedCache) put(key string, result allRateLimitedResult, now time.Time) {
	expiresAt := now.Add(allRateLimitedCacheTTL)
	if result.ok && result.resetAt.Before(expiresAt) {
		expiresAt = result.resetAt
	}
	if !now.Before(expiresAt) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]allRateLimitedEntry)
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= allRateLimitedCacheMaxEntries {
		for k, e := range c.entries {
			if !now.Before(e.expiresAt) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= allRateLimitedCacheMaxEntries {
			c.entries = make(map[string]allRateLimitedEntry)
		}
	}
	c.entries[key] = allRateLimitedEntry{result: result, expiresAt: expiresAt}
}

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
// Accounts on retired platforms are never scheduled and are not candidates.
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
//
// Answers are cached per process for allRateLimitedCacheTTL and computed
// once per key at a time. The cache key is the API Key, the group, the model
// and the client kind, not the group alone: candidates depend on the Key's
// user (trusted-only and carpool user lists), its market buyer state and on
// whether the client is Claude Code (claude_code_only fallback). Callers
// derive Retry-After from the returned time at write time.
func (s *GatewayService) AllAccountsRateLimitedUntil(ctx context.Context, apiKeyID int64, groupID *int64, requestedModel string) (time.Time, bool) {
	if s == nil || s.accountRepo == nil || groupID == nil || *groupID <= 0 || s.isRunModeSimple() ||
		len(requestedModel) > allRateLimitedMaxModelLen {
		return time.Time{}, false
	}
	// A forced platform resolves no group (see computeAllAccountsRateLimitedUntil);
	// rule it out before the cache, whose key does not carry it.
	if forced, _ := ctx.Value(ctxkey.ForcePlatform).(string); forced != "" {
		return time.Time{}, false
	}
	key := strconv.FormatInt(apiKeyID, 10) + "|" + strconv.FormatInt(*groupID, 10) + "|" +
		strconv.FormatBool(IsClaudeCodeClient(ctx)) + "|" + requestedModel
	if cached, ok := s.allRateLimited.get(key, time.Now()); ok {
		return cached.resetAt, cached.ok
	}
	ch := s.allRateLimited.group.DoChan(key, func() (any, error) {
		if cached, ok := s.allRateLimited.get(key, time.Now()); ok {
			return cached, nil
		}
		computeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), allRateLimitedComputeTimeout)
		defer cancel()
		resetAt, ok := s.computeAllAccountsRateLimitedUntil(computeCtx, groupID, requestedModel)
		result := allRateLimitedResult{resetAt: resetAt, ok: ok}
		s.allRateLimited.put(key, result, time.Now())
		return result, nil
	})
	var result allRateLimitedResult
	select {
	case r := <-ch:
		result, _ = r.Val.(allRateLimitedResult)
	case <-ctx.Done():
		// The shared computation keeps running for the other waiters.
		return time.Time{}, false
	}
	if !result.ok || !time.Now().Before(result.resetAt) {
		return time.Time{}, false
	}
	return result.resetAt, true
}

func (s *GatewayService) computeAllAccountsRateLimitedUntil(ctx context.Context, groupID *int64, requestedModel string) (time.Time, bool) {
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
		if domain.IsRetiredPlatform(acc.Platform) || // IsSchedulable rejects these first
			!s.isAccountAllowedForPlatform(acc, platform, useMixed) ||
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
