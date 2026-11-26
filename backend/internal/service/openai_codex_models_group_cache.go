package service

import (
	"context"
	"net/http"
	"time"
)

// The group cache serves the stored manifest of an OpenAI OAuth group (see
// CodexModelsManifestFallbackKey) without contacting upstream while it is
// fresh, and serves it immediately while one background refresh runs once it
// is older than that, up to the stale fallback's maximum age. It shares the
// entries, bounds and fetch-time ordering of the stale-on-error fallback.
const (
	// codexModelsManifestFreshTTL is measured from the upstream fetch. New
	// upstream models appear up to this late.
	codexModelsManifestFreshTTL = 20 * time.Minute
	// codexModelsManifestRefreshRetryAfter throttles background refreshes
	// of one representation after a failed refresh.
	codexModelsManifestRefreshRetryAfter = time.Minute
)

// CodexModelsManifestGroupCacheState classifies a group-cache lookup.
type CodexModelsManifestGroupCacheState uint8

const (
	// CodexModelsManifestGroupCacheMiss: no servable entry; fetch upstream.
	CodexModelsManifestGroupCacheMiss CodexModelsManifestGroupCacheState = iota
	// CodexModelsManifestGroupCacheFresh: serve without contacting upstream.
	CodexModelsManifestGroupCacheFresh
	// CodexModelsManifestGroupCacheRevalidate: serve and refresh in the
	// background.
	CodexModelsManifestGroupCacheRevalidate
)

// codexModelsManifestEntryHeaderNames are the response headers that describe
// the stored representation. Per-request headers (request IDs, cf-ray,
// set-cookie, date, rate-limit/quota state) are never stored or replayed.
// Codex reads only ETag from this endpoint.
var codexModelsManifestEntryHeaderNames = []string{"Etag", "Content-Type", "Cache-Control", "Last-Modified"}

func codexModelsManifestEntryHeaders(src http.Header) http.Header {
	var dst http.Header
	for _, name := range codexModelsManifestEntryHeaderNames {
		if values := src.Values(name); len(values) > 0 {
			if dst == nil {
				dst = make(http.Header, len(codexModelsManifestEntryHeaderNames))
			}
			dst[name] = append([]string(nil), values...)
		}
	}
	return dst
}

// CodexModelsManifestEntryHeaders returns a copy of the entry-level headers
// of an upstream manifest answer (what a group-cache hit would replay).
func CodexModelsManifestEntryHeaders(manifest *CodexModelsManifest) http.Header {
	if manifest == nil {
		return nil
	}
	return codexModelsManifestEntryHeaders(manifest.Headers)
}

// GroupCacheable reports whether manifest is a direct upstream answer of an
// OpenAI OAuth (non-relay) account, i.e. one the group cache stores.
func (m *CodexModelsManifest) GroupCacheable() bool {
	return m != nil && m.groupCacheable && !m.NotModified
}

func (c *codexModelsManifestFallbackCache) lookupGroupCache(key CodexModelsManifestFallbackKey, now time.Time) (CodexModelsManifestFallback, CodexModelsManifestGroupCacheState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !entry.fallback.GroupCacheable {
		return CodexModelsManifestFallback{}, CodexModelsManifestGroupCacheMiss, false
	}
	age := now.Sub(entry.fallback.StoredAt)
	if age > codexModelsManifestFallbackMaxAge {
		c.deleteLocked(key)
		return CodexModelsManifestFallback{}, CodexModelsManifestGroupCacheMiss, false
	}
	if age < codexModelsManifestFreshTTL {
		return entry.fallback, CodexModelsManifestGroupCacheFresh, false
	}
	refreshDue := entry.refreshFailedAt.IsZero() || now.Sub(entry.refreshFailedAt) >= codexModelsManifestRefreshRetryAfter
	return entry.fallback, CodexModelsManifestGroupCacheRevalidate, refreshDue
}

