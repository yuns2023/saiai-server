package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIHTTPRetryStep struct {
	status int
	body   string
	err    error
}

type openAIHTTPRetryUpstream struct {
	steps         []openAIHTTPRetryStep
	calls         int
	requestBodies [][]byte
}

func (u *openAIHTTPRetryUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	if req != nil && req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		u.requestBodies = append(u.requestBodies, body)
		_ = req.Body.Close()
	}
	if u.calls > len(u.steps) {
		return nil, fmt.Errorf("unexpected upstream call %d", u.calls)
	}
	step := u.steps[u.calls-1]
	if step.err != nil {
		return nil, step.err
	}
	return &http.Response{
		StatusCode: step.status,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{fmt.Sprintf("req-%d", u.calls)},
		},
		Body: io.NopCloser(bytes.NewBufferString(step.body)),
	}, nil
}

func (u *openAIHTTPRetryUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ bool) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func newOpenAIHTTPRetryContext(method, path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, bytes.NewReader(body))
	return c, recorder
}

func openAIHTTPRetryTestAccount() *Account {
	return &Account{
		ID:       801,
		Name:     "openai-retry-test",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":   "upstream-test-key",
			"pool_mode": true,
		},
	}
}

func openAIHTTPRetryTestService(upstream HTTPUpstream) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		httpUpstream:     upstream,
		rateLimitService: &RateLimitService{},
		cfg: &config.Config{Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		}},
	}
}

func openAIHTTPRetryEvents(t *testing.T, c *gin.Context) []*OpsUpstreamErrorEvent {
	t.Helper()
	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	return events
}

func TestShouldRetryOpenAIHTTPOnSameAccount_ExplicitStatusesAndGuards(t *testing.T) {
	c, _ := newOpenAIHTTPRetryContext(http.MethodPost, "/v1/responses", nil)

	for _, statusCode := range []int{500, 502, 503, 504} {
		require.True(t, shouldRetryOpenAIHTTPOnSameAccount(context.Background(), c, statusCode, false), statusCode)
	}
	for _, statusCode := range []int{501, 505, 529, 429} {
		require.False(t, shouldRetryOpenAIHTTPOnSameAccount(context.Background(), c, statusCode, false), statusCode)
		require.True(t, isSameAccountReplayExcludedStatus(statusCode), statusCode)
	}

	require.False(t, shouldRetryOpenAIHTTPOnSameAccount(context.Background(), c, http.StatusBadGateway, true))
	require.True(t, shouldRetryOpenAIHTTPOnSameAccount(WithAccountSwitchCount(context.Background(), 0, false), c, http.StatusBadGateway, false))
	require.False(t, shouldRetryOpenAIHTTPOnSameAccount(WithAccountSwitchCount(context.Background(), 1, false), c, http.StatusBadGateway, false))

	c.Writer.WriteHeaderNow()
	require.False(t, shouldRetryOpenAIHTTPOnSameAccount(context.Background(), c, http.StatusBadGateway, false))
}

func TestOpenAIGatewayService_Forward_ExcludedStatusesNeverReplayOnSameAccount(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex","stream":false,"input":"hello"}`)
	for _, status := range []int{429, 501, 505, 529} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, _ := newOpenAIHTTPRetryContext(http.MethodPost, "/v1/responses", body)
			upstream := &openAIHTTPRetryUpstream{steps: []openAIHTTPRetryStep{{
				status: status,
				body:   `{"error":{"message":"upstream rejected request"}}`,
			}}}

			result, err := openAIHTTPRetryTestService(upstream).Forward(c.Request.Context(), c, openAIHTTPRetryTestAccount(), body)
			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, status, failoverErr.StatusCode)
			require.False(t, failoverErr.RetryableOnSameAccount)
			require.Equal(t, 1, upstream.calls)
		})
	}
}

