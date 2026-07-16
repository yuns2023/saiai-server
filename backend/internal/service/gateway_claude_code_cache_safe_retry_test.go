package service

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type queuedClaudeCodeUpstream struct {
	responses []func() *http.Response
	calls     int
	bodies    [][]byte
}

func (u *queuedClaudeCodeUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, accountConcurrency, false)
}

func (u *queuedClaudeCodeUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ bool) (*http.Response, error) {
	u.calls++
	if req != nil && req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		u.bodies = append(u.bodies, body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	idx := u.calls - 1
	if idx >= len(u.responses) {
		idx = len(u.responses) - 1
	}
	return u.responses[idx](), nil
}

func claudeCodeCacheSafeRetryContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *ParsedRequest) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req = req.WithContext(SetClaudeCodeClient(req.Context(), true))
	setRealClaudeCodeRequestHeaders(req, "claude-cli/2.1.202 (external, cli)")
	c.Request = req

	userID := FormatMetadataUserID(
		strings.Repeat("a", 64),
		"",
		"11111111-2222-4333-8444-555555555555",
		"2.1.202",
	)
	body := []byte(`{"model":"claude-fable-5","stream":true,"metadata":{"user_id":` + strconvQuote(userID) + `},"max_tokens":64000,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.202.000; cc_entrypoint=cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"cached prompt","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`)
	body = expectedOAuthBillingBodyForTest(t, body, req.Header.Get("User-Agent"))
	parsed, err := ParseGatewayRequest(body, PlatformAnthropic)
	require.NoError(t, err)
	return c, rec, parsed
}

func claudeCodeCacheSafeRetryService(upstream HTTPUpstream) *GatewayService {
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		},
	}
	return &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		identityService:      NewIdentityService(&identityCacheStub{}, strings.Repeat("x", 32)),
		deferredService:      &DeferredService{},
	}
}

func claudeCodeCacheSafeRetryAccount() *Account {
	return &Account{
		ID:          211,
		Name:        "claude-code-cache-safe",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeSetupToken,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "setup-token",
		},
		Extra: map[string]any{
			"claude_oauth_mode": ClaudeOAuthModeCarpool,
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func transient502UnexpectedEOFResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader("unexpected EOF")),
	}
}

func transient502WithUpstreamRequestIDResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusBadGateway,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"x-request-id": []string{"req_general_502"},
		},
		Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"message":"temporary gateway failure"}}`)),
	}
}

func okClaudeStreamResponse() *http.Response {
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-fable-5","content":[],"usage":{"input_tokens":2,"cache_read_input_tokens":1234}}}`,
		"",
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}`,
		"",
		`data: {"type":"message_delta","usage":{"output_tokens":3}}`,
		"",
		`data: {"type":"message_stop"}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"req_retry_success"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

func TestGatewayService_ClaudeCodeCacheSafeTransient502RetriesSameAccountOnce(t *testing.T) {
	c, rec, parsed := claudeCodeCacheSafeRetryContext(t)
	upstream := &queuedClaudeCodeUpstream{responses: []func() *http.Response{
		transient502UnexpectedEOFResponse,
		okClaudeStreamResponse,
	}}
	svc := claudeCodeCacheSafeRetryService(upstream)

	result, err := svc.Forward(c.Request.Context(), c, claudeCodeCacheSafeRetryAccount(), parsed)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, upstream.calls)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1234, result.Usage.CacheReadInputTokens)

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "cache_safe_same_account_retry", events[0].Kind)
	require.Equal(t, int64(211), events[0].AccountID)
}

func TestGatewayService_ClaudeCodeCacheSafeTransient502PersistsReturnsClientRetryableError(t *testing.T) {
	c, rec, parsed := claudeCodeCacheSafeRetryContext(t)
	upstream := &queuedClaudeCodeUpstream{responses: []func() *http.Response{
		transient502UnexpectedEOFResponse,
		transient502UnexpectedEOFResponse,
	}}
	svc := claudeCodeCacheSafeRetryService(upstream)

	result, err := svc.Forward(c.Request.Context(), c, claudeCodeCacheSafeRetryAccount(), parsed)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, 2, upstream.calls)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "please retry")

	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	var clientRetryErr *ClientRetryableUpstreamError
	require.ErrorAs(t, err, &clientRetryErr)
	require.Equal(t, http.StatusBadGateway, clientRetryErr.StatusCode)

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 2)
	require.Equal(t, "cache_safe_same_account_retry", events[0].Kind)
	require.Equal(t, "cache_safe_passthrough", events[1].Kind)
}

func TestGatewayService_ClaudeCodeCacheSafe502DoesNotReplayAfterAccountSwitch(t *testing.T) {
	c, rec, parsed := claudeCodeCacheSafeRetryContext(t)
	upstream := &queuedClaudeCodeUpstream{responses: []func() *http.Response{
		transient502UnexpectedEOFResponse,
		okClaudeStreamResponse,
	}}
	svc := claudeCodeCacheSafeRetryService(upstream)
	ctx := WithAccountSwitchCount(c.Request.Context(), 1, false)

	result, err := svc.Forward(ctx, c, claudeCodeCacheSafeRetryAccount(), parsed)
	require.Nil(t, result)
	var clientRetryErr *ClientRetryableUpstreamError
	require.ErrorAs(t, err, &clientRetryErr)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, 1, upstream.calls)

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "cache_safe_passthrough", events[0].Kind)
}

func TestGatewayService_ClaudeCode502RetryModesShareOneReplayBudget(t *testing.T) {
	c, rec, parsed := claudeCodeCacheSafeRetryContext(t)
	upstream := &queuedClaudeCodeUpstream{responses: []func() *http.Response{
		transient502WithUpstreamRequestIDResponse,
		transient502UnexpectedEOFResponse,
		okClaudeStreamResponse,
	}}
	svc := claudeCodeCacheSafeRetryService(upstream)

	result, err := svc.Forward(c.Request.Context(), c, claudeCodeCacheSafeRetryAccount(), parsed)
	require.Nil(t, result)
	var clientRetryErr *ClientRetryableUpstreamError
	require.ErrorAs(t, err, &clientRetryErr)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, 2, upstream.calls, "specialized and general 502 handling must share one replay budget")

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 2)
	require.Equal(t, "same_account_5xx_retry", events[0].Kind)
	require.Equal(t, "cache_safe_passthrough", events[1].Kind)
}
