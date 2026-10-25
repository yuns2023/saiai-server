package service

import (
	"bytes"
	"context"
	"fmt"
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
	"github.com/tidwall/gjson"
)

// openAIWSTransformRecorder rewrites TEST_ONLY_RAW to TEST_ONLY_SENT and
// records which turns and input bytes it saw.
type openAIWSTransformRecorder struct {
	mu     sync.Mutex
	turns  []int
	inputs [][]byte
}

func (r *openAIWSTransformRecorder) transform(turn int, payload []byte) ([]byte, error) {
	r.mu.Lock()
	r.turns = append(r.turns, turn)
	r.inputs = append(r.inputs, append([]byte(nil), payload...))
	r.mu.Unlock()
	return bytes.ReplaceAll(payload, []byte("TEST_ONLY_RAW"), []byte("TEST_ONLY_SENT")), nil
}

func (r *openAIWSTransformRecorder) seen() ([]int, [][]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.turns...), append([][]byte(nil), r.inputs...)
}

func newOpenAIWSTransformRelayGateway(dialer openAIWSClientDialer) *OpenAIGatewayService {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 2
	repository := &openAICodexSnapshotAsyncRepo{updateExtraCh: make(chan map[string]any, 16), rateLimitCh: make(chan time.Time, 16)}
	return &OpenAIGatewayService{cfg: cfg, accountRepo: repository, rateLimitService: &RateLimitService{accountRepo: repository},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), openaiWSStateStore: NewOpenAIWSStateStore(nil), openaiWSPassthroughDialer: dialer}
}

// serveOpenAIWSTransformRelay runs ProxyResponsesWebSocketFromClient for one
// official-Codex client connection and returns the dialed client.
func serveOpenAIWSTransformRelay(t *testing.T, ctx context.Context, gateway *OpenAIGatewayService, account *Account, hooks *OpenAIWSIngressHooks) (*coderws.Conn, chan error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	serverErrors := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, acceptErr := coderws.Accept(writer, request, nil)
		if acceptErr != nil {
			serverErrors <- acceptErr
			return
		}
		defer func() { _ = connection.CloseNow() }()
		ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginContext.Request = request.Clone(request.Context())
		ginContext.Request.Header.Set("User-Agent", "codex_cli_rs/0.164.0")
		ginContext.Request.Header.Set("originator", "codex_cli_rs")
		ginContext.Set("api_key", &APIKey{UserID: 77})
		messageType, payload, readErr := connection.Read(request.Context())
		if readErr != nil {
			serverErrors <- readErr
			return
		}
		serverErrors <- gateway.ProxyResponsesWebSocketFromClient(request.Context(), ginContext, connection, account, "TEST_ONLY_OAUTH_A", messageType, payload, hooks)
	}))
	t.Cleanup(server.Close)
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.CloseNow() })
	return client, serverErrors
}

func readOpenAIWSUntilTerminal(t *testing.T, ctx context.Context, client *coderws.Conn) []byte {
	t.Helper()
	for {
		_, payload, err := client.Read(ctx)
		require.NoError(t, err)
		switch gjson.GetBytes(payload, "type").String() {
		case "response.completed", "response.failed", "error":
			return payload
		}
	}
}