func TestOpenAIGatewayService_Forward_HTTP5xxRetriesSameAccountOnceThenSucceeds(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex","stream":false,"input":"hello"}`)
	c, recorder := newOpenAIHTTPRetryContext(http.MethodPost, "/v1/responses", body)
	upstream := &openAIHTTPRetryUpstream{steps: []openAIHTTPRetryStep{
		{status: http.StatusBadGateway, body: `{"error":{"message":"temporary gateway failure"}}`},
		{status: http.StatusOK, body: `{"id":"resp_retry_ok","model":"gpt-5.3-codex","usage":{"input_tokens":1,"output_tokens":1}}`},
	}}

	result, err := openAIHTTPRetryTestService(upstream).Forward(c.Request.Context(), c, openAIHTTPRetryTestAccount(), body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, upstream.calls)
	require.Len(t, upstream.requestBodies, 2)
	require.JSONEq(t, string(upstream.requestBodies[0]), string(upstream.requestBodies[1]))
	require.Equal(t, http.StatusOK, recorder.Code)
	events := openAIHTTPRetryEvents(t, c)
	require.Len(t, events, 1)
	require.Equal(t, "retry", events[0].Kind)
	require.Equal(t, http.StatusBadGateway, events[0].UpstreamStatusCode)
	require.Equal(t, int64(801), events[0].AccountID)
}

func TestOpenAIGatewayService_Forward_HTTP5xxExhaustionFailsOverWithoutPoolRetry(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex","stream":false,"input":"hello"}`)
	c, _ := newOpenAIHTTPRetryContext(http.MethodPost, "/v1/responses", body)
	upstream := &openAIHTTPRetryUpstream{steps: []openAIHTTPRetryStep{
		{status: http.StatusServiceUnavailable, body: `{"error":{"message":"temporarily unavailable"}}`},
		{status: http.StatusServiceUnavailable, body: `{"error":{"message":"still unavailable"}}`},
	}}

	result, err := openAIHTTPRetryTestService(upstream).Forward(c.Request.Context(), c, openAIHTTPRetryTestAccount(), body)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, 2, upstream.calls)
	events := openAIHTTPRetryEvents(t, c)
	require.Len(t, events, 2)
	require.Equal(t, "retry", events[0].Kind)
	require.NotEmpty(t, events[0].UpstreamRequestBody)
	require.Equal(t, "failover", events[1].Kind)
}

func TestOpenAIGatewayService_Forward_HTTP5xxDoesNotRegainBudgetAfterAccountSwitch(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex","stream":false,"input":"hello"}`)
	c, _ := newOpenAIHTTPRetryContext(http.MethodPost, "/v1/responses", body)
	ctx := WithAccountSwitchCount(c.Request.Context(), 1, false)
	c.Request = c.Request.WithContext(ctx)
	upstream := &openAIHTTPRetryUpstream{steps: []openAIHTTPRetryStep{
		{status: http.StatusGatewayTimeout, body: `{"error":{"message":"timeout"}}`},
	}}

	result, err := openAIHTTPRetryTestService(upstream).Forward(ctx, c, openAIHTTPRetryTestAccount(), body)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, 1, upstream.calls)
	events := openAIHTTPRetryEvents(t, c)
	require.Len(t, events, 1)
	require.Equal(t, "failover", events[0].Kind)
}

func TestOpenAIGatewayService_Forward_HTTP5xxRetryBackoffHonorsContext(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex","stream":false,"input":"hello"}`)
	c, _ := newOpenAIHTTPRetryContext(http.MethodPost, "/v1/responses", body)
	ctx, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	upstream := &openAIHTTPRetryUpstream{steps: []openAIHTTPRetryStep{
		{status: http.StatusBadGateway, body: `{"error":{"message":"temporary failure"}}`},
	}}
	cancel()

	result, err := openAIHTTPRetryTestService(upstream).Forward(ctx, c, openAIHTTPRetryTestAccount(), body)

	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, upstream.calls)
	events := openAIHTTPRetryEvents(t, c)
	require.Len(t, events, 1)
	require.Equal(t, "retry", events[0].Kind)
}

