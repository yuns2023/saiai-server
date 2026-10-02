package service

import (
	"context"
	"net/http"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestOpenAIGatewayService_Forward_RejectsUnapprovedCodexHeadersBeforeUpstream(t *testing.T) {
	cases := []struct{ name, ua, originator string }{
		{"curl with official originator", "curl/7.76.1", "codex_cli_rs"},
		{"missing UA", "", "codex_cli_rs"},
		{"embedded product", "curl/7.76.1 codex_cli_rs/0.159.2", "codex_cli_rs"},
		{"unknown originator", "codex_cli_rs/0.159.2", "codex_fake"},
	}
	accounts := []Account{
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"codex_cli_only": true}},
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_upstream_protocol": OpenAIUpstreamProtocolCodexNativeRelayV1}},
	}
	for i, account := range accounts {
		for _, tc := range cases {
			t.Run(account.Type+"/"+tc.name, func(t *testing.T) {
				c := newCodexDetectorTestContext(tc.ua, tc.originator)
				c.Request.Header.Set("version", "0.159.2")
				c.Request.Header.Set("chatgpt-account-id", "TEST_ONLY_ACCOUNT")
				upstream := &httpUpstreamRecorder{}
				svc := &OpenAIGatewayService{httpUpstream: upstream}
				result, err := svc.Forward(context.Background(), c, &accounts[i], []byte(`{"model":"TEST_ONLY_MODEL","input":"TEST_ONLY_INPUT"}`))
				require.Error(t, err)
				require.Nil(t, result)
				require.Equal(t, http.StatusForbidden, c.Writer.Status())
				require.Nil(t, upstream.lastReq, "no provider request may be sent on rejection")

				// A placeholder downstream connection must remain untouched: the
				// account gate runs before protocol resolution or any upstream dial.
				dialer := &openAIWSQueueDialer{}
				svc.openaiWSPassthroughDialer = dialer
				wsErr := svc.proxyResponsesWebSocketFromClientOnce(context.Background(), c,
					&coderws.Conn{}, &accounts[i], "TEST_ONLY_TOKEN", coderws.MessageText,
					[]byte(`{"type":"response.create","model":"TEST_ONLY_MODEL"}`), nil, nil)
				var closeErr *OpenAIWSClientCloseError
				require.ErrorAs(t, wsErr, &closeErr)
				require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
				require.Zero(t, dialer.DialCount(), "no upstream websocket may be opened on rejection")
			})
		}
	}
}
