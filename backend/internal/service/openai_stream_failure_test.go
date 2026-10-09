package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestNativeResponsesStreamFailureIsObservedWithoutChangingWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name, event string
		status      int
	}{
		{"overload", `{"type":"error","error":{"code":"server_is_overloaded","type":"service_unavailable_error","message":"TEST_ONLY_PRIVATE_PROMPT"}}`, 503},
		{"explicit status", `{"type":"error","status":500,"error":{"code":"server_error","type":"api_error"}}`, 500},
		{"response failed", `{"type":"response.failed","response":{"id":"resp_TEST_ONLY_failed","error":{"code":"invalid_request_error","type":"invalid_request_error"}}}`, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := ": TEST_ONLY_COMMENT\n\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_TEST_ONLY_failed\"}}\n\ndata: " + test.event + "\n\n"
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			result, err := svc.handleStreamingResponse(context.Background(), resp, c,
				&Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, time.Now(), "gpt-6.1-sol", "gpt-6.1-sol")
			require.Error(t, err)
			require.Empty(t, result.responseID, "failed response must not create a successful continuation binding")
			require.Equal(t, wire, w.Body.String(), "observe the provider error without adding or rewriting events")
			require.Equal(t, 200, w.Code, "HTTP transport has already started")
			require.Equal(t, test.status, c.GetInt(OpsUpstreamStatusCodeKey))
			detail := c.GetString(OpsUpstreamErrorDetailKey)
			require.NotContains(t, detail, "TEST_ONLY_PRIVATE_PROMPT")
			var metadata map[string]any
			require.NoError(t, json.Unmarshal([]byte(detail), &metadata))
			require.Equal(t, float64(200), metadata["transport_status_code"])
			require.Equal(t, float64(test.status), metadata["upstream_status_code"])
		})
	}
}

func TestNativeResponsesStreamFailureRedactsReflectedCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("Authorization", "Bearer TEST_ONLY_GATEWAY_KEY")
	resp := &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {"TEST_ONLY_SELECTED_CREDENTIAL"}}}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "TEST_ONLY_SELECTED_CREDENTIAL"}}
	err := observeOpenAIStreamFailure(c, account, resp, []byte(`{"type":"error","status":503,"error":{"code":"TEST_ONLY_SELECTED_CREDENTIAL","type":"TEST_ONLY_GATEWAY_KEY","message":"TEST_ONLY_PRIVATE_MESSAGE"},"future":"TEST_ONLY_PRIVATE_EXTRA"}`))
	require.Error(t, err)
	detail := c.GetString(OpsUpstreamErrorDetailKey)
	for _, secret := range []string{"TEST_ONLY_SELECTED_CREDENTIAL", "TEST_ONLY_GATEWAY_KEY", "TEST_ONLY_PRIVATE_MESSAGE", "TEST_ONLY_PRIVATE_EXTRA"} {
		require.NotContains(t, detail, secret)
	}
}
