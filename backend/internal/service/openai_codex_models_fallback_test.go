//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func codexModelsFallbackTestManifest(body, etag string, fetchedAt time.Time) *CodexModelsManifest {
	return &CodexModelsManifest{Body: []byte(body), ETag: etag, fetchedAt: fetchedAt}
}

func TestCodexModelsManifestFallbackExpiresAfterMaxAge(t *testing.T) {
	var cache codexModelsManifestFallbackCache
	key := CodexModelsManifestFallbackKey{GroupID: 45, RawQuery: "client_version=0.164.0", Official: true, Originator: "codex-tui", Version: "0.164.0"}
	start := time.Unix(1_800_000_000, 0)
	cache.remember(key, 19, codexModelsFallbackTestManifest(`{"models":[]}`, `"e1"`, start), start)

	got, ok := cache.lookup(key, start.Add(codexModelsManifestFallbackMaxAge))
	require.True(t, ok)
	require.Equal(t, int64(19), got.AccountID)

	// A 304 refreshes the age only for the same account and validator.
	later := start.Add(time.Hour)
	cache.remember(key, 19, &CodexModelsManifest{ETag: `"other"`, NotModified: true, fetchedAt: later}, later)
	cache.remember(key, 20, &CodexModelsManifest{ETag: `"e1"`, NotModified: true, fetchedAt: later}, later)
	_, ok = cache.lookup(key, start.Add(codexModelsManifestFallbackMaxAge+time.Second))
	require.False(t, ok, "expired entry must not be served")
	require.Zero(t, cache.bytes)

	cache.remember(key, 19, codexModelsFallbackTestManifest(`{"models":[]}`, `"e1"`, start), start)
	cache.remember(key, 19, &CodexModelsManifest{ETag: `"e1"`, NotModified: true, fetchedAt: later}, later)
	_, ok = cache.lookup(key, start.Add(codexModelsManifestFallbackMaxAge+time.Second))
	require.True(t, ok)
}

// Serving an API-key cache copy is not a new upstream success.
func TestCodexModelsManifestFallbackKeepsUpstreamFetchTime(t *testing.T) {
	var cache codexModelsManifestFallbackCache
	key := CodexModelsManifestFallbackKey{GroupID: 45}
	start := time.Unix(1_800_000_000, 0)
	cached := codexModelsFallbackTestManifest(`{"models":[]}`, `"e1"`, start)
	cache.remember(key, 19, cached, start)
	cache.remember(key, 19, cached, start.Add(5*time.Hour))
	got, ok := cache.lookup(key, start.Add(5*time.Hour))
	require.True(t, ok)
	require.Equal(t, start, got.StoredAt)

	// A manifest that never came from an upstream answer is not stored.
	cache.remember(CodexModelsManifestFallbackKey{GroupID: 46}, 19, &CodexModelsManifest{Body: []byte(`{"models":[]}`)}, start)
	_, ok = cache.lookup(CodexModelsManifestFallbackKey{GroupID: 46}, start)
	require.False(t, ok)
}

func TestCodexModelsManifestFallbackBounds(t *testing.T) {
	var cache codexModelsManifestFallbackCache
	now := time.Unix(1_800_000_000, 0)
	key := CodexModelsManifestFallbackKey{GroupID: 1}
	cache.remember(key, 1, codexModelsFallbackTestManifest(`{"models":[]}`, "", now), now)
	big := bytes.Repeat([]byte("x"), codexModelsManifestCacheBodyLimit+1)
	cache.remember(key, 1, &CodexModelsManifest{Body: big, fetchedAt: now.Add(time.Second)}, now.Add(time.Second))
	_, ok := cache.lookup(key, now)
	require.False(t, ok, "an oversized answer replaces (drops) the older body")
	require.Zero(t, cache.bytes)

	// Byte budget.
	body := string(bytes.Repeat([]byte("y"), codexModelsManifestCacheBodyLimit))
	for i := 0; i < 40; i++ {
		cache.remember(CodexModelsManifestFallbackKey{GroupID: int64(i)}, 1, codexModelsFallbackTestManifest(body, "", now), now)
		require.LessOrEqual(t, cache.bytes, codexModelsManifestFallbackMaxBytes)
	}
	_, ok = cache.lookup(CodexModelsManifestFallbackKey{GroupID: 0}, now)
	require.False(t, ok, "oldest entries are evicted first")
	_, ok = cache.lookup(CodexModelsManifestFallbackKey{GroupID: 39}, now)
	require.True(t, ok)

	// Entry count with small bodies (key bytes are accounted too).
	cache = codexModelsManifestFallbackCache{}
	for i := 0; i < codexModelsManifestFallbackMaxEntries+8; i++ {
		k := CodexModelsManifestFallbackKey{GroupID: int64(i), RawQuery: strings.Repeat("q", 100)}
		cache.remember(k, 1, codexModelsFallbackTestManifest(`{"models":[]}`, "", now), now)
		require.LessOrEqual(t, len(cache.entries), codexModelsManifestFallbackMaxEntries)
	}
	_, ok = cache.lookup(CodexModelsManifestFallbackKey{GroupID: 7, RawQuery: strings.Repeat("q", 100)}, now)
	require.False(t, ok)
	_, ok = cache.lookup(CodexModelsManifestFallbackKey{GroupID: 8, RawQuery: strings.Repeat("q", 100)}, now)
	require.True(t, ok)
	want := 0
	for k, entry := range cache.entries {
		want += codexModelsManifestFallbackEntrySize(k, entry.fallback)
	}
	require.Equal(t, want, cache.bytes)
}

