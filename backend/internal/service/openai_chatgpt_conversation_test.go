package service

import (
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

func TestBuildChatGPTConversationRequestPreservesNativeShape(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/chatgpt/backend-api/f/conversation?foo=bar", nil)
	c.Request.Header.Set("User-Agent", "CodexBrowser Mozilla/5.0")
	c.Request.Header.Set("originator", "Codex Browser")
	c.Request.Header.Set("OAI-Language", "en-US")
	c.Request.Header.Set("Authorization", "Bearer local-gateway-key")
	c.Request.Header.Set("Cookie", "client-cookie=must-not-pass")
	c.Request.Header.Set("ChatGPT-Account-ID", "client-account")

	account := &Account{
		ID:       42,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "oauth-upstream-token",
			"chatgpt_account_id": "upstream-account",
		},
	}
	body := []byte(`{"action":"next","model":"auto","timezone":"America/Los_Angeles"}`)
	req, proxyURL, err := (&OpenAIGatewayService{}).BuildChatGPTConversationRequest(
		context.Background(), c, account, body, c.Request.URL.RequestURI(),
	)
	require.NoError(t, err)
	require.Empty(t, proxyURL)
	require.Equal(t, "https://chatgpt.com/backend-api/f/conversation?foo=bar", req.URL.String())
	require.Equal(t, "chatgpt.com", req.Host)
	require.Equal(t, "Bearer oauth-upstream-token", req.Header.Get("Authorization"))
	require.Equal(t, "upstream-account", req.Header.Get("ChatGPT-Account-ID"))
	require.Equal(t, "CodexBrowser Mozilla/5.0", req.Header.Get("User-Agent"))
	require.Equal(t, "Codex Browser", req.Header.Get("originator"))
	require.Empty(t, req.Header.Get("Cookie"))
	gotBody, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(gotBody))
}

func TestBuildChatGPTConversationRequestRejectsNonOAuthAndForeignPath(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/chatgpt/backend-api/f/conversation", strings.NewReader(`{}`))

	_, _, err := (&OpenAIGatewayService{}).BuildChatGPTConversationRequest(
		context.Background(), c, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, nil, c.Request.URL.Path,
	)
	require.ErrorContains(t, err, "requires an OpenAI OAuth account")

	_, _, err = (&OpenAIGatewayService{}).BuildChatGPTConversationRequest(
		context.Background(), c, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, nil, "/v1/responses",
	)
	require.ErrorContains(t, err, "invalid native ChatGPT path")
}

func TestForwardChatGPTConversationUsesConfiguredReplayUpstream(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: fixture\n\n")),
	}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIChatUpstreamBaseURL = "http://replay.example.test"
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/chatgpt/backend-api/f/conversation?fixture=1", nil)
	account := &Account{
		ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "replay-account",
		},
	}
	resp, err := svc.ForwardChatGPTConversation(
		context.Background(), c, account, []byte(`{"model":"auto"}`), c.Request.URL.RequestURI(),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "http://replay.example.test/backend-api/f/conversation?fixture=1", upstream.lastReq.URL.String())
}

func TestChatGPTConversationSessionIdentity(t *testing.T) {
	firstTurn := []byte(`{"action":"next","messages":[{}]}`)
	require.Empty(t, ResolveChatGPTConversationSessionHash(firstTurn))

	continuation := []byte(`{"conversation_id":"conv-test","parent_message_id":"msg-test"}`)
	hash := ResolveChatGPTConversationSessionHash(continuation)
	require.NotEmpty(t, hash)
	require.Equal(t, ChatGPTConversationSessionHash("conv-test"), hash)
	require.NotEqual(t, ChatGPTConversationSessionHash("conv-other"), hash)

	sse := []byte("data: {\"type\":\"message\",\"conversation_id\":\"conv-test\"}\n\n" +
		"data: [DONE]\n\n")
	require.Equal(t, "conv-test", ExtractChatGPTConversationID(sse))
	require.Equal(t, "conv-json", ExtractChatGPTConversationID([]byte(`{"conversation_id":"conv-json"}`)))
	require.Empty(t, ExtractChatGPTConversationID([]byte("data: [DONE]\n\n")))
}