// Native passthrough: the bytes written upstream for the first frame, a later
// turn and the in-connection quota failover replay of that turn are all the
// transformed bytes; the replay reuses the failed turn's logical number while
// the retained frame stays as the client sent it.
func TestOpenAIWSTransformUpstreamPayloadNativeRelayFirstLaterAndFailoverReplay(t *testing.T) {
	resetAt := time.Now().Add(90 * time.Minute).Truncate(time.Second)
	accountAConnection := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_tf_a_1","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":3}}}`),
		[]byte(fmt.Sprintf(`{"type":"error","error":{"code":"usage_limit_reached","type":"usage_limit_reached","message":"usage limit reached","resets_at":%d}}`, resetAt.Unix())),
	}}
	accountBConnection := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_tf_b_2","status":"completed","output":[],"usage":{"input_tokens":8,"output_tokens":2}}}`),
	}}
	gate := &openAIWSQuotaGateConn{openAIWSCaptureConn: accountAConnection, nextTurnWritten: make(chan struct{})}
	dialer := &openAIWSQuotaHandshakeDialer{queue: &openAIWSQueueDialer{conns: []openAIWSClientConn{gate, accountBConnection}}, resetAt: resetAt}
	gateway := newOpenAIWSTransformRelayGateway(dialer)
	accountA := &Account{ID: 881, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	accountB := &Account{ID: 882, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	recorder := &openAIWSTransformRecorder{}
	hooks := &OpenAIWSIngressHooks{
		TransformUpstreamPayload: recorder.transform,
		OnAccountExhausted: func(*OpenAIWSAccountFailoverError) (*OpenAIWSFailoverTarget, error) {
			return &OpenAIWSFailoverTarget{Account: accountB, Token: "TEST_ONLY_OAUTH_B"}, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	client, serverErrors := serveOpenAIWSTransformRelay(t, ctx, gateway, accountA, hooks)

	first := []byte(`{"type":"response.create","model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"TEST_ONLY_RAW one"}]}]}`)
	second := []byte(`{"type":"response.create","model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"TEST_ONLY_RAW two"}]}]}`)
	require.NoError(t, client.Write(ctx, coderws.MessageText, first))
	require.Equal(t, "resp_tf_a_1", gjson.GetBytes(readOpenAIWSUntilTerminal(t, ctx, client), "response.id").String())
	require.NoError(t, client.Write(ctx, coderws.MessageText, second))
	require.Equal(t, "resp_tf_b_2", gjson.GetBytes(readOpenAIWSUntilTerminal(t, ctx, client), "response.id").String())
	_ = client.Close(coderws.StatusNormalClosure, "done")
	select {
	case err := <-serverErrors:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("relay did not finish")
	}

	accountAConnection.mu.Lock()
	aWrites := append([][]byte(nil), accountAConnection.rawWrites...)
	accountAConnection.mu.Unlock()
	accountBConnection.mu.Lock()
	bWrites := append([][]byte(nil), accountBConnection.rawWrites...)
	accountBConnection.mu.Unlock()
	require.Len(t, aWrites, 2)
	require.Equal(t, bytes.ReplaceAll(first, []byte("TEST_ONLY_RAW"), []byte("TEST_ONLY_SENT")), aWrites[0], "first frame")
	require.Equal(t, bytes.ReplaceAll(second, []byte("TEST_ONLY_RAW"), []byte("TEST_ONLY_SENT")), aWrites[1], "later turn")
	require.Len(t, bWrites, 1)
	require.Equal(t, aWrites[1], bWrites[0], "failover replay")

	turns, inputs := recorder.seen()
	require.Equal(t, []int{1, 2, 2}, turns, "the replay keeps the failed turn's logical number")
	for _, input := range inputs {
		require.Contains(t, string(input), "TEST_ONLY_RAW", "retained frames stay original")
	}
}

// A handshake 429 replays the first frame on the replacement account; that
// replay is written through the transform too.
func TestOpenAIWSTransformUpstreamPayloadEarly429Replay(t *testing.T) {
	resetAt := time.Now().Add(90 * time.Minute).Truncate(time.Second)
	accountBConnection := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_tf_b_1","status":"completed","output":[],"usage":{"input_tokens":8,"output_tokens":2}}}`),
	}}
	dialer := &openAIWSQuotaHandshakeDialer{queue: &openAIWSQueueDialer{conns: []openAIWSClientConn{accountBConnection}}, resetAt: resetAt, rejectFirst: true}
	gateway := newOpenAIWSTransformRelayGateway(dialer)
	accountA := &Account{ID: 883, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	accountB := &Account{ID: 884, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	recorder := &openAIWSTransformRecorder{}
	hooks := &OpenAIWSIngressHooks{
		TransformUpstreamPayload: recorder.transform,
		OnAccountExhausted: func(*OpenAIWSAccountFailoverError) (*OpenAIWSFailoverTarget, error) {
			return &OpenAIWSFailoverTarget{Account: accountB, Token: "TEST_ONLY_OAUTH_B"}, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	client, serverErrors := serveOpenAIWSTransformRelay(t, ctx, gateway, accountA, hooks)
	first := []byte(`{"type":"response.create","model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"TEST_ONLY_RAW one"}]}]}`)
	require.NoError(t, client.Write(ctx, coderws.MessageText, first))
	require.Equal(t, "resp_tf_b_1", gjson.GetBytes(readOpenAIWSUntilTerminal(t, ctx, client), "response.id").String())
	_ = client.Close(coderws.StatusNormalClosure, "done")
	select {
	case err := <-serverErrors:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("relay did not finish")
	}
	require.Equal(t, int32(2), dialer.calls.Load())
	accountBConnection.mu.Lock()
	defer accountBConnection.mu.Unlock()
	require.Len(t, accountBConnection.rawWrites, 1)
	require.Equal(t, bytes.ReplaceAll(first, []byte("TEST_ONLY_RAW"), []byte("TEST_ONLY_SENT")), accountBConnection.rawWrites[0])
	turns, _ := recorder.seen()
	require.Equal(t, []int{1}, turns)
}

// A transform error ends the session before the frame is written upstream.
func TestOpenAIWSTransformUpstreamPayloadErrorIsNotForwarded(t *testing.T) {
	upstream := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_tf_1","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":3}}}`),
	}}
	gateway := newOpenAIWSTransformRelayGateway(&openAIWSQueueDialer{conns: []openAIWSClientConn{&openAIWSQuotaGateConn{openAIWSCaptureConn: upstream, holdOpen: true}}})
	account := &Account{ID: 885, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	hooks := &OpenAIWSIngressHooks{
		TransformUpstreamPayload: func(turn int, payload []byte) ([]byte, error) {
			if turn == 2 {
				return nil, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "TEST_ONLY_REASON", clientTurnRejectionMarker{})
			}
			return payload, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	client, serverErrors := serveOpenAIWSTransformRelay(t, ctx, gateway, account, hooks)
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)))
	require.Equal(t, "resp_tf_1", gjson.GetBytes(readOpenAIWSUntilTerminal(t, ctx, client), "response.id").String())
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","input":[{"type":"input_text","text":"x"}]}`)))
	select {
	case err := <-serverErrors:
		var marker clientTurnRejectionMarker
		require.ErrorAs(t, err, &marker)
	case <-ctx.Done():
		t.Fatal("relay did not end after the transform error")
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	require.Len(t, upstream.rawWrites, 1, "the rejected frame is never written upstream")
}

// Pool path: the possibly rebuilt turn is transformed right before it is
// written; the retained turn (used to rebuild the next one) stays original.
func TestOpenAIWSTransformUpstreamPayloadPoolPathRebuiltTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	captureConn := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_tf_pool_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_tf_pool_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: captureConn})
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
	account := &Account{ID: 886, Name: "openai-ws-transform-pool", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	recorder := &openAIWSTransformRecorder{}
	resultHashes := make(chan string, 4)
	hooks := &OpenAIWSIngressHooks{
		TransformUpstreamPayload: recorder.transform,
		AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
			if turnErr == nil && result != nil {
				resultHashes <- result.RequestPayloadHash
			}
		},
	}
	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r.Clone(r.Context())
		ginCtx.Request.Header.Set("User-Agent", "unit-test-agent/1.0")
		msgType, firstMessage, readErr := conn.Read(r.Context())
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", msgType, firstMessage, hooks)
	}))
	defer wsServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientConn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	require.NoError(t, clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"TEST_ONLY_RAW hello"}]}`)))
	_, payload, err := clientConn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "resp_tf_pool_1", gjson.GetBytes(payload, "response.id").String())
	// An incremental frame the pool path rebuilds by replaying turn 1's input.
	require.NoError(t, clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_stale_external","input":[{"type":"input_text","text":"world"}]}`)))
	_, payload, err = clientConn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "resp_tf_pool_2", gjson.GetBytes(payload, "response.id").String())
	_ = clientConn.Close(coderws.StatusNormalClosure, "done")
	select {
	case <-serverErrCh:
	case <-ctx.Done():
		t.Fatal("session did not end")
	}

	captureConn.mu.Lock()
	writes := append([]map[string]any(nil), captureConn.writes...)
	captureConn.mu.Unlock()
	require.Len(t, writes, 2)
	for i, write := range writes {
		encoded := fmt.Sprint(write["input"])
		require.Contains(t, encoded, "TEST_ONLY_SENT", "write %d", i)
		require.NotContains(t, encoded, "TEST_ONLY_RAW", "write %d", i)
	}
	turns, inputs := recorder.seen()
	require.Equal(t, []int{1, 2}, turns)
	for _, input := range inputs {
		sent := bytes.ReplaceAll(input, []byte("TEST_ONLY_RAW"), []byte("TEST_ONLY_SENT"))
		require.Equal(t, HashUsageRequestPayload(sent), <-resultHashes, "usage hash describes the sent bytes")
	}
	require.Len(t, gjson.GetBytes(inputs[1], "input").Array(), 2, "turn 2 was rebuilt with turn 1's input")
	require.Contains(t, string(inputs[1]), "TEST_ONLY_RAW", "the retained turn stays original")
}

