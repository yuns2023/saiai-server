package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const zeroOutputRefusalBody = `{"id":"msg_1","type":"message","role":"assistant","content":[],"stop_reason":"refusal","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":0,"cache_creation_input_tokens":200000,"cache_read_input_tokens":0}}`

func outputTokensValue(p *int64) string {
	if p == nil {
		return "<nil>"
	}
	return strconv.FormatInt(*p, 10)
}

func observedInt64(v int64) *int64 { return &v }

func stopReasonValue(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestSanitizeUpstreamStopReason(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"end_turn", `{"v":"end_turn"}`, "end_turn"},
		{"refusal", `{"v":"refusal"}`, "refusal"},
		{"trim_and_lowercase", `{"v":"  Refusal \n"}`, "refusal"},
		{"unknown_well_formed_kept", `{"v":"model_context_window_exceeded2"}`, "model_context_window_exceeded2"},
		{"exactly_32", `{"v":"` + strings.Repeat("a", 32) + `"}`, strings.Repeat("a", 32)},
		{"overlong_null", `{"v":"` + strings.Repeat("a", 33) + `"}`, "<nil>"},
		{"hyphen_not_stripped", `{"v":"end-turn"}`, "<nil>"},
		{"space_not_stripped", `{"v":"end turn"}`, "<nil>"},
		{"non_ascii", `{"v":"拒绝"}`, "<nil>"},
		{"empty", `{"v":""}`, "<nil>"},
		{"blank", `{"v":"   "}`, "<nil>"},
		{"null", `{"v":null}`, "<nil>"},
		{"missing", `{}`, "<nil>"},
		{"number", `{"v":1}`, "<nil>"},
		{"bool", `{"v":true}`, "<nil>"},
		{"object", `{"v":{"x":"end_turn"}}`, "<nil>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, stopReasonValue(sanitizeUpstreamStopReason(gjson.Get(tc.json, "v"))))
		})
	}
}

func newStopReasonTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c, rec
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "Request-Id": []string{"req_upstream_1"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestNonStreamingResponse_RecordsRefusalStopReasonWithZeroOutput(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{}, rateLimitService: &RateLimitService{}}
	c, rec := newStopReasonTestContext(t)

	usage, obs, err := svc.handleNonStreamingResponse(context.Background(), jsonResponse(zeroOutputRefusalBody), c, &Account{ID: 1}, "claude-sonnet-5", "claude-sonnet-5")

	require.NoError(t, err)
	require.Equal(t, ClaudeUsage{InputTokens: 3, OutputTokens: 0, CacheCreationInputTokens: 200000}, *usage)
	require.Equal(t, "refusal", stopReasonValue(obs.stopReason))
	require.Equal(t, "0", outputTokensValue(obs.finalOutputTokens))
	require.JSONEq(t, zeroOutputRefusalBody, rec.Body.String(), "observation must not alter the forwarded body")
}

func TestNonStreamingResponse_BadStopReasonNeverBreaksForwarding(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{}, rateLimitService: &RateLimitService{}}
	for _, body := range []string{
		`{"stop_reason":42,"usage":{"input_tokens":3,"output_tokens":5}}`,
		`{"stop_reason":{"nested":true},"usage":{"input_tokens":3,"output_tokens":5}}`,
		`{"stop_reason":"` + strings.Repeat("x", 64) + `","usage":{"input_tokens":3,"output_tokens":5}}`,
		`{"usage":{"input_tokens":3}}`,
	} {
		c, rec := newStopReasonTestContext(t)
		usage, obs, err := svc.handleNonStreamingResponse(context.Background(), jsonResponse(body), c, &Account{ID: 1}, "m", "m")
		require.NoError(t, err, body)
		require.Equal(t, 3, usage.InputTokens)
		require.Nil(t, obs.stopReason, body)
		require.Equal(t, http.StatusOK, rec.Code)
	}
	// output_tokens missing must be distinguishable from explicit zero.
	c, _ := newStopReasonTestContext(t)
	_, obs, err := svc.handleNonStreamingResponse(context.Background(), jsonResponse(`{"stop_reason":"end_turn","usage":{"input_tokens":3}}`), c, &Account{ID: 1}, "m", "m")
	require.NoError(t, err)
	require.Nil(t, obs.finalOutputTokens)
	require.Equal(t, "end_turn", stopReasonValue(obs.stopReason))
}

