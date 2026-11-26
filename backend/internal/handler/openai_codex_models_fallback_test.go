package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const codexModelsFallbackManifest = `{"models":[{"slug":"test-only-new"},{"slug":"test-only-blocked"}]}`

type codexModelsFallbackUpstream struct {
	respond func(r *http.Request) (*http.Response, error)
	calls   int
	repo    *codexModelsFallbackRepo
}

// codexModelsFallbackRepo can fail account listing after a number of calls.
type codexModelsFallbackRepo struct {
	*chatGPTAccountRepo
	listCalls int
	failFrom  int
}

func (r *codexModelsFallbackRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]service.Account, error) {
	r.listCalls++
	if r.failFrom > 0 && r.listCalls >= r.failFrom {
		return nil, errors.New("TEST_ONLY_REPO_FAILURE")
	}
	return r.chatGPTAccountRepo.ListSchedulableByGroupIDAndPlatform(ctx, groupID, platform)
}

type codexModelsFallbackErrReader struct{ err error }

func (r codexModelsFallbackErrReader) Read([]byte) (int, error) { return 0, r.err }

func codexModelsFallbackOK(r *http.Request) (*http.Response, error) {
	if r.Header.Get("If-None-Match") == `"TEST_ONLY_ETAG"` {
		return &http.Response{StatusCode: http.StatusNotModified, Header: http.Header{"Etag": {`"TEST_ONLY_ETAG"`}}, Body: http.NoBody}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{
		"Content-Type":     {"application/json"},
		"Etag":             {`"TEST_ONLY_ETAG"`},
		"Cache-Control":    {"private, no-store"},
		"X-Test-Only-Hint": {"native"},
		"Date":             {"Mon, 01 Jan 2024 00:00:00 GMT"},
	}, Body: io.NopCloser(strings.NewReader(codexModelsFallbackManifest))}, nil
}

// codexModelsFallbackBodyTimeout mirrors production: headers arrive, then the
// body read hits the request deadline.
func codexModelsFallbackBodyTimeout(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(codexModelsFallbackErrReader{err: context.DeadlineExceeded})}, nil
}

func codexModelsFallbackStatus(status int) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
}

func codexModelsFallbackAccount(id int64) service.Account {
	return service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"access_token": "TEST_ONLY_UPSTREAM_TOKEN", "chatgpt_account_id": "TEST_ONLY_UPSTREAM_ACCOUNT"}}
}

func newCodexModelsFallbackHandler(t *testing.T, accounts ...service.Account) (*OpenAIGatewayHandler, *codexModelsFallbackUpstream) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	client, err := httpclient.GetClient(httpclient.Options{Timeout: 15 * time.Second, ResponseHeaderTimeout: 10 * time.Second})
	require.NoError(t, err)
	original := client.Transport
	t.Cleanup(func() { client.Transport = original })
	upstream := &codexModelsFallbackUpstream{respond: codexModelsFallbackOK}
	client.Transport = codexModelsAdmissionTransport(func(r *http.Request) (*http.Response, error) {
		upstream.calls++
		return upstream.respond(r)
	})
	if len(accounts) == 0 {
		accounts = []service.Account{codexModelsFallbackAccount(19)}
	}
	cfg := &config.Config{}
	upstream.repo = &codexModelsFallbackRepo{chatGPTAccountRepo: &chatGPTAccountRepo{account: accounts[0], accounts: accounts}}
	svc := service.NewOpenAIGatewayService(upstream.repo, nil, nil, nil, nil, nil, nil,
		cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	// These tests exercise the stale-on-error fallback directly; with the
	// group cache an OAuth entry younger than its max age is served before
	// upstream is asked (see openai_codex_models_group_cache_test.go).
	svc.DisableCodexModelsManifestGroupCacheForTest()
	return NewOpenAIGatewayHandler(svc, nil, nil, nil, nil, nil, nil, cfg, nil), upstream
}

type codexModelsFallbackRequest struct {
	group       *service.Group
	query       string
	userAgent   string
	originator  string
	ifNoneMatch string
	version     string
	ctx         context.Context
	extra       http.Header
}

func serveCodexModelsFallback(h *OpenAIGatewayHandler, req codexModelsFallbackRequest) (*httptest.ResponseRecorder, *gin.Context) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	query := req.query
	if query == "" {
		query = "client_version=0.164.0"
	}
	c.Request = httptest.NewRequest(http.MethodGet, "/backend-api/codex/models?"+query, nil)
	if req.ctx != nil {
		c.Request = c.Request.WithContext(req.ctx)
	}
	ua, originator := req.userAgent, req.originator
	if ua == "" {
		ua, originator = "codex-tui/0.164.0", "codex-tui"
	}
	c.Request.Header.Set("User-Agent", ua)
	c.Request.Header.Set("Originator", originator)
	if req.version != "" {
		c.Request.Header.Set("Version", req.version)
	}
	if req.ifNoneMatch != "" {
		c.Request.Header.Set("If-None-Match", req.ifNoneMatch)
	}
	for name, values := range req.extra {
		c.Request.Header[name] = values
	}
	group := req.group
	if group == nil {
		group = &service.Group{ID: 45, Platform: service.PlatformOpenAI}
	}
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 3, GroupID: &group.ID, Group: group})
	h.CodexModels(c)
	return rec, c
}

