//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

type attemptBudgetTestCache struct {
	GatewayCache
	mu   sync.Mutex
	used int64
	err  error
}

func (c *attemptBudgetTestCache) ReserveOpenAIProviderAttempt(ctx context.Context, _ string, _ int64, limit int64, _ time.Time) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return c.used, err
	}
	if c.err != nil {
		return c.used, c.err
	}
	if c.used >= limit {
		return c.used, ErrOpenAIProviderAttemptBudgetExhausted
	}
	c.used++
	return c.used, nil
}

func attemptBudgetTestService(limit int64) (*OpenAIGatewayService, *attemptBudgetTestCache) {
	cache := &attemptBudgetTestCache{}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIProviderAttemptBudget = config.OpenAIProviderAttemptBudgetConfig{
		Enabled: true, ID: "TEST_ONLY-budget", APIKeyID: 7, MaxAttempts: limit,
		ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	return &OpenAIGatewayService{cfg: cfg, cache: cache}, cache
}

func attemptBudgetTestContext(svc *OpenAIGatewayService, id int64) context.Context {
	c, _ := auditOAuthPolicyContext("/v1/responses")
	c.Set("api_key", &APIKey{ID: id})
	return svc.withOpenAIProviderAttemptBudget(context.Background(), c, auditOAuthPolicyAccount())
}

func TestOpenAIProviderAttemptBudget_ConcurrentScopeAndRestart(t *testing.T) {
	svc, cache := attemptBudgetTestService(5)
	var sent atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Go(func() {
			// A fresh service/request must consume the same distributed budget.
			restarted := &OpenAIGatewayService{cfg: svc.cfg, cache: cache}
			if err := reserveOpenAIProviderAttempt(attemptBudgetTestContext(restarted, 7)); err == nil {
				sent.Add(1)
			} else if !errors.Is(err, ErrOpenAIProviderAttemptBudgetExhausted) {
				t.Errorf("unexpected refusal: %v", err)
			}
		})
	}
	wg.Wait()
	require.Equal(t, int64(5), sent.Load())
	require.NoError(t, reserveOpenAIProviderAttempt(attemptBudgetTestContext(svc, 8)))
	require.Equal(t, int64(5), cache.used)
}

func TestOpenAIProviderAttemptBudget_FailsClosedAndRedacts(t *testing.T) {
	svc, cache := attemptBudgetTestService(5)
	for _, cause := range []error{ErrOpenAIProviderAttemptBudgetUnarmed, errors.New("TEST_ONLY_CREDENTIAL_MUST_NOT_ESCAPE")} {
		cache.err = cause
		err := reserveOpenAIProviderAttempt(attemptBudgetTestContext(svc, 7))
		require.Error(t, err)
		require.NotContains(t, err.Error(), "TEST_ONLY_CREDENTIAL")
	}
	cache.err = nil
	require.ErrorIs(t, reserveOpenAIProviderAttempt(attemptBudgetTestContext(svc, 0)), ErrOpenAIProviderAttemptBudgetUnavailable)
	svc.cache = nil
	require.ErrorIs(t, reserveOpenAIProviderAttempt(attemptBudgetTestContext(svc, 7)), ErrOpenAIProviderAttemptBudgetUnavailable)
	svc.cache = cache
	svc.cfg.Gateway.OpenAIProviderAttemptBudget.ExpiresAt = "2000-01-01T00:00:00Z"
	require.ErrorIs(t, reserveOpenAIProviderAttempt(attemptBudgetTestContext(svc, 7)), ErrOpenAIProviderAttemptBudgetExpired)
	require.Zero(t, cache.used)
	svc.cfg.Gateway.OpenAIProviderAttemptBudget.Enabled = false
	require.NoError(t, reserveOpenAIProviderAttempt(attemptBudgetTestContext(svc, 0)))
}

