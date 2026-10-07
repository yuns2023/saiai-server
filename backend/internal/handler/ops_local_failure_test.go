package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func opsFailureTestQueue(t *testing.T) chan opsErrorLogJob {
	t.Helper()
	resetOpsErrorLoggerStateForTest(t)
	t.Cleanup(func() { resetOpsErrorLoggerStateForTest(t) })
	opsErrorLogOnce.Do(func() {})
	queue := make(chan opsErrorLogJob, 10)
	opsErrorLogMu.Lock()
	opsErrorLogQueue = queue
	opsErrorLogMu.Unlock()
	return queue
}

func TestOpsErrorLoggerRecordsLocalFailureAfterTransportStarts(t *testing.T) {
	queue := opsFailureTestQueue(t)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, tc := range []struct {
		name       string
		wireStatus int
		fail       func(*gin.Context)
		wantStatus int
		wantPhase  string
	}{
		{"ws_account_busy", http.StatusSwitchingProtocols, func(c *gin.Context) {
			closeOpenAIClientWSWithOps(c, nil, coderws.StatusTryAgainLater, "account is busy, please retry later")
		}, http.StatusTooManyRequests, "routing"},
		{"ws_no_account", http.StatusSwitchingProtocols, func(c *gin.Context) {
			closeOpenAIClientWSWithOps(c, nil, coderws.StatusTryAgainLater, "no available account")
		}, http.StatusServiceUnavailable, "routing"},
		{"sse_account_wait_timeout", http.StatusOK, func(c *gin.Context) {
			(&OpenAIGatewayHandler{}).handleConcurrencyError(c, context.DeadlineExceeded, "account", true)
		}, http.StatusTooManyRequests, "routing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.GET("/v1/responses", OpsErrorLoggerMiddleware(ops), func(c *gin.Context) {
				setOpsRequestContext(c, "mock-codex-model", true, []byte(`{"model":"mock-codex-model","private_input":"must-not-be-logged"}`))
				setOpsSelectedAccount(c, 18, service.PlatformOpenAI)
				c.Status(tc.wireStatus)
				c.Writer.WriteHeaderNow()
				tc.fail(c)
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
			require.Equal(t, tc.wireStatus, w.Code, "monitoring must not rewrite the transport status")
			select {
			case job := <-queue:
				require.Equal(t, tc.wantStatus, job.entry.StatusCode)
				require.Equal(t, "mock-codex-model", job.entry.Model)
				require.Equal(t, int64(18), *job.entry.AccountID)
				require.Equal(t, tc.wantPhase, job.entry.ErrorPhase)
				require.Equal(t, "gateway", job.entry.ErrorSource)
				require.Equal(t, "platform", job.entry.ErrorOwner)
				require.Nil(t, job.entry.UpstreamStatusCode, "a local rejection is not an upstream HTTP failure")
				require.NotContains(t, job.entry.ErrorBody, "must-not-be-logged")
				if tc.wireStatus == http.StatusSwitchingProtocols {
					require.Contains(t, job.entry.ErrorBody, `"transport_status_code":101`)
					require.Contains(t, job.entry.ErrorBody, `"websocket_close_code":1013`)
				}
			default:
				t.Fatal("terminal local failure was not queued")
			}
		})
	}
	require.Empty(t, queue)
}

func TestOpsErrorLoggerKeepsSuccessfulUpgradeAndUpstreamErrorsSeparate(t *testing.T) {
	queue := opsFailureTestQueue(t)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	r := gin.New()
	r.GET("/v1/responses", OpsErrorLoggerMiddleware(ops), func(c *gin.Context) { c.Status(http.StatusSwitchingProtocols) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
	require.Empty(t, queue, "a successful HTTP 101 alone is not an error")

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	setOpsLocalFailure(c, opsLocalFailure{status: 429, errType: "rate_limit_error", message: "local busy"})
	status, body, local := opsFailureResponse(c, 503, []byte(`{"error":{"message":"upstream original"}}`))
	require.Equal(t, 503, status)
	require.Equal(t, `{"error":{"message":"upstream original"}}`, string(body))
	require.False(t, local, "an existing error response retains its original body")
}

func TestOpenAIWSLocalBusyPreservesCloseFrameAndLogsRequestedModel(t *testing.T) {
	queue := opsFailureTestQueue(t)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h := newOpenAIHandlerForPreviousResponseIDValidation(t, &concurrencyCacheMock{
		acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return false, nil },
	})
	r := gin.New()
	r.Use(OpsErrorLoggerMiddleware(ops), func(c *gin.Context) {
		groupID := int64(2)
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 101, GroupID: &groupID, User: &service.User{ID: 1}})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})
		c.Next()
	})
	r.GET("/openai/v1/responses", h.ResponsesWebSocket)
	server := httptest.NewServer(r)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses", nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"mock-requested","tools":[{"type":"image_generation"}]} `)))
	_, _, err = conn.Read(ctx)
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.Code)
	require.Equal(t, "too many concurrent requests, please retry later", closeErr.Reason)
	select {
	case job := <-queue:
		require.Equal(t, "mock-requested", job.entry.Model)
		require.Equal(t, http.StatusTooManyRequests, job.entry.StatusCode)
		require.Equal(t, "request", job.entry.ErrorPhase)
		require.Nil(t, job.entry.AccountID)
		require.Nil(t, job.entry.UpstreamStatusCode)
	case <-ctx.Done():
		t.Fatal("local WebSocket rejection missing from Ops")
	}
}

func TestOpsPhaseRecognizesNativeOAuthNoAccount(t *testing.T) {
	require.Equal(t, "routing", classifyOpsPhase(normalizeOpsErrorType("service_unavailable", ""), "No available OpenAI OAuth account", ""))
}
