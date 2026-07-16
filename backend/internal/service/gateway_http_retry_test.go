package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type scriptedGatewayHTTPResult struct {
	response func() *http.Response
	err      error
}

type scriptedGatewayHTTPUpstream struct {
	results []scriptedGatewayHTTPResult
	calls   int
	bodies  [][]byte
}

func (u *scriptedGatewayHTTPUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, accountConcurrency, false)
}

func (u *scriptedGatewayHTTPUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ bool) (*http.Response, error) {
	u.calls++
	if req != nil && req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		u.bodies = append(u.bodies, append([]byte(nil), body...))
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	idx := u.calls - 1
	if idx >= len(u.results) {
		idx = len(u.results) - 1
	}
	result := u.results[idx]
	if result.response == nil {
		return nil, result.err
	}
	return result.response(), result.err
}

func gatewayHTTPRetryRequest(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *ParsedRequest) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	body := []byte(`{"model":"claude-fable-5","stream":false,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	parsed, err := ParseGatewayRequest(body, PlatformAnthropic)
	require.NoError(t, err)
	return c, rec, parsed
}

func gatewayHTTPRetryAccount() *Account {
	return &Account{
		ID:          902,
		Name:        "gateway-http-retry",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "test-key"},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func gatewayHTTPErrorResponse(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"req_retry_error"}},
		Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"message":"temporary upstream failure"}}`)),
	}
}

func gatewayHTTPSuccessResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"req_retry_success"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"msg_retry","type":"message","role":"assistant","model":"claude-fable-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`,
		)),
	}
}

func TestGatewayService_HTTPServerErrorRetriesInitialAccountOnce(t *testing.T) {
	c, rec, parsed := gatewayHTTPRetryRequest(t)
	upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{
		{response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusServiceUnavailable) }},
		{response: gatewayHTTPSuccessResponse},
	}}
	svc := claudeCodeCacheSafeRetryService(upstream)

	result, err := svc.Forward(c.Request.Context(), c, gatewayHTTPRetryAccount(), parsed)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 2, upstream.calls)
	require.Len(t, upstream.bodies, 2)
	require.Equal(t, upstream.bodies[0], upstream.bodies[1])

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "same_account_5xx_retry", events[0].Kind)
}

func TestGatewayService_HTTPServerErrorSecondFailureFailsOver(t *testing.T) {
	c, _, parsed := gatewayHTTPRetryRequest(t)
	upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{
		{response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusInternalServerError) }},
		{response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusInternalServerError) }},
	}}
	svc := claudeCodeCacheSafeRetryService(upstream)

	result, err := svc.Forward(c.Request.Context(), c, gatewayHTTPRetryAccount(), parsed)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusInternalServerError, failoverErr.StatusCode)
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, 2, upstream.calls)
}

func TestGatewayService_HTTPServerErrorRetrySurvivesPriorGenericAttempts(t *testing.T) {
	c, rec, parsed := gatewayHTTPRetryRequest(t)
	upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{
		{response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusTeapot) }},
		{response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusTeapot) }},
		{response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusTeapot) }},
		{response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusTeapot) }},
		{response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusServiceUnavailable) }},
		{response: gatewayHTTPSuccessResponse},
	}}
	svc := claudeCodeCacheSafeRetryService(upstream)
	account := gatewayHTTPRetryAccount()
	account.Credentials["custom_error_codes_enabled"] = true
	account.Credentials["custom_error_codes"] = []any{float64(http.StatusBadRequest)}

	result, err := svc.Forward(c.Request.Context(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 6, upstream.calls)
	require.Len(t, upstream.bodies, 6)
	for i := 1; i < len(upstream.bodies); i++ {
		require.Equal(t, upstream.bodies[0], upstream.bodies[i])
	}
}

func TestGatewayService_HTTPServerErrorDoesNotRetryAfterAccountSwitch(t *testing.T) {
	c, _, parsed := gatewayHTTPRetryRequest(t)
	upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{
		{response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusBadGateway) }},
	}}
	svc := claudeCodeCacheSafeRetryService(upstream)
	ctx := WithAccountSwitchCount(context.Background(), 1, false)

	result, err := svc.Forward(ctx, c, gatewayHTTPRetryAccount(), parsed)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, 1, upstream.calls)
}

func TestGatewayService_TransportErrorIsNotReplayed(t *testing.T) {
	c, _, parsed := gatewayHTTPRetryRequest(t)
	upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{{
		err: errors.New("connection reset before response"),
	}}}
	svc := claudeCodeCacheSafeRetryService(upstream)

	result, err := svc.Forward(c.Request.Context(), c, gatewayHTTPRetryAccount(), parsed)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, 1, upstream.calls)
}

