package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const codexModelsGroupCacheNewManifest = `{"models":[{"slug":"test-only-newer"}]}`

// codexModelsGroupCacheEnv drives the real handler with an intercepted
// upstream, a fake clock and a hook for background refreshes.
type codexModelsGroupCacheEnv struct {
	h        *OpenAIGatewayHandler
	svc      *service.OpenAIGatewayService
	calls    atomic.Int32
	mu       sync.Mutex
	respond  func(*http.Request) (*http.Response, error)
	requests []*http.Request
	now      atomic.Int64
	refresh  chan struct{}
}

func newCodexModelsGroupCacheEnv(t *testing.T, httpUpstream service.HTTPUpstream, accounts ...service.Account) *codexModelsGroupCacheEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	client, err := httpclient.GetClient(httpclient.Options{Timeout: 15 * time.Second, ResponseHeaderTimeout: 10 * time.Second})
	require.NoError(t, err)
	original := client.Transport
	t.Cleanup(func() { client.Transport = original })
	env := &codexModelsGroupCacheEnv{respond: codexModelsFallbackOK, refresh: make(chan struct{}, 16)}
	env.now.Store(time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC).UnixNano())
	client.Transport = codexModelsAdmissionTransport(func(r *http.Request) (*http.Response, error) {
		env.calls.Add(1)
		env.mu.Lock()
		env.requests = append(env.requests, r.Clone(context.Background()))
		respond := env.respond
		env.mu.Unlock()
		return respond(r)
	})
	if len(accounts) == 0 {
		accounts = []service.Account{codexModelsFallbackAccount(19)}
	}
	cfg := &config.Config{}
	env.svc = service.NewOpenAIGatewayService(&chatGPTAccountRepo{account: accounts[0], accounts: accounts}, nil, nil, nil, nil, nil, nil,
		cfg, nil, nil, nil, nil, nil, httpUpstream, nil, nil)
	env.svc.SetCodexModelsManifestClockForTest(func() time.Time { return time.Unix(0, env.now.Load()) })
	env.h = NewOpenAIGatewayHandler(env.svc, nil, nil, nil, nil, nil, nil, cfg, nil)
	env.h.codexModelsManifestRefreshDone = func() { env.refresh <- struct{}{} }
	return env
}

func (e *codexModelsGroupCacheEnv) setRespond(respond func(*http.Request) (*http.Response, error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.respond = respond
}

func (e *codexModelsGroupCacheEnv) lastRequest() *http.Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.requests[len(e.requests)-1]
}

func (e *codexModelsGroupCacheEnv) advance(d time.Duration) { e.now.Add(int64(d)) }

func (e *codexModelsGroupCacheEnv) waitRefresh(t *testing.T) {
	t.Helper()
	select {
	case <-e.refresh:
	case <-time.After(5 * time.Second):
		t.Fatal("background refresh did not finish")
	}
}

func (e *codexModelsGroupCacheEnv) requireNoRefresh(t *testing.T) {
	t.Helper()
	select {
	case <-e.refresh:
		t.Fatal("unexpected background refresh")
	default:
	}
}

func codexModelsGroupCacheBody(body, etag string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{
			"Content-Type":  {"application/json"},
			"Etag":          {etag},
			"Cache-Control": {"private, no-store"},
			"X-Request-Id":  {"TEST_ONLY_REQUEST_ID"},
		}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
}

func TestCodexModelsGroupCacheCoalescesConcurrentMisses(t *testing.T) {
	env := newCodexModelsGroupCacheEnv(t, nil)
	release := make(chan struct{})
	env.setRespond(func(r *http.Request) (*http.Response, error) {
		<-release
		return codexModelsFallbackOK(r)
	})
	const n = 10
	recs := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			recs[i], _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
		}(i)
	}
	require.Eventually(t, func() bool { return env.calls.Load() == 1 }, 5*time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond) // let the other requests join the flight
	close(release)
	wg.Wait()

	require.EqualValues(t, 1, env.calls.Load(), "concurrent cold requests share one upstream fetch")
	perRequestHeaders := 0
	for _, rec := range recs {
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, codexModelsFallbackManifest, rec.Body.String())
		require.Equal(t, `"TEST_ONLY_ETAG"`, rec.Header().Get("ETag"))
		require.Contains(t, []string{"miss", "hit"}, rec.Header().Get("X-Saiai-Manifest-Cache"))
		if rec.Header().Get("X-Test-Only-Hint") != "" {
			perRequestHeaders++
		}
	}
	require.Equal(t, 1, perRequestHeaders, "only the request that fetched gets the full upstream headers")
}

