//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type modelsCatalogRecordingUpstream struct {
	requests  []*http.Request
	accounts  []int64
	responses []func(req *http.Request) (*http.Response, error)
}

func (u *modelsCatalogRecordingUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, accountConcurrency, false)
}

func (u *modelsCatalogRecordingUpstream) DoWithTLS(req *http.Request, _ string, accountID int64, _ int, _ bool) (*http.Response, error) {
	u.requests = append(u.requests, req)
	u.accounts = append(u.accounts, accountID)
	idx := len(u.requests) - 1
	if idx >= len(u.responses) {
		idx = len(u.responses) - 1
	}
	return u.responses[idx](req)
}

func modelsCatalogJSON(status int, body string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
}

func modelsCatalogIDs(models []ModelsCatalogEntry) []string {
	ids := make([]string, len(models))
	for i, model := range models {
		ids[i] = model.ID
	}
	return ids
}

func TestModelsCatalogCacheServesStaleCopyAfterFailedRefresh(t *testing.T) {
	var cache modelsCatalogCache
	calls := 0
	fetch := func(context.Context) ([]ModelsCatalogEntry, error) {
		calls++
		if calls == 1 {
			return []ModelsCatalogEntry{{ID: "a"}}, nil
		}
		return nil, errors.New("upstream down")
	}
	models, err := cache.get(context.Background(), "k", fetch)
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, modelsCatalogIDs(models))

	// Fresh copy: no refetch.
	_, err = cache.get(context.Background(), "k", fetch)
	require.NoError(t, err)
	require.Equal(t, 1, calls)

	// Expired copy + failing refresh keeps serving the last good catalog.
	cache.entries["k"].fetchedAt = time.Now().Add(-modelsCatalogTTL - time.Second)
	models, err = cache.get(context.Background(), "k", fetch)
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, modelsCatalogIDs(models))
	require.Equal(t, 2, calls)

	// Within the failure backoff no new upstream call is made.
	_, err = cache.get(context.Background(), "k", fetch)
	require.NoError(t, err)
	require.Equal(t, 2, calls)

	// Past the stale window the copy is dropped.
	cache.entries["k"].staleUntil = time.Now().Add(-time.Second)
	cache.entries["k"].failedAt = time.Time{}
	_, err = cache.get(context.Background(), "k", fetch)
	require.Error(t, err)
	_, err = cache.get(context.Background(), "k", fetch)
	require.ErrorIs(t, err, ErrModelsCatalogUnavailable)
	require.Equal(t, 3, calls)
}

func TestModelsCatalogCacheRefreshSurvivesCallerCancel(t *testing.T) {
	var cache modelsCatalogCache
	release := make(chan struct{})
	done := make(chan struct{})
	fetch := func(ctx context.Context) ([]ModelsCatalogEntry, error) {
		defer close(done)
		<-release
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return []ModelsCatalogEntry{{ID: "a"}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cache.get(ctx, "k", fetch)
	require.ErrorIs(t, err, context.Canceled)
	close(release)
	<-done
	require.Eventually(t, func() bool {
		models, _ := cache.lookup("k", time.Now())
		return len(models) == 1
	}, time.Second, 10*time.Millisecond)
}

func newAnthropicModelsCatalogService(upstream HTTPUpstream, accounts ...Account) *GatewayService {
	return &GatewayService{accountRepo: &mockAccountRepoForPlatform{accounts: accounts}, httpUpstream: upstream}
}

func TestListAnthropicModelsPagesOfficialCatalogFromOAuthAccounts(t *testing.T) {
	upstream := &modelsCatalogRecordingUpstream{responses: []func(*http.Request) (*http.Response, error){
		modelsCatalogJSON(http.StatusOK, `{"data":[{"type":"model","id":"claude-opus-5-5","display_name":"Claude Opus 5.5","extra":{"k":1}},{"type":"model","id":"claude-sonnet-5-5"}],"has_more":true,"first_id":"claude-opus-5-5","last_id":"claude-sonnet-5-5"}`),
		modelsCatalogJSON(http.StatusOK, `{"data":[{"type":"model","id":"claude-haiku-5-5"},{"type":"model","id":"claude-sonnet-5-5"}],"has_more":false}`),
	}}
	groupID := int64(7)
	svc := newAnthropicModelsCatalogService(upstream,
		Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 0, Credentials: map[string]any{"api_key": "sk-relay", "base_url": "https://other.example"}},
		Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive, Schedulable: true, Priority: 1, Credentials: map[string]any{"access_token": "sk-ant-oat-test"}},
	)

	models, err := svc.ListAnthropicModels(context.Background(), &groupID)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}, modelsCatalogIDs(models))
	require.JSONEq(t, `{"type":"model","id":"claude-opus-5-5","display_name":"Claude Opus 5.5","extra":{"k":1}}`, string(models[0].Raw))

	require.Len(t, upstream.requests, 2)
	require.Equal(t, []int64{2, 2}, upstream.accounts, "API-key relay accounts are never a catalog source")
	first := upstream.requests[0]
	require.Equal(t, http.MethodGet, first.Method)
	require.Equal(t, "api.anthropic.com", first.URL.Host)
	require.Equal(t, "/v1/models", first.URL.Path)
	require.Equal(t, "true", first.URL.Query().Get("beta"))
	require.Equal(t, "1000", first.URL.Query().Get("limit"))
	require.Empty(t, first.URL.Query().Get("after_id"))
	require.Equal(t, "Bearer sk-ant-oat-test", first.Header.Get("Authorization"))
	require.Equal(t, "oauth-2025-04-20", first.Header.Get("anthropic-beta"))
	require.Equal(t, "2023-06-01", first.Header.Get("anthropic-version"))
	require.Contains(t, first.Header.Get("User-Agent"), "claude-cli/")
	require.Empty(t, first.Header.Get("x-api-key"))
	require.Nil(t, first.Body)
	require.Equal(t, "claude-sonnet-5-5", upstream.requests[1].URL.Query().Get("after_id"))

	// Second call within the TTL is served from cache.
	_, err = svc.ListAnthropicModels(context.Background(), &groupID)
	require.NoError(t, err)
	require.Len(t, upstream.requests, 2)
}