func requireCodexModelsRecovered(t *testing.T, c *gin.Context, wantStatus int) {
	t.Helper()
	msg, ok := c.Get(service.OpsUpstreamErrorMessageKey)
	require.True(t, ok, "stale serve must leave a recovered upstream error for ops")
	require.Contains(t, msg, "served from stale fallback")
	status, _ := c.Get(service.OpsUpstreamStatusCodeKey)
	if wantStatus > 0 {
		require.Equal(t, wantStatus, status)
	} else {
		require.Nil(t, status)
	}
}

func TestCodexModelsStaleFallbackAfterSingleAccountTimeout(t *testing.T) {
	h, upstream := newCodexModelsFallbackHandler(t)
	rec, _ := serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, rec.Header().Get("X-Saiai-Manifest-Stale"))

	upstream.respond = codexModelsFallbackBodyTimeout
	upstream.calls = 0
	rec, c := serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, codexModelsFallbackManifest, rec.Body.String())
	require.Equal(t, "1", rec.Header().Get("X-Saiai-Manifest-Stale"))
	require.Equal(t, "stale", rec.Header().Get("X-Saiai-Manifest-Cache"))
	require.Equal(t, `"TEST_ONLY_ETAG"`, rec.Header().Get("ETag"))
	require.Empty(t, rec.Header().Get("X-Test-Only-Hint"), "upstream headers of the original answer are not replayed")
	require.Empty(t, rec.Header().Get("Date"))
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, 1, upstream.calls, "the single account is still tried first")
	requireCodexModelsRecovered(t, c, 0)

	// Transport failures and upstream 5xx/429 behave the same way.
	for _, respond := range []func(*http.Request) (*http.Response, error){
		func(*http.Request) (*http.Response, error) { return nil, errors.New("TEST_ONLY_DIAL_FAILURE") },
		codexModelsFallbackStatus(http.StatusBadGateway),
		codexModelsFallbackStatus(http.StatusTooManyRequests),
	} {
		upstream.respond = respond
		rec, _ = serveCodexModelsFallback(h, codexModelsFallbackRequest{})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, codexModelsFallbackManifest, rec.Body.String())
	}
	upstream.respond = codexModelsFallbackStatus(http.StatusServiceUnavailable)
	_, c = serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	requireCodexModelsRecovered(t, c, http.StatusServiceUnavailable)
}

func TestCodexModelsStaleFallbackNeverSucceededKeeps502(t *testing.T) {
	h, upstream := newCodexModelsFallbackHandler(t)
	upstream.respond = codexModelsFallbackBodyTimeout
	rec, _ := serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "read Codex models manifest response")
	require.Empty(t, rec.Header().Get("X-Saiai-Manifest-Stale"))
}

func TestCodexModelsStaleFallbackDoesNotMaskAccountDecisions(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		h, upstream := newCodexModelsFallbackHandler(t)
		rec, _ := serveCodexModelsFallback(h, codexModelsFallbackRequest{})
		require.Equal(t, http.StatusOK, rec.Code)
		upstream.respond = codexModelsFallbackStatus(status)
		rec, _ = serveCodexModelsFallback(h, codexModelsFallbackRequest{})
		require.Equal(t, http.StatusBadGateway, rec.Code, "status %d", status)
		require.Contains(t, rec.Body.String(), "upstream returned status")
		require.Empty(t, rec.Header().Get("X-Saiai-Manifest-Stale"))
	}

	// An invalid manifest is not upstream unavailability either.
	h, upstream := newCodexModelsFallbackHandler(t)
	_, _ = serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	upstream.respond = func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`<html>`))}, nil
	}
	rec, _ := serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	require.Equal(t, http.StatusBadGateway, rec.Code)
}