func TestAnthropicAPIKeyPassthroughNonStreaming_RecordsStopReason(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{}, rateLimitService: &RateLimitService{}}
	c, rec := newStopReasonTestContext(t)

	usage, obs, err := svc.handleNonStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), jsonResponse(zeroOutputRefusalBody), c, &Account{ID: 1})

	require.NoError(t, err)
	require.Equal(t, 200000, usage.CacheCreationInputTokens)
	require.Equal(t, "refusal", stopReasonValue(obs.stopReason))
	require.Equal(t, "0", outputTokensValue(obs.finalOutputTokens))
	require.Equal(t, zeroOutputRefusalBody, rec.Body.String())
}

func TestBedrockNonStreaming_RecordsStopReason(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{}}
	c, _ := newStopReasonTestContext(t)
	body := `{"type":"message","stop_reason":"max_tokens","usage":{"input_tokens":4,"output_tokens":9}}`

	usage, obs, err := svc.handleBedrockNonStreamingResponse(context.Background(), jsonResponse(body), c, &Account{ID: 1})

	require.NoError(t, err)
	require.Equal(t, 9, usage.OutputTokens)
	require.Equal(t, "max_tokens", stopReasonValue(obs.stopReason))
	require.Equal(t, "9", outputTokensValue(obs.finalOutputTokens))
}

func anthropicDeltaWithReason(reason string, outputTokens string) string {
	usage := ""
	if outputTokens != "" {
		usage = `,"usage":{"output_tokens":` + outputTokens + `}`
	}
	return "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":" + reason + ",\"stop_sequence\":null}" + usage + "}\n\n"
}

const anthropicMessageStopEvent = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestStreamingResponse_RecordsStopReasonOnNormalTerminal(t *testing.T) {
	// Grace disabled: plain Anthropic stream with a real message_stop.
	svc := newAnthropicTerminalRecoveryTestService(0)
	svc.cfg.Gateway.AnthropicStreamTerminalGraceSeconds = 0
	c, _ := newAnthropicTerminalRecoveryContext(t)
	stream := anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock + anthropicDeltaWithReason(`"end_turn"`, "7") + anthropicMessageStopEvent
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, anthropicTerminalRecoveryAccount(AccountTypeOAuth), time.Now(), "claude-sonnet-5", "claude-sonnet-5", false)

	require.NoError(t, err)
	require.Equal(t, "end_turn", stopReasonValue(result.upstream.stopReason))
	require.Equal(t, "7", outputTokensValue(result.upstream.finalOutputTokens))
	require.Equal(t, 7, result.usage.OutputTokens)
}

func TestStreamingResponse_RecordsStopReasonOnGraceRecovery(t *testing.T) {
	svc := newAnthropicTerminalRecoveryTestService(500 * time.Millisecond)
	c, recorder := newAnthropicTerminalRecoveryContext(t)
	ctx := SetClaudeCodeClient(context.Background(), true)
	stream := anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock + anthropicFinalDelta(0)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}

	result, err := svc.handleStreamingResponse(ctx, resp, c, anthropicTerminalRecoveryAccount(AccountTypeSetupToken), time.Now(), "claude-sonnet-5", "claude-sonnet-5", false)

	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: message_stop"), "recovery behavior unchanged")
	require.Equal(t, "end_turn", stopReasonValue(result.upstream.stopReason))
	require.Equal(t, "0", outputTokensValue(result.upstream.finalOutputTokens))
}