// Native relays are other gateways: their 5xx/508 may carry a downstream
// auth failure or relay loop, so they never qualify for the stale fallback.
func TestCodexModelsManifestStaleFallbackClassification(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		err         error
		nativeRelay bool
		want        bool
	}{
		{name: "timeout", err: context.DeadlineExceeded, want: true},
		{name: "canceled", err: context.Canceled},
		{name: "502", status: http.StatusBadGateway, want: true},
		{name: "429", status: http.StatusTooManyRequests, want: true},
		{name: "401", status: http.StatusUnauthorized},
		{name: "403", status: http.StatusForbidden},
		{name: "relay 508", status: http.StatusLoopDetected, nativeRelay: true},
		{name: "relay 502", status: http.StatusBadGateway, nativeRelay: true},
		{name: "relay timeout", err: context.DeadlineExceeded, nativeRelay: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &codexModelsHTTPUpstreamStub{err: tc.err}
			if tc.err == nil {
				stub.response = &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}
			}
			svc := &OpenAIGatewayService{httpUpstream: stub}
			_, err := svc.fetchCodexModelsManifestUpstream(context.Background(), codexModelsManifestRequest{
				url: "https://models.example.test/v1/models", headers: http.Header{}, useAPIKeyUpstream: true, nativeRelay: tc.nativeRelay,
			}, "")
			require.Error(t, err)
			require.Equal(t, tc.want, IsCodexModelsManifestStaleFallbackError(err))
			require.False(t, IsCodexModelsManifestStaleFallbackError(errors.New("plain")))
		})
	}
}

// A 304 only refreshes the age (same account, same ETag, newer fetch); it
// never deletes the entry.
func TestCodexModelsManifestFallback304OnlyRefreshes(t *testing.T) {
	var cache codexModelsManifestFallbackCache
	key := CodexModelsManifestFallbackKey{GroupID: 45}
	start := time.Unix(1_800_000_000, 0)
	later := start.Add(time.Hour)
	cache.remember(key, 19, codexModelsFallbackTestManifest(`{"models":[]}`, `"e1"`, start), start)
	cache.remember(key, 19, &CodexModelsManifest{ETag: `"e2"`, NotModified: true, fetchedAt: later}, later)
	cache.remember(key, 20, &CodexModelsManifest{ETag: `"e1"`, NotModified: true, fetchedAt: later}, later)
	cache.remember(key, 19, &CodexModelsManifest{NotModified: true, fetchedAt: later}, later)
	got, ok := cache.lookup(key, later)
	require.True(t, ok)
	require.Equal(t, start, got.StoredAt)

	cache.remember(key, 19, &CodexModelsManifest{ETag: `"e1"`, NotModified: true, fetchedAt: later}, later)
	// An older, out-of-order 304 does not move the age back.
	cache.remember(key, 19, &CodexModelsManifest{ETag: `"e1"`, NotModified: true, fetchedAt: start.Add(time.Minute)}, later)
	got, ok = cache.lookup(key, later)
	require.True(t, ok)
	require.Equal(t, later, got.StoredAt)

	// An older, out-of-order 200 does not replace a newer entry.
	cache.remember(key, 20, codexModelsFallbackTestManifest(`{"models":[{"slug":"old"}]}`, `"e0"`, start.Add(time.Minute)), later)
	got, ok = cache.lookup(key, later)
	require.True(t, ok)
	require.Equal(t, `{"models":[]}`, string(got.Body))
}

func TestCodexModelsManifestFallbackStoresCompactCopy(t *testing.T) {
	var cache codexModelsManifestFallbackCache
	key := CodexModelsManifestFallbackKey{GroupID: 45}
	now := time.Unix(1_800_000_000, 0)
	backing := make([]byte, 0, 4096)
	backing = append(backing, `{"models":[]}`...)
	cache.remember(key, 19, &CodexModelsManifest{Body: backing, fetchedAt: now}, now)
	backing[2] = 'X'
	got, ok := cache.lookup(key, now)
	require.True(t, ok)
	require.Equal(t, `{"models":[]}`, string(got.Body))
	require.Equal(t, len(got.Body), cap(got.Body))
}
