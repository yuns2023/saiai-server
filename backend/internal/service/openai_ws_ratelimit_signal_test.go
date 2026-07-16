package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

type openAIWSRateLimitSignalRepo struct {
	stubOpenAIAccountRepo
	rateLimitCalls []time.Time
	updateExtra    []map[string]any
}

type openAICodexSnapshotAsyncRepo struct {
	stubOpenAIAccountRepo
	updateExtraCh chan map[string]any
	rateLimitCh   chan time.Time
}

type openAICodexExtraListRepo struct {
	stubOpenAIAccountRepo
	rateLimitCh chan time.Time
}

type codexSnapshotWriteEvent struct {
	call        int
	usedPercent float64
	err         error
}

type openAICodexSnapshotControlledRepo struct {
	stubOpenAIAccountRepo
	mu           sync.Mutex
	calls        int
	started      chan codexSnapshotWriteEvent
	finished     chan codexSnapshotWriteEvent
	releaseFirst chan struct{}
	failFirst    bool
}

func awaitCodexSnapshotWriteEvent(t *testing.T, ch <-chan codexSnapshotWriteEvent, description string) codexSnapshotWriteEvent {
	t.Helper()
	select {
	case event := <-ch:
		return event
	case <-time.After(2 * time.Second):
		t.Fatalf("waiting for %s", description)
		return codexSnapshotWriteEvent{}
	}
}

func (r *openAIWSRateLimitSignalRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.rateLimitCalls = append(r.rateLimitCalls, resetAt)
	return nil
}

func (r *openAIWSRateLimitSignalRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	copied := make(map[string]any, len(updates))
	for k, v := range updates {
		copied[k] = v
	}
	r.updateExtra = append(r.updateExtra, copied)
	return nil
}

func (r *openAICodexSnapshotAsyncRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	if r.rateLimitCh != nil {
		r.rateLimitCh <- resetAt
	}
	return nil
}

func (r *openAICodexSnapshotAsyncRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	if r.updateExtraCh != nil {
		copied := make(map[string]any, len(updates))
		for k, v := range updates {
			copied[k] = v
		}
		r.updateExtraCh <- copied
	}
	return nil
}

func (r *openAICodexExtraListRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	if r.rateLimitCh != nil {
		r.rateLimitCh <- resetAt
	}
	return nil
}

func (r *openAICodexSnapshotControlledRepo) UpdateExtra(ctx context.Context, _ int64, updates map[string]any) error {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()

	usedPercent, _ := updates["codex_5h_used_percent"].(float64)
	event := codexSnapshotWriteEvent{call: call, usedPercent: usedPercent}
	r.started <- event
	if call == 1 && r.releaseFirst != nil {
		select {
		case <-r.releaseFirst:
		case <-ctx.Done():
			event.err = ctx.Err()
			r.finished <- event
			return event.err
		}
	}
	if call == 1 && r.failFirst {
		event.err = errors.New("injected Codex snapshot persistence failure")
	}
	r.finished <- event
	return event.err
}

func (r *openAICodexExtraListRepo) ListWithFilters(_ context.Context, params pagination.PaginationParams, platform, accountType, status, search string, groupID int64) ([]Account, *pagination.PaginationResult, error) {
	_ = platform
	_ = accountType
	_ = status
	_ = search
	_ = groupID
	return r.accounts, &pagination.PaginationResult{Total: int64(len(r.accounts)), Page: params.Page, PageSize: params.PageSize}, nil
}