func TestListAnthropicModelsFallsThroughFailingAccounts(t *testing.T) {
	upstream := &modelsCatalogRecordingUpstream{responses: []func(*http.Request) (*http.Response, error){
		modelsCatalogJSON(http.StatusForbidden, `{"type":"error","error":{"type":"permission_error","message":"nope"}}`),
		modelsCatalogJSON(http.StatusOK, `<html>oops</html>`),
		modelsCatalogJSON(http.StatusOK, `{"data":[{"id":"claude-opus-5-5"}],"has_more":false}`),
	}}
	groupID := int64(8)
	accounts := []Account{}
	for id := int64(1); id <= 4; id++ {
		accounts = append(accounts, Account{ID: id, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "t"}})
	}
	svc := newAnthropicModelsCatalogService(upstream, accounts...)
	models, err := svc.ListAnthropicModels(context.Background(), &groupID)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-opus-5-5"}, modelsCatalogIDs(models))
	require.Equal(t, []int64{1, 2, 3}, upstream.accounts)
}

func TestListAnthropicModelsWithoutOAuthSourceIsUnavailable(t *testing.T) {
	upstream := &modelsCatalogRecordingUpstream{responses: []func(*http.Request) (*http.Response, error){modelsCatalogJSON(http.StatusOK, `{"data":[]}`)}}
	groupID := int64(9)
	svc := newAnthropicModelsCatalogService(upstream,
		Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "k"}},
	)
	_, err := svc.ListAnthropicModels(context.Background(), &groupID)
	require.ErrorIs(t, err, ErrModelsCatalogUnavailable)
	require.Empty(t, upstream.requests)
}

func TestListAnthropicModelsRejectsRepeatedCursor(t *testing.T) {
	upstream := &modelsCatalogRecordingUpstream{responses: []func(*http.Request) (*http.Response, error){
		modelsCatalogJSON(http.StatusOK, `{"data":[{"id":"a"}],"has_more":true,"last_id":"a"}`),
		modelsCatalogJSON(http.StatusOK, `{"data":[{"id":"b"}],"has_more":true,"last_id":"b"}`),
		modelsCatalogJSON(http.StatusOK, `{"data":[{"id":"c"}],"has_more":true,"last_id":"a"}`),
	}}
	groupID := int64(10)
	svc := newAnthropicModelsCatalogService(upstream,
		Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "t"}},
	)
	_, err := svc.ListAnthropicModels(context.Background(), &groupID)
	require.Error(t, err)
	require.Len(t, upstream.requests, 3, "a cursor seen before stops paging")
}

func TestListAnthropicModelsAcceptsEmptyCatalog(t *testing.T) {
	upstream := &modelsCatalogRecordingUpstream{responses: []func(*http.Request) (*http.Response, error){
		modelsCatalogJSON(http.StatusOK, `{"data":[],"has_more":false}`),
	}}
	groupID := int64(11)
	svc := newAnthropicModelsCatalogService(upstream,
		Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "t"}},
	)
	models, err := svc.ListAnthropicModels(context.Background(), &groupID)
	require.NoError(t, err)
	require.NotNil(t, models)
	require.Empty(t, models)
}

