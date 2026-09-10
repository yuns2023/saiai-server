package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	req          *http.Request
	body         []byte
	responseBody string
	calls        int
}

type chatGPTStickyCache struct {
	service.GatewayCache
	bindings map[string]int64
}

func (c *chatGPTStickyCache) GetSessionAccountID(_ context.Context, _ int64, hash string) (int64, error) {
	return c.bindings[hash], nil
}

func (c *chatGPTStickyCache) SetSessionAccountID(_ context.Context, _ int64, hash string, accountID int64, _ time.Duration) error {
	if c.bindings == nil {
		c.bindings = make(map[string]int64)
	}
	c.bindings[hash] = accountID
	return nil
}

func (c *chatGPTStickyCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (u *chatGPTReplayUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	u.req = req
	u.body, _ = io.ReadAll(req.Body)
	responseBody := u.responseBody
	if responseBody == "" {
		responseBody = "data: {\"type\":\"message_stream_complete\",\"conversation_id\":\"fixture-conversation\"}\n\ndata: [DONE]\n\n"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"Set-Cookie":   []string{"upstream-secret=must-not-pass"},
		},
		Body: io.NopCloser(bytes.NewBufferString(responseBody)),
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
	cache := &chatGPTStickyCache{bindings: make(map[string]int64)}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIChatEnabled = true
	cfg.Gateway.OpenAIChatModelRequestCap = 1
	cfg.Gateway.OpenAIChatUnaccountedAllowed = true
	cfg.Gateway.OpenAIChatUpstreamBaseURL = "http://replay.example.test"
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	svc := service.NewOpenAIGatewayService(
		&chatGPTAccountRepo{account: account}, nil, nil, nil, nil, nil, cache, cfg,
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
	c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 5, Concurrency: 2})

	h.ChatGPTConversation(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	require.Empty(t, w.Header().Get("Set-Cookie"))
	require.Equal(t, "data: {\"type\":\"message_stream_complete\",\"conversation_id\":\"fixture-conversation\"}\n\ndata: [DONE]\n\n", w.Body.String())
	require.NotNil(t, upstream.req)
	require.Equal(t, "http://replay.example.test/backend-api/f/conversation?fixture=1", upstream.req.URL.String())
	require.Equal(t, string(body), string(upstream.body))
	require.Equal(t, "Bearer oauth-upstream-token", upstream.req.Header.Get("Authorization"))
	require.Equal(t, "upstream-account", upstream.req.Header.Get("ChatGPT-Account-ID"))
	require.Equal(t, "CodexBrowser Mozilla/5.0", upstream.req.Header.Get("User-Agent"))
	require.Equal(t, "Codex Browser", upstream.req.Header.Get("originator"))
	require.Empty(t, upstream.req.Header.Get("Cookie"))
	require.Equal(t, 1, upstream.calls)
	require.Equal(
		t, account.ID,
		cache.bindings["openai:"+service.ChatGPTConversationSessionHash("fixture-conversation")],
	)

	// A conversation_id and [DONE] without the protocol's explicit terminal
	// event are insufficient to confirm affinity or future accounting.
	h.openAIChatModelRequests.Store(0)
	upstream.responseBody = "data: {\"conversation_id\":\"partial-conversation\"}\n\ndata: [DONE]\n\n"
	partial := httptest.NewRecorder()
	cPartial, _ := gin.CreateTestContext(partial)
	cPartial.Request = httptest.NewRequest(http.MethodPost, "/chatgpt/backend-api/f/conversation", bytes.NewReader(body))
	cPartial.Request.Header.Set("Content-Type", "application/json")
	cPartial.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
		ID: 3, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
	})
	cPartial.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 5, Concurrency: 2})
	h.ChatGPTConversation(cPartial)
	require.Equal(t, http.StatusOK, partial.Code)
	require.NotContains(t, cache.bindings, "openai:"+service.ChatGPTConversationSessionHash("partial-conversation"))
	require.Equal(t, 2, upstream.calls)

	second := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(second)
	c2.Request = httptest.NewRequest(http.MethodPost, "/chatgpt/backend-api/f/conversation", bytes.NewReader(body))
	c2.Request.Header.Set("Content-Type", "application/json")
	c2.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
		ID: 3, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
	})
	c2.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 5, Concurrency: 2})
	h.ChatGPTConversation(c2)
	require.Equal(t, http.StatusTooManyRequests, second.Code)
	require.Contains(t, second.Body.String(), "request_cap_exceeded")
	require.Equal(t, 2, upstream.calls)
}

func TestChatGPTConversationRejectsUnaccountedModelRequestByDefault(t *testing.T) {
	groupID := int64(11)
	cfg := &config.Config{}
	cfg.Gateway.OpenAIChatEnabled = true
	cfg.Gateway.OpenAIChatUpstreamBaseURL = "http://replay.example.test"
	h := NewOpenAIGatewayHandler(nil, nil, nil, nil, nil, nil, nil, cfg)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(
		http.MethodPost,
		"/chatgpt/backend-api/f/conversation",
		bytes.NewBufferString(`{"model":"auto"}`),
	)
	c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
		ID: 9, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
	})

	h.ChatGPTConversation(c)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "accounting_unavailable")
}

func TestReleaseChatGPTControlSelection(t *testing.T) {
	released := 0
	releaseChatGPTControlSelection(&service.AccountSelectionResult{
		Acquired: true,
		ReleaseFunc: func() {
			released++
		},
	})
	require.Equal(t, 1, released)

	releaseChatGPTControlSelection(&service.AccountSelectionResult{
		Acquired: false,
		ReleaseFunc: func() {
			released++
		},
	})
	require.Equal(t, 1, released)
}