func TestOpenAIGatewayService_Forward_WSv2ErrorEventUsageLimitPersistsRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	resetAt := time.Now().Add(2 * time.Hour).Unix()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket failed: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		var req map[string]any
		if err := conn.ReadJSON(&req); err != nil {
			t.Errorf("read ws request failed: %v", err)
			return
		}
		_ = conn.WriteJSON(map[string]any{
			"type": "error",
			"error": map[string]any{
				"code":      "rate_limit_exceeded",
				"type":      "usage_limit_reached",
				"message":   "The usage limit has been reached",
				"resets_at": resetAt,
			},
		})
	}))
	defer wsServer.Close()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "unit-test-agent/1.0")

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_http_should_not_run"}`)),
		},
	}

	cfg := newOpenAIWSV2TestConfig()
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true

	account := Account{
		ID:          501,
		Name:        "openai-ws-rate-limit-event",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": wsServer.URL,
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}
	repo := &openAIWSRateLimitSignalRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}}}
	rateSvc := &RateLimitService{accountRepo: repo}
	svc := &OpenAIGatewayService{
		accountRepo:      repo,
		rateLimitService: rateSvc,
		httpUpstream:     upstream,
		cache:            &stubGatewayCache{},
		cfg:              cfg,
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
	}

	body := []byte(`{"model":"gpt-5.1","stream":false,"input":[{"type":"input_text","text":"hello"}]}`)
	result, err := svc.Forward(context.Background(), c, &account, body)
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Nil(t, upstream.lastReq, "WS 限流 error event 不应回退到同账号 HTTP")
	require.Len(t, repo.rateLimitCalls, 1)
	require.WithinDuration(t, time.Unix(resetAt, 0), repo.rateLimitCalls[0], 2*time.Second)
}

