package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestNativeAPIKeyResponsesPreserveClientBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		t.Run(path, func(t *testing.T) {
			body := []byte(`{ "model":"gpt-5", "stream":false,"max_output_tokens":37,"max_completion_tokens":39,"reasoning":{"effort":"minimal"},"prompt_cache_retention":"24h","safety_identifier":"test-user","previous_response_id":"resp_previous","future_field":{"exact":9007199254740993},"input":[] }`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			c.Request.Header.Set("User-Agent", "generic-sdk/1.0")
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"resp_ok","model":"gpt-5","usage":{"input_tokens":1,"output_tokens":1}}`)),
			}}
			svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "mock-upstream-key"}}
			_, err := svc.Forward(c.Request.Context(), c, account, body)
			require.NoError(t, err)
			require.Equal(t, body, upstream.lastBody)
			require.Equal(t, "Bearer mock-upstream-key", upstream.lastReq.Header.Get("Authorization"))
		})
	}
}

func TestAnthropicAPIKeyPassthroughPreservesQueryAndApplicationHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, countTokens := range []bool{false, true} {
		for _, query := range []string{"", "?", "?beta=false&future=a%2fb&future=a+b&empty="} {
			path := "/v1/messages"
			if countTokens {
				path += "/count_tokens"
			}
			t.Run(path+query, func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, path+query, nil)
				for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Cookie", "Proxy-Authorization", "Keep-Alive", "Te", "Trailer", "Upgrade", "X-Forwarded-For", "CF-Ray", "X-Envoy-Trace", "X-Saiai-Internal", "X-Sub2api-Internal", "X-Hop-Only"} {
					c.Request.Header.Set(name, "must-not-leave-gateway")
				}
				c.Request.Header.Add("Connection", "keep-alive")
				c.Request.Header.Add("Connection", "X-Hop-Only")
				c.Request.Header.Set("X-Client-Request-ID", "client-id")
				c.Request.Header.Add("X-Future-Feature", "first")
				c.Request.Header.Add("X-Future-Feature", "second")
				c.Request.Header.Set("Anthropic-Beta", "future-beta")
				body := []byte(`{ "model": "claude-fable-5", "future": 9007199254740993 }`)
				svc := &GatewayService{cfg: &config.Config{}}
				account := newAnthropicAPIKeyAccountForTest()
				var req *http.Request
				var err error
				if countTokens {
					req, err = svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(context.Background(), c, account, body, "mock-upstream")
				} else {
					req, err = svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(context.Background(), c, account, body, "mock-upstream")
				}
				require.NoError(t, err)
				require.Equal(t, "api.anthropic.com", req.URL.Host)
				require.Equal(t, c.Request.URL.RequestURI(), req.URL.RequestURI())
				require.Equal(t, []string{"first", "second"}, req.Header.Values("X-Future-Feature"))
				require.Equal(t, "client-id", req.Header.Get("X-Client-Request-ID"))
				require.Equal(t, "future-beta", req.Header.Get("Anthropic-Beta"))
				require.Equal(t, "mock-upstream", req.Header.Get("X-Api-Key"))
				for _, values := range req.Header {
					require.NotContains(t, values, "must-not-leave-gateway")
				}
				require.Empty(t, req.Header.Get("Connection"))
				got, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.Equal(t, body, got)
			})
		}
	}
}

func TestClaudeOAuthForwardPreservesClientCacheLayout(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		t.Run(accountType, func(t *testing.T) {
			c, _, parsed := claudeCodeCacheSafeRetryContext(t)
			body, err := sjson.SetRawBytes(parsed.Body, "messages.0.content", []byte(`[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}},{"type":"text","text":"b","cache_control":{"type":"ephemeral"}},{"type":"text","text":"c","cache_control":{"type":"ephemeral"}},{"type":"text","text":"d","cache_control":{"type":"ephemeral"}},{"type":"text","text":"e","cache_control":{"type":"ephemeral"}},{"type":"thinking","thinking":"opaque","cache_control":{"type":"ephemeral"},"signature":"opaque"}]`))
			require.NoError(t, err)
			body, err = sjson.SetBytes(body, "stream", false)
			require.NoError(t, err)
			body = expectedOAuthBillingBodyForTest(t, body, c.GetHeader("User-Agent"))
			parsed, err = ParseGatewayRequest(body, PlatformAnthropic)
			require.NoError(t, err)
			upstream := &anthropicHTTPUpstreamRecorder{resp: gatewayHTTPSuccessResponse()}
			svc := claudeCodeCacheSafeRetryService(upstream)
			account := claudeCodeCacheSafeRetryAccount()
			account.Type = accountType
			if accountType == AccountTypeOAuth {
				account.Extra["account_uuid"] = "upstream-account"
			}
			_, err = svc.Forward(c.Request.Context(), c, account, parsed)
			require.NoError(t, err)
			require.Equal(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(upstream.lastBody, "messages").Raw)
			require.Equal(t, 6, strings.Count(string(upstream.lastBody), `"cache_control"`))
		})
	}
}
