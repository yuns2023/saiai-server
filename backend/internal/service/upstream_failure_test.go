//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClassifyUpstreamFailure_DeviceAuthorizationRevoked(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"type":"error","error":{"message":"此设备已被解绑，请在终端重新完成登录"}}`),
		[]byte(`{"type":"error","error":{"message":"reclaude 客户端状态异常，请重启 reclaude 后重试"}}`),
		[]byte(`{"type":"error","error":{"message":"ReClaude client status is unavailable; restart ReClaude and retry"}}`),
	} {
		require.Equal(
			t,
			UpstreamFailureDeviceAuthorizationRevoked,
			classifyUpstreamFailure(PlatformAnthropic, http.StatusBadRequest, body),
		)
	}
	body := []byte(`{"type":"error","error":{"message":"此设备已被解绑，请在终端重新完成登录"}}`)
	require.Equal(t, UpstreamFailureNone, classifyUpstreamFailure(PlatformOpenAI, http.StatusBadRequest, body))
	require.Equal(t, UpstreamFailureNone, classifyUpstreamFailure(PlatformAnthropic, http.StatusUnauthorized, body))
	require.Equal(
		t,
		UpstreamFailureNone,
		classifyUpstreamFailure(
			PlatformAnthropic,
			http.StatusBadRequest,
			[]byte(`{"error":{"message":"prompt contains 此设备已被解绑 as ordinary text"}}`),
		),
	)
	require.Equal(
		t,
		UpstreamFailureNone,
		classifyUpstreamFailure(
			PlatformAnthropic,
			http.StatusBadRequest,
			[]byte(`{"error":{"message":"reclaude was mentioned without a client restart failure"}}`),
		),
	)
}

func TestClientSafeUpstreamErrorMessage_RedactsRestrictedIdentity(t *testing.T) {
	for _, message := range []string{
		"reclaude 客户端状态异常",
		"ReClaude client status unavailable",
		`{"error":{"message":"RECLAUDE failed"}}`,
	} {
		require.Equal(t, DeviceAuthorizationUnavailableClientMessage, ClientSafeUpstreamErrorMessage(message))
	}
	require.Equal(t, "ordinary upstream error", ClientSafeUpstreamErrorMessage("ordinary upstream error"))
}

func TestRateLimitService_DeviceAuthorizationRevokedDisablesPoolAccount(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       901,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}
	body := []byte(`{"error":{"message":"reclaude 客户端状态异常，请重启 reclaude 后重试"}}`)

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusBadRequest, http.Header{}, body)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.NotContains(t, strings.ToLower(repo.lastErrorMsg), "reclaude")
}

func TestGatewayHandleErrorResponse_RedactsRestrictedIdentityBeforeRaw400Passthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := &GatewayService{}
	account := &Account{ID: 903, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"message":"ReClaude upstream temporarily unavailable"}}`)
	require.Equal(t, UpstreamFailureNone, classifyUpstreamFailure(account.Platform, http.StatusBadRequest, body))
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"X-Request-Id": []string{"req-redacted"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}

	_, err := svc.handleErrorResponse(context.Background(), resp, c, account)

	require.Error(t, err)
	require.NotContains(t, strings.ToLower(err.Error()), "reclaude")
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.NotContains(t, strings.ToLower(recorder.Body.String()), "reclaude")
	require.Contains(t, recorder.Body.String(), DeviceAuthorizationUnavailableClientMessage)
}

func TestGatewayHandleErrorResponse_ClassifiesDeviceAuthorizationBeforeRaw400Passthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	repo := &rateLimitAccountRepoStub{}
	rateLimitSvc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &GatewayService{rateLimitService: rateLimitSvc}
	account := &Account{
		ID:       902,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(http.StatusUnauthorized)},
		},
	}
	require.True(t, svc.shouldRetryUpstreamError(account, http.StatusBadRequest), "default API-key policy would otherwise route this 400 through generic retry handling")
	body := []byte(`{"error":{"message":"此设备已被解绑，请运行 private-upstream-cli 登录"}}`)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"X-Request-Id": []string{"req-test"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}

	_, err := svc.handleErrorResponse(context.Background(), resp, c, account)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, UpstreamFailureDeviceAuthorizationRevoked, failoverErr.Kind)
	require.Equal(t, http.StatusBadRequest, failoverErr.StatusCode)
	require.Equal(t, 0, recorder.Body.Len(), "service must not write the raw 400 before handler failover")
}

func TestGatewayForward_DeviceAuthorizationBypassesGenericAPIKeyRetryPolicy(t *testing.T) {
	c, recorder, parsed := gatewayHTTPRetryRequest(t)
	upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{{
		response: func() *http.Response {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(bytes.NewBufferString(
					`{"error":{"message":"reclaude 客户端状态异常，请重启 reclaude 后重试"}}`,
				)),
			}
		},
	}}}
	repo := &rateLimitAccountRepoStub{}
	svc := claudeCodeCacheSafeRetryService(upstream)
	svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := gatewayHTTPRetryAccount()
	account.Credentials["custom_error_codes_enabled"] = true
	account.Credentials["custom_error_codes"] = []any{float64(http.StatusUnauthorized)}
	require.True(t, svc.shouldRetryUpstreamError(account, http.StatusBadRequest))

	result, err := svc.Forward(c.Request.Context(), c, account, parsed)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, UpstreamFailureDeviceAuthorizationRevoked, failoverErr.Kind)
	require.Equal(t, 1, upstream.calls, "a revoked device authorization must not be retried on the same account")
	require.Equal(t, 1, repo.setErrorCalls)
	require.Empty(t, recorder.Body.String())
}
