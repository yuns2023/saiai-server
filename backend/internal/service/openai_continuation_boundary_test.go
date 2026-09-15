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

type groupScopedOpenAIAccountRepo struct {
	stubOpenAIAccountRepo
	byGroup map[int64][]Account
}

func (r groupScopedOpenAIAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]Account, error) {
	var result []Account
	for _, account := range r.byGroup[groupID] {
		if account.Platform == platform {
			result = append(result, account)
		}
	}
	return result, nil
}

func TestOpenAIContinuationAccountBoundary(t *testing.T) {
	ctx := context.Background()
	userID := int64(55)
	store := NewOpenAIWSStateStore(nil)
	svc := &OpenAIGatewayService{openaiWSStateStore: store}
	require.NoError(t, store.BindResponseAccountForUser(ctx, userID, "resp_from_a", 11, time.Hour))
	otherUserOwner, err := store.GetResponseAccountForUser(ctx, userID+1, "resp_from_a")
	require.NoError(t, err)
	require.Zero(t, otherUserOwner, "a response binding must not cross user boundaries")

	owner, err := svc.OpenAIContinuationAccountID(ctx, userID, "resp_from_a")
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
	userID := int64(55)
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
	require.NoError(t, store.BindResponseAccountForUser(ctx, userID, "resp_from_a", accountA.ID, time.Hour))
	owner, err := svc.OpenAIContinuationAccountID(ctx, userID, "resp_from_a")
	require.NoError(t, err)
	require.Equal(t, accountA.ID, owner)

	selection, _, err := svc.SelectAccountWithSchedulerForUser(ctx, &groupID, userID, "resp_from_a", "session-from-a", "gpt-5.1", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	if selection.ReleaseFunc != nil {
		defer selection.ReleaseFunc()
	}
	require.Equal(t, accountB.ID, selection.Account.ID)
	require.False(t, OpenAIContinuationAccountMatches("resp_from_a", owner, selection.Account.ID))
	retainedOwner, err := svc.OpenAIContinuationAccountID(ctx, userID, "resp_from_a")
	require.NoError(t, err)
	require.Equal(t, accountA.ID, retainedOwner, "failed scheduling must not erase response ownership")
}

func TestOpenAIContinuationCrossGroupRequiresSameAvailableAccount(t *testing.T) {
	ctx := context.Background()
	userID := int64(55)
	groupA := int64(17)
	groupB := int64(18)
	groupWithoutAccount := int64(19)
	accountA := Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	accountB := Account{ID: 22, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	repo := groupScopedOpenAIAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{accountA, accountB}},
		byGroup: map[int64][]Account{
			groupA:              {accountA},
			groupB:              {accountA, accountB},
			groupWithoutAccount: {accountB},
		},
	}
	store := NewOpenAIWSStateStore(nil)
	svc := &OpenAIGatewayService{
		accountRepo:        repo,
		cfg:                newOpenAIWSV2TestConfig(),
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
		openaiWSStateStore: store,
	}
	require.NoError(t, store.BindResponseAccountForUser(ctx, userID, "resp_cross_group", accountA.ID, time.Hour))

	selection, _, err := svc.SelectAccountWithSchedulerForUser(ctx, &groupB, userID, "resp_cross_group", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, accountA.ID, selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}

	selection, _, err = svc.SelectAccountWithSchedulerForUser(ctx, &groupWithoutAccount, userID, "resp_cross_group", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, accountB.ID, selection.Account.ID)
	require.False(t, OpenAIContinuationAccountMatches("resp_cross_group", accountA.ID, selection.Account.ID))
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIHTTPResponseBindsAccountBeforeContinuation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(17)
	responseID := "resp_from_a_http"
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session_id", "one-client-session")
	c.Set("api_key", &APIKey{ID: 5, UserID: 55, GroupID: &groupID})

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

	owner, err := svc.OpenAIContinuationAccountID(context.Background(), 55, responseID)
	require.NoError(t, err)
	require.Equal(t, accountA.ID, owner)
	require.False(t, OpenAIContinuationAccountMatches(responseID, owner, 22))
	otherUserOwner, err := svc.OpenAIContinuationAccountID(context.Background(), 56, responseID)
	require.NoError(t, err)
	require.Zero(t, otherUserOwner)
}

func TestOpenAIHTTPStreamingResponseBindsAccountAfterCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(17)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session_id", "one-client-session")
	c.Set("api_key", &APIKey{ID: 5, UserID: 55, GroupID: &groupID})

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
	owner, err := svc.OpenAIContinuationAccountID(context.Background(), 55, "resp_stream_a")
	require.NoError(t, err)
	require.Equal(t, accountA.ID, owner)
	interimOwner, err := svc.OpenAIContinuationAccountID(context.Background(), 55, "resp_created_interim")
	require.NoError(t, err)
	require.Zero(t, interimOwner)
}