func TestOpenAIGatewayService_Forward_WSv2Handshake429PersistsRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-codex-primary-used-percent", "100")
		w.Header().Set("x-codex-primary-reset-after-seconds", "7200")
		w.Header().Set("x-codex-primary-window-minutes", "10080")
		w.Header().Set("x-codex-secondary-used-percent", "3")
		w.Header().Set("x-codex-secondary-reset-after-seconds", "1800")
		w.Header().Set("x-codex-secondary-window-minutes", "300")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_exceeded","message":"rate limited"}}`))
	}))
	defer server.Close()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "unit-test-agent/1.0")

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_http_should_not_run"}`)),
		},
	}

	cfg := newOpenAIWSV2TestConfig()
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true

	account := Account{
		ID:          502,
		Name:        "openai-ws-rate-limit-handshake",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": server.URL,
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}
	repo := &openAIWSRateLimitSignalRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}}}
	rateSvc := &RateLimitService{accountRepo: repo}
	svc := &OpenAIGatewayService{
		accountRepo:      repo,
		rateLimitService: rateSvc,
		httpUpstream:     upstream,
		cache:            &stubGatewayCache{},
		cfg:              cfg,
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
	}

	body := []byte(`{"model":"gpt-5.1","stream":false,"input":[{"type":"input_text","text":"hello"}]}`)
	result, err := svc.Forward(context.Background(), c, &account, body)
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Nil(t, upstream.lastReq, "WS 握手 429 不应回退到同账号 HTTP")
	require.Len(t, repo.rateLimitCalls, 1)
	require.NotEmpty(t, repo.updateExtra, "握手 429 的 x-codex 头应立即落库")
	require.Contains(t, repo.updateExtra[0], "codex_usage_updated_at")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ErrorEventUsageLimitPersistsRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := newOpenAIWSV2TestConfig()
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	resetAt := time.Now().Add(90 * time.Minute).Unix()
	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"error","error":{"code":"rate_limit_exceeded","type":"usage_limit_reached","message":"The usage limit has been reached","resets_at":PLACEHOLDER}}`),
		},
	}
	captureConn.events[0] = []byte(strings.ReplaceAll(string(captureConn.events[0]), "PLACEHOLDER", strconv.FormatInt(resetAt, 10)))
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	account := Account{
		ID:          503,
		Name:        "openai-ingress-rate-limit",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}
	repo := &openAIWSRateLimitSignalRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}}}
	rateSvc := &RateLimitService{accountRepo: repo}
	svc := &OpenAIGatewayService{
		accountRepo:      repo,
		rateLimitService: rateSvc,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		cfg:              cfg,
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- io.ErrUnexpectedEOF
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, &account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	select {
	case serverErr := <-serverErrCh:
		require.Error(t, serverErr)
		require.Len(t, repo.rateLimitCalls, 1)
		require.WithinDuration(t, time.Unix(resetAt, 0), repo.rateLimitCalls[0], 2*time.Second)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
}

func TestOpenAIGatewayService_UpdateCodexUsageSnapshot_ExhaustedSnapshotSetsRateLimit(t *testing.T) {
	repo := &openAICodexSnapshotAsyncRepo{
		updateExtraCh: make(chan map[string]any, 1),
		rateLimitCh:   make(chan time.Time, 1),
	}
	svc := &OpenAIGatewayService{accountRepo: repo}
	snapshot := &OpenAICodexUsageSnapshot{
		PrimaryUsedPercent:         ptrFloat64WS(100),
		PrimaryResetAfterSeconds:   ptrIntWS(3600),
		PrimaryWindowMinutes:       ptrIntWS(10080),
		SecondaryUsedPercent:       ptrFloat64WS(12),
		SecondaryResetAfterSeconds: ptrIntWS(1200),
		SecondaryWindowMinutes:     ptrIntWS(300),
	}
	before := time.Now()
	svc.updateCodexUsageSnapshot(context.Background(), 601, snapshot)

	select {
	case updates := <-repo.updateExtraCh:
		require.Equal(t, 100.0, updates["codex_7d_used_percent"])
	case <-time.After(2 * time.Second):
		t.Fatal("等待 codex 快照落库超时")
	}

	select {
	case resetAt := <-repo.rateLimitCh:
		require.WithinDuration(t, before.Add(time.Hour), resetAt, 2*time.Second)
	case <-time.After(2 * time.Second):
		t.Fatal("等待 codex 100% 自动切换限流超时")
	}
}

func TestOpenAIGatewayService_UpdateCodexUsageSnapshot_NonExhaustedSnapshotDoesNotSetRateLimit(t *testing.T) {
	repo := &openAICodexSnapshotAsyncRepo{
		updateExtraCh: make(chan map[string]any, 1),
		rateLimitCh:   make(chan time.Time, 1),
	}
	svc := &OpenAIGatewayService{accountRepo: repo}
	snapshot := &OpenAICodexUsageSnapshot{
		PrimaryUsedPercent:         ptrFloat64WS(94),
		PrimaryResetAfterSeconds:   ptrIntWS(3600),
		PrimaryWindowMinutes:       ptrIntWS(10080),
		SecondaryUsedPercent:       ptrFloat64WS(22),
		SecondaryResetAfterSeconds: ptrIntWS(1200),
		SecondaryWindowMinutes:     ptrIntWS(300),
	}
	svc.updateCodexUsageSnapshot(context.Background(), 602, snapshot)

	select {
	case <-repo.updateExtraCh:
	case <-time.After(2 * time.Second):
		t.Fatal("等待 codex 快照落库超时")
	}

	select {
	case resetAt := <-repo.rateLimitCh:
		t.Fatalf("unexpected rate limit reset at: %v", resetAt)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestOpenAIGatewayService_UpdateCodexUsageSnapshot_ThrottlesExtraWrites(t *testing.T) {
	repo := &openAICodexSnapshotAsyncRepo{
		updateExtraCh: make(chan map[string]any, 2),
		rateLimitCh:   make(chan time.Time, 2),
	}
	svc := &OpenAIGatewayService{
		accountRepo:           repo,
		codexSnapshotThrottle: newAccountWriteThrottle(time.Hour),
	}
	snapshot := &OpenAICodexUsageSnapshot{
		PrimaryUsedPercent:         ptrFloat64WS(94),
		PrimaryResetAfterSeconds:   ptrIntWS(3600),
		PrimaryWindowMinutes:       ptrIntWS(10080),
		SecondaryUsedPercent:       ptrFloat64WS(22),
		SecondaryResetAfterSeconds: ptrIntWS(1200),
		SecondaryWindowMinutes:     ptrIntWS(300),
	}

	svc.updateCodexUsageSnapshot(context.Background(), 777, snapshot)
	svc.updateCodexUsageSnapshot(context.Background(), 777, snapshot)

	select {
	case <-repo.updateExtraCh:
	case <-time.After(2 * time.Second):
		t.Fatal("等待第一次 codex 快照落库超时")
	}

	select {
	case updates := <-repo.updateExtraCh:
		t.Fatalf("unexpected second codex snapshot write: %v", updates)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestOpenAIGatewayService_UpdateCodexUsageSnapshot_BypassesThrottleAtFiveHourAdmissionBoundaries(t *testing.T) {
	repo := &openAICodexSnapshotAsyncRepo{
		updateExtraCh: make(chan map[string]any, 5),
		rateLimitCh:   make(chan time.Time, 1),
	}
	svc := &OpenAIGatewayService{
		accountRepo:           repo,
		codexSnapshotThrottle: newAccountWriteThrottle(time.Hour),
	}
	snapshot := func(used5h float64, reset5hSeconds int) *OpenAICodexUsageSnapshot {
		return &OpenAICodexUsageSnapshot{
			PrimaryUsedPercent:         ptrFloat64WS(10),
			PrimaryResetAfterSeconds:   ptrIntWS(3600),
			PrimaryWindowMinutes:       ptrIntWS(10080),
			SecondaryUsedPercent:       ptrFloat64WS(used5h),
			SecondaryResetAfterSeconds: ptrIntWS(reset5hSeconds),
			SecondaryWindowMinutes:     ptrIntWS(300),
		}
	}
	awaitWrite := func(want float64) {
		t.Helper()
		select {
		case updates := <-repo.updateExtraCh:
			require.Equal(t, want, updates["codex_5h_used_percent"])
		case <-time.After(2 * time.Second):
			t.Fatalf("waiting for codex 5h boundary write: want=%v", want)
		}
	}

	svc.updateCodexUsageSnapshot(context.Background(), 778, snapshot(79, 1200))
	awaitWrite(79)

	// Crossing strictly above 80% must be visible immediately to new-session admission.
	svc.updateCodexUsageSnapshot(context.Background(), 778, snapshot(81, 1200))
	awaitWrite(81)

	// Same-side telemetry remains throttled.
	svc.updateCodexUsageSnapshot(context.Background(), 778, snapshot(82, 1200))
	select {
	case updates := <-repo.updateExtraCh:
		t.Fatalf("unexpected same-side codex snapshot write: %v", updates)
	case <-time.After(200 * time.Millisecond):
	}

	// A material reset-window shift and a later recovery both bypass throttling.
	svc.updateCodexUsageSnapshot(context.Background(), 778, snapshot(82, 1800))
	awaitWrite(82)
	svc.updateCodexUsageSnapshot(context.Background(), 778, snapshot(20, 1800))
	awaitWrite(20)
}

func TestOpenAIGatewayService_UpdateCodexUsageSnapshot_SerializesBoundaryWritesPerAccount(t *testing.T) {
	repo := &openAICodexSnapshotControlledRepo{
		started:      make(chan codexSnapshotWriteEvent, 2),
		finished:     make(chan codexSnapshotWriteEvent, 2),
		releaseFirst: make(chan struct{}),
	}
	svc := &OpenAIGatewayService{
		accountRepo:           repo,
		codexSnapshotThrottle: newAccountWriteThrottle(time.Hour),
	}
	snapshot := func(used5h float64) *OpenAICodexUsageSnapshot {
		return &OpenAICodexUsageSnapshot{
			PrimaryUsedPercent:         ptrFloat64WS(10),
			PrimaryResetAfterSeconds:   ptrIntWS(3600),
			PrimaryWindowMinutes:       ptrIntWS(10080),
			SecondaryUsedPercent:       ptrFloat64WS(used5h),
			SecondaryResetAfterSeconds: ptrIntWS(1200),
			SecondaryWindowMinutes:     ptrIntWS(300),
		}
	}

	svc.updateCodexUsageSnapshot(context.Background(), 779, snapshot(79))
	firstStarted := awaitCodexSnapshotWriteEvent(t, repo.started, "first Codex snapshot write to start")
	require.Equal(t, 1, firstStarted.call)
	require.Equal(t, 79.0, firstStarted.usedPercent)

	svc.updateCodexUsageSnapshot(context.Background(), 779, snapshot(81))
	select {
	case event := <-repo.started:
		t.Fatalf("second snapshot started before the first completed: %+v", event)
	case <-time.After(150 * time.Millisecond):
	}

	close(repo.releaseFirst)
	firstFinished := awaitCodexSnapshotWriteEvent(t, repo.finished, "first Codex snapshot write to finish")
	require.Equal(t, 1, firstFinished.call)
	require.NoError(t, firstFinished.err)
	select {
	case secondStarted := <-repo.started:
		require.Equal(t, 2, secondStarted.call)
		require.Equal(t, 81.0, secondStarted.usedPercent)
	case <-time.After(2 * time.Second):
		t.Fatal("waiting for second serialized Codex snapshot write")
	}
	secondFinished := awaitCodexSnapshotWriteEvent(t, repo.finished, "second Codex snapshot write to finish")
	require.Equal(t, 2, secondFinished.call)
	require.NoError(t, secondFinished.err)
}

func TestOpenAIGatewayService_UpdateCodexUsageSnapshot_FailedInFlightWritePersistsLatestPendingSnapshot(t *testing.T) {
	repo := &openAICodexSnapshotControlledRepo{
		started:      make(chan codexSnapshotWriteEvent, 2),
		finished:     make(chan codexSnapshotWriteEvent, 2),
		releaseFirst: make(chan struct{}),
		failFirst:    true,
	}
	svc := &OpenAIGatewayService{
		accountRepo:           repo,
		codexSnapshotThrottle: newAccountWriteThrottle(time.Hour),
	}
	snapshot := func(used5h float64) *OpenAICodexUsageSnapshot {
		return &OpenAICodexUsageSnapshot{
			PrimaryUsedPercent:         ptrFloat64WS(10),
			PrimaryResetAfterSeconds:   ptrIntWS(3600),
			PrimaryWindowMinutes:       ptrIntWS(10080),
			SecondaryUsedPercent:       ptrFloat64WS(used5h),
			SecondaryResetAfterSeconds: ptrIntWS(1200),
			SecondaryWindowMinutes:     ptrIntWS(300),
		}
	}

	svc.updateCodexUsageSnapshot(context.Background(), 780, snapshot(81))
	firstStarted := awaitCodexSnapshotWriteEvent(t, repo.started, "failed Codex snapshot write to start")
	require.Equal(t, 1, firstStarted.call)

	// While the first write is blocked, retain only the latest same-side
	// snapshot. It must be persisted automatically if the active write fails.
	svc.updateCodexUsageSnapshot(context.Background(), 780, snapshot(82))
	svc.updateCodexUsageSnapshot(context.Background(), 780, snapshot(83))
	svc.updateCodexUsageSnapshot(context.Background(), 780, snapshot(84))
	select {
	case event := <-repo.started:
		t.Fatalf("pending snapshot started before the active write completed: %+v", event)
	case <-time.After(150 * time.Millisecond):
	}

	close(repo.releaseFirst)
	firstFinished := awaitCodexSnapshotWriteEvent(t, repo.finished, "failed Codex snapshot write to finish")
	require.Error(t, firstFinished.err)
	select {
	case secondStarted := <-repo.started:
		require.Equal(t, 2, secondStarted.call)
		require.Equal(t, 84.0, secondStarted.usedPercent)
	case <-time.After(2 * time.Second):
		t.Fatal("waiting for latest pending snapshot after Codex persistence failure")
	}
	secondFinished := awaitCodexSnapshotWriteEvent(t, repo.finished, "Codex snapshot retry to finish")
	require.Equal(t, 2, secondFinished.call)
	require.NoError(t, secondFinished.err)
	select {
	case event := <-repo.started:
		t.Fatalf("expected pending snapshots to coalesce to one write, got: %+v", event)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestAccountWriteThrottle_OlderArrivalCannotReplaceLatestPendingSnapshot(t *testing.T) {
	throttle := newAccountWriteThrottle(time.Hour)
	accountID := int64(781)
	baseTime := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	openObservation := codexFiveHourAdmissionObservation{
		class:    codexFiveHourAdmissionOpen,
		resetAt:  baseTime.Add(2 * time.Hour),
		hasReset: true,
	}
	blockedObservation := codexFiveHourAdmissionObservation{
		class:    codexFiveHourAdmissionBlocked,
		resetAt:  baseTime.Add(2 * time.Hour),
		hasReset: true,
	}

	active, start := throttle.BeginCodexSnapshotWrite(accountID, baseTime, openObservation, map[string]any{"codex_5h_used_percent": 79.0})
	require.True(t, start)
	require.NotNil(t, active)

	// Simulate concurrent callers acquiring the coordinator lock out of timestamp
	// order: the newer blocked observation is accepted before an older open one.
	newerPending, start := throttle.BeginCodexSnapshotWrite(accountID, baseTime.Add(2*time.Second), blockedObservation, map[string]any{"codex_5h_used_percent": 81.0})
	require.False(t, start)
	require.Nil(t, newerPending)
	olderPending, start := throttle.BeginCodexSnapshotWrite(accountID, baseTime.Add(time.Second), openObservation, map[string]any{"codex_5h_used_percent": 79.0})
	require.False(t, start)
	require.Nil(t, olderPending)

	next := throttle.CompleteCodexSnapshotWrite(active, false, baseTime.Add(3*time.Second))
	require.NotNil(t, next)
	require.Equal(t, 81.0, next.updates["codex_5h_used_percent"])
	require.Nil(t, throttle.CompleteCodexSnapshotWrite(next, true, baseTime.Add(4*time.Second)))
}

func ptrFloat64WS(v float64) *float64 { return &v }
func ptrIntWS(v int) *int             { return &v }

func TestOpenAIGatewayService_GetSchedulableAccount_ExhaustedCodexExtraSetsRateLimit(t *testing.T) {
	resetAt := time.Now().Add(6 * 24 * time.Hour)
	account := Account{
		ID:          701,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"codex_7d_used_percent": 100.0,
			"codex_7d_reset_at":     resetAt.UTC().Format(time.RFC3339),
		},
	}
	repo := &openAICodexExtraListRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}}, rateLimitCh: make(chan time.Time, 1)}
	svc := &OpenAIGatewayService{accountRepo: repo}

	fresh, err := svc.getSchedulableAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.NotNil(t, fresh)
	require.NotNil(t, fresh.RateLimitResetAt)
	require.WithinDuration(t, resetAt.UTC(), *fresh.RateLimitResetAt, time.Second)
	select {
	case persisted := <-repo.rateLimitCh:
		require.WithinDuration(t, resetAt.UTC(), persisted, time.Second)
	case <-time.After(2 * time.Second):
		t.Fatal("等待旧快照补写限流状态超时")
	}
}

func TestAdminService_ListAccounts_ExhaustedCodexExtraReturnsRateLimitedAccount(t *testing.T) {
	resetAt := time.Now().Add(4 * 24 * time.Hour)
	repo := &openAICodexExtraListRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{{
			ID:          702,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Extra: map[string]any{
				"codex_7d_used_percent": 100.0,
				"codex_7d_reset_at":     resetAt.UTC().Format(time.RFC3339),
			},
		}}},
		rateLimitCh: make(chan time.Time, 1),
	}
	svc := &adminServiceImpl{accountRepo: repo}

	accounts, total, err := svc.ListAccounts(context.Background(), 1, 20, PlatformOpenAI, AccountTypeOAuth, "", "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, accounts, 1)
	require.NotNil(t, accounts[0].RateLimitResetAt)
	require.WithinDuration(t, resetAt.UTC(), *accounts[0].RateLimitResetAt, time.Second)
	select {
	case persisted := <-repo.rateLimitCh:
		require.WithinDuration(t, resetAt.UTC(), persisted, time.Second)
	case <-time.After(2 * time.Second):
		t.Fatal("等待列表补写限流状态超时")
	}
}

func TestOpenAIWSErrorHTTPStatusFromRaw_UsageLimitReachedIs429(t *testing.T) {
	require.Equal(t, http.StatusTooManyRequests, openAIWSErrorHTTPStatusFromRaw("", "usage_limit_reached"))
	require.Equal(t, http.StatusTooManyRequests, openAIWSErrorHTTPStatusFromRaw("rate_limit_exceeded", ""))
}