func TestListAnthropicModelsRotatesPastFailedSources(t *testing.T) {
	upstream := &modelsCatalogRecordingUpstream{responses: []func(*http.Request) (*http.Response, error){
		modelsCatalogJSON(http.StatusUnauthorized, `{}`),
		modelsCatalogJSON(http.StatusUnauthorized, `{}`),
		modelsCatalogJSON(http.StatusUnauthorized, `{}`),
		modelsCatalogJSON(http.StatusOK, `{"data":[{"id":"claude-opus-5-5"}],"has_more":false}`),
	}}
	groupID := int64(12)
	accounts := []Account{}
	for id := int64(1); id <= 4; id++ {
		accounts = append(accounts, Account{ID: id, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "t"}})
	}
	svc := newAnthropicModelsCatalogService(upstream, accounts...)
	_, err := svc.ListAnthropicModels(context.Background(), &groupID)
	require.Error(t, err)
	require.Equal(t, []int64{1, 2, 3}, upstream.accounts)

	// The next refresh (after the cache backoff) skips the failed sources.
	svc.anthropicModels.entries["anthropic:12"].failedAt = time.Now().Add(-modelsCatalogRetryBackoff - time.Second)
	models, err := svc.ListAnthropicModels(context.Background(), &groupID)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-opus-5-5"}, modelsCatalogIDs(models))
	require.Equal(t, []int64{1, 2, 3, 4}, upstream.accounts)
}

func TestModelsCatalogCacheRejectsOversizedCatalog(t *testing.T) {
	var cache modelsCatalogCache
	big := ModelsCatalogEntry{ID: "a", Raw: make([]byte, modelsCatalogMaxBytes)}
	_, err := cache.get(context.Background(), "k", func(context.Context) ([]ModelsCatalogEntry, error) {
		return []ModelsCatalogEntry{big}, nil
	})
	require.Error(t, err)
}

func TestOpenAIModelsFromCodexManifest(t *testing.T) {
	models, err := openAIModelsFromCodexManifest([]byte(`{"models":[{"slug":"gpt-6.1-sol","display_name":"GPT-6.1 Sol","visibility":"list"},{"slug":"codex-auto-review","visibility":"hide"},{"slug":"gpt-6.1-sol"},{"slug":""}]}`))
	require.NoError(t, err)
	ids := modelsCatalogIDs(models)
	require.Equal(t, []string{"gpt-6.1-sol", "codex-auto-review"}, ids[:2])
	require.Contains(t, ids, "gpt-image-2")
	require.JSONEq(t, `{"id":"gpt-6.1-sol","object":"model","created":0,"owned_by":"openai","type":"model","display_name":"GPT-6.1 Sol"}`, string(models[0].Raw))

	models, err = openAIModelsFromCodexManifest([]byte(`{"models":[]}`))
	require.NoError(t, err, "an empty manifest is a valid catalog")
	require.Equal(t, []string{"gpt-image-2"}, modelsCatalogIDs(models))
	_, err = openAIModelsFromCodexManifest([]byte(`{"other":[]}`))
	require.Error(t, err)
	_, err = openAIModelsFromCodexManifest([]byte(`not json`))
	require.Error(t, err)
}

func TestObserveCodexClientVersionRaisesCatalogVersion(t *testing.T) {
	codexModelsCatalogVersion.mu.Lock()
	savedVersion, savedSet := codexModelsCatalogVersion.version, codexModelsCatalogVersion.set
	codexModelsCatalogVersion.set = false
	codexModelsCatalogVersion.mu.Unlock()
	t.Cleanup(func() {
		codexModelsCatalogVersion.mu.Lock()
		codexModelsCatalogVersion.version, codexModelsCatalogVersion.set = savedVersion, savedSet
		codexModelsCatalogVersion.mu.Unlock()
	})

	require.Equal(t, codexModelsCatalogBaselineVersion, currentCodexModelsCatalogVersion())
	ObserveCodexClientVersion("codex_cli_rs/0.100.0 (Linux)", "codex_cli_rs", "0.100.0")
	require.Equal(t, codexModelsCatalogBaselineVersion, currentCodexModelsCatalogVersion(), "older clients never lower the baseline")
	ObserveCodexClientVersion("codex_cli_rs/0.180.0 (Linux)", "codex_cli_rs", "0.180.0")
	require.Equal(t, "0.180.0", currentCodexModelsCatalogVersion())
	ObserveCodexClientVersion("codex_cli_rs/0.190.0-alpha.1 (Linux)", "codex_cli_rs", "0.190.0-alpha.1")
	ObserveCodexClientVersion("curl/8.0", "", "0.200.0")
	ObserveCodexClientVersion("codex_cli_rs/0.999.0 (Linux)", "codex_cli_rs", "0.999.0")
	ObserveCodexClientVersion("codex_cli_rs/1.0.0 (Linux)", "codex_cli_rs", "1.0.0")
	require.Equal(t, "0.180.0", currentCodexModelsCatalogVersion(), "implausible versions are ignored")
}
