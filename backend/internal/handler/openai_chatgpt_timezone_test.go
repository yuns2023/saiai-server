package handler

import (
	"bytes"
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

func TestChatGPTConversationRewritesGuardedGroupTimezone(t *testing.T) {
	previous := codexTimezoneNow
	codexTimezoneNow = func() time.Time { return time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { codexTimezoneNow = previous })

	const body = `{"conversation_origin":null,"timezone":"Asia/Shanghai","timezone_offset_min":-480}`
	cases := []struct {
		name   string
		group  service.Group
		status int
		sent   string
	}{
		{"guard off forwards unchanged", service.Group{}, 403, body},
		{"rewrite group", service.Group{CodexCNTimezoneGuard: true, CodexTimezoneAction: service.CodexTimezoneActionRewrite}, 403,
			`{"conversation_origin":null,"timezone":"America/Los_Angeles","timezone_offset_min":480}`},
		{"reject group rewrites instead of rejecting", service.Group{CodexCNTimezoneGuard: true, CodexTimezoneAction: service.CodexTimezoneActionReject}, 403,
			`{"conversation_origin":null,"timezone":"America/Los_Angeles","timezone_offset_min":480}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			groupID := int64(9)
			account := service.Account{ID: 19, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Status: service.StatusActive, Schedulable: true, Concurrency: 10,
				Credentials: map[string]any{"access_token": "TEST_ONLY", "chatgpt_account_id": "TEST_ONLY_ACCOUNT"}}
			concurrency := service.NewConcurrencyService(&chatGPTSlotCache{active: make(map[string]int64), attempts: make(map[int64]int)})
			upstream := &chatGPTReplayUpstream{statusCode: 403, contentType: "application/json", responseBody: `{"detail":"TEST_ONLY"}`}
			cfg := &config.Config{}
			cfg.Gateway.OpenAIChatEnabled = true
			svc := service.NewOpenAIGatewayService(&chatGPTAccountRepo{account: account}, nil, nil, nil, nil, nil, nil,
				cfg, nil, concurrency, nil, nil, nil, upstream, nil, nil)
			h := NewOpenAIGatewayHandler(svc, concurrency, nil, nil, nil, nil, nil, cfg, nil)
			group := tc.group
			group.ID, group.Platform = groupID, service.PlatformOpenAI

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/chatgpt/backend-api/conversation/init", bytes.NewBufferString(body))
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{ID: 3, GroupID: &groupID, Group: &group})
			h.ChatGPTConversation(c)

			require.Equal(t, tc.status, w.Code)
			require.Equal(t, 1, upstream.calls)
			require.JSONEq(t, tc.sent, string(upstream.body))
		})
	}
}

func TestChatGPTConversationRejectsRepeatedTimezoneInGuardedGroup(t *testing.T) {
	groupID := int64(9)
	cfg := &config.Config{}
	cfg.Gateway.OpenAIChatEnabled = true
	upstream := &chatGPTReplayUpstream{statusCode: 200}
	concurrency := service.NewConcurrencyService(&chatGPTSlotCache{active: make(map[string]int64), attempts: make(map[int64]int)})
	svc := service.NewOpenAIGatewayService(&chatGPTAccountRepo{}, nil, nil, nil, nil, nil, nil,
		cfg, nil, concurrency, nil, nil, nil, upstream, nil, nil)
	h := NewOpenAIGatewayHandler(svc, concurrency, nil, nil, nil, nil, nil, cfg, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/chatgpt/backend-api/conversation/init",
		bytes.NewBufferString(`{"timezone":"America/Los_Angeles","timezone":"Asia/Shanghai"}`))
	c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{ID: 3, GroupID: &groupID, Group: &service.Group{ID: groupID,
		Platform: service.PlatformOpenAI, CodexCNTimezoneGuard: true, CodexTimezoneAction: service.CodexTimezoneActionRewrite}})
	h.ChatGPTConversation(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Zero(t, upstream.calls)
}
