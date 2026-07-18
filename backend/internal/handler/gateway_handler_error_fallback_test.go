package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayEnsureForwardErrorResponse_WritesFallbackWhenNotWritten(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	h := &GatewayHandler{}
	wrote := h.ensureForwardErrorResponse(c, false)

	require.True(t, wrote)
	require.Equal(t, http.StatusBadGateway, w.Code)

	var parsed map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &parsed)
	require.NoError(t, err)
	assert.Equal(t, "error", parsed["type"])
	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errorObj["type"])
	assert.Equal(t, "Upstream request failed", errorObj["message"])
}

func TestGatewayEnsureForwardErrorResponse_DoesNotOverrideWrittenResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.String(http.StatusTeapot, "already written")

	h := &GatewayHandler{}
	wrote := h.ensureForwardErrorResponse(c, false)

	require.False(t, wrote)
	require.Equal(t, http.StatusTeapot, w.Code)
	assert.Equal(t, "already written", w.Body.String())
}

func TestGatewayHandleClientRequestError_PreservesMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	h := &GatewayHandler{}
	h.handleClientRequestError(c, &service.ClientRequestError{
		StatusCode: http.StatusBadRequest,
		ErrorType:  "invalid_request_error",
		Message:    "Please use the official Claude Code client. If it is already installed, run SAIAI CLI initialization and try again. Contact your administrator if the issue persists.",
	}, false)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var parsed map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &parsed)
	require.NoError(t, err)
	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "invalid_request_error", errorObj["type"])
	assert.Equal(t, "Please use the official Claude Code client. If it is already installed, run SAIAI CLI initialization and try again. Contact your administrator if the issue persists.", errorObj["message"])
}

func TestGatewayHandleFailoverExhausted_CarpoolDevicesFullUsesCapacityMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	h := &GatewayHandler{}
	h.handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode: http.StatusTooManyRequests,
		Cause:      service.ErrClaudeOAuthCarpoolDevicesFull,
	}, service.PlatformAnthropic, false)

	require.Equal(t, http.StatusTooManyRequests, w.Code)
	var parsed map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &parsed)
	require.NoError(t, err)
	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "rate_limit_error", errorObj["type"])
	assert.Equal(t, service.ClaudeOAuthCarpoolDevicesFullAllAccountsMessage, errorObj["message"])
}

func TestGatewayHandleFailoverExhausted_DeviceAuthorizationUsesNeutralMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	h := &GatewayHandler{}
	h.handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:   http.StatusBadRequest,
		ResponseBody: []byte(`{"error":{"message":"reclaude 客户端状态异常，请重启 reclaude 后重试"}}`),
		Kind:         service.UpstreamFailureDeviceAuthorizationRevoked,
	}, service.PlatformAnthropic, false)

	require.Equal(t, http.StatusBadGateway, w.Code)
	require.NotContains(t, strings.ToLower(w.Body.String()), "reclaude")
	require.NotContains(t, w.Body.String(), "客户端状态异常")

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &parsed))
	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errorObj["type"])
	assert.Equal(t, service.DeviceAuthorizationUnavailableClientMessage, errorObj["message"])
}

func TestOpenAIGatewayAnthropicFailover_DeviceAuthorizationUsesNeutralMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	h := &OpenAIGatewayHandler{}
	h.handleAnthropicFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:   http.StatusBadRequest,
		ResponseBody: []byte(`{"error":{"message":"reclaude 客户端状态异常，请重启 reclaude 后重试"}}`),
		Kind:         service.UpstreamFailureDeviceAuthorizationRevoked,
	}, false)

	require.Equal(t, http.StatusBadGateway, w.Code)
	require.NotContains(t, strings.ToLower(w.Body.String()), "reclaude")
	require.NotContains(t, w.Body.String(), "客户端状态异常")
	require.Contains(t, w.Body.String(), service.DeviceAuthorizationUnavailableClientMessage)
}

func TestGatewayErrorWriters_RedactRestrictedUpstreamIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(*GatewayHandler, *gin.Context)
	}{
		{
			name: "direct json",
			write: func(h *GatewayHandler, c *gin.Context) {
				h.errorResponse(c, http.StatusBadGateway, "upstream_error", "ReClaude failed")
			},
		},
		{
			name: "streaming sse",
			write: func(h *GatewayHandler, c *gin.Context) {
				h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "reclaude failed", true)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

			tc.write(&GatewayHandler{}, c)

			require.NotContains(t, strings.ToLower(w.Body.String()), "reclaude")
			require.Contains(t, w.Body.String(), service.DeviceAuthorizationUnavailableClientMessage)
		})
	}
}

func TestOpenAIErrorWriters_RedactRestrictedUpstreamIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(*OpenAIGatewayHandler, *gin.Context)
	}{
		{
			name: "openai json",
			write: func(h *OpenAIGatewayHandler, c *gin.Context) {
				h.writeError(c, gatewayErrorEnvelope{Status: http.StatusBadGateway, Type: "upstream_error", Message: "ReClaude failed"}, false)
			},
		},
		{
			name: "openai streaming sse",
			write: func(h *OpenAIGatewayHandler, c *gin.Context) {
				h.writeError(c, gatewayErrorEnvelope{Status: http.StatusBadGateway, Type: "upstream_error", Message: "reclaude failed"}, true)
			},
		},
		{
			name: "anthropic json",
			write: func(h *OpenAIGatewayHandler, c *gin.Context) {
				h.writeAnthropicError(c, gatewayErrorEnvelope{Status: http.StatusBadGateway, Type: "upstream_error", Message: "ReClaude failed"}, false)
			},
		},
		{
			name: "anthropic streaming sse",
			write: func(h *OpenAIGatewayHandler, c *gin.Context) {
				h.writeAnthropicError(c, gatewayErrorEnvelope{Status: http.StatusBadGateway, Type: "upstream_error", Message: "reclaude failed"}, true)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

			tc.write(&OpenAIGatewayHandler{}, c)

			require.NotContains(t, strings.ToLower(w.Body.String()), "reclaude")
			require.Contains(t, w.Body.String(), service.DeviceAuthorizationUnavailableClientMessage)
		})
	}
}