func TestStreamingResponse_KeepsStopReasonAfterClientDisconnectDrain(t *testing.T) {
	svc := newAnthropicTerminalRecoveryTestService(0)
	svc.cfg.Gateway.AnthropicStreamTerminalGraceSeconds = 0
	c, _ := newAnthropicTerminalRecoveryContext(t)
	c.Writer = &failWriteResponseWriter{ResponseWriter: c.Writer}
	stream := anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock + anthropicDeltaWithReason(`"max_tokens"`, "12") + anthropicMessageStopEvent
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, anthropicTerminalRecoveryAccount(AccountTypeOAuth), time.Now(), "claude-sonnet-5", "claude-sonnet-5", false)

	require.NoError(t, err)
	require.True(t, result.clientDisconnect)
	require.Equal(t, "max_tokens", stopReasonValue(result.upstream.stopReason))
	require.Equal(t, 12, result.usage.OutputTokens)
}

func TestStreamingResponse_InvalidLaterStopReasonDoesNotClearValidOne(t *testing.T) {
	svc := newAnthropicTerminalRecoveryTestService(0)
	svc.cfg.Gateway.AnthropicStreamTerminalGraceSeconds = 0
	c, _ := newAnthropicTerminalRecoveryContext(t)
	stream := anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock +
		anthropicDeltaWithReason(`"Pause_Turn"`, "2") +
		anthropicDeltaWithReason(`null`, "") +
		anthropicDeltaWithReason(`"bad reason!"`, "") +
		anthropicMessageStopEvent
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}

	result, err := svc.handleStreamingResponse(context.Background(), resp, c, anthropicTerminalRecoveryAccount(AccountTypeOAuth), time.Now(), "m", "m", false)

	require.NoError(t, err)
	require.Equal(t, "pause_turn", stopReasonValue(result.upstream.stopReason))
}

func TestPassthroughStreaming_RecordsStopReasonAndIgnoresMessageStartOutput(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}, rateLimitService: &RateLimitService{}}

	// message_start carries the initial output_tokens snapshot (0); the final
	// message_delta has no usage.output_tokens, so the output must count as
	// "not reported" rather than an explicit zero.
	c, _ := newStopReasonTestContext(t)
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":11,"output_tokens":0,"cache_creation_input_tokens":5000}}}`,
		"",
		`data: {"type":"message_delta","delta":{"stop_reason":"REFUSAL "}}`,
		"",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}
	result, err := svc.handleStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "m")
	require.NoError(t, err)
	require.Equal(t, "refusal", stopReasonValue(result.upstream.stopReason))
	require.Nil(t, result.upstream.finalOutputTokens)

	// Explicit final output_tokens=0 is reported.
	c, _ = newStopReasonTestContext(t)
	stream = strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":11,"output_tokens":1}}}`,
		"",
		`data: {"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":0}}`,
		"",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}
	result, err = svc.handleStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "m")
	require.NoError(t, err)
	require.Equal(t, "0", outputTokensValue(result.upstream.finalOutputTokens))
	require.Equal(t, 0, result.usage.OutputTokens)
}

func buildBedrockChunkFrame(t *testing.T, event string) []byte {
	t.Helper()
	payload := []byte(`{"bytes":"` + base64.StdEncoding.EncodeToString([]byte(event)) + `"}`)
	var headersBuf bytes.Buffer
	for _, h := range [][2]string{{":event-type", "chunk"}, {":message-type", "event"}} {
		_ = headersBuf.WriteByte(byte(len(h[0])))
		_, _ = headersBuf.WriteString(h[0])
		_ = headersBuf.WriteByte(7)
		_ = binary.Write(&headersBuf, binary.BigEndian, uint16(len(h[1])))
		_, _ = headersBuf.WriteString(h[1])
	}
	headers := headersBuf.Bytes()
	tab := crc32.MakeTable(crc32.IEEE)
	var prelude bytes.Buffer
	_ = binary.Write(&prelude, binary.BigEndian, uint32(12+len(headers)+len(payload)+4))
	_ = binary.Write(&prelude, binary.BigEndian, uint32(len(headers)))
	var frame bytes.Buffer
	_, _ = frame.Write(prelude.Bytes())
	_ = binary.Write(&frame, binary.BigEndian, crc32.Checksum(prelude.Bytes(), tab))
	_, _ = frame.Write(headers)
	_, _ = frame.Write(payload)
	_ = binary.Write(&frame, binary.BigEndian, crc32.Checksum(frame.Bytes(), tab))
	return frame.Bytes()
}

