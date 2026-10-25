package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type clientTurnRejectionMarker struct{}

func (clientTurnRejectionMarker) Error() string { return "TEST_ONLY_CONTENT_REJECTION" }

// A later client turn rejected by OnClientTurn on the native OAuth relay
// (official Codex => resumable client reader) must: let the hook write its
// own error frame to the client, never forward the rejected frame, keep the
// completed turn's usage, and return the hook's error cause to the handler.
func TestOpenAIWSNativeRelayLaterClientTurnRejection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 2
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 2

	upstream := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_turn_1","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":3}}}`),
	}}
	dialer := &openAIWSQueueDialer{conns: []openAIWSClientConn{&openAIWSQuotaGateConn{openAIWSCaptureConn: upstream, holdOpen: true}}}
	account := &Account{ID: 871, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	repository := &openAICodexSnapshotAsyncRepo{updateExtraCh: make(chan map[string]any, 16), rateLimitCh: make(chan time.Time, 16)}
	gateway := &OpenAIGatewayService{cfg: cfg, accountRepo: repository, rateLimitService: &RateLimitService{accountRepo: repository}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), openaiWSStateStore: NewOpenAIWSStateStore(nil), openaiWSPassthroughDialer: dialer}

	completed := make(chan int, 4)
	serverErrors := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, acceptErr := coderws.Accept(writer, request, nil)
		if acceptErr != nil {
			serverErrors <- acceptErr
			return
		}
		defer func() { _ = connection.CloseNow() }()
		hooks := &OpenAIWSIngressHooks{
			OnClientTurn: func(turn int, payload []byte) error {
				if turn < 2 || !strings.Contains(string(payload), "TEST_ONLY_REJECT") {
					return nil
				}
				writeCtx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
				defer cancel()
				_ = connection.Write(writeCtx, coderws.MessageText, []byte(`{"type":"error","status":403,"error":{"type":"permission_error","code":"TEST_ONLY_CODE","message":"rejected"}}`))
				return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "TEST_ONLY_REASON", clientTurnRejectionMarker{})
			},
			AfterTurn: func(turn int, result *OpenAIForwardResult, turnErr error) {
				if turnErr == nil && result != nil && result.Usage.OutputTokens > 0 {
					completed <- turn
				}
			},
			OnAccountExhausted: func(*OpenAIWSAccountFailoverError) (*OpenAIWSFailoverTarget, error) {
				return nil, errors.New("unexpected failover")
			},
		}
		ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginContext.Request = request.Clone(request.Context())
		ginContext.Request.Header.Set("User-Agent", "codex_cli_rs/0.163.0 (Mac OS 15.5.0; arm64) Apple_Terminal/455.1")
		ginContext.Request.Header.Set("originator", "codex_cli_rs")
		ginContext.Set("api_key", &APIKey{UserID: 77})
		messageType, payload, readErr := connection.Read(request.Context())
		if readErr != nil {
			serverErrors <- readErr
			return
		}
		proxyErr := gateway.ProxyResponsesWebSocketFromClient(request.Context(), ginContext, connection, account, "TEST_ONLY_OAUTH", messageType, payload, hooks)
		_ = connection.Close(coderws.StatusPolicyViolation, "TEST_ONLY_REASON")
		serverErrors <- proxyErr
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()

	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]}]}`)))
	_, payload, err := client.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "resp_turn_1", gjson.GetBytes(payload, "response.id").String())

	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"TEST_ONLY_REJECT"}]}]}`)))
	_, payload, err = client.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "error", gjson.GetBytes(payload, "type").String())
	require.Equal(t, "TEST_ONLY_CODE", gjson.GetBytes(payload, "error.code").String())

	select {
	case proxyErr := <-serverErrors:
		var marker clientTurnRejectionMarker
		require.ErrorAs(t, proxyErr, &marker)
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, proxyErr, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
	case <-ctx.Done():
		t.Fatal("relay did not end after the rejected turn")
	}
	require.Equal(t, 1, <-completed, "the forwarded turn keeps its usage")
	// After return the handler's 1008 close is best effort: stopping the
	// resumable reader can tear the connection down first. That is why the
	// error frame is written from the hook, while the relay still owns it.
	_, _, err = client.Read(ctx)
	require.Error(t, err)
	upstream.mu.Lock()
	writes := len(upstream.writes)
	upstream.mu.Unlock()
	require.Equal(t, 1, writes, "the rejected frame is never forwarded upstream")
}
