package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIsOpenAIRequestServiceTierSupported(t *testing.T) {
	allowed := []string{
		`{"model":"gpt-5.4"}`,
		`{"model":"gpt-5.4","service_tier":null}`,
		`{"model":"gpt-5.4","service_tier":""}`,
		`{"model":"gpt-5.4","service_tier":"auto"}`,
		`{"model":"gpt-5.4","service_tier":"default"}`,
		`{"model":"gpt-5.4","service_tier":"flex"}`,
		`{"model":"gpt-5.4","service_tier":"priority"}`,
		`{"model":"gpt-5.4","service_tier":"fast"}`,
		`{"model":"gpt-5.4","service_tier":" FAST "}`,
		`{"model":"gpt-5.4","service_tier":"Priority"}`,
		`{"type":"response.create","model":"gpt-5.4","service_tier":"auto","response":{"service_tier":"flex"}}`,
		`{"metadata":{"service_tier":"ultrafast"}}`,
	}
	for _, body := range allowed {
		require.True(t, IsOpenAIRequestServiceTierSupported([]byte(body)), body)
	}

	rejected := []string{
		`{"model":"gpt-5.4","service_tier":"ultrafast"}`,
		`{"model":"gpt-5.4","service_tier":"ULTRAFAST"}`,
		`{"model":"gpt-5.4","service_tier":"scale"}`,
		`{"model":"gpt-5.4","service_tier":"garbage"}`,
		`{"model":"gpt-5.4","service_tier":1}`,
		`{"model":"gpt-5.4","service_tier":true}`,
		`{"model":"gpt-5.4","service_tier":{"tier":"default"}}`,
		`{"model":"gpt-5.4","service_tier":["default"]}`,
		// Parsers disagree on which duplicate wins; every occurrence must be billable.
		`{"model":"gpt-5.4","service_tier":"default","service_tier":"ultrafast"}`,
		`{"model":"gpt-5.4","service_tier":"ultrafast","service_tier":"default"}`,
		`{"model":"gpt-5.4","service_tier":"default","service_tier":"priority"}`,
		`{"type":"response.create","response":{"service_tier":"flex","service_tier":"priority"}}`,
		`{"model":"gpt-5.4","service\u005ftier":"ultrafast"}`,
		`{"type":"response.create","response":{"model":"gpt-5.4","service_tier":"ultrafast"}}`,
	}
	for _, body := range rejected {
		require.False(t, IsOpenAIRequestServiceTierSupported([]byte(body)), body)
	}

	require.LessOrEqual(t, len(OpenAIUnsupportedServiceTierMessage), 123, "must fit a WebSocket close reason")
}

func TestOpenAIRequestDuplicateKey(t *testing.T) {
	cases := map[string]string{
		`{"model":"gpt-5.4","input":"hi"}`:                                     "",
		`{"model":"gpt-5.4","input":"a","input":"b","metadata":{"model":"x"}}`: "",
		`{"type":"response.cancel","type":"response.create"}`:                  "type",
		`{"model":"gpt-5.4","model":"gpt-6-astra"}`:                            "model",
		`{"model":"gpt-5.4","stream":false,"stream":true}`:                     "stream",
		`{"previous_response_id":"resp_a","previous_response_id":"resp_b"}`:    "previous_response_id",
		`{"service_tier":"default","service_tier":"priority"}`:                 "service_tier",
		`{"response":{"model":"a"},"response":{"model":"b"}}`:                  "response",
		`{"type":"response.create","response":{"model":"a","model":"b"}}`:      "response.model",
		`{"m\u006fdel":"gpt-5.4","model":"gpt-6-astra"}`:                       "model",
		`[1,2]`:    "",
		`not json`: "",
		``:         "",
	}
	for body, want := range cases {
		require.Equal(t, want, OpenAIRequestDuplicateKey([]byte(body)), body)
	}
	require.LessOrEqual(t, len(OpenAIDuplicateKeyMessage("previous_response_id")), 123)
	require.LessOrEqual(t, len(OpenAIDuplicateKeyMessage("response.service_tier")), 123)
}