func TestOpenAIFailedStreamDoesNotBindResponseAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(17)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 5, UserID: 55, GroupID: &groupID})
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
	owner, err := svc.OpenAIContinuationAccountID(context.Background(), 55, "resp_failed_a")
	require.NoError(t, err)
	require.Zero(t, owner)
}

func TestOpenAITurnStateNeverCrossesOAuthAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := NewOpenAIWSStateStore(nil)
	svc := &OpenAIGatewayService{openaiWSStateStore: store}
	accountA := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	accountB := &Account{ID: 22, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	userID := int64(55)
	sessionHash := "one-client-session"
	store.BindSessionTurnState(0, openAIWSUserTurnStateSessionHash(userID, accountA.ID, sessionHash), "state-from-a", time.Hour)

	require.Equal(t, "state-from-a", svc.resolveOpenAIWSTurnStateForAccount(accountA, userID, sessionHash, "state-from-a"))
	require.Empty(t, svc.resolveOpenAIWSTurnStateForAccount(accountB, userID, sessionHash, "state-from-a"))
	require.Empty(t, svc.resolveOpenAIWSTurnStateForAccount(accountA, userID+1, sessionHash, "state-from-a"), "a different user must not retrieve the cached token")
	store.BindSessionTurnState(0, openAIWSUserTurnStateSessionHash(userID, accountB.ID, sessionHash), "state-from-b", time.Hour)
	require.Equal(t, "state-from-b", svc.resolveOpenAIWSTurnStateForAccount(accountB, userID, sessionHash, "state-from-a"))

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session_id", "one-client-session")
	c.Request.Header.Set(openAIWSTurnStateHeader, "state-from-a")
	c.Request.Header.Set("User-Agent", "codex_exec/0.154.0")
	groupA := int64(17)
	c.Set("api_key", &APIKey{ID: 5, UserID: userID, GroupID: &groupA})
	store.BindSessionTurnState(0, openAIWSUserTurnStateSessionHash(userID, accountB.ID, svc.GenerateSessionHash(c, nil)), "state-from-b", time.Hour)
	request, err := svc.buildUpstreamRequest(context.Background(), c, accountB, []byte(`{"model":"gpt-5.1"}`), "test-token", false, "", true)
	require.NoError(t, err)
	require.Equal(t, "state-from-b", request.Header.Get(openAIWSTurnStateHeader))
	requestA, err := svc.buildUpstreamRequest(context.Background(), c, accountA, []byte(`{"model":"gpt-5.1"}`), "test-token", false, "", true)
	require.NoError(t, err)
	require.NotEqual(t, requestA.Header.Get("session_id"), request.Header.Get("session_id"))
	wsState := svc.resolveOpenAIWSTurnStateForAccount(accountB, userID, svc.GenerateSessionHash(c, nil), c.GetHeader(openAIWSTurnStateHeader))
	wsHeaders, _ := svc.buildOpenAIWSHeaders(c, accountB, "test-token", OpenAIWSProtocolDecision{}, true, wsState, "", "")
	require.Equal(t, "state-from-b", wsHeaders.Get(openAIWSTurnStateHeader))
	wsHeadersA, _ := svc.buildOpenAIWSHeaders(c, accountA, "test-token", OpenAIWSProtocolDecision{}, true, "state-from-a", "", "")
	require.NotEqual(t, wsHeadersA.Get("session_id"), wsHeaders.Get("session_id"))

	userHash := svc.GenerateSessionHash(c, nil)
	store.BindSessionTurnState(0, openAIWSUserTurnStateSessionHash(userID, accountA.ID, userHash), "state-from-a", time.Hour)
	owned, err := svc.buildUpstreamRequest(context.Background(), c, accountA, []byte(`{"model":"gpt-5.1"}`), "test-token", false, "", true)
	require.NoError(t, err)
	require.Equal(t, "state-from-a", owned.Header.Get(openAIWSTurnStateHeader))
	groupB := int64(18)
	c.Set("api_key", &APIKey{ID: 6, UserID: userID, GroupID: &groupB})
	otherKey, err := svc.buildUpstreamRequest(context.Background(), c, accountA, []byte(`{"model":"gpt-5.1"}`), "test-token", false, "", true)
	require.NoError(t, err)
	require.Equal(t, "state-from-a", otherKey.Header.Get(openAIWSTurnStateHeader))
	require.Equal(t, owned.Header.Get("session_id"), otherKey.Header.Get("session_id"))
	c.Set("api_key", &APIKey{ID: 7, UserID: userID + 1, GroupID: &groupB})
	otherUser, err := svc.buildUpstreamRequest(context.Background(), c, accountA, []byte(`{"model":"gpt-5.1"}`), "test-token", false, "", true)
	require.NoError(t, err)
	require.Empty(t, otherUser.Header.Get(openAIWSTurnStateHeader))
	require.NotEqual(t, owned.Header.Get("session_id"), otherUser.Header.Get("session_id"))
}