// One account answering 401 and the next timing out still surfaces the error:
// the fallback needs every attempt to be upstream unavailability.
func TestCodexModelsStaleFallbackMixedAttemptsSurfaceError(t *testing.T) {
	h, upstream := newCodexModelsFallbackHandler(t, codexModelsFallbackAccount(19), codexModelsFallbackAccount(20))
	rec, _ := serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	require.Equal(t, http.StatusOK, rec.Code)
	upstream.calls = 0
	upstream.respond = func(r *http.Request) (*http.Response, error) {
		if upstream.calls == 1 {
			return codexModelsFallbackStatus(http.StatusUnauthorized)(r)
		}
		return codexModelsFallbackBodyTimeout(r)
	}
	rec, _ = serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	require.Equal(t, 2, upstream.calls)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.Empty(t, rec.Header().Get("X-Saiai-Manifest-Stale"))
}

func TestCodexModelsStaleFallbackIfNoneMatch(t *testing.T) {
	h, upstream := newCodexModelsFallbackHandler(t)
	_, _ = serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	// A 304 for the stored ETag keeps the entry; then the upstream fails.
	// gin only commits c.Status at the end of the engine, so read the writer.
	_, c := serveCodexModelsFallback(h, codexModelsFallbackRequest{ifNoneMatch: `"TEST_ONLY_ETAG"`})
	require.Equal(t, http.StatusNotModified, c.Writer.Status())
	upstream.respond = codexModelsFallbackBodyTimeout

	rec, c := serveCodexModelsFallback(h, codexModelsFallbackRequest{ifNoneMatch: `W/"TEST_ONLY_ETAG"`})
	require.Equal(t, http.StatusNotModified, c.Writer.Status())
	require.Empty(t, rec.Body.String())
	require.Equal(t, "1", rec.Header().Get("X-Saiai-Manifest-Stale"))
	require.Equal(t, `"TEST_ONLY_ETAG"`, rec.Header().Get("ETag"))

	rec, _ = serveCodexModelsFallback(h, codexModelsFallbackRequest{ifNoneMatch: `"TEST_ONLY_OTHER"`})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, codexModelsFallbackManifest, rec.Body.String())

	// A matching tag on a second If-None-Match line still validates.
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/backend-api/codex/models?client_version=0.164.0", nil)
	c.Request.Header.Set("User-Agent", "codex-tui/0.164.0")
	c.Request.Header.Set("Originator", "codex-tui")
	c.Request.Header.Add("If-None-Match", `"TEST_ONLY_OTHER"`)
	c.Request.Header.Add("If-None-Match", `"TEST_ONLY_ETAG"`)
	group := &service.Group{ID: 45, Platform: service.PlatformOpenAI}
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 3, GroupID: &group.ID, Group: group})
	h.CodexModels(c)
	require.Equal(t, http.StatusNotModified, c.Writer.Status())
}

