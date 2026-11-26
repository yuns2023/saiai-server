package service

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// The stale-on-error fallback keeps the last manifest a group's upstream
// answered with, so a transient upstream failure does not push Codex back to
// its bundled (older) model list. It is consulted only after every account
// attempt failed with upstream unavailability; a successful upstream answer is
// always passed through unchanged. The cache is in-process: with several
// gateway replicas each one keeps its own copy, which is acceptable for a
// best-effort fallback.
const (
	codexModelsManifestFallbackMaxEntries = 64
	codexModelsManifestFallbackMaxBytes   = 32 << 20
	// Long enough to bridge an upstream incident of a few hours, short
	// enough that a list never outlives a catalog/plan change by much. Every
	// success refreshes the entry, so the age only grows while failing.
	codexModelsManifestFallbackMaxAge = 6 * time.Hour
)

// CodexModelsManifestFallbackKey identifies requests that the success path
// would answer with the same manifest. The group's blocked-model filter is
// not part of the key: the unfiltered body is stored and the current filter
// is applied when it is served. Native relay accounts never use the fallback.
type CodexModelsManifestFallbackKey struct {
	GroupID int64
	// RawQuery is the client's query verbatim (client_version and future
	// parameters are forwarded upstream as-is).
	RawQuery string
	// Official is false when the gateway's discovery identity replaced the
	// client's. Originator and Version are the official client's forwarded
	// identity (lower-cased / trimmed).
	Official   bool
	Originator string
	Version    string
}

func (k CodexModelsManifestFallbackKey) size() int {
	return len(k.RawQuery) + len(k.Originator) + len(k.Version) + 64
}

// CodexModelsManifestFallback is a stored manifest. Only the representation
// validator is kept: other upstream response headers (request IDs, cache
// policy, client controls) belong to the original answer and are not replayed.
type CodexModelsManifestFallback struct {
	// Body is the manifest before the group's blocked-model filter.
	Body      []byte
	ETag      string
	AccountID int64
	StoredAt  time.Time
}

type codexModelsManifestFallbackEntry struct {
	fallback CodexModelsManifestFallback
	order    uint64
}

type codexModelsManifestFallbackCache struct {
	mu        sync.Mutex
	entries   map[CodexModelsManifestFallbackKey]*codexModelsManifestFallbackEntry
	bytes     int
	nextOrder uint64
}

func codexModelsManifestFallbackEntrySize(key CodexModelsManifestFallbackKey, fallback CodexModelsManifestFallback) int {
	return key.size() + len(fallback.Body) + len(fallback.ETag)
}

func (c *codexModelsManifestFallbackCache) remember(key CodexModelsManifestFallbackKey, accountID int64, manifest *CodexModelsManifest, now time.Time) {
	if manifest == nil {
		return
	}
	// Only a real upstream answer counts; a manifest without fetchedAt did
	// not come from fetchCodexModelsManifestUpstream.
	fetchedAt := manifest.fetchedAt
	if fetchedAt.IsZero() || fetchedAt.After(now) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if manifest.NotModified {
		// A 304 only refreshes the age, and only when the same account
		// validated the same representation. It never deletes: results can
		// arrive out of order, and a newer 200 replaces the entry anyway.
		entry, ok := c.entries[key]
		etag := strings.TrimSpace(manifest.ETag)
		if !ok || entry.fallback.AccountID != accountID || etag == "" || etag != entry.fallback.ETag {
			return
		}
		if fetchedAt.After(entry.fallback.StoredAt) {
			c.nextOrder++
			entry.order = c.nextOrder
			entry.fallback.StoredAt = fetchedAt
		}
		return
	}
	if entry, ok := c.entries[key]; ok && !fetchedAt.After(entry.fallback.StoredAt) {
		// The API-key cache hands back the same older answer; keep its age.
		return
	}
	c.deleteLocked(key)
	if len(manifest.Body) == 0 || len(manifest.Body) > codexModelsManifestCacheBodyLimit {
		return
	}
	if c.entries == nil {
		c.entries = make(map[CodexModelsManifestFallbackKey]*codexModelsManifestFallbackEntry)
	}
	fallback := CodexModelsManifestFallback{
		// An exact-length copy, so the byte budget counts what is retained
		// (not a larger read buffer behind manifest.Body).
		Body:      append(make([]byte, 0, len(manifest.Body)), manifest.Body...),
		ETag:      strings.TrimSpace(manifest.ETag),
		AccountID: accountID,
		StoredAt:  fetchedAt,
	}
	size := codexModelsManifestFallbackEntrySize(key, fallback)
	if size > codexModelsManifestFallbackMaxBytes {
		return
	}
	for len(c.entries) > 0 && (len(c.entries) >= codexModelsManifestFallbackMaxEntries || c.bytes+size > codexModelsManifestFallbackMaxBytes) {
		c.evictOldestLocked(now)
	}
	c.nextOrder++
	c.entries[key] = &codexModelsManifestFallbackEntry{fallback: fallback, order: c.nextOrder}
	c.bytes += size
}