func TestOpenAIProviderAttemptBudget_DoesNotDelegateLimitToAnotherGateway(t *testing.T) {
	svc, cache := attemptBudgetTestService(5)
	c, _ := auditOAuthPolicyContext("/v1/responses")
	c.Set("api_key", &APIKey{ID: 7})
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://another-gateway.example.test"}}
	require.ErrorIs(t, reserveOpenAIProviderAttempt(svc.withOpenAIProviderAttemptBudget(context.Background(), c, account)), ErrOpenAIProviderAttemptBudgetAccountUnsupported)
	account.Credentials["base_url"] = "https://api.openai.com"
	require.NoError(t, reserveOpenAIProviderAttempt(svc.withOpenAIProviderAttemptBudget(context.Background(), c, account)))
	require.Equal(t, int64(1), cache.used)
}

func TestOpenAIProviderAttemptBudget_HTTPRetriesSpendAttempts(t *testing.T) {
	svc, cache := attemptBudgetTestService(1)
	c, recorder := auditOAuthPolicyContext("/v1/responses")
	c.Set("api_key", &APIKey{ID: 7})
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{
		auditOAuthPolicyResponse(500, http.Header{"Content-Type": {"application/json"}}, `{"error":{"message":"TEST_ONLY_retry"}}`),
		auditOAuthPolicyResponse(200, http.Header{"Content-Type": {"application/json"}}, `{"id":"resp_should_not_be_sent"}`),
	}}
	svc.httpUpstream = upstream
	body := []byte(`{ "model":"gpt-6.1-sol", "stream":false, "reasoning":{"effort":"high"}, "future":true, "input":[] }`)
	_, err := svc.Forward(context.Background(), c, auditOAuthPolicyAccount(), body)
	require.ErrorIs(t, err, ErrOpenAIProviderAttemptBudgetExhausted)
	require.Equal(t, 1, upstream.callCount)
	require.Equal(t, body, upstream.bodies[0])
	require.Nil(t, upstream.reqs[0].GetBody)
	require.True(t, NativeCodexRejectsRedirects(upstream.reqs[0].Context()))
	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
	require.Contains(t, recorder.Body.String(), "attempt_budget_exhausted")
	require.Equal(t, int64(1), cache.used)
}

func TestOpenAIProviderAttemptBudget_HTTPWSAndPrewarmShareBudget(t *testing.T) {
	svc, cache := attemptBudgetTestService(4)
	ctx := attemptBudgetTestContext(svc, 7)
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{auditOAuthPolicyResponse(200, nil, `{}`)}}
	svc.httpUpstream = upstream
	req := httptest.NewRequest(http.MethodPost, "http://loopback.example.test/v1/responses", strings.NewReader("TEST_ONLY_BODY")).WithContext(ctx)
	resp, err := svc.doOpenAIUpstream(req, "", 1, 1)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	wire := []byte("{\n \"type\":\"response.create\", \"generate\":false, \"future\":true }")
	conn := &openAIWSCaptureConn{}
	pooled := &openAIWSConn{ws: conn}
	require.NoError(t, pooled.writeJSON(map[string]any{"type": "response.create", "generate": false}, ctx))
	require.NoError(t, pooled.writeFrameWithTimeout(ctx, coderws.MessageBinary, wire, time.Second))
	relay := &openAIProviderBudgetFrameConn{FrameConn: conn, budgetContext: ctx}
	require.NoError(t, relay.WriteFrame(context.Background(), coderws.MessageText, wire))
	denied := relay.WriteFrame(context.Background(), coderws.MessageBinary, []byte("TEST_ONLY_FUTURE_EVENT"))
	require.ErrorIs(t, denied, ErrOpenAIProviderAttemptBudgetExhausted)
	require.ErrorIs(t, pooled.writeJSON(map[string]any{"type": "response.create"}, ctx), ErrOpenAIProviderAttemptBudgetExhausted)
	_, err = svc.doOpenAIUpstream(req, "", 1, 1)
	require.ErrorIs(t, err, ErrOpenAIProviderAttemptBudgetExhausted)
	require.Equal(t, int64(4), cache.used)
	require.Equal(t, 1, upstream.callCount)
	require.Equal(t, []coderws.MessageType{coderws.MessageBinary, coderws.MessageText}, conn.writeTypes)
	require.Equal(t, [][]byte{wire, wire}, conn.rawWrites)
	// A shared pool must not retain the preceding request's API-key scope.
	require.NoError(t, pooled.writeJSON(map[string]any{"type": "response.create"}, attemptBudgetTestContext(svc, 8)))
}