func TestNormalizeOpenAIReportedServiceTier_UnknownBillsAsPriority(t *testing.T) {
	known := map[string]*string{
		"default":    openAIStringPtrForTest("default"),
		" Priority ": openAIStringPtrForTest("priority"),
		"flex":       openAIStringPtrForTest("flex"),
		"fast":       openAIStringPtrForTest("priority"),
		"":           nil,
		"auto":       nil,
	}
	for raw, want := range known {
		got := normalizeOpenAIReportedServiceTier(raw)
		require.Equal(t, want, got, raw)
		require.Empty(t, unrecognizedOpenAIReportedServiceTier(raw), raw)
	}

	for _, raw := range []string{"ultrafast", "scale", " Turbo "} {
		got := normalizeOpenAIReportedServiceTier(raw)
		require.NotNil(t, got, raw)
		require.Equal(t, "priority", *got, raw)
		require.Equal(t, strings.TrimSpace(raw), unrecognizedOpenAIReportedServiceTier(raw))
	}

	// A cheaper or absent request tier never lowers an unknown report.
	for _, requested := range []*string{nil, openAIStringPtrForTest("flex"), openAIStringPtrForTest("default")} {
		got := resolveOpenAIServiceTier(normalizeOpenAIReportedServiceTier("ultrafast"), requested)
		require.NotNil(t, got)
		require.Equal(t, "priority", *got)
	}
	// Request "auto" leaves billing to the reported tier, as before.
	got := resolveOpenAIServiceTier(normalizeOpenAIReportedServiceTier("default"), extractOpenAIServiceTierFromBody([]byte(`{"service_tier":"auto"}`)))
	require.NotNil(t, got)
	require.Equal(t, "default", *got)
	require.Nil(t, resolveOpenAIServiceTier(nil, extractOpenAIServiceTierFromBody([]byte(`{"service_tier":"auto"}`))))
}

func TestOpenAIUsageExtraction_RecordsUnrecognizedReportedServiceTier(t *testing.T) {
	usage, ok := extractOpenAIUsageFromJSONBytes([]byte(`{"service_tier":"ultrafast","usage":{"input_tokens":3,"output_tokens":2}}`))
	require.True(t, ok)
	require.NotNil(t, usage.ReportedServiceTier)
	require.Equal(t, "priority", *usage.ReportedServiceTier)
	require.Equal(t, "ultrafast", usage.UnrecognizedReportedServiceTier)

	usage, ok = extractOpenAIUsageFromJSONBytes([]byte(`{"service_tier":"default","usage":{"input_tokens":3,"output_tokens":2}}`))
	require.True(t, ok)
	require.Equal(t, "default", *usage.ReportedServiceTier)
	require.Empty(t, usage.UnrecognizedReportedServiceTier)

	var wsUsage OpenAIUsage
	parseOpenAIWSResponseUsageFromCompletedEvent([]byte(`{"type":"response.completed","response":{"service_tier":"scale","usage":{"input_tokens":1,"output_tokens":1}}}`), &wsUsage)
	require.Equal(t, "priority", *wsUsage.ReportedServiceTier)
	require.Equal(t, "scale", wsUsage.UnrecognizedReportedServiceTier)

	var sseUsage OpenAIUsage
	svc := &OpenAIGatewayService{}
	svc.parseSSEUsageBytes([]byte(`{"type":"response.completed","response":{"service_tier":"ultrafast","usage":{"input_tokens":1,"output_tokens":1}}}`), &sseUsage)
	require.Equal(t, "priority", *sseUsage.ReportedServiceTier)
	require.Equal(t, "ultrafast", sseUsage.UnrecognizedReportedServiceTier)
}

