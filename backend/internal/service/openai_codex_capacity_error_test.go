//go:build unit

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
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexCapacityErrorsRemoveClientUpsellTriggers(t *testing.T) {
	for _, payload := range []string{
		`{"error":{"type":"usage_limit_reached","plan_type":"plus","resets_at":123,"message":"Upgrade to Pro"}}`,
		`{"type":"error","sequence_number":9,"error":{"type":"usage_limit_reached","plan_type":"plus","message":"Upgrade to Pro"}}`,
		`{"type":"response.failed","response":{"id":"resp_owned","status":"failed","error":{"code":"usage_limit_reached","plan_type":"plus"},"usage":{"output_tokens":2}}}`,
		`{"type":"response.done","response":{"id":"resp_owned","status":"failed","error":{"type":"insufficient_quota","message":"buy credits"}}}`,
	} {
		for _, account := range []*Account{auditOAuthPolicyAccount(), newOpenAINativeRelayAccount("https://relay.example")} {
			normalized := normalizeOpenAICodexCapacityError(account, []byte(payload))
			require.Contains(t, string(normalized), codexUpstreamCapacityCode)
			require.NotContains(t, string(normalized), "usage_limit_reached")
			require.NotContains(t, string(normalized), "plan_type")
			require.NotContains(t, string(normalized), "resets_at")
			require.NotContains(t, string(normalized), "Upgrade to Pro")
			require.Equal(t, gjson.Get(payload, "type").String(), gjson.GetBytes(normalized, "type").String())
			require.Equal(t, gjson.Get(payload, "response.id").String(), gjson.GetBytes(normalized, "response.id").String())
			require.Equal(t, gjson.Get(payload, "response.usage.output_tokens").Int(), gjson.GetBytes(normalized, "response.usage.output_tokens").Int())
		}
		apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
		require.Equal(t, payload, string(normalizeOpenAICodexCapacityError(apiKey, []byte(payload))))
	}
}

func TestCodexCapacityNormalizationLeavesUnrelatedFramesUnchanged(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.output_text.delta","delta":"usage_limit_reached"}`,
		`{"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded"}}`,
		`{"type":"error","error":{"type":"rate_limit_exceeded"}}`,
		`{"type":"response.done","response":{"status":"completed","error":{"type":"usage_limit_reached"}}}`,
		`not json`,
	} {
		require.Equal(t, payload, string(normalizeOpenAICodexCapacityError(auditOAuthPolicyAccount(), []byte(payload))))
	}
}

func TestCodexCapacityErrorSSEObservesOriginalBeforeNormalization(t *testing.T) {
	payload := `{"type":"response.failed","response":{"id":"resp_capacity","status":"failed","error":{"type":"usage_limit_reached","plan_type":"plus","resets_at":123}}}`
	ctx, recorder := auditOAuthPolicyContext("/v1/responses")
	response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: " + payload + "\n\n"))}
	svc := &OpenAIGatewayService{}
	_, err := svc.handleStreamingResponse(context.Background(), response, ctx, auditOAuthPolicyAccount(), time.Now(), "model", "model")
	require.Error(t, err)
	require.Contains(t, recorder.Body.String(), codexUpstreamCapacityCode)
	require.NotContains(t, recorder.Body.String(), "usage_limit_reached")
	require.NotContains(t, recorder.Body.String(), "plan_type")
}

func TestCodexCapacityNativeRelayHTTPErrorDoesNotChangeAccount(t *testing.T) {
	payload := `{"error":{"type":"usage_limit_reached","plan_type":"plus","resets_at":123}}`
	ctx, recorder := auditOAuthPolicyContext("/v1/responses")
	account := newOpenAINativeRelayAccount("https://relay.example")
	svc := &OpenAIGatewayService{}
	_, err := svc.handleNativeRelayErrorResponse(&http.Response{StatusCode: 429, Header: make(http.Header)}, ctx, account, []byte(payload))
	require.Error(t, err)
	require.Equal(t, 503, recorder.Code)
	require.Equal(t, 429, ctx.GetInt(OpsUpstreamStatusCodeKey))
	require.Contains(t, recorder.Body.String(), codexUpstreamCapacityCode)
	require.Nil(t, account.RateLimitResetAt)
}

func TestCodexCapacityOAuthHTTPFailureUsesCapacityStatus(t *testing.T) {
	payload := `{"error":{"type":"usage_limit_reached","plan_type":"plus","resets_at":123}}`
	ctx, recorder := auditOAuthPolicyContext("/v1/responses")
	ctx.Header("Content-Length", "9999")
	account := auditOAuthPolicyAccount()
	svc := &OpenAIGatewayService{}
	_, err := svc.handleErrorResponse(context.Background(), &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, ctx, account, nil)
	require.Error(t, err)
	require.Equal(t, 503, recorder.Code)
	require.Empty(t, recorder.Header().Get("Content-Length"))
	require.Equal(t, 429, ctx.GetInt(OpsUpstreamStatusCodeKey))
	require.Contains(t, recorder.Body.String(), codexUpstreamCapacityCode)
}

func TestCodexCapacityWSPassthroughNormalizesOnlyDownstream(t *testing.T) {
	for _, messageType := range []coderws.MessageType{coderws.MessageText, coderws.MessageBinary} {
		t.Run(messageType.String(), func(t *testing.T) {
			payload := []byte(`{"type":"error","error":{"type":"usage_limit_reached","plan_type":"plus","resets_at":123}}`)
			conn := &openAIWSCaptureConn{events: [][]byte{payload}, eventTypes: []coderws.MessageType{messageType}}
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModePassthrough
			account := auditOAuthPolicyAccount()
			account.Extra = map[string]any{"responses_websockets_v2_enabled": true}
			svc := &OpenAIGatewayService{cfg: cfg, openaiWSPassthroughDialer: &openAIWSCaptureDialer{conn: conn}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg)}
			serverErr := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				client, err := coderws.Accept(writer, request, nil)
				if err != nil {
					serverErr <- err
					return
				}
				defer func() { _ = client.CloseNow() }()
				ctx, _ := auditOAuthPolicyContext("/v1/responses")
				kind, first, err := client.Read(request.Context())
				if err != nil {
					serverErr <- err
					return
				}
				serverErr <- svc.ProxyResponsesWebSocketFromClient(request.Context(), ctx, client, account, "MOCK_ONLY", kind, first, nil)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer func() { _ = client.CloseNow() }()
			request := []byte(`{"type":"response.create","model":"fixture","input":[]}`)
			require.NoError(t, client.Write(ctx, coderws.MessageText, request))
			_, received, err := client.Read(ctx)
			require.NoError(t, err)
			require.Contains(t, string(received), codexUpstreamCapacityCode)
			require.NotContains(t, string(received), "usage_limit_reached")
			require.NoError(t, <-serverErr)
			conn.mu.Lock()
			require.Equal(t, request, conn.rawWrites[0])
			conn.mu.Unlock()
		})
	}
}
