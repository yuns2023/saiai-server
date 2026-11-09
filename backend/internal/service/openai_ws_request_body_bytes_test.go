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

// openAIWSBodyBytesRecorder collects the per-turn RequestBodyBytes reported to
// AfterTurn, keyed by logical turn.
type openAIWSBodyBytesRecorder struct {
	mu    sync.Mutex
	sizes map[int]*int64
}

func (r *openAIWSBodyBytesRecorder) afterTurn(turn int, result *OpenAIForwardResult, turnErr error) {
	if turnErr != nil || result == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sizes == nil {
		r.sizes = map[int]*int64{}
	}
	r.sizes[turn] = result.RequestBodyBytes
}

func (r *openAIWSBodyBytesRecorder) get(turn int) (*int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.sizes[turn]
	return value, ok
}

// growOpenAIWSPayload makes the bytes sent upstream much larger than the
// client's frame so a post-transform measurement cannot pass.
func growOpenAIWSPayload(_ int, payload []byte) ([]byte, error) {
	return bytes.ReplaceAll(payload, []byte("TEST_ONLY_RAW"), []byte("TEST_ONLY_SENT"+strings.Repeat("_", 2048))), nil
}

// Native passthrough: turn 1 reports the caller-supplied original size (the
// first frame may be a migrated payload), turn 2 its own client frame, and the
// in-connection quota failover replay of turn 2 keeps turn 2's original size.
func TestOpenAIWSPassthroughRequestBodyBytesPerTurnAndFailoverReplay(t *testing.T) {
	resetAt := time.Now().Add(90 * time.Minute).Truncate(time.Second)
	accountAConnection := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_bb_a_1","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":3}}}`),
		[]byte(fmt.Sprintf(`{"type":"error","error":{"code":"usage_limit_reached","type":"usage_limit_reached","message":"usage limit reached","resets_at":%d}}`, resetAt.Unix())),
	}}
	accountBConnection := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_bb_b_2","status":"completed","output":[],"usage":{"input_tokens":8,"output_tokens":2}}}`),
	}}
	gate := &openAIWSQuotaGateConn{openAIWSCaptureConn: accountAConnection, nextTurnWritten: make(chan struct{})}
	dialer := &openAIWSQuotaHandshakeDialer{queue: &openAIWSQueueDialer{conns: []openAIWSClientConn{gate, accountBConnection}}, resetAt: resetAt}
	gateway := newOpenAIWSTransformRelayGateway(dialer)
	accountA := &Account{ID: 891, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	accountB := &Account{ID: 892, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	recorder := &openAIWSBodyBytesRecorder{}
	firstOriginal := int64(777)
	hooks := &OpenAIWSIngressHooks{
		TransformUpstreamPayload: growOpenAIWSPayload,
		AfterTurn:                recorder.afterTurn,
		OnAccountExhausted: func(*OpenAIWSAccountFailoverError) (*OpenAIWSFailoverTarget, error) {
			return &OpenAIWSFailoverTarget{Account: accountB, Token: "TEST_ONLY_OAUTH_B"}, nil
		},
		FirstTurnRequestBodyBytes: &firstOriginal,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	client, serverErrors := serveOpenAIWSTransformRelay(t, ctx, gateway, accountA, hooks)

	first := []byte(`{"type":"response.create","model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"TEST_ONLY_RAW one"}]}]}`)
	second := []byte(`{"type":"response.create","model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"TEST_ONLY_RAW two, a longer turn"}]}]}`)
	require.NoError(t, client.Write(ctx, coderws.MessageText, first))
	require.Equal(t, "resp_bb_a_1", gjson.GetBytes(readOpenAIWSUntilTerminal(t, ctx, client), "response.id").String())
	require.NoError(t, client.Write(ctx, coderws.MessageText, second))
	require.Equal(t, "resp_bb_b_2", gjson.GetBytes(readOpenAIWSUntilTerminal(t, ctx, client), "response.id").String())
	_ = client.Close(coderws.StatusNormalClosure, "done")
	select {
	case err := <-serverErrors:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("relay did not finish")
	}

	turn1, ok := recorder.get(1)
	require.True(t, ok)
	require.NotNil(t, turn1)
	require.Equal(t, firstOriginal, *turn1, "turn 1 uses the caller's original size, not the frame handed in")
	turn2, ok := recorder.get(2)
	require.True(t, ok)
	require.NotNil(t, turn2)
	require.Equal(t, int64(len(second)), *turn2, "the failover replay keeps the turn's original client size")
}

// A handshake 429 replays the first frame on another account: the replayed
// turn keeps the caller-supplied original size (never len(ReplayPayload())).
func TestOpenAIWSPassthroughRequestBodyBytesEarly429Replay(t *testing.T) {
	resetAt := time.Now().Add(90 * time.Minute).Truncate(time.Second)
	accountBConnection := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_bb_b_1","status":"completed","output":[],"usage":{"input_tokens":8,"output_tokens":2}}}`),
	}}
	dialer := &openAIWSQuotaHandshakeDialer{queue: &openAIWSQueueDialer{conns: []openAIWSClientConn{accountBConnection}}, resetAt: resetAt, rejectFirst: true}
	gateway := newOpenAIWSTransformRelayGateway(dialer)
	accountA := &Account{ID: 893, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	accountB := &Account{ID: 894, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	recorder := &openAIWSBodyBytesRecorder{}
	firstOriginal := int64(4321)
	hooks := &OpenAIWSIngressHooks{
		TransformUpstreamPayload: growOpenAIWSPayload,
		AfterTurn:                recorder.afterTurn,
		OnAccountExhausted: func(*OpenAIWSAccountFailoverError) (*OpenAIWSFailoverTarget, error) {
			return &OpenAIWSFailoverTarget{Account: accountB, Token: "TEST_ONLY_OAUTH_B"}, nil
		},
		FirstTurnRequestBodyBytes: &firstOriginal,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	client, serverErrors := serveOpenAIWSTransformRelay(t, ctx, gateway, accountA, hooks)
	first := []byte(`{"type":"response.create","model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"TEST_ONLY_RAW one"}]}]}`)
	require.NoError(t, client.Write(ctx, coderws.MessageText, first))
	require.Equal(t, "resp_bb_b_1", gjson.GetBytes(readOpenAIWSUntilTerminal(t, ctx, client), "response.id").String())
	_ = client.Close(coderws.StatusNormalClosure, "done")
	select {
	case err := <-serverErrors:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("relay did not finish")
	}
	require.Equal(t, int32(2), dialer.calls.Load())
	turn1, ok := recorder.get(1)
	require.True(t, ok)
	require.NotNil(t, turn1)
	require.Equal(t, firstOriginal, *turn1)
}

func TestOpenAIWSReplayTrackerCarriesOriginalRequestBodyBytes(t *testing.T) {
	tracker := newOpenAIWSPassthroughReplayTracker(NewOpenAIWSStateStore(nil), 0)
	size := int64(98765)
	tracker.RegisterTurnWithRequestBodyBytes(3, coderws.MessageText, []byte(`{"type":"response.create","model":"m","input":[]}`), &size)
	failure, ok := tracker.BuildFailoverError(11, nil)
	require.True(t, ok)
	require.NotNil(t, failure.RequestBodyBytes())
	require.Equal(t, size, *failure.RequestBodyBytes())

	unknown := newOpenAIWSPassthroughReplayTracker(NewOpenAIWSStateStore(nil), 0)
	unknown.RegisterTurnWithRequestBodyBytes(1, coderws.MessageText, []byte(`{"type":"response.create","model":"m","input":[]}`), nil)
	failure, ok = unknown.BuildFailoverError(11, nil)
	require.True(t, ok)
	require.Nil(t, failure.RequestBodyBytes())

	remapped := remapOpenAIWSIngressHooksForFailover(&OpenAIWSIngressHooks{}, 2, &size)
	require.Equal(t, size, *remapped.FirstTurnRequestBodyBytes)
	require.Nil(t, remapOpenAIWSIngressHooksForFailover(&OpenAIWSIngressHooks{FirstTurnRequestBodyBytes: &size}, 2, nil).FirstTurnRequestBodyBytes,
		"a replay never inherits the session's first-turn size")
}

// Pool (ingress forwarder) path: turn 1 reports the caller's original size;
// turn 2 reports its raw client frame, not the rebuilt (replayed history),
// normalized or transformed payload that is actually sent.
func TestOpenAIWSPoolPathRequestBodyBytesPerTurn(t *testing.T) {
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
		[]byte(`{"type":"response.completed","response":{"id":"resp_bb_pool_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_bb_pool_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: captureConn})
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
	account := &Account{ID: 895, Name: "openai-ws-body-bytes-pool", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	recorder := &openAIWSBodyBytesRecorder{}
	firstOriginal := int64(555)
	hooks := &OpenAIWSIngressHooks{
		TransformUpstreamPayload:  growOpenAIWSPayload,
		AfterTurn:                 recorder.afterTurn,
		FirstTurnRequestBodyBytes: &firstOriginal,
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
	require.Equal(t, "resp_bb_pool_1", gjson.GetBytes(payload, "response.id").String())
	// Surrounding whitespace is trimmed by the forwarder and the frame is
	// rebuilt with turn 1's input; the recorded size is the frame as read.
	second := []byte(`  {"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_stale_external","input":[{"type":"input_text","text":"world"}]}  `)
	require.NoError(t, clientConn.Write(ctx, coderws.MessageText, second))
	_, payload, err = clientConn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "resp_bb_pool_2", gjson.GetBytes(payload, "response.id").String())
	_ = clientConn.Close(coderws.StatusNormalClosure, "done")
	select {
	case <-serverErrCh:
	case <-ctx.Done():
		t.Fatal("session did not end")
	}

	turn1, ok := recorder.get(1)
	require.True(t, ok)
	require.NotNil(t, turn1)
	require.Equal(t, firstOriginal, *turn1)
	turn2, ok := recorder.get(2)
	require.True(t, ok)
	require.NotNil(t, turn2)
	require.Equal(t, int64(len(second)), *turn2)
}

// A quota error on the first turn after the connection is up replays that
// turn from the replay tracker; it must keep the caller's original size.
func TestOpenAIWSPassthroughRequestBodyBytesFirstTurnMidConnectionReplay(t *testing.T) {
	resetAt := time.Now().Add(90 * time.Minute).Truncate(time.Second)
	accountAConnection := &openAIWSCaptureConn{events: [][]byte{
		[]byte(fmt.Sprintf(`{"type":"error","error":{"code":"usage_limit_reached","type":"usage_limit_reached","message":"usage limit reached","resets_at":%d}}`, resetAt.Unix())),
	}}
	accountBConnection := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_bb_b_mid_1","status":"completed","output":[],"usage":{"input_tokens":8,"output_tokens":2}}}`),
	}}
	dialer := &openAIWSQuotaHandshakeDialer{queue: &openAIWSQueueDialer{conns: []openAIWSClientConn{accountAConnection, accountBConnection}}, resetAt: resetAt}
	gateway := newOpenAIWSTransformRelayGateway(dialer)
	accountA := &Account{ID: 896, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	accountB := &Account{ID: 897, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	recorder := &openAIWSBodyBytesRecorder{}
	firstOriginal := int64(2468)
	hooks := &OpenAIWSIngressHooks{
		TransformUpstreamPayload: growOpenAIWSPayload,
		AfterTurn:                recorder.afterTurn,
		OnAccountExhausted: func(*OpenAIWSAccountFailoverError) (*OpenAIWSFailoverTarget, error) {
			return &OpenAIWSFailoverTarget{Account: accountB, Token: "TEST_ONLY_OAUTH_B"}, nil
		},
		FirstTurnRequestBodyBytes: &firstOriginal,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	client, serverErrors := serveOpenAIWSTransformRelay(t, ctx, gateway, accountA, hooks)
	first := []byte(`{"type":"response.create","model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"TEST_ONLY_RAW one"}]}]}`)
	require.NoError(t, client.Write(ctx, coderws.MessageText, first))
	require.Equal(t, "resp_bb_b_mid_1", gjson.GetBytes(readOpenAIWSUntilTerminal(t, ctx, client), "response.id").String())
	_ = client.Close(coderws.StatusNormalClosure, "done")
	select {
	case err := <-serverErrors:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("relay did not finish")
	}
	accountBConnection.mu.Lock()
	bWrites := len(accountBConnection.rawWrites)
	accountBConnection.mu.Unlock()
	require.Equal(t, 1, bWrites, "turn 1 was replayed on account B")
	turn1, ok := recorder.get(1)
	require.True(t, ok)
	require.NotNil(t, turn1)
	require.Equal(t, firstOriginal, *turn1)
}

// The first client frame is a non-turn control frame: the caller-supplied
// first-turn size belongs to it and must not be used. The first
// response.create reports its own raw size, and the account failover replay
// of that turn keeps the same size.
func TestOpenAIWSPassthroughRequestBodyBytesControlFirstFrameThenFailover(t *testing.T) {
	resetAt := time.Now().Add(90 * time.Minute).Truncate(time.Second)
	accountAConnection := &openAIWSCaptureConn{events: [][]byte{
		[]byte(fmt.Sprintf(`{"type":"error","error":{"code":"usage_limit_reached","type":"usage_limit_reached","message":"usage limit reached","resets_at":%d}}`, resetAt.Unix())),
	}}
	// Hold the quota error until both the control frame and the create are written.
	gate := &openAIWSQuotaGateConn{openAIWSCaptureConn: accountAConnection, nextTurnWritten: make(chan struct{}), gateRead: 1}
	accountBConnection := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_bb_ctrl_b","status":"completed","output":[],"usage":{"input_tokens":8,"output_tokens":2}}}`),
	}}
	dialer := &openAIWSQuotaHandshakeDialer{queue: &openAIWSQueueDialer{conns: []openAIWSClientConn{gate, accountBConnection}}, resetAt: resetAt}
	gateway := newOpenAIWSTransformRelayGateway(dialer)
	accountA := &Account{ID: 898, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	accountB := &Account{ID: 899, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	recorder := &openAIWSBodyBytesRecorder{}
	controlSize := int64(31)
	hooks := &OpenAIWSIngressHooks{
		TransformUpstreamPayload: growOpenAIWSPayload,
		AfterTurn:                recorder.afterTurn,
		OnAccountExhausted: func(*OpenAIWSAccountFailoverError) (*OpenAIWSFailoverTarget, error) {
			return &OpenAIWSFailoverTarget{Account: accountB, Token: "TEST_ONLY_OAUTH_B"}, nil
		},
		// The handler's original first Read: the control frame.
		FirstTurnRequestBodyBytes: &controlSize,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	client, serverErrors := serveOpenAIWSTransformRelay(t, ctx, gateway, accountA, hooks)
	control := []byte(`{"type":"session.update","session":{}}`)
	create := []byte(`{"type":"response.create","model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"TEST_ONLY_RAW after a control frame"}]}]}`)
	require.NoError(t, client.Write(ctx, coderws.MessageText, control))
	require.NoError(t, client.Write(ctx, coderws.MessageText, create))
	require.Equal(t, "resp_bb_ctrl_b", gjson.GetBytes(readOpenAIWSUntilTerminal(t, ctx, client), "response.id").String())
	_ = client.Close(coderws.StatusNormalClosure, "done")
	select {
	case err := <-serverErrors:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("relay did not finish")
	}
	accountBConnection.mu.Lock()
	bWrites := len(accountBConnection.rawWrites)
	accountBConnection.mu.Unlock()
	require.Equal(t, 1, bWrites, "the create was replayed on account B")
	turn1, ok := recorder.get(1)
	require.True(t, ok)
	require.NotNil(t, turn1)
	require.Equal(t, int64(len(create)), *turn1)
}