func TestCodexModelsStaleFallbackAppliesCurrentBlockedFilter(t *testing.T) {
	h, upstream := newCodexModelsFallbackHandler(t)
	// Stored while the group had no blacklist.
	_, _ = serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	upstream.respond = codexModelsFallbackBodyTimeout
	group := &service.Group{ID: 45, Platform: service.PlatformOpenAI, BlockedModelPatterns: []string{"test-only-blocked"}}
	rec, c := serveCodexModelsFallback(h, codexModelsFallbackRequest{group: group, ifNoneMatch: `"TEST_ONLY_ETAG"`})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"models":[{"slug":"test-only-new"}]}`, rec.Body.String())
	require.Empty(t, rec.Header().Get("ETag"))
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "1", rec.Header().Get("X-Saiai-Manifest-Stale"))
	requireCodexModelsRecovered(t, c, 0)
}

func TestCodexModelsStaleFallbackIsolation(t *testing.T) {
	h, upstream := newCodexModelsFallbackHandler(t)
	_, _ = serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	upstream.respond = codexModelsFallbackBodyTimeout

	cases := map[string]codexModelsFallbackRequest{
		"other group":          {group: &service.Group{ID: 46, Platform: service.PlatformOpenAI}},
		"other client_version": {query: "client_version=0.165.0"},
		"other query params":   {query: "client_version=0.164.0&future=1"},
		"non-official client":  {userAgent: "saiai_daemon_check/0.164.0", originator: "saiai_daemon_check"},
		"other originator":     {userAgent: "codex_cli_rs/0.164.0", originator: "codex_cli_rs"},
		"other version header": {version: "0.165.0"},
	}
	for name, req := range cases {
		rec, _ := serveCodexModelsFallback(h, req)
		require.Equal(t, http.StatusBadGateway, rec.Code, name)
		require.Empty(t, rec.Header().Get("X-Saiai-Manifest-Stale"), name)
	}
	rec, _ := serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestCodexModelsStaleFallbackClientCanceled(t *testing.T) {
	h, upstream := newCodexModelsFallbackHandler(t)
	_, _ = serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	ctx, cancel := context.WithCancel(context.Background())
	upstream.respond = func(r *http.Request) (*http.Response, error) {
		cancel()
		return nil, r.Context().Err()
	}
	rec, c := serveCodexModelsFallback(h, codexModelsFallbackRequest{ctx: ctx})
	require.Empty(t, rec.Body.String())
	require.Empty(t, rec.Header().Get("X-Saiai-Manifest-Stale"))
	_, recorded := c.Get(service.OpsUpstreamErrorMessageKey)
	require.False(t, recorded)
}

func TestCodexModelsStaleFallbackNoAccountsNotMasked(t *testing.T) {
	h, upstream := newCodexModelsFallbackHandler(t)
	_, _ = serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	upstream.repo.accounts = []service.Account{}
	upstream.calls = 0
	rec, _ := serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	require.Zero(t, upstream.calls)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "No available OpenAI accounts")
}

// Non-official clients keep their Version header upstream, so it is part of
// the key: a timeout for version B must not serve version A's list.
func TestCodexModelsStaleFallbackKeysOnVersionForNonOfficialClients(t *testing.T) {
	h, upstream := newCodexModelsFallbackHandler(t)
	const bodyA = `{"models":[{"slug":"test-only-version-a"}]}`
	upstream.respond = func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Version") == "0.164.0-a" {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bodyA))}, nil
		}
		return codexModelsFallbackBodyTimeout(r)
	}
	nonOfficial := codexModelsFallbackRequest{userAgent: "saiai_daemon_check/0.164.0", originator: "saiai_daemon_check"}
	reqA, reqB := nonOfficial, nonOfficial
	reqA.version, reqB.version = "0.164.0-a", "0.164.0-b"
	rec, _ := serveCodexModelsFallback(h, reqA)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, bodyA, rec.Body.String())

	rec, _ = serveCodexModelsFallback(h, reqB)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.Empty(t, rec.Header().Get("X-Saiai-Manifest-Stale"))

	upstream.respond = codexModelsFallbackBodyTimeout
	rec, _ = serveCodexModelsFallback(h, reqA)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, bodyA, rec.Body.String())
	require.Equal(t, "1", rec.Header().Get("X-Saiai-Manifest-Stale"))
}

// A selection failure other than an exhausted candidate list keeps the
// original error answer.
func TestCodexModelsStaleFallbackOnlyAfterCandidatesExhausted(t *testing.T) {
	h, upstream := newCodexModelsFallbackHandler(t)
	_, _ = serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	upstream.respond = codexModelsFallbackBodyTimeout
	upstream.repo.failFrom = upstream.repo.listCalls + 2
	rec, _ := serveCodexModelsFallback(h, codexModelsFallbackRequest{})
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "read Codex models manifest response")
	require.Empty(t, rec.Header().Get("X-Saiai-Manifest-Stale"))
}

type codexModelsFallbackHTTPUpstream struct {
	requests []*http.Request
	respond  func(*http.Request) (*http.Response, error)
}

func (u *codexModelsFallbackHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.requests = append(u.requests, req)
	return u.respond(req)
}

func (u *codexModelsFallbackHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ bool) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

// The success path keeps origin/main's If-None-Match semantics: only the
// first header line is the client's validator.
func TestCodexModelsSuccessPathUsesFirstIfNoneMatchLine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &codexModelsFallbackHTTPUpstream{respond: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Etag": {`"TEST_ONLY_SECOND"`}},
			Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"test-only-api-key"}]}`))}, nil
	}}
	account := service.Account{ID: 30, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "TEST_ONLY_API_KEY", "base_url": "https://models.example.test/v1"}}
	cfg := &config.Config{}
	svc := service.NewOpenAIGatewayService(&chatGPTAccountRepo{account: account}, nil, nil, nil, nil, nil, nil,
		cfg, nil, nil, nil, nil, nil, upstream, nil, nil)
	h := NewOpenAIGatewayHandler(svc, nil, nil, nil, nil, nil, nil, cfg, nil)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/backend-api/codex/models?client_version=0.164.0", nil)
	c.Request.Header.Set("User-Agent", "codex-tui/0.164.0")
	c.Request.Header.Set("Originator", "codex-tui")
	c.Request.Header.Add("If-None-Match", `"TEST_ONLY_FIRST"`)
	c.Request.Header.Add("If-None-Match", `"TEST_ONLY_SECOND"`)
	group := &service.Group{ID: 45, Platform: service.PlatformOpenAI}
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 3, GroupID: &group.ID, Group: group})
	h.CodexModels(c)
	require.Equal(t, http.StatusOK, c.Writer.Status(), rec.Body.String())
	require.JSONEq(t, `{"models":[{"slug":"test-only-api-key"}]}`, rec.Body.String())
	require.Equal(t, `"TEST_ONLY_SECOND"`, rec.Header().Get("ETag"))
	require.Len(t, upstream.requests, 1)
}
