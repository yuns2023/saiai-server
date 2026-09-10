package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type chatGPTAccountRepo struct {
	service.AccountRepository
	account service.Account
}

func (r *chatGPTAccountRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]service.Account, error) {
	return []service.Account{r.account}, nil
}

func (r *chatGPTAccountRepo) GetByID(context.Context, int64) (*service.Account, error) {
	account := r.account
	return &account, nil
}

type chatGPTReplayUpstream struct {
	req   *http.Request
	body  []byte
	calls int
}

func (u *chatGPTReplayUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	u.req = req
	u.body, _ = io.ReadAll(req.Body)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"Set-Cookie":   []string{"upstream-secret=must-not-pass"},
		},
		Body: io.NopCloser(bytes.NewBufferString("data: fixture-one\n\ndata: [DONE]\n\n")),
	}, nil
}

func (u *chatGPTReplayUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ bool) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestChatGPTConversationStreamsReplayWithoutProtocolConversion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(9)
	account := service.Account{
		ID: 81, Name: "oauth-replay", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Status: service.StatusActive,
		Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID},
		Credentials: map[string]any{
			"access_token":       "oauth-upstream-token",
			"chatgpt_account_id": "upstream-account",
		},
	}
	upstream := &chatGPTReplayUpstream{}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIChatEnabled = true
	cfg.Gateway.OpenAIChatModelRequestCap = 1
	cfg.Gateway.OpenAIChatUpstreamBaseURL = "http://replay.example.test"
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	svc := service.NewOpenAIGatewayService(
		&chatGPTAccountRepo{account: account}, nil, nil, nil, nil, nil, nil, cfg,
		nil, nil, nil, nil, nil, upstream, nil, nil,
	)
	h := NewOpenAIGatewayHandler(svc, nil, nil, nil, nil, nil, nil, cfg)

	body := []byte(`{"action":"next","messages":[{"id":"m1"}],"model":"auto","timezone":"America/Los_Angeles","timezone_offset_min":420}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/chatgpt/backend-api/f/conversation?fixture=1", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "CodexBrowser Mozilla/5.0")
	c.Request.Header.Set("originator", "Codex Browser")
	c.Request.Header.Set("Authorization", "Bearer local-gateway-key")
	c.Request.Header.Set("Cookie", "client-secret=must-not-pass")
	c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
		ID: 3, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
	})

	h.ChatGPTConversation(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	require.Empty(t, w.Header().Get("Set-Cookie"))
	require.Equal(t, "data: fixture-one\n\ndata: [DONE]\n\n", w.Body.String())
	require.NotNil(t, upstream.req)
	require.Equal(t, "http://replay.example.test/backend-api/f/conversation?fixture=1", upstream.req.URL.String())
	require.Equal(t, string(body), string(upstream.body))
	require.Equal(t, "Bearer oauth-upstream-token", upstream.req.Header.Get("Authorization"))
	require.Equal(t, "upstream-account", upstream.req.Header.Get("ChatGPT-Account-ID"))
	require.Equal(t, "CodexBrowser Mozilla/5.0", upstream.req.Header.Get("User-Agent"))
	require.Equal(t, "Codex Browser", upstream.req.Header.Get("originator"))
	require.Empty(t, upstream.req.Header.Get("Cookie"))
	require.Equal(t, 1, upstream.calls)

	second := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(second)
	c2.Request = httptest.NewRequest(http.MethodPost, "/chatgpt/backend-api/f/conversation", bytes.NewReader(body))
	c2.Request.Header.Set("Content-Type", "application/json")
	c2.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
		ID: 3, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
	})
	h.ChatGPTConversation(c2)
	require.Equal(t, http.StatusTooManyRequests, second.Code)
	require.Contains(t, second.Body.String(), "request_cap_exceeded")
	require.Equal(t, 1, upstream.calls)
}
