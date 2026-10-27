package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsErrorLoggerClassifiesRelayedDetailRejections(t *testing.T) {
	queue := opsFailureTestQueue(t)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, tc := range []struct {
		name, body, wantType, wantPhase, wantOwner, wantSeverity, wantMessage string
		status                                                                int
		upstreamBody                                                          string
		wantRetryable                                                         bool
	}{
		{name: "client_shape_400", status: 400, body: `{"detail":"Input must be a list"}`,
			wantType: "invalid_request_error", wantPhase: "request", wantOwner: "client", wantSeverity: "P3",
			wantMessage: "Input must be a list"},
		{name: "provider_503", status: 503, body: `{"detail":"Service temporarily unavailable"}`,
			wantType: "upstream_error", wantPhase: "upstream", wantOwner: "provider", wantSeverity: "P1",
			wantMessage: "Service temporarily unavailable", wantRetryable: true},
		// A gateway body that merely shares the upstream status is not a relay.
		{name: "local_body_same_status", status: 400, body: `{"detail":"Local rejection"}`, upstreamBody: `{"detail":"Different"}`,
			wantType: "api_error", wantPhase: "internal", wantOwner: "platform", wantSeverity: "P2",
			wantMessage: "Local rejection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstreamBody := tc.upstreamBody
			if upstreamBody == "" {
				upstreamBody = tc.body
			}
			r := gin.New()
			r.POST("/v1/responses", OpsErrorLoggerMiddleware(ops), func(c *gin.Context) {
				setOpsRequestContext(c, "mock-codex-model", true, []byte(`{"model":"mock-codex-model"}`))
				service.SetOpsUpstreamError(c, tc.status, "mock upstream message", upstreamBody)
				c.Data(tc.status, "application/json", []byte(tc.body))
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			require.Equal(t, tc.status, w.Code)
			require.Equal(t, tc.body, w.Body.String(), "classification must not change the client response")
			select {
			case job := <-queue:
				require.Equal(t, tc.wantType, job.entry.ErrorType)
				require.Equal(t, tc.wantPhase, job.entry.ErrorPhase)
				require.Equal(t, tc.wantOwner, job.entry.ErrorOwner)
				require.Equal(t, tc.wantSeverity, job.entry.Severity)
				require.Equal(t, tc.wantRetryable, job.entry.IsRetryable)
				require.Equal(t, tc.wantMessage, job.entry.ErrorMessage)
				if tc.upstreamBody == "" {
					require.Equal(t, "upstream_http", job.entry.ErrorSource)
				} else {
					require.Equal(t, "gateway", job.entry.ErrorSource)
				}
			default:
				t.Fatal("error was not queued")
			}
		})
	}
	require.Empty(t, queue)
}

func TestApplyOpsRelayedUpstreamClassificationKeepsOtherEntries(t *testing.T) {
	status, other := 400, 502
	body := `{"detail":"Input must be a list"}`
	for _, entry := range []*service.OpsInsertErrorLogInput{
		{StatusCode: 400, ErrorPhase: "internal", ErrorType: "api_error", ErrorBody: body},
		{StatusCode: 400, ErrorPhase: "internal", ErrorType: "api_error", ErrorBody: body, UpstreamStatusCode: &other, UpstreamErrorDetail: &body},
		{StatusCode: 400, ErrorPhase: "routing", ErrorType: "api_error", ErrorBody: body, UpstreamStatusCode: &status, UpstreamErrorDetail: &body},
	} {
		before := *entry
		applyOpsRelayedUpstreamClassification(entry)
		require.Equal(t, before, *entry)
	}
}

func TestChatGPTConversationSearchNeverForwardsSharedAccountHistory(t *testing.T) {
	r := gin.New()
	h := &OpenAIGatewayHandler{}
	r.GET("/chatgpt/backend-api/conversations/search", h.ChatGPTConversationSearch)
	r.GET("/chatgpt/backend-api/conversations/:conversation_id", func(c *gin.Context) { t.Fatal("search was routed as a conversation read") })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/chatgpt/backend-api/conversations/search?query=TEST_ONLY&cursor=", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"items":[],"cursor":null}`, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}
