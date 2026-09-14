package service

import (
	"context"
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

func TestOpenAIContinuationAccountBoundary(t *testing.T) {
	ctx := context.Background()
	groupID := int64(17)
	store := NewOpenAIWSStateStore(nil)
	svc := &OpenAIGatewayService{openaiWSStateStore: store}
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_from_a", 11, time.Hour))
	otherGroupOwner, err := store.GetResponseAccount(ctx, groupID+1, "resp_from_a")
	require.NoError(t, err)
	require.Zero(t, otherGroupOwner, "a response binding must not cross group boundaries")

	owner, err := svc.OpenAIContinuationAccountID(ctx, &groupID, "resp_from_a")
	require.NoError(t, err)
	require.Equal(t, int64(11), owner)
	require.True(t, OpenAIContinuationAccountMatches("resp_from_a", owner, 11))
	require.False(t, OpenAIContinuationAccountMatches("resp_from_a", owner, 22))
	require.False(t, OpenAIContinuationAccountMatches("resp_unknown", 0, 22))
	require.True(t, OpenAIContinuationAccountMatches("", 0, 22))
}

func TestOpenAIContinuationRejectsScheduledAccountSwitch(t *testing.T) {
	ctx := context.Background()
	groupID := int64(17)
	rateLimitedUntil := time.Now().Add(time.Hour)
	accountA := Account{
		ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		RateLimitResetAt: &rateLimitedUntil,
		Extra:            map[string]any{"responses_websockets_v2_enabled": true},
	}
	accountB := Account{
		ID: 22, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Extra: map[string]any{"responses_websockets_v2_enabled": true},
	}
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{accountA, accountB}},
		cache:              cache,
		cfg:                newOpenAIWSV2TestConfig(),
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
		openaiWSStateStore: store,
	}
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_from_a", accountA.ID, time.Hour))
	owner, err := svc.OpenAIContinuationAccountID(ctx, &groupID, "resp_from_a")
	require.NoError(t, err)
	require.Equal(t, accountA.ID, owner)

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "resp_from_a", "session-from-a", "gpt-5.1", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	if selection.ReleaseFunc != nil {
		defer selection.ReleaseFunc()
	}
	require.Equal(t, accountB.ID, selection.Account.ID)
	require.False(t, OpenAIContinuationAccountMatches("resp_from_a", owner, selection.Account.ID))
	retainedOwner, err := svc.OpenAIContinuationAccountID(ctx, &groupID, "resp_from_a")
	require.NoError(t, err)
	require.Equal(t, accountA.ID, retainedOwner, "failed scheduling must not erase response ownership")
}

func TestOpenAIHTTPResponseBindsAccountBeforeContinuation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(17)
	responseID := "resp_from_a_http"
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session_id", "one-client-session")
	c.Set("api_key", &APIKey{GroupID: &groupID})

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_from_a_http","usage":{"input_tokens":1,"output_tokens":1}}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}
	accountA := &Account{
		ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-only-key"},
	}
	_, err := svc.Forward(context.Background(), c, accountA, []byte(`{"model":"gpt-5.1","stream":false,"input":[]}`))
	require.NoError(t, err)

	owner, err := svc.OpenAIContinuationAccountID(context.Background(), &groupID, responseID)
	require.NoError(t, err)
	require.Equal(t, accountA.ID, owner)
	require.False(t, OpenAIContinuationAccountMatches(responseID, owner, 22))
}

func TestOpenAIHTTPStreamingResponseBindsAccountAfterCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(17)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session_id", "one-client-session")
	c.Set("api_key", &APIKey{GroupID: &groupID})

	stream := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_created_interim\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_stream_a\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(stream)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}
	accountA := &Account{
		ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-only-key"},
	}
	_, err := svc.Forward(context.Background(), c, accountA, []byte(`{"model":"gpt-5.1","stream":true,"input":[]}`))
	require.NoError(t, err)
	owner, err := svc.OpenAIContinuationAccountID(context.Background(), &groupID, "resp_stream_a")
	require.NoError(t, err)
	require.Equal(t, accountA.ID, owner)
	interimOwner, err := svc.OpenAIContinuationAccountID(context.Background(), &groupID, "resp_created_interim")
	require.NoError(t, err)
	require.Zero(t, interimOwner)
}