func TestOpenAIGatewayService_Forward_HTTP5xxBudgetDoesNotMultiplyBodyRecovery(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex","stream":false,"previous_response_id":"resp_stale","input":"hello"}`)
	c, _ := newOpenAIHTTPRetryContext(http.MethodPost, "/v1/responses", body)
	upstream := &openAIHTTPRetryUpstream{steps: []openAIHTTPRetryStep{
		{status: http.StatusBadRequest, body: `{"error":{"code":"previous_response_not_found","message":"stale response"}}`},
		{status: http.StatusInternalServerError, body: `{"error":{"message":"temporary failure"}}`},
		{status: http.StatusInternalServerError, body: `{"error":{"message":"persistent failure"}}`},
	}}
	account := openAIHTTPRetryTestAccount()
	account.Type = AccountTypeOAuth
	account.Credentials = map[string]any{"access_token": "oauth-test-token"}

	result, err := openAIHTTPRetryTestService(upstream).Forward(c.Request.Context(), c, account, body)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, 3, upstream.calls)
	require.Len(t, upstream.requestBodies, 3)
	require.Contains(t, string(upstream.requestBodies[0]), "previous_response_id")
	require.NotContains(t, string(upstream.requestBodies[1]), "previous_response_id")
	require.NotContains(t, string(upstream.requestBodies[2]), "previous_response_id")
}

func TestOpenAIGatewayService_ForwardAsAnthropic_HTTP5xxRetriesOnceThenFailsOver(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	c, _ := newOpenAIHTTPRetryContext(http.MethodPost, "/v1/messages", body)
	upstream := &openAIHTTPRetryUpstream{steps: []openAIHTTPRetryStep{
		{status: http.StatusGatewayTimeout, body: `{"error":{"message":"temporary timeout"}}`},
		{status: http.StatusGatewayTimeout, body: `{"error":{"message":"persistent timeout"}}`},
	}}

	result, err := openAIHTTPRetryTestService(upstream).ForwardAsAnthropic(c.Request.Context(), c, openAIHTTPRetryTestAccount(), body, "cache-key")

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusGatewayTimeout, failoverErr.StatusCode)
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, 2, upstream.calls)
	require.Len(t, upstream.requestBodies, 2)
	require.JSONEq(t, string(upstream.requestBodies[0]), string(upstream.requestBodies[1]))
	events := openAIHTTPRetryEvents(t, c)
	require.Len(t, events, 2)
	require.Equal(t, "retry", events[0].Kind)
	require.NotEmpty(t, events[0].UpstreamRequestBody)
	require.Equal(t, "failover", events[1].Kind)
}

func TestOpenAIGatewayService_ForwardAsAnthropic_DoesNotRegainRetryAfterAccountSwitch(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	c, _ := newOpenAIHTTPRetryContext(http.MethodPost, "/v1/messages", body)
	ctx := WithAccountSwitchCount(c.Request.Context(), 1, false)
	upstream := &openAIHTTPRetryUpstream{steps: []openAIHTTPRetryStep{{
		status: http.StatusGatewayTimeout,
		body:   `{"error":{"message":"timeout"}}`,
	}}}

	result, err := openAIHTTPRetryTestService(upstream).ForwardAsAnthropic(ctx, c, openAIHTTPRetryTestAccount(), body, "cache-key")
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, 1, upstream.calls)
}

func TestOpenAIGatewayService_ForwardAsAnthropic_ExcludedStatusesNeverReplay(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	for _, status := range []int{429, 501, 505, 529} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, _ := newOpenAIHTTPRetryContext(http.MethodPost, "/v1/messages", body)
			upstream := &openAIHTTPRetryUpstream{steps: []openAIHTTPRetryStep{{
				status: status,
				body:   `{"error":{"message":"upstream rejected request"}}`,
			}}}

			result, err := openAIHTTPRetryTestService(upstream).ForwardAsAnthropic(c.Request.Context(), c, openAIHTTPRetryTestAccount(), body, "")
			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, status, failoverErr.StatusCode)
			require.False(t, failoverErr.RetryableOnSameAccount)
			require.Equal(t, 1, upstream.calls)
		})
	}
}

func TestOpenAIGatewayService_ForwardAsAnthropic_TransportErrorIsNotReplayed(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	c, recorder := newOpenAIHTTPRetryContext(http.MethodPost, "/v1/messages", body)
	upstream := &openAIHTTPRetryUpstream{steps: []openAIHTTPRetryStep{
		{err: errors.New("dial failed")},
	}}

	result, err := openAIHTTPRetryTestService(upstream).ForwardAsAnthropic(c.Request.Context(), c, openAIHTTPRetryTestAccount(), body, "")

	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	events := openAIHTTPRetryEvents(t, c)
	require.Len(t, events, 1)
	require.Equal(t, "request_error", events[0].Kind)
}
