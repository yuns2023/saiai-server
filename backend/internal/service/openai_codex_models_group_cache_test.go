//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func codexModelsGroupCacheTestManifest(body, etag string, fetchedAt time.Time) *CodexModelsManifest {
	return &CodexModelsManifest{Body: []byte(body), ETag: etag, fetchedAt: fetchedAt, groupCacheable: true,
		Headers: http.Header{
			"Etag":          {etag},
			"Content-Type":  {"application/json"},
			"Cache-Control": {"private, no-store"},
			"X-Request-Id":  {"TEST_ONLY_REQUEST_ID"},
			"Set-Cookie":    {"TEST_ONLY_COOKIE=1"},
			"Date":          {"Mon, 01 Jan 2024 00:00:00 GMT"},
			"Cf-Ray":        {"TEST_ONLY_RAY"},
		}}
}

func TestCodexModelsManifestGroupCacheStates(t *testing.T) {
	var cache codexModelsManifestFallbackCache
	key := CodexModelsManifestFallbackKey{GroupID: 45, RawQuery: "client_version=0.164.0", Official: true, Originator: "codex-tui"}
	start := time.Unix(1_800_000_000, 0)
	cache.remember(key, 19, codexModelsGroupCacheTestManifest(`{"models":[]}`, `"e1"`, start), start)

	entry, state, refresh := cache.lookupGroupCache(key, start.Add(codexModelsManifestFreshTTL-time.Second))
	require.Equal(t, CodexModelsManifestGroupCacheFresh, state)
	require.False(t, refresh)
	require.Equal(t, http.Header{"Etag": {`"e1"`}, "Content-Type": {"application/json"}, "Cache-Control": {"private, no-store"}}, entry.Headers,
		"only entry-level headers are stored")

	_, state, refresh = cache.lookupGroupCache(key, start.Add(codexModelsManifestFreshTTL))
	require.Equal(t, CodexModelsManifestGroupCacheRevalidate, state)
	require.True(t, refresh)

	// A failed refresh throttles retries but does not extend the age.
	failedAt := start.Add(time.Hour)
	cache.markRefreshFailed(key, start, failedAt)
	_, state, refresh = cache.lookupGroupCache(key, failedAt.Add(codexModelsManifestRefreshRetryAfter-time.Second))
	require.Equal(t, CodexModelsManifestGroupCacheRevalidate, state)
	require.False(t, refresh)
	_, _, refresh = cache.lookupGroupCache(key, failedAt.Add(codexModelsManifestRefreshRetryAfter))
	require.True(t, refresh)
	_, state, _ = cache.lookupGroupCache(key, start.Add(codexModelsManifestFallbackMaxAge+time.Second))
	require.Equal(t, CodexModelsManifestGroupCacheMiss, state)
	_, ok := cache.lookup(key, start)
	require.False(t, ok, "expired entries are deleted")
}

func TestCodexModelsManifestGroupCacheOnlyServesOAuthEntries(t *testing.T) {
	var cache codexModelsManifestFallbackCache
	key := CodexModelsManifestFallbackKey{GroupID: 45}
	start := time.Unix(1_800_000_000, 0)
	plain := codexModelsGroupCacheTestManifest(`{"models":[]}`, `"e1"`, start)
	plain.groupCacheable = false
	cache.remember(key, 30, plain, start)
	_, state, _ := cache.lookupGroupCache(key, start)
	require.Equal(t, CodexModelsManifestGroupCacheMiss, state)
	_, ok := cache.lookup(key, start)
	require.True(t, ok, "the stale fallback still has it")

	// RememberCodexModelsManifestFallback never creates a hit.
	svc := &OpenAIGatewayService{codexModelsManifestClock: func() time.Time { return start.Add(time.Second) }}
	svc.RememberCodexModelsManifestFallback(key, 19, codexModelsGroupCacheTestManifest(`{"models":[1]}`, `"e2"`, start.Add(time.Second)))
	_, state, _ = svc.LookupCodexModelsManifestGroupCache(key)
	require.Equal(t, CodexModelsManifestGroupCacheMiss, state)
	svc.codexModelsManifestClock = func() time.Time { return start.Add(2 * time.Second) }
	svc.RememberCodexModelsManifestGroupCache(key, 19, codexModelsGroupCacheTestManifest(`{"models":[2]}`, `"e3"`, start.Add(2*time.Second)))
	entry, state, _ := svc.LookupCodexModelsManifestGroupCache(key)
	require.Equal(t, CodexModelsManifestGroupCacheFresh, state)
	require.Equal(t, `{"models":[2]}`, string(entry.Body))

	// Forget keeps the entry for the fallback only.
	svc.ForgetCodexModelsManifestGroupCache(key, entry.StoredAt)
	_, state, _ = svc.LookupCodexModelsManifestGroupCache(key)
	require.Equal(t, CodexModelsManifestGroupCacheMiss, state)
	_, ok = svc.LookupCodexModelsManifestFallback(key)
	require.True(t, ok)

	svc.codexModelsManifestGroupCacheOff = true
	require.False(t, svc.CodexModelsManifestGroupCacheEligible(context.Background(), nil))
}

func TestCodexModelsManifestGroupCacheHeadersCountInBudget(t *testing.T) {
	key := CodexModelsManifestFallbackKey{GroupID: 1}
	withHeaders := CodexModelsManifestFallback{Body: []byte("x"), Headers: http.Header{"Content-Type": {"application/json"}}}
	require.Equal(t, codexModelsManifestFallbackEntrySize(key, CodexModelsManifestFallback{Body: []byte("x")})+len("Content-Type")+len("application/json"),
		codexModelsManifestFallbackEntrySize(key, withHeaders))
}

func TestCodexModelsManifestGroupCacheEligibility(t *testing.T) {
	oauth := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}
	apiKey := Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	relay := apiKey
	relay.Extra = map[string]any{"openai_upstream_protocol": OpenAIUpstreamProtocolCodexNativeRelayV1}
	groupID := int64(45)
	for name, tc := range map[string]struct {
		accounts []Account
		want     bool
	}{
		"oauth only": {[]Account{oauth}, true},
		"with key":   {[]Account{oauth, apiKey}, false},
		"with relay": {[]Account{oauth, relay}, false},
		"empty":      {[]Account{}, false},
	} {
		svc := &OpenAIGatewayService{accountRepo: codexModelsGroupCacheTestRepo{accounts: tc.accounts}}
		require.Equal(t, tc.want, svc.CodexModelsManifestGroupCacheEligible(context.Background(), &groupID), name)
	}
}

type codexModelsGroupCacheTestRepo struct {
	AccountRepository
	accounts []Account
}

func (r codexModelsGroupCacheTestRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	return r.accounts, nil
}
