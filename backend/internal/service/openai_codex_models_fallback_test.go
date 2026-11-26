//go:build unit

package service

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexModelsManifestFallbackExpiresAfterMaxAge(t *testing.T) {
	var cache codexModelsManifestFallbackCache
	key := CodexModelsManifestFallbackKey{GroupID: 45, RawQuery: "client_version=0.164.0", Official: true, Originator: "codex-tui"}
	start := time.Unix(1_800_000_000, 0)
	cache.remember(key, 19, &CodexModelsManifest{Body: []byte(`{"models":[]}`), ETag: `"e1"`, Headers: http.Header{"Etag": {`"e1"`}}}, true, start)

	got, ok := cache.lookup(key, start.Add(codexModelsManifestFallbackMaxAge))
	require.True(t, ok)
	require.Equal(t, int64(19), got.AccountID)
	got.Headers.Set("Etag", "mutated")

	// A matching 304 refreshes the age; a mismatching one does not.
	cache.remember(key, 20, &CodexModelsManifest{ETag: `"other"`, NotModified: true}, true, start.Add(time.Hour))
	_, ok = cache.lookup(key, start.Add(codexModelsManifestFallbackMaxAge+time.Second))
	require.False(t, ok, "expired entry must not be served")
	require.Zero(t, cache.bytes)

	cache.remember(key, 19, &CodexModelsManifest{Body: []byte(`{"models":[]}`), ETag: `"e1"`, Headers: http.Header{"Etag": {`"e1"`}}}, true, start)
	cache.remember(key, 20, &CodexModelsManifest{ETag: `"e1"`, NotModified: true}, true, start.Add(time.Hour))
	got, ok = cache.lookup(key, start.Add(codexModelsManifestFallbackMaxAge+time.Second))
	require.True(t, ok)
	require.Equal(t, `"e1"`, got.Headers.Get("Etag"), "lookups must not share header maps")
}

func TestCodexModelsManifestFallbackBounds(t *testing.T) {
	var cache codexModelsManifestFallbackCache
	now := time.Unix(1_800_000_000, 0)
	big := bytes.Repeat([]byte("x"), codexModelsManifestCacheBodyLimit+1)
	key := CodexModelsManifestFallbackKey{GroupID: 1}
	cache.remember(key, 1, &CodexModelsManifest{Body: []byte(`{"models":[]}`)}, false, now)
	cache.remember(key, 1, &CodexModelsManifest{Body: big}, false, now)
	_, ok := cache.lookup(key, now)
	require.False(t, ok, "an oversized answer replaces (drops) the older body")

	body := bytes.Repeat([]byte("y"), codexModelsManifestCacheBodyLimit)
	for i := 0; i < codexModelsManifestFallbackMaxEntries+8; i++ {
		cache.remember(CodexModelsManifestFallbackKey{GroupID: int64(i)}, 1, &CodexModelsManifest{Body: body}, false, now)
		require.LessOrEqual(t, len(cache.entries), codexModelsManifestFallbackMaxEntries)
		require.LessOrEqual(t, cache.bytes, codexModelsManifestFallbackMaxBytes)
	}
	_, ok = cache.lookup(CodexModelsManifestFallbackKey{GroupID: 0}, now)
	require.False(t, ok, "oldest entries are evicted first")
	_, ok = cache.lookup(CodexModelsManifestFallbackKey{GroupID: codexModelsManifestFallbackMaxEntries + 7}, now)
	require.True(t, ok)
}