func TestOpenAIGatewayServiceRecordUsage_UnrecognizedReportedTierBilledAsPriorityAndLogged(t *testing.T) {
	logSink, restore := captureStructuredLog(t)
	defer restore()

	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	usage, ok := extractOpenAIUsageFromJSONBytes([]byte(`{"service_tier":"ultrafast","usage":{"input_tokens":100,"output_tokens":50}}`))
	require.True(t, ok)

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:   "resp_unknown_reported_tier",
			ServiceTier: resolveOpenAIServiceTier(usage.ReportedServiceTier, nil),
			Usage:       usage,
			Model:       "gpt-5.4",
			Duration:    time.Second,
		},
		APIKey:  &APIKey{ID: 1101},
		User:    &User{ID: 2101},
		Account: &Account{ID: 3101, Name: "tier-test-account"},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.NotNil(t, usageRepo.lastLog.ServiceTier)
	require.Equal(t, "priority", *usageRepo.lastLog.ServiceTier)

	baseCost, calcErr := svc.billingService.CalculateCost("gpt-5.4", UsageTokens{InputTokens: 100, OutputTokens: 50}, 1.0)
	require.NoError(t, calcErr)
	require.InDelta(t, baseCost.TotalCost*2, usageRepo.lastLog.TotalCost, 1e-10)

	require.True(t, logSink.ContainsMessageAtLevel("unrecognized service tier", "warn"))
	require.True(t, logSink.ContainsFieldValue("reported_service_tier", "ultrafast"))
	require.True(t, logSink.ContainsFieldValue("account_id", "3101"))
	require.True(t, logSink.ContainsFieldValue("model", "gpt-5.4"))
	require.True(t, logSink.ContainsFieldValue("upstream_request_id", "resp_unknown_reported_tier"))
}

func TestOpenAIGatewayServiceRecordUsage_KnownReportedTierDoesNotLog(t *testing.T) {
	logSink, restore := captureStructuredLog(t)
	defer restore()

	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	usage, ok := extractOpenAIUsageFromJSONBytes([]byte(`{"service_tier":"default","usage":{"input_tokens":100,"output_tokens":50}}`))
	require.True(t, ok)

	require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:   "resp_known_reported_tier",
			ServiceTier: resolveOpenAIServiceTier(usage.ReportedServiceTier, nil),
			Usage:       usage,
			Model:       "gpt-5.4",
			Duration:    time.Second,
		},
		APIKey:  &APIKey{ID: 1102},
		User:    &User{ID: 2102},
		Account: &Account{ID: 3102},
	}))
	require.Equal(t, "default", *usageRepo.lastLog.ServiceTier)
	require.False(t, logSink.ContainsMessage("unrecognized service tier"))
}

// The v1 ingress parser and the v2 relay validate every client frame. These
// tests prove that a rejected later frame is never written upstream, that the
// client is closed with 1008 and that an unknown reported tier is billed at
// priority. No hook validates here: the service enforces it on its own.
func TestOpenAIWSIngress_RejectsUnsupportedServiceTierOnLaterTurnWithoutUpstreamWrite(t *testing.T) {
	const ultrafastTurn = `{"type":"response.create","model":"gpt-5.4","stream":false,"service_tier":"ultrafast","previous_response_id":"resp_tier_turn_1"}`
	// gjson reads the first "type", so the relay would not see a turn; an
	// upstream that keeps the last key would still create a response.
	const ambiguousTypeTurn = `{"type":"response.cancel","type":"response.create","model":"gpt-5.4","service_tier":"ultrafast"}`
	// A billable tier does not make an ambiguous "type" acceptable.
	const ambiguousTypeDefaultTier = `{"type":"response.cancel","model":"gpt-5.4","service_tier":"default","type":"response.create"}`
	// v1 reads the first "model" and sjson rewrites only the first.
	const ambiguousModelTurn = `{"type":"response.create","model":"gpt-5.4","model":"gpt-6-astra","previous_response_id":"resp_tier_turn_1"}`
	tierReason := OpenAIUnsupportedServiceTierMessage
	cases := []struct {
		name          string
		mode          string
		secondPayload string
		reason        string
	}{
		{name: "ctx_pool_unbillable_tier", mode: "ctx_pool", secondPayload: ultrafastTurn, reason: tierReason},
		{name: "ctx_pool_duplicate_model", mode: "ctx_pool", secondPayload: ambiguousModelTurn, reason: OpenAIDuplicateKeyMessage("model")},
		{name: "ctx_pool_duplicate_type", mode: "ctx_pool", secondPayload: `{"type":"response.create","model":"gpt-5.4","type":"response.cancel"}`, reason: OpenAIDuplicateKeyMessage("type")},
		{name: "passthrough_unbillable_tier", mode: "passthrough", secondPayload: ultrafastTurn, reason: tierReason},
		{name: "passthrough_duplicate_model", mode: "passthrough", secondPayload: ambiguousModelTurn, reason: OpenAIDuplicateKeyMessage("model")},
		{name: "passthrough_non_turn_frame", mode: "passthrough", secondPayload: ambiguousTypeTurn, reason: OpenAIDuplicateKeyMessage("type")},
		{name: "passthrough_duplicate_type_billable_tier", mode: "passthrough", secondPayload: ambiguousTypeDefaultTier, reason: OpenAIDuplicateKeyMessage("type")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runOpenAIWSServiceTierRejectionScenario(t, tc.mode, tc.secondPayload, tc.reason, false)
		})
	}
}