func TestBedrockStreaming_RecordsStopReason(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{}}
	c, _ := newStopReasonTestContext(t)
	var body bytes.Buffer
	for _, event := range []string{
		`{"type":"message_start","message":{"usage":{"input_tokens":4,"output_tokens":1}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"stop_sequence"},"usage":{"output_tokens":6}}`,
		`{"type":"message_stop"}`,
	} {
		_, _ = body.Write(buildBedrockChunkFrame(t, event))
	}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(&body)}

	result, err := svc.handleBedrockStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "m")

	require.NoError(t, err)
	require.Equal(t, "stop_sequence", stopReasonValue(result.upstream.stopReason))
	require.Equal(t, "6", outputTokensValue(result.upstream.finalOutputTokens))
	require.Equal(t, 6, result.usage.OutputTokens)
}

func TestAnthropicPassthroughForward_DoesNotLeakStopReasonAcrossAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	upstream := &anthropicHTTPUpstreamRecorder{}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}

	// Attempt 1 (stream) observes a refusal but fails before a terminal event.
	c1, _ := newStopReasonTestContext(t)
	upstream.resp = &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":9}}}`,
			"",
			`data: {"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":0}}`,
			"",
		}, "\n"))),
	}
	body := []byte(`{"model":"claude-sonnet-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	result, err := svc.Forward(context.Background(), c1, newAnthropicAPIKeyAccountForTest(), &ParsedRequest{Body: body, Model: "claude-sonnet-5", Stream: true})
	require.Error(t, err)
	require.Nil(t, result)

	// Attempt 2 (another account) succeeds without a stop_reason.
	c2, _ := newStopReasonTestContext(t)
	upstream.resp = jsonResponse(`{"type":"message","usage":{"input_tokens":9,"output_tokens":4}}`)
	account2 := newAnthropicAPIKeyAccountForTest()
	account2.ID = 202
	body = []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`)
	result, err = svc.Forward(context.Background(), c2, account2, &ParsedRequest{Body: body, Model: "claude-sonnet-5"})
	require.NoError(t, err)
	require.Nil(t, result.StopReason)
	require.Equal(t, "4", outputTokensValue(result.UpstreamFinalOutputTokens))

	// A successful refusal attempt propagates to ForwardResult.
	c3, _ := newStopReasonTestContext(t)
	upstream.resp = jsonResponse(zeroOutputRefusalBody)
	result, err = svc.Forward(context.Background(), c3, account2, &ParsedRequest{Body: body, Model: "claude-sonnet-5"})
	require.NoError(t, err)
	require.Equal(t, "refusal", stopReasonValue(result.StopReason))
	require.True(t, isZeroOutputUsage(result))
}

