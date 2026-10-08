package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSPassthroughProviderRefusalHasNoSuccessfulUsage(t *testing.T) {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	request := []byte(` {"type":"response.create","model":"test-luna","reasoning":{"effort":"low"},"input":[],"tools":[{"type":"image_gen"}]} `)
	frame := []byte(` {"type":"error","status":400,"error":{"type":"invalid_request_error","message":"The 'test-luna' model is not supported when using Codex with a ChatGPT account."}} `)
	upstream := &openAIWSCaptureConn{events: [][]byte{frame}}
	dialer := &openAIWSCaptureDialer{conn: upstream}
	svc := &OpenAIGatewayService{cfg: cfg, openaiWSPassthroughDialer: dialer, cache: &stubGatewayCache{}}
	account := &Account{ID: 452, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	done := make(chan struct{})
	var successes int
	var failures []*OpenAIWSUpstreamFailure
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
		messageType, first, err := conn.Read(ctx)
		if err != nil {
			return
		}
		_ = svc.proxyResponsesWebSocketV2Passthrough(ctx, c, conn, account, "TEST_ONLY_TOKEN", messageType, first, &OpenAIWSIngressHooks{
			AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
				if result != nil && err == nil {
					successes++
				}
			},
			OnUpstreamError: func(failure *OpenAIWSUpstreamFailure) { failures = append(failures, failure) },
		}, OpenAIWSProtocolDecision{}, nil)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, request))
	_, got, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, frame, got)
	_ = conn.CloseNow()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("mock relay did not finish")
	}
	require.Zero(t, successes, "error or graceful EOF must not trigger a successful usage callback")
	require.Len(t, failures, 1)
	require.Equal(t, 400, failures[0].Status)
	require.Equal(t, "test-luna", failures[0].Model)
	require.Equal(t, 1, failures[0].Turn)
	require.Equal(t, int64(452), failures[0].AccountID)
	require.Equal(t, request, upstream.rawWrites[0])
	require.Equal(t, 1, dialer.DialCount(), "do not retry or switch accounts for a generic model refusal")
}
