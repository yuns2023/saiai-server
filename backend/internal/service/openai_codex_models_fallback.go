package service

import (
	"errors"
	"net/http"
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
// is applied when it is served.
type CodexModelsManifestFallbackKey struct {
	GroupID int64
	// RawQuery is the client's query verbatim (client_version and future
	// parameters are forwarded upstream as-is).
	RawQuery string
	// Official is false when the gateway's discovery identity replaced the
	// client's; Originator is the (lower-cased) official client originator.
	Official   bool
	Originator string
}

// CodexModelsManifestFallback is a stored manifest plus what the success path
// wrote with it.
type CodexModelsManifestFallback struct {
	// Body is the manifest before the group's blocked-model filter.
	Body []byte
	ETag string
	// NativeHeaders reports whether Headers are the upstream response headers
	// written through WriteNativeCodexResponseHeaders (OAuth / native relay).
	NativeHeaders bool
	Headers       http.Header
	AccountID     int64
	StoredAt      time.Time
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

func (c *codexModelsManifestFallbackCache) remember(key CodexModelsManifestFallbackKey, accountID int64, manifest *CodexModelsManifest, nativeHeaders bool, now time.Time) {
	if manifest == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if manifest.NotModified {
		// A 304 confirms the stored body only when it validates the same
		// representation.
		if entry, ok := c.entries[key]; ok && entry.fallback.ETag != "" && entry.fallback.ETag == strings.TrimSpace(manifest.ETag) {
			c.nextOrder++
			entry.order = c.nextOrder
			entry.fallback.StoredAt = now
		}
		return
	}
	if len(manifest.Body) == 0 || len(manifest.Body) > codexModelsManifestCacheBodyLimit {
		c.deleteLocked(key)
		return
	}
	if c.entries == nil {
		c.entries = make(map[CodexModelsManifestFallbackKey]*codexModelsManifestFallbackEntry)
	}
	c.deleteLocked(key)
	for len(c.entries) > 0 && (len(c.entries) >= codexModelsManifestFallbackMaxEntries || c.bytes+len(manifest.Body) > codexModelsManifestFallbackMaxBytes) {
		c.evictOldestLocked(now)
	}
	fallback := CodexModelsManifestFallback{
		Body:          manifest.Body,
		ETag:          strings.TrimSpace(manifest.ETag),
		NativeHeaders: nativeHeaders,
		AccountID:     accountID,
		StoredAt:      now,
	}
	if nativeHeaders {
		fallback.Headers = manifest.Headers.Clone()
	}
	c.nextOrder++
	c.entries[key] = &codexModelsManifestFallbackEntry{fallback: fallback, order: c.nextOrder}
	c.bytes += len(manifest.Body)
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
	fallback := entry.fallback
	fallback.Headers = fallback.Headers.Clone()
	return fallback, true
}

func (c *codexModelsManifestFallbackCache) deleteLocked(key CodexModelsManifestFallbackKey) {
	if entry, ok := c.entries[key]; ok {
		c.bytes -= len(entry.fallback.Body)
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
// to serve from a successful upstream answer. nativeHeaders must match whether
// the success path writes manifest.Headers to the client.
func (s *OpenAIGatewayService) RememberCodexModelsManifestFallback(key CodexModelsManifestFallbackKey, accountID int64, manifest *CodexModelsManifest, nativeHeaders bool) {
	s.codexModelsManifestFallback.remember(key, accountID, manifest, nativeHeaders, time.Now())
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
