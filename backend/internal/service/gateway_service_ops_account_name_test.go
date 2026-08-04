package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fixedOpsAccountNameUpstream struct {
	resp *http.Response
}

func (u *fixedOpsAccountNameUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, accountConcurrency, false)
}

func (u *fixedOpsAccountNameUpstream) DoWithTLS(_ *http.Request, _ string, _ int64, _ int, _ bool) (*http.Response, error) {
	return u.resp, nil
}

func TestGatewayService_HandleErrorResponseForModelSerializesOpsAccountIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	account := &Account{ID: 46703, Name: "anthropic-account", Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"x-request-id": []string{"req_01account_name"}},
		Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"fallbacks: Extra inputs are not permitted"}}`)),
	}

	result, err := (&GatewayService{}).handleErrorResponseForModel(context.Background(), resp, c, account, "claude-fable-5")
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	requireSerializedOpsAccountIdentity(t, c, "http_error", account.ID, account.Name)
}

func TestGatewayService_ForwardFailoverSerializesOpsAccountIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := []byte(`{"model":"claude-fable-5","stream":false,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	parsed, err := ParseGatewayRequest(body, PlatformAnthropic)
	require.NoError(t, err)

	cfg := &config.Config{}
	svc := &GatewayService{
		cfg:                  cfg,
		httpUpstream:         &fixedOpsAccountNameUpstream{resp: &http.Response{StatusCode: http.StatusNotImplemented, Header: http.Header{"x-request-id": []string{"req_01failover"}}, Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"message":"upstream failed"}}`))}},
		rateLimitService:     &RateLimitService{},
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
	}
	account := &Account{
		ID:          88,
		Name:        "failover-account",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "TEST_ONLY_API_KEY"},
		Concurrency: 1,
	}

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusNotImplemented, failoverErr.StatusCode)
	requireSerializedOpsAccountIdentity(t, c, "failover", account.ID, account.Name)
}

func TestGatewayService_HandleRetryExhaustedSerializesOpsAccountIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	account := &Account{ID: 99, Name: "retry-account", Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     http.Header{"x-request-id": []string{"req_01retry"}},
		Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"message":"retry failed"}}`)),
	}

	result, err := (&GatewayService{}).handleRetryExhaustedError(context.Background(), resp, c, account)
	require.Nil(t, result)
	require.Error(t, err)
	requireSerializedOpsAccountIdentity(t, c, "retry_exhausted", account.ID, account.Name)
}

func requireSerializedOpsAccountIdentity(t *testing.T, c *gin.Context, wantKind string, wantID int64, wantName string) {
	t.Helper()

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, wantKind, events[0].Kind)
	require.Equal(t, wantID, events[0].AccountID)
	require.Equal(t, wantName, events[0].AccountName)

	serialized := marshalOpsUpstreamErrors(events)
	require.NotNil(t, serialized)
	var payload []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(*serialized), &payload))
	require.Len(t, payload, 1)
	require.Contains(t, payload[0], "account_id")
	require.Contains(t, payload[0], "account_name")
	var gotID int64
	var gotName string
	require.NoError(t, json.Unmarshal(payload[0]["account_id"], &gotID))
	require.NoError(t, json.Unmarshal(payload[0]["account_name"], &gotName))
	require.Equal(t, wantID, gotID)
	require.Equal(t, wantName, gotName)
}