func TestCodexModelsGroupCacheFreshHitAndLocal304(t *testing.T) {
	env := newCodexModelsGroupCacheEnv(t, nil)
	// A cold request with a validator is fetched unconditionally and answered
	// locally.
	_, c := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{ifNoneMatch: `"TEST_ONLY_ETAG"`})
	require.Equal(t, http.StatusNotModified, c.Writer.Status())
	require.EqualValues(t, 1, env.calls.Load())
	require.Empty(t, env.lastRequest().Header.Get("If-None-Match"))

	env.advance(19 * time.Minute)
	rec, c := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, codexModelsFallbackManifest, rec.Body.String())
	require.Equal(t, "hit", rec.Header().Get("X-Saiai-Manifest-Cache"))
	require.Equal(t, `"TEST_ONLY_ETAG"`, rec.Header().Get("ETag"))
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	require.Empty(t, rec.Header().Get("X-Test-Only-Hint"), "per-request upstream headers are not replayed")
	require.Empty(t, rec.Header().Get("Date"))
	require.Empty(t, rec.Header().Get("X-Saiai-Manifest-Stale"))
	_, recorded := c.Get(service.OpsUpstreamErrorMessageKey)
	require.False(t, recorded)

	for _, inm := range []string{`"TEST_ONLY_ETAG"`, `W/"TEST_ONLY_ETAG"`, `"TEST_ONLY_OTHER", "TEST_ONLY_ETAG"`} {
		rec, c = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{ifNoneMatch: inm})
		require.Equal(t, http.StatusNotModified, c.Writer.Status(), inm)
		require.Empty(t, rec.Body.String())
		require.Equal(t, `"TEST_ONLY_ETAG"`, rec.Header().Get("ETag"))
	}
	rec, _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{ifNoneMatch: `"TEST_ONLY_OTHER"`})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, codexModelsFallbackManifest, rec.Body.String())
	require.EqualValues(t, 1, env.calls.Load(), "hits never contact upstream")
	env.requireNoRefresh(t)
}

func TestCodexModelsGroupCacheStaleWhileRevalidate(t *testing.T) {
	env := newCodexModelsGroupCacheEnv(t, nil)
	_, _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	env.advance(21 * time.Minute)

	release := make(chan struct{})
	env.setRespond(func(r *http.Request) (*http.Response, error) {
		<-release
		return codexModelsGroupCacheBody(codexModelsGroupCacheNewManifest, `"TEST_ONLY_NEW"`)(r)
	})
	// Several requests while the refresh runs: all get the old entry at
	// once, and only one refresh reaches upstream.
	for i := 0; i < 5; i++ {
		rec, _ := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, codexModelsFallbackManifest, rec.Body.String())
		require.Equal(t, "refresh", rec.Header().Get("X-Saiai-Manifest-Cache"))
		require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	}
	close(release)
	env.waitRefresh(t)
	require.EqualValues(t, 2, env.calls.Load())
	env.requireNoRefresh(t)

	rec, _ := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	require.Equal(t, codexModelsGroupCacheNewManifest, rec.Body.String(), "a successful refresh replaces the entry")
	require.Equal(t, "hit", rec.Header().Get("X-Saiai-Manifest-Cache"))
	require.Equal(t, `"TEST_ONLY_NEW"`, rec.Header().Get("ETag"))
	require.Empty(t, rec.Header().Get("X-Request-Id"))

	// A failed refresh keeps the entry and does not extend its age.
	env.advance(21 * time.Minute)
	env.setRespond(codexModelsFallbackBodyTimeout)
	rec, _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	require.Equal(t, codexModelsGroupCacheNewManifest, rec.Body.String())
	require.Equal(t, "refresh", rec.Header().Get("X-Saiai-Manifest-Cache"))
	env.waitRefresh(t)
	require.EqualValues(t, 3, env.calls.Load())
	rec, _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	require.Equal(t, codexModelsGroupCacheNewManifest, rec.Body.String())
	env.requireNoRefresh(t)
	require.EqualValues(t, 3, env.calls.Load(), "refresh retries are throttled after a failure")

	env.advance(61 * time.Second)
	_, _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	env.waitRefresh(t)
	require.EqualValues(t, 4, env.calls.Load())

	// The entry was fetched 21m after the start; past 6 h from that it is a
	// miss, and with upstream still failing the fallback has expired too.
	env.advance(6*time.Hour - 21*time.Minute - 61*time.Second + time.Second)
	rec, _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.Empty(t, rec.Header().Get("X-Saiai-Manifest-Cache"))
	require.EqualValues(t, 5, env.calls.Load())
}