// A rejected frame sent while the previous turn is still streaming must not
// cancel that turn's billing: the relay drains it to the terminal usage event.
// v1 reads the next client frame only after the current turn has completed
// and been billed, so the scenario is specific to the v2 relay.
func TestOpenAIWSPassthrough_RejectedFrameMidStreamStillBillsInFlightTurn(t *testing.T) {
	for name, payload := range map[string]string{
		"duplicate_type":  `{"type":"response.cancel","type":"response.create","model":"gpt-5.4"}`,
		"unbillable_tier": `{"type":"response.create","model":"gpt-5.4","service_tier":"ultrafast"}`,
	} {
		t.Run(name, func(t *testing.T) {
			reason := OpenAIUnsupportedServiceTierMessage
			if name == "duplicate_type" {
				reason = OpenAIDuplicateKeyMessage("type")
			}
			runOpenAIWSServiceTierRejectionScenario(t, "passthrough", payload, reason, true)
		})
	}
}

// Two pipelined turns are in flight when a frame is rejected: draining must
// continue until both terminal events have been billed, not stop at the first.
func TestOpenAIWSPassthrough_RejectedFrameDrainsAllInFlightTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	upstreamConn := &openAIWSCaptureConn{
		readDelays: []time.Duration{300 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond},
		events: [][]byte{
			[]byte(`{"type":"response.created","response":{"id":"resp_pipe_a"}}`),
			[]byte(`{"type":"response.created","response":{"id":"resp_pipe_b"}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_pipe_a","model":"gpt-5.4","usage":{"input_tokens":10,"output_tokens":20}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_pipe_b","model":"gpt-5.4","usage":{"input_tokens":30,"output_tokens":40}}}`),
		},
	}
	svc := &OpenAIGatewayService{
		cfg:                       cfg,
		httpUpstream:              &httpUpstreamRecorder{},
		cache:                     &stubGatewayCache{},
		openaiWSResolver:          NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:             NewCodexToolCorrector(),
		openaiWSPassthroughDialer: &openAIWSCaptureDialer{conn: upstreamConn},
	}
	account := &Account{
		ID: 4602, Name: "openai-ws-pipelined", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra:       map[string]any{"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough},
	}
	resultCh := make(chan *OpenAIForwardResult, 4)
	hooks := &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
		if turnErr == nil && result != nil {
			resultCh <- result
		}
	}}
	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r.Clone(r.Context())
		msgType, firstMessage, readErr := conn.Read(r.Context())
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", msgType, firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()
	for _, payload := range []string{
		`{"type":"response.create","model":"gpt-5.4"}`,
		`{"type":"response.create","model":"gpt-5.4"}`,
		`{"type":"response.cancel","type":"response.create","model":"gpt-5.4"}`,
	} {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
		cancel()
	}

	select {
	case serverErr := <-serverErrCh:
		var closeErr *OpenAIWSClientCloseError
		require.True(t, errors.As(serverErr, &closeErr), "unexpected error: %v", serverErr)
		require.Equal(t, OpenAIDuplicateKeyMessage("type"), closeErr.Reason())
	case <-time.After(5 * time.Second):
		t.Fatal("rejected frame did not end the session")
	}
	close(resultCh)
	billed := map[string]int{}
	for result := range resultCh {
		billed[result.RequestID] = result.Usage.OutputTokens
	}
	require.Equal(t, map[string]int{"resp_pipe_a": 20, "resp_pipe_b": 40}, billed, "every in-flight turn must be billed")
	upstreamConn.mu.Lock()
	defer upstreamConn.mu.Unlock()
	require.Len(t, upstreamConn.rawWrites, 2, "the rejected frame must not be written upstream")
}