func TestGatewayService_AnthropicPassthroughHTTPServerErrorRetriesOnce(t *testing.T) {
	c, _, parsed := gatewayHTTPRetryRequest(t)
	upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{
		{response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusServiceUnavailable) }},
		{response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusServiceUnavailable) }},
	}}
	svc := claudeCodeCacheSafeRetryService(upstream)

	result, err := svc.forwardAnthropicAPIKeyPassthroughWithInput(
		c.Request.Context(),
		c,
		gatewayHTTPRetryAccount(),
		anthropicPassthroughForwardInput{
			Body:          parsed.Body,
			RequestModel:  parsed.Model,
			OriginalModel: parsed.Model,
			RequestStream: parsed.Stream,
			StartTime:     time.Now(),
		},
	)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, 2, upstream.calls)
	require.False(t, failoverErr.RetryableOnSameAccount)
}

func TestGatewayService_AnthropicPassthroughDoesNotRegainRetryAfterAccountSwitch(t *testing.T) {
	c, _, parsed := gatewayHTTPRetryRequest(t)
	upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{{
		response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusServiceUnavailable) },
	}}}
	svc := claudeCodeCacheSafeRetryService(upstream)
	ctx := WithAccountSwitchCount(c.Request.Context(), 1, false)

	result, err := svc.forwardAnthropicAPIKeyPassthroughWithInput(
		ctx,
		c,
		gatewayHTTPRetryAccount(),
		anthropicPassthroughForwardInput{
			Body:          parsed.Body,
			RequestModel:  parsed.Model,
			OriginalModel: parsed.Model,
			RequestStream: parsed.Stream,
			StartTime:     time.Now(),
		},
	)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, 1, upstream.calls)
}

func TestGatewayService_BedrockHTTPServerErrorRetriesOnce(t *testing.T) {
	c, _, parsed := gatewayHTTPRetryRequest(t)
	upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{
		{response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusGatewayTimeout) }},
		{response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusGatewayTimeout) }},
	}}
	svc := claudeCodeCacheSafeRetryService(upstream)
	account := gatewayHTTPRetryAccount()
	account.Type = AccountTypeBedrock
	account.Credentials = map[string]any{"auth_mode": "apikey"}

	resp, err := svc.executeBedrockUpstream(
		c.Request.Context(),
		c,
		account,
		parsed.Body,
		"anthropic.claude-test-v1:0",
		"us-east-1",
		false,
		nil,
		"test-bedrock-key",
		"",
	)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	require.Equal(t, http.StatusGatewayTimeout, resp.StatusCode)
	require.Equal(t, 2, upstream.calls)
}

func TestGatewayService_BedrockDoesNotRegainRetryAfterAccountSwitch(t *testing.T) {
	c, _, parsed := gatewayHTTPRetryRequest(t)
	upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{{
		response: func() *http.Response { return gatewayHTTPErrorResponse(http.StatusGatewayTimeout) },
	}}}
	svc := claudeCodeCacheSafeRetryService(upstream)
	account := gatewayHTTPRetryAccount()
	account.Type = AccountTypeBedrock
	account.Credentials = map[string]any{"auth_mode": "apikey"}
	ctx := WithAccountSwitchCount(c.Request.Context(), 1, false)

	resp, err := svc.executeBedrockUpstream(
		ctx,
		c,
		account,
		parsed.Body,
		"anthropic.claude-test-v1:0",
		"us-east-1",
		false,
		nil,
		"test-bedrock-key",
		"",
	)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	require.Equal(t, http.StatusGatewayTimeout, resp.StatusCode)
	require.Equal(t, 1, upstream.calls)
}

func TestGatewayService_HTTPServerErrorRetryStatusBoundary(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504} {
		require.True(t, isGatewayHTTPSameAccountRetryStatus(status), status)
		require.False(t, isSameAccountReplayExcludedStatus(status), status)
	}
	for _, status := range []int{429, 501, 505, 529} {
		require.False(t, isGatewayHTTPSameAccountRetryStatus(status), status)
		require.True(t, isSameAccountReplayExcludedStatus(status), status)
	}
	c, _ := newOpenAIHTTPRetryContext(http.MethodPost, "/v1/messages", nil)
	c.Writer.WriteHeaderNow()
	require.False(t, shouldRetryGatewayHTTPOnSameAccount(context.Background(), c, http.StatusBadGateway, false))
}

func TestGatewayService_ExcludedStatusesNeverReplayOnSameAccount(t *testing.T) {
	for _, status := range []int{429, 501, 505, 529} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, _, parsed := gatewayHTTPRetryRequest(t)
			upstream := &scriptedGatewayHTTPUpstream{results: []scriptedGatewayHTTPResult{{
				response: func() *http.Response { return gatewayHTTPErrorResponse(status) },
			}}}
			svc := claudeCodeCacheSafeRetryService(upstream)
			account := gatewayHTTPRetryAccount()
			account.Credentials["pool_mode"] = true
			account.Credentials["custom_error_codes_enabled"] = true
			account.Credentials["custom_error_codes"] = []any{float64(http.StatusBadRequest)}

			result, err := svc.Forward(c.Request.Context(), c, account, parsed)
			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, status, failoverErr.StatusCode)
			require.False(t, failoverErr.RetryableOnSameAccount)
			require.Equal(t, 1, upstream.calls)
		})
	}
}