func (c *codexModelsManifestFallbackCache) lookup(key CodexModelsManifestFallbackKey, now time.Time) (CodexModelsManifestFallback, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return CodexModelsManifestFallback{}, false
	}
	if now.Sub(entry.fallback.StoredAt) > codexModelsManifestFallbackMaxAge {
		c.deleteLocked(key)
		return CodexModelsManifestFallback{}, false
	}
	return entry.fallback, true
}

func (c *codexModelsManifestFallbackCache) deleteLocked(key CodexModelsManifestFallbackKey) {
	if entry, ok := c.entries[key]; ok {
		c.bytes -= codexModelsManifestFallbackEntrySize(key, entry.fallback)
		delete(c.entries, key)
	}
}

func (c *codexModelsManifestFallbackCache) evictOldestLocked(now time.Time) {
	var oldestKey CodexModelsManifestFallbackKey
	var oldest *codexModelsManifestFallbackEntry
	for key, entry := range c.entries {
		if now.Sub(entry.fallback.StoredAt) > codexModelsManifestFallbackMaxAge {
			c.deleteLocked(key)
			return
		}
		if oldest == nil || entry.order < oldest.order {
			oldestKey, oldest = key, entry
		}
	}
	if oldest != nil {
		c.deleteLocked(oldestKey)
	}
}

// RememberCodexModelsManifestFallback records a manifest the handler is about
// to serve from a successful upstream answer (callers skip native relays).
func (s *OpenAIGatewayService) RememberCodexModelsManifestFallback(key CodexModelsManifestFallbackKey, accountID int64, manifest *CodexModelsManifest) {
	s.codexModelsManifestFallback.remember(key, accountID, manifest, time.Now())
}

// LookupCodexModelsManifestFallback returns the last good manifest for key if
// it is younger than the fallback age limit.
func (s *OpenAIGatewayService) LookupCodexModelsManifestFallback(key CodexModelsManifestFallbackKey) (CodexModelsManifestFallback, bool) {
	return s.codexModelsManifestFallback.lookup(key, time.Now())
}

// IsCodexModelsManifestStaleFallbackError reports whether err is upstream
// unavailability (transport failure, timeout, 429 or 5xx) for which a stale
// manifest may be served instead of an error.
func IsCodexModelsManifestStaleFallbackError(err error) bool {
	var upstreamErr *codexModelsManifestUpstreamError
	return errors.As(err, &upstreamErr) && upstreamErr.staleFallback
}

// CodexModelsManifestUpstreamStatus returns the upstream HTTP status behind a
// manifest error, or 0 when no upstream response was received.
func CodexModelsManifestUpstreamStatus(err error) int {
	var upstreamErr *codexModelsManifestUpstreamError
	if errors.As(err, &upstreamErr) {
		return upstreamErr.upstreamStatus
	}
	return 0
}

// CodexModelsManifestETagMatches reports whether an If-None-Match header
// value validates etag (weak comparison, "*" matches).
func CodexModelsManifestETagMatches(ifNoneMatch, etag string) bool {
	return codexModelsManifestETagMatches(ifNoneMatch, etag)
}
