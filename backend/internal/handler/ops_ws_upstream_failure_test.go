package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsWSFailureTraceBelongsToFailingTurnWithoutCapturingBody(t *testing.T) {
	queue := opsFailureTestQueue(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Set(opsWSRecorderKey, &opsWSFailureRecorder{ops: service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil), turn: 2})
	request := []byte(`{"model":"MOCK_ONLY","input":"TEST_ONLY_PRIVATE_CONTENT"}`)
	recordOpenAIWSUpstreamFailure(c, &service.OpenAIWSUpstreamFailure{Turn: 2, Model: "MOCK_ONLY", Status: 503, EventType: "error", ErrorType: "api_error", Message: "MOCK_ONLY failure", RequestPayloadHash: service.HashUsageRequestPayload(request), UpstreamRequestID: "TEST_ONLY_UPSTREAM_TRACE"})
	entry := (<-queue).entry
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.ErrorBody), &payload))
	require.Equal(t, float64(2), payload["websocket_turn"])
	require.Equal(t, service.HashUsageRequestPayload(request), payload["request_payload_sha256"])
	require.Equal(t, "TEST_ONLY_UPSTREAM_TRACE", payload["upstream_request_id"])
	require.Equal(t, "TEST_ONLY_UPSTREAM_TRACE", entry.UpstreamErrors[0].UpstreamRequestID)
	require.NotContains(t, entry.ErrorBody, "TEST_ONLY_PRIVATE_CONTENT")
	require.Nil(t, entry.RequestBodyJSON)
	recordOpenAIWSUpstreamFailure(c, &service.OpenAIWSUpstreamFailure{Turn: 2, Status: 503, EventType: "error", Message: "MOCK_ONLY failure", RequestPayloadHash: "TEST_ONLY_NOT_A_DIGEST", UpstreamRequestID: "Bearer TEST_ONLY_SECRET\nBAD_HEADER"})
	entry = (<-queue).entry
	require.NotContains(t, entry.ErrorBody, "request_payload_sha256")
	require.NotContains(t, entry.ErrorBody, "TEST_ONLY_SECRET")
	require.Empty(t, entry.UpstreamErrors[0].UpstreamRequestID)
}