func runOpenAIWSServiceTierRejectionScenario(t *testing.T, mode, secondPayload, reason string, midStream bool) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	turnOneCompleted := []byte(`{"type":"response.completed","response":{"id":"resp_tier_turn_1","model":"gpt-5.4","service_tier":"ultrafast","usage":{"input_tokens":120,"output_tokens":3400}}}`)
	upstreamConn := &openAIWSCaptureConn{
		// Keep the passthrough relay's upstream reader busy after turn 1.
		readDelays: []time.Duration{0, 5 * time.Second},
		events: [][]byte{
			turnOneCompleted,
			[]byte(`{"type":"response.completed","response":{"id":"resp_tier_turn_2","model":"gpt-5.4","service_tier":"default","usage":{"input_tokens":2,"output_tokens":3}}}`),
		},
	}
	if midStream {
		// Turn 1 streams a delta, then completes after the bad frame was sent.
		upstreamConn.readDelays = []time.Duration{0, 300 * time.Millisecond}
		upstreamConn.events = [][]byte{
			[]byte(`{"type":"response.output_text.delta","response_id":"resp_tier_turn_1","delta":"partial"}`),
			turnOneCompleted,
		}
	}
	captureDialer := &openAIWSCaptureDialer{conn: upstreamConn}
	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
	}
	extra := map[string]any{"responses_websockets_v2_enabled": true}
	if mode == "passthrough" {
		cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
		cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
		svc.openaiWSPassthroughDialer = captureDialer
		extra = map[string]any{"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough}
	} else {
		pool := newOpenAIWSConnPool(cfg)
		pool.setClientDialerForTest(captureDialer)
		svc.openaiWSPool = pool
	}
	account := &Account{
		ID:          4601,
		Name:        "openai-ws-service-tier",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra:       extra,
	}

	resultCh := make(chan *OpenAIForwardResult, 2)
	hooks := &OpenAIWSIngressHooks{
		AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
			if turnErr == nil && result != nil {
				resultCh <- result
			}
		},
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
		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", msgType, firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	write := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}

	write(`{"type":"response.create","model":"gpt-5.4","stream":false,"service_tier":"auto"}`)
	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	if midStream {
		require.Equal(t, "response.output_text.delta", gjson.GetBytes(event, "type").String())
	} else {
		require.Equal(t, "resp_tier_turn_1", gjson.GetBytes(event, "response.id").String())
	}

	write(secondPayload)

	select {
	case result := <-resultCh:
		require.Equal(t, "resp_tier_turn_1", result.RequestID)
		require.Equal(t, 120, result.Usage.InputTokens, "turn 1 must be billed with its full usage")
		require.Equal(t, 3400, result.Usage.OutputTokens)
		require.NotNil(t, result.ServiceTier)
		require.Equal(t, "priority", *result.ServiceTier, "unknown reported tier must bill as priority")
		require.Equal(t, "ultrafast", result.Usage.UnrecognizedReportedServiceTier)
	case <-time.After(3 * time.Second):
		t.Fatal("turn 1 result not delivered")
	}

	select {
	case serverErr := <-serverErrCh:
		var closeErr *OpenAIWSClientCloseError
		require.True(t, errors.As(serverErr, &closeErr), "unexpected error: %v", serverErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
		require.Equal(t, reason, closeErr.Reason())
	case <-time.After(5 * time.Second):
		t.Fatal("rejected frame did not end the session")
	}

	upstreamConn.mu.Lock()
	defer upstreamConn.mu.Unlock()
	require.Len(t, upstreamConn.writes, 1, "the rejected frame must not be written upstream")
	require.Equal(t, "auto", upstreamConn.writes[0]["service_tier"], "allowed tiers are forwarded unchanged")
	if mode == "passthrough" {
		require.Len(t, upstreamConn.rawWrites, 1, "v2 passthrough relays raw frames")
	} else {
		require.Empty(t, upstreamConn.rawWrites, "v1 ingress writes JSON")
	}
}

func openAIStringPtrForTest(value string) *string {
	return &value
}