func (c *codexModelsManifestFallbackCache) markRefreshFailed(key CodexModelsManifestFallbackKey, storedAt, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Only the representation the refresh tried to replace is throttled; a
	// newer entry stored meanwhile keeps its own schedule.
	if entry, ok := c.entries[key]; ok && entry.fallback.StoredAt.Equal(storedAt) {
		entry.refreshFailedAt = now
	}
}

func (c *codexModelsManifestFallbackCache) forgetGroupCache(key CodexModelsManifestFallbackKey, storedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok && entry.fallback.StoredAt.Equal(storedAt) {
		entry.fallback.GroupCacheable = false
	}
}

func (s *OpenAIGatewayService) codexModelsManifestNow() time.Time {
	if s != nil && s.codexModelsManifestClock != nil {
		return s.codexModelsManifestClock()
	}
	return time.Now()
}

// LookupCodexModelsManifestGroupCache returns the group's stored OAuth
// manifest for key and whether it is fresh or due for revalidation. refresh
// reports whether a background refresh should be started (it is throttled
// after a failed refresh).
func (s *OpenAIGatewayService) LookupCodexModelsManifestGroupCache(key CodexModelsManifestFallbackKey) (entry CodexModelsManifestFallback, state CodexModelsManifestGroupCacheState, refresh bool) {
	if s.codexModelsManifestGroupCacheOff {
		return CodexModelsManifestFallback{}, CodexModelsManifestGroupCacheMiss, false
	}
	return s.codexModelsManifestFallback.lookupGroupCache(key, s.codexModelsManifestNow())
}

// MarkCodexModelsManifestRefreshFailed records a failed background refresh
// of the entry stored at storedAt. The entry is kept and its age unchanged.
func (s *OpenAIGatewayService) MarkCodexModelsManifestRefreshFailed(key CodexModelsManifestFallbackKey, storedAt time.Time) {
	s.codexModelsManifestFallback.markRefreshFailed(key, storedAt, s.codexModelsManifestNow())
}

// ForgetCodexModelsManifestGroupCache stops serving the entry stored at
// storedAt as a group-cache hit (for example after the group gained a relay
// or API-key account). It stays available to the stale fallback.
func (s *OpenAIGatewayService) ForgetCodexModelsManifestGroupCache(key CodexModelsManifestFallbackKey, storedAt time.Time) {
	s.codexModelsManifestFallback.forgetGroupCache(key, storedAt)
}

// CodexModelsManifestGroupCacheEligible reports whether every schedulable
// account of the group is an OpenAI OAuth account that is not a native relay.
// Groups with relay or API-key accounts keep the uncached path (relays have
// their own fallback, API-key upstreams their own cache).
func (s *OpenAIGatewayService) CodexModelsManifestGroupCacheEligible(ctx context.Context, groupID *int64) bool {
	if s.codexModelsManifestGroupCacheOff {
		return false
	}
	accounts, err := s.listSchedulableAccounts(ctx, groupID)
	if err != nil || len(accounts) == 0 {
		return false
	}
	for i := range accounts {
		if !accounts[i].IsOpenAIOAuth() || accounts[i].IsOpenAICodexNativeRelay() {
			return false
		}
	}
	return true
}

// CodexModelsManifestRefreshBudget bounds a detached refresh: one upstream
// timeout per account attempt.
func CodexModelsManifestRefreshBudget(maxSwitches int) time.Duration {
	if maxSwitches < 0 {
		maxSwitches = 0
	}
	return time.Duration(maxSwitches+1)*codexModelsManifestRequestTimeout + 5*time.Second
}

// SetCodexModelsManifestClockForTest replaces the manifest caches' clock.
func (s *OpenAIGatewayService) SetCodexModelsManifestClockForTest(now func() time.Time) {
	s.codexModelsManifestClock = now
}

// DisableCodexModelsManifestGroupCacheForTest turns the group cache off so
// tests can reach the stale-on-error fallback with OAuth accounts.
func (s *OpenAIGatewayService) DisableCodexModelsManifestGroupCacheForTest() {
	s.codexModelsManifestGroupCacheOff = true
}