func TestLogZeroOutputUsage(t *testing.T) {
	refusal := "refusal"
	account := &Account{ID: 77, Platform: PlatformAnthropic}
	base := func() *ForwardResult {
		return &ForwardResult{
			RequestID:                 "req_upstream_1",
			Model:                     "claude-sonnet-5",
			UpstreamModel:             "claude-sonnet-5-20261001",
			Usage:                     ClaudeUsage{InputTokens: 3, CacheCreationInputTokens: 200000},
			StopReason:                &refusal,
			UpstreamFinalOutputTokens: observedInt64(0),
		}
	}

	capture := func(result *ForwardResult) *observer.ObservedLogs {
		core, logs := observer.New(zap.WarnLevel)
		ctx := context.WithValue(context.Background(), ctxkey.RequestID, "local-req")
		ctx = logger.IntoContext(ctx, zap.New(core).With(zap.String("request_id", "local-req")))
		LogZeroOutputUsage(ctx, account, result)
		return logs
	}

	logs := capture(base())
	require.Equal(t, 1, logs.Len())
	entry := logs.All()[0]
	require.Equal(t, "gateway.zero_output_usage", entry.Message)
	fields := entry.ContextMap()
	require.Equal(t, "anthropic", fields["platform"])
	require.Equal(t, int64(77), fields["account_id"])
	require.Equal(t, "claude-sonnet-5", fields["model"])
	require.Equal(t, "claude-sonnet-5-20261001", fields["upstream_model"])
	require.Equal(t, "req_upstream_1", fields["upstream_request_id"])
	require.Equal(t, "local-req", fields["request_id"])
	require.Equal(t, int64(0), fields["output_tokens"])
	require.Equal(t, "refusal", fields["stop_reason"])
	require.Equal(t, false, fields["stream"])
	require.Equal(t, int64(3), fields["input_tokens"])
	require.Equal(t, int64(200000), fields["cache_creation_tokens"])
	require.Equal(t, int64(0), fields["cache_read_tokens"])
	require.Equal(t, false, fields["client_disconnect"])

	missing := base()
	missing.UpstreamFinalOutputTokens = nil
	require.Zero(t, capture(missing).Len(), "missing output_tokens is not an explicit zero")

	positive := base()
	positive.Usage.OutputTokens = 1
	positive.UpstreamFinalOutputTokens = observedInt64(1)
	require.Zero(t, capture(positive).Len())

	empty := base()
	empty.Usage = ClaudeUsage{}
	require.Zero(t, capture(empty).Len(), "no prompt/cache tokens consumed")

	cacheReadOnly := base()
	cacheReadOnly.Usage = ClaudeUsage{CacheReadInputTokens: 10}
	cacheReadOnly.StopReason = nil
	logs = capture(cacheReadOnly)
	require.Equal(t, 1, logs.Len())
	require.Equal(t, "", logs.All()[0].ContextMap()["stop_reason"])

	require.Zero(t, capture(nil).Len())

	negative := base()
	negative.UpstreamFinalOutputTokens = observedInt64(-1)
	require.Zero(t, capture(negative).Len(), "only an explicit 0 counts")

	// Final raw value wins even if the merged billing usage kept an earlier
	// positive delta (merge ignores non-positive deltas).
	finalZero := base()
	finalZero.Usage.OutputTokens = 7
	logs = capture(finalZero)
	require.Equal(t, 1, logs.Len())
	require.Equal(t, int64(0), logs.All()[0].ContextMap()["output_tokens"])
}

func TestNumericOutputTokensRejectsNonIntegers(t *testing.T) {
	for _, raw := range []string{`{"v":0.5}`, `{"v":-0.5}`, `{"v":-1}`, `{"v":"0"}`, `{"v":null}`, `{}`} {
		require.Nil(t, numericOutputTokens(gjson.Get(raw, "v")), raw)
	}
	require.Equal(t, "0", outputTokensValue(numericOutputTokens(gjson.Get(`{"v":0}`, "v"))))
	require.Equal(t, "12", outputTokensValue(numericOutputTokens(gjson.Get(`{"v":12}`, "v"))))
	obs := observeAnthropicNonStreamBody([]byte(`{"stop_reason":"refusal","usage":{"input_tokens":3,"output_tokens":0.5}}`))
	require.Nil(t, obs.finalOutputTokens)
}

func TestPassthroughStreaming_FinalOutputTokensUsesLastDeltaNotMergedUsage(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}, rateLimitService: &RateLimitService{}}
	c, _ := newStopReasonTestContext(t)
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":11}}}`,
		"",
		`data: {"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":7}}`,
		"",
		`data: {"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":0}}`,
		"",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}
	result, err := svc.handleStreamingResponseAnthropicAPIKeyPassthrough(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "m")
	require.NoError(t, err)
	require.Equal(t, 7, result.usage.OutputTokens, "billing merge unchanged")
	require.Equal(t, "0", outputTokensValue(result.upstream.finalOutputTokens))
	require.Equal(t, "refusal", stopReasonValue(result.upstream.stopReason))
}
