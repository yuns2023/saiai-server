//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func ageTurnStateOwner(svc *OpenAIGatewayService, user int64, state string, remaining time.Duration) time.Time {
	store := svc.getOpenAIWSStateStore().(*defaultOpenAIWSStateStore)
	key := openAITurnStateAccountKey(user, state)
	store.responseToAccountMu.Lock()
	defer store.responseToAccountMu.Unlock()
	b := store.responseToAccount[key]
	b.expiresAt = time.Now().Add(remaining)
	store.responseToAccount[key] = b
	return b.expiresAt
}
func turnStateDeadline(svc *OpenAIGatewayService, user int64, state string) time.Time {
	store := svc.getOpenAIWSStateStore().(*defaultOpenAIWSStateStore)
	store.responseToAccountMu.RLock()
	defer store.responseToAccountMu.RUnlock()
	return store.responseToAccount[openAITurnStateAccountKey(user, state)].expiresAt
}

func TestOpenAINativeTurnStateSuccessSlidingExpiry(t *testing.T) {
	svc := auditOAuthPolicyService(nil)
	svc.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 120
	account := auditOAuthPolicyAccount()
	svc.bindOpenAINativeTurnState(account, 55, "mock_session", "MOCK_STATE")
	require.Greater(t, time.Until(turnStateDeadline(svc, 55, "MOCK_STATE")), 23*time.Hour)
	require.Equal(t, 2*time.Minute, svc.openAIWSSessionStickyTTL())
	headers := http.Header{}
	headers.Set(openAIWSTurnStateHeader, "MOCK_STATE")
	keys := openAINativeTurnStateKeys(55, headers, []byte(`{"client_metadata":{"x-codex-turn-state":"MOCK_STATE"}}`))
	require.Len(t, keys, 1)
	for range 3 {
		before := ageTurnStateOwner(svc, 55, "MOCK_STATE", time.Second)
		require.NoError(t, svc.validateOpenAINativeTurnState(context.Background(), account, 55, "mock_session", headers))
		require.Equal(t, before, turnStateDeadline(svc, 55, "MOCK_STATE"))
		svc.renewOpenAINativeTurnStateAfterSuccess(context.Background(), account, 55, keys)
		require.Greater(t, time.Until(turnStateDeadline(svc, 55, "MOCK_STATE")), 23*time.Hour)
	}
	before := ageTurnStateOwner(svc, 55, "MOCK_STATE", -time.Second)
	svc.renewOpenAINativeTurnStateAfterSuccess(context.Background(), account, 55, keys)
	require.Equal(t, before, turnStateDeadline(svc, 55, "MOCK_STATE"), "late completion cannot revive expiry")
	other := *account
	other.ID++
	svc.bindOpenAINativeTurnState(&other, 55, "mock_session", "MOCK_STATE")
	before = ageTurnStateOwner(svc, 55, "MOCK_STATE", time.Minute)
	svc.renewOpenAINativeTurnStateAfterSuccess(context.Background(), account, 55, keys)
	require.Equal(t, before, turnStateDeadline(svc, 55, "MOCK_STATE"))
	owner, err := svc.getOpenAIWSStateStore().GetTurnStateAccountForUser(context.Background(), 55, "MOCK_STATE")
	require.NoError(t, err)
	require.Equal(t, other.ID, owner)
}

func TestOpenAINativeTurnStateHTTPRenewsOnlySuccess(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		stream, renew bool
	}{
		{"json_success", `{"id":"resp_mock","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}`, false, true},
		{"json_failed", `{"id":"resp_mock","status":"failed","usage":{"input_tokens":1,"output_tokens":0}}`, false, false},
		{"sse_success", "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_mock\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", true, true},
		{"sse_failed", "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_mock\",\"status\":\"failed\",\"error\":{\"code\":\"server_error\"}}}\n\n", true, false},
		{"sse_incomplete", "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_mock\",\"status\":\"incomplete\"}}\n\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := auditOAuthPolicyContext("/v1/responses")
			c.Request.Header.Set(openAIWSTurnStateHeader, "MOCK_HEADER_STATE")
			typ := "application/json"
			if tc.stream {
				typ = "text/event-stream"
			}
			upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{auditOAuthPolicyResponse(200, http.Header{"Content-Type": {typ}}, tc.body)}}
			svc := auditOAuthPolicyService(upstream)
			account := auditOAuthPolicyAccount()
			before := map[string]time.Time{}
			for _, state := range []string{"MOCK_HEADER_STATE", "MOCK_BODY_STATE"} {
				svc.bindOpenAINativeTurnState(account, 0, "", state)
				before[state] = ageTurnStateOwner(svc, 0, state, time.Minute)
			}
			stream := "false"
			if tc.stream {
				stream = "true"
			}
			_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-6.1-sol","stream":`+stream+`,"client_metadata":{"x-codex-turn-state":"MOCK_BODY_STATE"}}`))
			if tc.renew {
				require.NoError(t, err)
			}
			require.Equal(t, 1, upstream.callCount)
			for state, deadline := range before {
				if tc.renew {
					require.Greater(t, time.Until(turnStateDeadline(svc, 0, state)), 23*time.Hour)
				} else {
					require.Equal(t, deadline, turnStateDeadline(svc, 0, state))
				}
			}
		})
	}
}

