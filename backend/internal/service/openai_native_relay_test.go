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
)

func newOpenAINativeRelayAccount(baseURL string) *Account {
	return &Account{
		ID:          901,
		Name:        "native-relay",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "upstream-relay-key",
			"base_url": baseURL,
		},
		Extra: map[string]any{
			"openai_upstream_protocol": OpenAIUpstreamProtocolCodexNativeRelayV1,
		},
	}
}

func newOfficialCodexRelayContext(path string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.153.4")
	c.Request.Header.Set("originator", "codex_cli_rs")
	c.Request.Header.Set("OpenAI-Beta", "responses=client-native")
	c.Request.Header.Set("Version", "0.153.4")
	c.Request.Header.Set("chatgpt-account-id", "client-account")
	c.Request.Header.Set("Authorization", "Bearer inbound-key")
	c.Request.Header.Set("Cookie", "session=must-not-forward")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	return c, rec
}

func TestOpenAICodexNativeRelayForwardsRequestShapeAndIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := newOfficialCodexRelayContext("/openai/v1/responses/compact?trace=keep&mode=native")
	body := []byte(`{"model":"gpt-5.5","stream":false,"reasoning":{"effort":"minimal"},"previous_response_id":"resp_keep","future_field":{"keep":true}}`)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_ok","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}`)),
	}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, nativeRelayInstanceID: "relay-a"}

	result, err := svc.Forward(context.Background(), c, newOpenAINativeRelayAccount("https://relay.example/v1"), body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, body, upstream.lastBody)
	require.Equal(t, "https://relay.example/v1/responses/compact?trace=keep&mode=native", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer upstream-relay-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "client-account", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.Equal(t, "codex_cli_rs/0.153.4", upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "codex_cli_rs", upstream.lastReq.Header.Get("originator"))
	require.Equal(t, "responses=client-native", upstream.lastReq.Header.Get("OpenAI-Beta"))
	require.Equal(t, "0.153.4", upstream.lastReq.Header.Get("Version"))
	require.Equal(t, "relay-a", upstream.lastReq.Header.Get(openAINativeRelayChainHeader))
	require.Empty(t, upstream.lastReq.Header.Get("Cookie"))
}

func TestOpenAICodexNativeRelayComposesAcrossHopsAndTerminatesAtOAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.5","stream":true,"previous_response_id":"resp_keep","input":"hello"}`)
	account := newOpenAINativeRelayAccount("https://relay-b.example")

	cA, _ := newOfficialCodexRelayContext("/v1/responses")
	svcA := &OpenAIGatewayService{cfg: &config.Config{}, nativeRelayInstanceID: "relay-a"}
	reqA, err := svcA.buildUpstreamRequest(context.Background(), cA, account, body, "key-a", true, "", true)
	require.NoError(t, err)
	require.Equal(t, "relay-a", reqA.Header.Get(openAINativeRelayChainHeader))

	cB, _ := newOfficialCodexRelayContext("/v1/responses")
	cB.Request.Header = reqA.Header.Clone()
	svcB := &OpenAIGatewayService{cfg: &config.Config{}, nativeRelayInstanceID: "relay-b"}
	reqB, err := svcB.buildUpstreamRequest(context.Background(), cB, account, body, "key-b", true, "", true)
	require.NoError(t, err)
	require.Equal(t, "relay-a, relay-b", reqB.Header.Get(openAINativeRelayChainHeader))
	require.Equal(t, "Bearer key-b", reqB.Header.Get("Authorization"))
	require.Equal(t, "client-account", reqB.Header.Get("chatgpt-account-id"))

	cTerminal, _ := newOfficialCodexRelayContext("/v1/responses")
	cTerminal.Request.Header = reqB.Header.Clone()
	oauthAccount := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "terminal-oauth-account",
		},
	}
	terminalReq, err := svcB.buildUpstreamRequest(context.Background(), cTerminal, oauthAccount, body, "oauth-token", true, "", true)
	require.NoError(t, err)
	require.Empty(t, terminalReq.Header.Get(openAINativeRelayChainHeader), "internal relay chain must not reach the provider")
	require.Equal(t, "terminal-oauth-account", terminalReq.Header.Get("chatgpt-account-id"))
	require.Equal(t, "Bearer oauth-token", terminalReq.Header.Get("Authorization"))
	terminalBody, err := io.ReadAll(terminalReq.Body)
	require.NoError(t, err)
	require.Equal(t, body, terminalBody)
}