func TestOpenAIFailedStreamDoesNotBindResponseAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(17)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{GroupID: &groupID})
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_failed_a\"}}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed_a\"}}\n\n")),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}
	accountA := &Account{
		ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-only-key"},
	}
	_, err := svc.Forward(context.Background(), c, accountA, []byte(`{"model":"gpt-5.1","stream":true,"input":[]}`))
	require.NoError(t, err)
	owner, err := svc.OpenAIContinuationAccountID(context.Background(), &groupID, "resp_failed_a")
	require.NoError(t, err)
	require.Zero(t, owner)
}

func TestOpenAITurnStateNeverCrossesOAuthAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := NewOpenAIWSStateStore(nil)
	svc := &OpenAIGatewayService{openaiWSStateStore: store}
	accountA := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	accountB := &Account{ID: 22, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	sessionHash := "one-client-session"
	store.BindSessionTurnState(17, openAIWSAccountTurnStateSessionHash(5, accountA.ID, sessionHash), "state-from-a", time.Hour)

	require.Equal(t, "state-from-a", svc.resolveOpenAIWSTurnStateForAccount(accountA, 17, 5, sessionHash, "state-from-a"))
	require.Empty(t, svc.resolveOpenAIWSTurnStateForAccount(accountB, 17, 5, sessionHash, "state-from-a"))
	require.Empty(t, svc.resolveOpenAIWSTurnStateForAccount(accountA, 17, 6, sessionHash, "state-from-a"), "a different SAIAI Key must not retrieve the cached token")
	store.BindSessionTurnState(17, openAIWSAccountTurnStateSessionHash(5, accountB.ID, sessionHash), "state-from-b", time.Hour)
	require.Equal(t, "state-from-b", svc.resolveOpenAIWSTurnStateForAccount(accountB, 17, 5, sessionHash, "state-from-a"))

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session_id", "one-client-session")
	c.Request.Header.Set(openAIWSTurnStateHeader, "state-from-a")
	c.Request.Header.Set("User-Agent", "codex_exec/0.154.0")
	store.BindSessionTurnState(0, openAIWSAccountTurnStateSessionHash(0, accountB.ID, svc.GenerateSessionHash(c, nil)), "state-from-b", time.Hour)
	request, err := svc.buildUpstreamRequest(context.Background(), c, accountB, []byte(`{"model":"gpt-5.1"}`), "test-token", false, "", true)
	require.NoError(t, err)
	require.Equal(t, "state-from-b", request.Header.Get(openAIWSTurnStateHeader))
	requestA, err := svc.buildUpstreamRequest(context.Background(), c, accountA, []byte(`{"model":"gpt-5.1"}`), "test-token", false, "", true)
	require.NoError(t, err)
	require.NotEqual(t, requestA.Header.Get("session_id"), request.Header.Get("session_id"))
	wsState := svc.resolveOpenAIWSTurnStateForAccount(accountB, 0, 0, svc.GenerateSessionHash(c, nil), c.GetHeader(openAIWSTurnStateHeader))
	wsHeaders, _ := svc.buildOpenAIWSHeaders(c, accountB, "test-token", OpenAIWSProtocolDecision{}, true, wsState, "", "")
	require.Equal(t, "state-from-b", wsHeaders.Get(openAIWSTurnStateHeader))
	wsHeadersA, _ := svc.buildOpenAIWSHeaders(c, accountA, "test-token", OpenAIWSProtocolDecision{}, true, "state-from-a", "", "")
	require.NotEqual(t, wsHeadersA.Get("session_id"), wsHeaders.Get("session_id"))

	groupID := int64(17)
	c.Set("api_key", &APIKey{ID: 5, GroupID: &groupID})
	keyedHash := svc.GenerateSessionHash(c, nil)
	store.BindSessionTurnState(groupID, openAIWSAccountTurnStateSessionHash(5, accountA.ID, keyedHash), "state-from-a", time.Hour)
	owned, err := svc.buildUpstreamRequest(context.Background(), c, accountA, []byte(`{"model":"gpt-5.1"}`), "test-token", false, "", true)
	require.NoError(t, err)
	require.Equal(t, "state-from-a", owned.Header.Get(openAIWSTurnStateHeader))
	c.Set("api_key", &APIKey{ID: 6, GroupID: &groupID})
	otherKey, err := svc.buildUpstreamRequest(context.Background(), c, accountA, []byte(`{"model":"gpt-5.1"}`), "test-token", false, "", true)
	require.NoError(t, err)
	require.Empty(t, otherKey.Header.Get(openAIWSTurnStateHeader))
}