type renewalCache struct {
	stubGatewayCache
	mu       sync.Mutex
	owner    int64
	deadline time.Time
	fail     bool
}

func (c *renewalCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owner > 0 && time.Now().Before(c.deadline) {
		return c.owner, nil
	}
	return 0, nil
}
func (c *renewalCache) RefreshSessionTTLForAccount(_ context.Context, _ int64, _ string, owner int64, ttl, interval time.Duration) (time.Duration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return 0, errors.New("MOCK_CACHE_FAILURE")
	}
	if c.owner != owner || !time.Now().Before(c.deadline) {
		return 0, nil
	}
	if time.Until(c.deadline) <= ttl-interval {
		c.deadline = time.Now().Add(ttl)
	}
	return time.Until(c.deadline), nil
}
func TestOpenAINativeTurnStateSharedRenewal(t *testing.T) {
	account := auditOAuthPolicyAccount()
	cache := &renewalCache{owner: account.ID, deadline: time.Now().Add(time.Minute)}
	first := &OpenAIGatewayService{cache: cache}
	second := &OpenAIGatewayService{cache: cache}
	headers := http.Header{}
	headers.Set(openAIWSTurnStateHeader, "MOCK_STATE")
	keys := openAINativeTurnStateKeys(55, headers, nil)
	require.NoError(t, first.validateOpenAINativeTurnState(context.Background(), account, 55, "", headers))
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first.renewOpenAINativeTurnStateAfterSuccess(context.Background(), account, 55, keys)
		}()
	}
	wg.Wait()
	require.Greater(t, time.Until(cache.deadline), 23*time.Hour)
	require.NoError(t, second.validateOpenAINativeTurnState(context.Background(), account, 55, "", headers))
	before := turnStateDeadline(first, 55, "MOCK_STATE")
	cache.fail = true
	first.renewOpenAINativeTurnStateAfterSuccess(context.Background(), account, 55, keys)
	require.Equal(t, before, turnStateDeadline(first, 55, "MOCK_STATE"))
	cache.fail = false
	cache.owner++
	first.renewOpenAINativeTurnStateAfterSuccess(context.Background(), account, 55, keys)
	require.Equal(t, before, turnStateDeadline(first, 55, "MOCK_STATE"))
	cache.owner = 0
	first.renewOpenAINativeTurnStateAfterSuccess(context.Background(), account, 55, keys)
	require.Equal(t, before, turnStateDeadline(first, 55, "MOCK_STATE"))
}

func TestOpenAINativeTurnStateWSRenewsOnlySuccessfulTurn(t *testing.T) {
	for _, event := range []string{"response.completed", "response.failed", "response.incomplete"} {
		t.Run(event, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
			wire := []byte(`{"type":"` + event + `","response":{"id":"resp_mock","usage":{"input_tokens":1,"output_tokens":1}}}`)
			upstream := &openAIWSCaptureConn{events: [][]byte{wire}}
			svc := &OpenAIGatewayService{cfg: cfg, openaiWSPassthroughDialer: &openAIWSCaptureDialer{conn: upstream}}
			account := auditOAuthPolicyAccount()
			before := map[string]time.Time{}
			for _, state := range []string{"MOCK_HEADER_STATE", "MOCK_FRAME_STATE", "MOCK_UNRELATED_STATE"} {
				svc.bindOpenAINativeTurnState(account, 0, "", state)
				before[state] = ageTurnStateOwner(svc, 0, state, time.Minute)
			}
			done := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r
				ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
				defer cancel()
				typ, first, err := conn.Read(ctx)
				if err != nil {
					return
				}
				_ = svc.proxyResponsesWebSocketV2Passthrough(ctx, c, conn, account, "MOCK_ONLY", typ, first, nil, OpenAIWSProtocolDecision{}, nil)
			}))
			defer server.Close()
			c, _ := auditOAuthPolicyContext("/v1/responses")
			c.Request.Header.Set(openAIWSTurnStateHeader, "MOCK_HEADER_STATE")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &coderws.DialOptions{HTTPHeader: c.Request.Header})
			require.NoError(t, err)
			defer conn.CloseNow()
			payload := []byte(`{"type":"response.create","model":"gpt-6.1-sol","client_metadata":{"x-codex-turn-state":"MOCK_FRAME_STATE"}}`)
			require.NoError(t, conn.Write(ctx, coderws.MessageText, payload))
			_, got, err := conn.Read(ctx)
			require.NoError(t, err)
			require.Equal(t, wire, got)
			_ = conn.CloseNow()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("mock relay did not finish")
			}
			for state, deadline := range before {
				if event == "response.completed" && state != "MOCK_UNRELATED_STATE" {
					require.Greater(t, time.Until(turnStateDeadline(svc, 0, state)), 23*time.Hour)
				} else {
					require.Equal(t, deadline, turnStateDeadline(svc, 0, state))
				}
			}
		})
	}
}