func TestOpenAICodexNativeRelayForwardsModelsManifestWithoutAdjustment(t *testing.T) {
	manifestBody := []byte(`{"models":[{"slug":"gpt-5.6-sol","use_responses_lite":true}]}`)
	responseHeaders := make(http.Header)
	responseHeaders.Set("Content-Type", "application/json")
	responseHeaders.Set("ETag", `"native"`)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     responseHeaders,
		Body:       io.NopCloser(bytes.NewReader(manifestBody)),
	}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, nativeRelayInstanceID: "relay-a"}
	headers := make(http.Header)
	headers.Set("User-Agent", "codex_cli_rs/0.153.4")
	headers.Set("originator", "codex_cli_rs")
	headers.Set("Version", "0.153.4")
	headers.Set("chatgpt-account-id", "client-account")
	headers.Set("Authorization", "Bearer inbound-key")

	manifest, err := svc.FetchCodexModelsManifest(
		context.Background(),
		newOpenAINativeRelayAccount("https://relay.example/openai/v1"),
		"0.153.4",
		"",
		headers,
	)

	require.NoError(t, err)
	require.Equal(t, manifestBody, manifest.Body)
	require.Equal(t, `"native"`, manifest.ETag)
	require.Equal(t, "https://relay.example/openai/v1/models?client_version=0.153.4", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer upstream-relay-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "client-account", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.Equal(t, "codex_cli_rs/0.153.4", upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "relay-a", upstream.lastReq.Header.Get(openAINativeRelayChainHeader))
}

func TestOpenAICodexNativeRelayReturnsUpstreamErrorWithoutRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, rec := newOfficialCodexRelayContext("/v1/responses")
	errorBody := `{"error":{"type":"rate_limit_error","message":"upstream pool busy"}}`
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"Retry-After":  []string{"7"},
		},
		Body: io.NopCloser(strings.NewReader(errorBody)),
	}}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, nativeRelayInstanceID: "relay-a"}
	account := newOpenAINativeRelayAccount("https://relay.example")
	account.Credentials["pool_mode"] = true
	account.Credentials["pool_mode_retry_count"] = 10

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.5","input":"hello"}`))

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, 1, upstream.callCount, "native relay must not multiply retries even when pool mode is stale/enabled")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, errorBody, rec.Body.String())
	require.Equal(t, "7", rec.Header().Get("Retry-After"))
}

func TestOpenAICodexNativeRelayDetectsLoopBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, rec := newOfficialCodexRelayContext("/v1/responses")
	c.Request.Header.Set(openAINativeRelayChainHeader, "relay-z, relay-a")
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, nativeRelayInstanceID: "relay-a"}

	result, err := svc.Forward(context.Background(), c, newOpenAINativeRelayAccount("https://relay.example"), []byte(`{"model":"gpt-5.5"}`))

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusLoopDetected, rec.Code)
	require.Nil(t, upstream.lastReq)
}

func TestOpenAICodexNativeRelayRejectsWebSocketV1(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, rec := newOfficialCodexRelayContext("/v1/responses")
	SetOpenAIClientTransport(c, OpenAIClientTransportWS)
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, nativeRelayInstanceID: "relay-a"}

	result, err := svc.Forward(context.Background(), c, newOpenAINativeRelayAccount("https://relay.example"), []byte(`{"model":"gpt-5.5"}`))

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "HTTP/SSE only")
	require.Nil(t, upstream.lastReq)
}

func TestOpenAICodexNativeRelayRequiresGatewayBaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, baseURL := range []string{"", "https://api.openai.com/v1"} {
		t.Run(baseURL, func(t *testing.T) {
			c, rec := newOfficialCodexRelayContext("/v1/responses")
			account := newOpenAINativeRelayAccount(baseURL)
			upstream := &httpUpstreamRecorder{}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, nativeRelayInstanceID: "relay-a"}

			result, err := svc.Forward(context.Background(), c, account, bytes.TrimSpace([]byte(`{"model":"gpt-5.5"}`)))

			require.Error(t, err)
			require.Nil(t, result)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), "upstream Gateway Base URL")
			require.Nil(t, upstream.lastReq)
		})
	}
}