func TestCodexModelsGroupCacheRefreshDoesNotUseFinishedRequest(t *testing.T) {
	env := newCodexModelsGroupCacheEnv(t, nil)
	_, _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	env.advance(30 * time.Minute)
	release := make(chan struct{})
	env.setRespond(func(r *http.Request) (*http.Response, error) {
		<-release
		return codexModelsGroupCacheBody(codexModelsGroupCacheNewManifest, `"TEST_ONLY_NEW"`)(r)
	})
	ctx, cancel := context.WithCancel(context.Background())
	rec, c := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{ctx: ctx, ifNoneMatch: `"TEST_ONLY_OTHER"`})
	require.Equal(t, codexModelsFallbackManifest, rec.Body.String())
	// The request is over: its context is canceled and the gin.Context is
	// cleared, as a pooled context would be reused.
	cancel()
	c.Request = nil
	c.Keys = nil
	close(release)
	env.waitRefresh(t)

	out := env.lastRequest()
	require.Empty(t, out.Header.Get("If-None-Match"), "the refresh is unconditional")
	require.Equal(t, "codex-tui/0.164.0", out.Header.Get("User-Agent"))
	require.Equal(t, "client_version=0.164.0", out.URL.RawQuery)
	rec, _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	require.Equal(t, codexModelsGroupCacheNewManifest, rec.Body.String())
}