func TestOpenAIProviderAttemptBudget_DenialIsNeverRetriedOrCompleted(t *testing.T) {
	for _, cause := range []error{ErrOpenAIProviderAttemptBudgetExhausted, ErrOpenAIProviderAttemptBudgetExpired,
		ErrOpenAIProviderAttemptBudgetUnarmed, ErrOpenAIProviderAttemptBudgetUnavailable} {
		err := fmt.Errorf("wrapped: %w", cause)
		_, retryable := classifyOpenAIWSReconnectReason(wrapOpenAIWSFallback("write_request", err))
		require.False(t, retryable)
		require.False(t, isOpenAIWSIngressTurnRetryable(wrapOpenAIWSIngressTurnError("write_upstream", err, false)))
		_, code, _, _, ok := resolveOpenAIWSFallbackErrorResponse(wrapOpenAIWSFallback("prewarm_write", err))
		require.True(t, ok)
		require.Contains(t, code, "attempt_budget_")
		c, recorder := auditOAuthPolicyContext("/v1/responses")
		require.True(t, WriteOpenAIProviderAttemptBudgetError(c, err))
		require.NotContains(t, recorder.Body.String(), "response.completed")
	}
}

func TestOpenAIProviderAttemptBudget_GatewayGeneratePrewarmSpendsAllowance(t *testing.T) {
	svc, cache := attemptBudgetTestService(1)
	svc.cfg.Gateway.OpenAIWS.PrewarmGenerateEnabled = true
	ctx := attemptBudgetTestContext(svc, 7)
	upstream := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_TEST_ONLY_warmup"}}`)}}
	lease := &openAIWSConnLease{conn: &openAIWSConn{id: "TEST_ONLY-warmup", ws: upstream}}
	decision := OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}
	payload := map[string]any{"type": "response.create", "model": "gpt-6.1-sol", "future": true}
	require.NoError(t, svc.performOpenAIWSGeneratePrewarm(ctx, lease, decision, payload, "", nil,
		auditOAuthPolicyAccount(), nil, 0, 0))
	require.Equal(t, int64(1), cache.used)
	require.Equal(t, false, upstream.writes[0]["generate"])
	require.NotContains(t, payload, "generate", "prewarm must not change the following client payload")
	require.ErrorIs(t, lease.WriteJSONWithContextTimeout(ctx, payload, time.Second), ErrOpenAIProviderAttemptBudgetExhausted)
	require.Len(t, upstream.writes, 1)
}

func TestOpenAIProviderAttemptBudget_NativeChatControlDoesNotSpendAllowance(t *testing.T) {
	svc, cache := attemptBudgetTestService(1)
	svc.cfg.Gateway.OpenAIChatUpstreamBaseURL = "http://loopback.example.test"
	svc.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{
		auditOAuthPolicyResponse(200, nil, `{}`), auditOAuthPolicyResponse(200, nil, `{}`),
	}}
	svc.httpUpstream = upstream
	c, _ := auditOAuthPolicyContext("/chatgpt/backend-api/f/conversation/prepare")
	c.Set("api_key", &APIKey{ID: 7})
	for _, path := range []string{"/chatgpt/backend-api/f/conversation/prepare?future=1", "/chatgpt/backend-api/f/conversation?future=2"} {
		response, err := svc.ForwardChatGPTConversation(context.Background(), c, auditOAuthPolicyAccount(), []byte(`{"model":"auto"}`), path)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
	}
	require.Equal(t, int64(1), cache.used)
	_, err := svc.ForwardChatGPTConversation(context.Background(), c, auditOAuthPolicyAccount(), []byte(`{"model":"auto"}`), "/chatgpt/backend-api/f/conversation?future=3")
	require.ErrorIs(t, err, ErrOpenAIProviderAttemptBudgetExhausted)
	require.Equal(t, 2, upstream.callCount)
}