func TestOpsWSProviderFailureIsLoggedOnceBeforeDisconnect(t *testing.T) {
	queue := opsFailureTestQueue(t)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	r := gin.New()
	r.GET("/v1/responses", OpsErrorLoggerMiddleware(ops), func(c *gin.Context) {
		c.Request.Header.Set("Upgrade", "websocket")
		c.Request.Header.Set("User-Agent", "codex-tui/0.163.0")
		c.Request.Header.Set("version", "0.163.0")
		c.Request.Header.Set("chatgpt-account-id", "TEST_ONLY_IDENTITY")
		require.True(t, codexClientPolicyMatched(c, "local_proxy_only"))
		setOpsRequestContext(c, "first-model", true, nil)
		c.Status(http.StatusSwitchingProtocols)
		c.Writer.WriteHeaderNow()
		opsWSStartTurn(c, 1)
		recordOpenAIWSUpstreamFailure(c, &service.OpenAIWSUpstreamFailure{Turn: 1, AccountID: 19, Model: "requested-luna", EventType: "error", Status: 400, ErrorType: "invalid_request_error", Code: "model_not_supported", Message: "requested-luna is not supported"})
		require.Len(t, queue, 1, "record the refusal even while the socket stays open")
		closeOpenAIClientWSWithOps(c, nil, coderws.StatusInternalError, "upstream websocket proxy failed")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
	require.Equal(t, 101, w.Code)
	require.Len(t, queue, 1, "a subsequent disconnect must not append a misleading 500")
	entry := (<-queue).entry
	require.Equal(t, 400, entry.StatusCode)
	require.Equal(t, 400, *entry.UpstreamStatusCode)
	require.Equal(t, "requested-luna", entry.Model)
	require.NotNil(t, entry.Diagnostics)
	require.Equal(t, "websocket", entry.Diagnostics.Transport)
	require.Equal(t, 101, entry.Diagnostics.TransportStatusCode)
	require.Len(t, entry.Diagnostics.Checks, 4)
	for _, check := range entry.Diagnostics.Checks {
		require.True(t, check.Passed)
	}
	require.Equal(t, int64(19), *entry.AccountID)
	require.Equal(t, "provider", entry.ErrorOwner)
	require.Equal(t, "upstream_ws", entry.ErrorSource)
	require.Contains(t, entry.ErrorBody, `"transport_status_code":101`)
	require.Contains(t, entry.ErrorBody, `"code":"model_not_supported"`)
	require.Contains(t, entry.ErrorMessage, "not supported")
}

func TestOpsWSLaterTurnDoesNotInheritProviderRefusal(t *testing.T) {
	queue := opsFailureTestQueue(t)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	r := gin.New()
	r.GET("/v1/responses", OpsErrorLoggerMiddleware(ops), func(c *gin.Context) {
		c.Status(101)
		c.Writer.WriteHeaderNow()
		opsWSStartTurn(c, 1)
		recordOpenAIWSUpstreamFailure(c, &service.OpenAIWSUpstreamFailure{Turn: 1, Model: "first", Status: 400, ErrorType: "invalid_request_error", EventType: "error", Message: "first denied"})
		opsWSStartTurn(c, 2)
		setOpsRequestContext(c, "second", true, nil)
		opsWSSuccessfulTurn(c, 2)
		require.False(t, opsWSHasCurrentUpstreamFailure(c))
		closeOpenAIClientWSWithOps(c, nil, coderws.StatusInternalError, "upstream websocket proxy failed")
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
	require.Len(t, queue, 2)
	require.Equal(t, "first", (<-queue).entry.Model)
	entry := (<-queue).entry
	require.Equal(t, "second", entry.Model)
	require.Equal(t, 500, entry.StatusCode)
	require.Equal(t, "gateway", entry.ErrorSource)
	require.Nil(t, entry.UpstreamStatusCode)
}

func TestOpsWSUnknownStatusDoesNotInventProviderStatus(t *testing.T) {
	queue := opsFailureTestQueue(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Set(opsWSRecorderKey, &opsWSFailureRecorder{ops: service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil), turn: 1})
	recordOpenAIWSUpstreamFailure(c, &service.OpenAIWSUpstreamFailure{Turn: 1, EventType: "error", Message: "failure", ErrorType: "server_error"})
	entry := (<-queue).entry
	require.Equal(t, 502, entry.StatusCode)
	require.Nil(t, entry.UpstreamStatusCode)
	require.NotContains(t, entry.ErrorBody, "upstream_status_code")
}

func TestOpsWSIncompleteReasonIsSavedWithoutProviderStatusOrBody(t *testing.T) {
	for _, reason := range []string{"max_output_tokens", "content_filter", "steered", "", "Bearer TEST_ONLY_SECRET", strings.Repeat("x", 129)} {
		t.Run(reason, func(t *testing.T) {
			queue := opsFailureTestQueue(t)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			c.Set(opsWSRecorderKey, &opsWSFailureRecorder{ops: service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil), turn: 8})
			recordOpenAIWSUpstreamFailure(c, &service.OpenAIWSUpstreamFailure{
				Turn: 8, EventType: "response.incomplete", ResponseID: "resp_incomplete",
				IncompleteReason: reason, Message: "Upstream WebSocket response.incomplete",
			})
			entry := (<-queue).entry
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(entry.ErrorBody), &payload))
			require.Equal(t, 502, entry.StatusCode)
			require.Nil(t, entry.UpstreamStatusCode)
			require.NotContains(t, payload, "upstream_status_code")
			require.Equal(t, float64(101), payload["transport_status_code"])
			require.Equal(t, float64(8), payload["websocket_turn"])
			require.Equal(t, "resp_incomplete", payload["response_id"])
			if reason == "max_output_tokens" || reason == "content_filter" || reason == "steered" {
				require.Equal(t, map[string]any{"reason": reason}, payload["incomplete_details"])
			} else {
				require.NotContains(t, payload, "incomplete_details")
			}
			require.NotContains(t, entry.ErrorBody, "TEST_ONLY_SECRET")
			require.Nil(t, entry.RequestBodyJSON)
			require.Equal(t, entry.ErrorBody, entry.UpstreamErrors[0].Detail)
		})
	}
}

func TestOpsWSFailureSavesRelayDiagnosticsAndCFRay(t *testing.T) {
	queue := opsFailureTestQueue(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Set(opsWSRecorderKey, &opsWSFailureRecorder{ops: service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil), turn: 3})
	sent := int64(1500)
	discard := true
	recordOpenAIWSUpstreamFailure(c, &service.OpenAIWSUpstreamFailure{
		Turn: 3, EventType: "response.incomplete", ResponseID: "resp_incomplete", IncompleteReason: "interrupted",
		Message: "Upstream WebSocket response.incomplete (interrupted)", UpstreamCFRay: "8f1a2b3c4d5e6f70-SJC",
		RelayDiagnostics: &openaiwsv2.RelayFailureDiagnostics{
			Attribution: "response_id", MsSinceSent: &sent, UpstreamEvents: 4,
			ClientControl: []openaiwsv2.RelayControlFrameDiagnostics{{Type: "response.interrupt", MsAfterSent: 1400, MsBeforeFailure: 100, Target: "current", DiscardPartialItems: &discard, Forwarded: &discard}},
		},
	})
	entry := (<-queue).entry
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.ErrorBody), &payload))
	require.Equal(t, "8f1a2b3c4d5e6f70-SJC", payload["upstream_cf_ray"])
	diagnostics, ok := payload["relay_diagnostics"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "response_id", diagnostics["attribution"])
	require.Equal(t, float64(1500), diagnostics["ms_since_sent"])
	control := diagnostics["client_control"].([]any)[0].(map[string]any)
	require.Equal(t, "response.interrupt", control["type"])
	require.Equal(t, true, control["discard_partial_items"])
	require.Equal(t, entry.ErrorBody, entry.UpstreamErrors[0].Detail)

	recordOpenAIWSUpstreamFailure(c, &service.OpenAIWSUpstreamFailure{Turn: 3, EventType: "error", Message: "failure", UpstreamCFRay: "Bearer TEST_ONLY_SECRET"})
	entry = (<-queue).entry
	require.NotContains(t, entry.ErrorBody, "upstream_cf_ray")
	require.NotContains(t, entry.ErrorBody, "relay_diagnostics")
}