func TestCodexModelsGroupCacheAppliesCurrentBlockedFilterOnHits(t *testing.T) {
	env := newCodexModelsGroupCacheEnv(t, nil)
	_, _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	blocked := &service.Group{ID: 45, Platform: service.PlatformOpenAI, BlockedModelPatterns: []string{"test-only-blocked"}}
	rec, _ := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{group: blocked, ifNoneMatch: `"TEST_ONLY_ETAG"`})
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"models":[{"slug":"test-only-new"}]}`, rec.Body.String())
	require.Empty(t, rec.Header().Get("ETag"))
	require.Empty(t, rec.Header().Get("Last-Modified"))
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "hit", rec.Header().Get("X-Saiai-Manifest-Cache"))

	rec, _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	require.Equal(t, codexModelsFallbackManifest, rec.Body.String(), "removing the filter takes effect at once")
	require.Equal(t, `"TEST_ONLY_ETAG"`, rec.Header().Get("ETag"))
	require.EqualValues(t, 1, env.calls.Load())
}

func TestCodexModelsGroupCacheIsolation(t *testing.T) {
	env := newCodexModelsGroupCacheEnv(t, nil)
	_, _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	cases := map[string]codexModelsFallbackRequest{
		"other group":          {group: &service.Group{ID: 46, Platform: service.PlatformOpenAI}},
		"other client_version": {query: "client_version=0.165.0"},
		"other query params":   {query: "client_version=0.164.0&future=1"},
		"non-official client":  {userAgent: "saiai_daemon_check/0.164.0", originator: "saiai_daemon_check"},
		"other originator":     {userAgent: "codex_cli_rs/0.164.0", originator: "codex_cli_rs"},
		"other version header": {version: "0.165.0"},
	}
	for name, req := range cases {
		before := env.calls.Load()
		rec, _ := serveCodexModelsFallback(env.h, req)
		require.Equal(t, http.StatusOK, rec.Code, name)
		require.Equal(t, "miss", rec.Header().Get("X-Saiai-Manifest-Cache"), name)
		require.Equal(t, before+1, env.calls.Load(), name)
	}
	rec, _ := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	require.Equal(t, "hit", rec.Header().Get("X-Saiai-Manifest-Cache"))
}

func TestCodexModelsGroupCacheDoesNotStoreAccountDecisions(t *testing.T) {
	env := newCodexModelsGroupCacheEnv(t, nil)
	env.setRespond(codexModelsFallbackStatus(http.StatusUnauthorized))
	for i := 0; i < 2; i++ {
		rec, _ := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
		require.Equal(t, http.StatusBadGateway, rec.Code)
	}
	require.EqualValues(t, 2, env.calls.Load(), "a 401 is never cached")

	// A refresh that meets a 401 follows the account switch loop.
	second := codexModelsFallbackAccount(20)
	second.Credentials = map[string]any{"access_token": "TEST_ONLY_SECOND_TOKEN", "chatgpt_account_id": "TEST_ONLY_SECOND_ACCOUNT"}
	env = newCodexModelsGroupCacheEnv(t, nil, codexModelsFallbackAccount(19), second)
	_, _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	env.advance(21 * time.Minute)
	env.setRespond(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "Bearer TEST_ONLY_SECOND_TOKEN" {
			return codexModelsGroupCacheBody(codexModelsGroupCacheNewManifest, `"TEST_ONLY_NEW"`)(r)
		}
		return codexModelsFallbackStatus(http.StatusUnauthorized)(r)
	})
	rec, _ := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	require.Equal(t, codexModelsFallbackManifest, rec.Body.String())
	env.waitRefresh(t)
	rec, _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	require.Equal(t, codexModelsGroupCacheNewManifest, rec.Body.String())
	require.Equal(t, "hit", rec.Header().Get("X-Saiai-Manifest-Cache"))
}

func TestCodexModelsGroupCacheWaiterCancel(t *testing.T) {
	env := newCodexModelsGroupCacheEnv(t, nil)
	release := make(chan struct{})
	env.setRespond(func(r *http.Request) (*http.Response, error) {
		<-release
		return codexModelsFallbackOK(r)
	})
	leaderDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec, _ := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
		leaderDone <- rec
	}()
	require.Eventually(t, func() bool { return env.calls.Load() == 1 }, 5*time.Second, time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec, _ := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{ctx: ctx})
		waiterDone <- rec
	}()
	time.Sleep(20 * time.Millisecond) // let the waiter join
	cancel()
	select {
	case rec := <-waiterDone:
		require.Empty(t, rec.Body.String(), "a canceled waiter returns without waiting for the leader")
	case <-time.After(5 * time.Second):
		t.Fatal("canceled waiter kept waiting")
	}
	close(release)
	rec := <-leaderDone
	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 1, env.calls.Load())
}

func TestCodexModelsGroupCacheLeaderCancelDoesNotFailWaiters(t *testing.T) {
	env := newCodexModelsGroupCacheEnv(t, nil)
	var attempts atomic.Int32
	env.setRespond(func(r *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			// The leader's fetch ends with its client.
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return codexModelsFallbackOK(r)
	})
	ctx, cancel := context.WithCancel(context.Background())
	leaderDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec, _ := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{ctx: ctx})
		leaderDone <- rec
	}()
	require.Eventually(t, func() bool { return env.calls.Load() == 1 }, 5*time.Second, time.Millisecond)
	waiterDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec, _ := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
		waiterDone <- rec
	}()
	time.Sleep(20 * time.Millisecond) // let the waiter join
	cancel()
	require.Empty(t, (<-leaderDone).Body.String())
	select {
	case rec := <-waiterDone:
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, codexModelsFallbackManifest, rec.Body.String())
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not recover from the leader's cancel")
	}
	require.EqualValues(t, 2, env.calls.Load())
}

func TestCodexModelsGroupCacheExcludesRelayAndAPIKeyGroups(t *testing.T) {
	newHTTPUpstream := func() *codexModelsFallbackHTTPUpstream {
		return &codexModelsFallbackHTTPUpstream{respond: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Etag": {`"TEST_ONLY_KEY"`}},
				Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"test-only-api-key"}]}`))}, nil
		}}
	}
	apiKey := service.Account{ID: 30, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "TEST_ONLY_API_KEY", "base_url": "https://models.example.test/v1"}}
	relay := apiKey
	relay.ID = 31
	relay.Extra = map[string]any{"openai_upstream_protocol": service.OpenAIUpstreamProtocolCodexNativeRelayV1}

	// API-key upstream: its own 30 s cache still answers the second request.
	upstream := newHTTPUpstream()
	env := newCodexModelsGroupCacheEnv(t, upstream, apiKey)
	for i := 0; i < 2; i++ {
		rec, _ := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Empty(t, rec.Header().Get("X-Saiai-Manifest-Cache"))
	}
	require.Len(t, upstream.requests, 1)

	// Native relay: every request goes to the relay, as before.
	upstream = newHTTPUpstream()
	env = newCodexModelsGroupCacheEnv(t, upstream, relay)
	for i := 0; i < 2; i++ {
		rec, _ := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Empty(t, rec.Header().Get("X-Saiai-Manifest-Cache"))
	}
	require.Len(t, upstream.requests, 2)

	// A group mixing OAuth and relay accounts keeps the uncached path.
	upstream = newHTTPUpstream()
	env = newCodexModelsGroupCacheEnv(t, upstream, codexModelsFallbackAccount(19), relay)
	for i := 0; i < 4; i++ {
		rec, _ := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Empty(t, rec.Header().Get("X-Saiai-Manifest-Cache"))
	}
	require.Equal(t, 4, len(upstream.requests)+int(env.calls.Load()))
}

// With the group cache on, an upstream outage after the fresh window is
// answered from the entry at once (no 15 s wait), and the failed refresh
// keeps it.
func TestCodexModelsGroupCacheServesDuringOutage(t *testing.T) {
	env := newCodexModelsGroupCacheEnv(t, nil)
	_, _ = serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
	env.advance(2 * time.Hour)
	env.setRespond(codexModelsFallbackBodyTimeout)
	for i := 0; i < 2; i++ {
		rec, c := serveCodexModelsFallback(env.h, codexModelsFallbackRequest{})
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, codexModelsFallbackManifest, rec.Body.String())
		require.Equal(t, "refresh", rec.Header().Get("X-Saiai-Manifest-Cache"))
		_, recorded := c.Get(service.OpsUpstreamErrorMessageKey)
		require.False(t, recorded, "the request itself did not meet the upstream error")
		if i == 0 {
			env.waitRefresh(t)
		}
	}
	require.EqualValues(t, 2, env.calls.Load())
}