// Without a rewrite the pool path keeps the base usage hash behaviour (no
// per-turn hash from the service; the handler falls back as before).
func TestOpenAIWSTransformUpstreamPayloadPoolPathUnchangedKeepsBaseHash(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	captureConn := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_tf_pool_same","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: captureConn})
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
	account := &Account{ID: 887, Name: "openai-ws-transform-pool-same", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	hashes := make(chan string, 2)
	hooks := &OpenAIWSIngressHooks{
		TransformUpstreamPayload: func(_ int, payload []byte) ([]byte, error) { return payload, nil },
		AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
			if turnErr == nil && result != nil {
				hashes <- result.RequestPayloadHash
			}
		},
	}
	done := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r.Clone(r.Context())
		ginCtx.Request.Header.Set("User-Agent", "unit-test-agent/1.0")
		msgType, firstMessage, readErr := conn.Read(r.Context())
		if readErr != nil {
			done <- readErr
			return
		}
		done <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", msgType, firstMessage, hooks)
	}))
	defer wsServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientConn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()
	require.NoError(t, clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)))
	_, _, err = clientConn.Read(ctx)
	require.NoError(t, err)
	_ = clientConn.Close(coderws.StatusNormalClosure, "done")
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("session did not end")
	}
	require.Empty(t, <-hashes)
}
