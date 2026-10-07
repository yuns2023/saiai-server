package handler

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type chatGPTSharedTurnCache struct {
	*chatGPTStickyCache
	service.ChatGPTTurnCache
}

type chatGPTDisconnectLeaseWriter struct {
	gin.ResponseWriter
	cancel      context.CancelFunc
	afterCancel func()
}

func (w *chatGPTDisconnectLeaseWriter) Write(p []byte) (int, error) {
	w.cancel()
	w.afterCancel()
	return 0, errors.New("TEST_ONLY client disconnected")
}

func TestChatGPTProHandoffResumeUsesOriginalAccountPriceAndIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(17)
	user := &service.User{ID: 21, Balance: 100}
	owner := service.Account{ID: 22, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive,
		Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{"access_token": "TEST_ONLY_OAUTH", "chatgpt_account_id": "TEST_ONLY_ACCOUNT"}}
	accounts := &chatGPTAccountRepo{account: owner}
	slots := &chatGPTSlotCache{active: make(map[string]int64), attempts: make(map[int64]int)}
	concurrency := service.NewConcurrencyService(slots)
	shared := &chatGPTSharedTurnCache{&chatGPTStickyCache{bindings: make(map[string]int64)}, service.NewChatGPTMemoryTurnCache()}
	usage := &chatGPTUsageLogCapture{}
	provider := &chatGPTReplayUpstream{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.OpenAIChatEnabled = true
	billing := service.NewBillingCacheService(nil, nil, nil, nil, cfg)
	t.Cleanup(billing.Stop)
	prices := &chatGPTPriceReader{price: 0.02, tiers: map[string]float64{"instant": 0.01, "medium": 0.02, "high": 0.03, "extreme": 0.04, "pro": 0.05}}
	newHandler := func() *OpenAIGatewayHandler {
		svc := service.NewOpenAIGatewayService(accounts, usage, nil, nil, nil, nil, shared, cfg, nil, concurrency, nil, nil, billing, provider, &service.DeferredService{}, nil)
		h := NewOpenAIGatewayHandler(svc, concurrency, billing, nil, nil, nil, nil, cfg, nil)
		h.chatGPTBillingSettings = prices
		return h
	}
	disconnect := false
	run := func(h *OpenAIGatewayHandler, path, body string, keyID, userID, gid int64) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		clientContext, cancel := context.WithCancel(context.Background())
		defer cancel()
		router := gin.New()
		forward := func(c *gin.Context) {
			if disconnect {
				c.Writer = &chatGPTDisconnectLeaseWriter{c.Writer, cancel, func() {
					require.Error(t, clientContext.Err())
					require.NoError(t, slots.lastAccountContext.Err(), "account lease must follow the upstream drain")
					require.NoError(t, slots.lastUserContext.Err(), "user lease must follow the upstream drain")
					require.Len(t, slots.active, 1)
					require.Len(t, slots.users, 1)
				}}
			}
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{ID: keyID, GroupID: &gid,
				Group: &service.Group{ID: gid, Platform: service.PlatformOpenAI, RateMultiplier: 1.25}, User: user})
			c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: userID, Concurrency: 2})
			h.ChatGPTConversation(c)
		}
		router.POST("/chatgpt/backend-api/f/conversation", forward)
		router.POST("/chatgpt/backend-api/f/conversation/*subpath", forward)
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body)).WithContext(clientContext)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", "TEST_ONLY_Desktop")
		router.ServeHTTP(w, request)
		require.Empty(t, slots.active, "every completed HTTP leg must release its slot")
		require.Empty(t, slots.users)
		return w
	}
	scope := service.ChatGPTTurnScope{UserID: user.ID, APIKeyID: 23, GroupID: groupID}
	body := `{ "action":"next", "messages":[{"id":"TEST_ONLY_USER_MESSAGE","content":{"parts":["private synthetic text"]}}], "model":"gpt-6-pro", "extension":{"kept":true} }`
	handoff := "data: {\"type\":\"resume_conversation_token\",\"conversation_id\":\"TEST_ONLY_CONVERSATION\",\"token\":\"TEST_ONLY_SECRET_RESUME_TOKEN\"}\n\ndata: {\"type\":\"stream_handoff\",\"conversation_id\":\"TEST_ONLY_CONVERSATION\"}\n\n"
	imageEvent := "data: {\"message\":{\"id\":\"image-tool\",\"status\":\"finished_successfully\",\"author\":{\"role\":\"tool\",\"name\":\"image_gen\"},\"content\":{\"content_type\":\"multimodal_text\",\"parts\":[{\"content_type\":\"image_asset_pointer\",\"asset_pointer\":\"sediment://PRIVATE_GENERATED_IMAGE\"}]}}}\n\n"
	handoff = imageEvent + handoff
	provider.responseBody = handoff
	provider.onSend = func() { require.Len(t, slots.active, 1) }
	first := run(newHandler(), "/chatgpt/backend-api/f/conversation", body, 23, 21, groupID)
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, handoff, first.Body.String())
	require.Equal(t, []byte(body), provider.body)
	require.Empty(t, usage.logs)
	pending, err := shared.GetChatGPTResume(context.Background(), scope.ResumeKey("TEST_ONLY_CONVERSATION"))
	require.NoError(t, err)
	require.NotNil(t, pending)
	require.Equal(t, 0.05, pending.BasePriceUSD)
	require.Equal(t, int64(22), pending.AccountID)
	identity := service.ResolveChatGPTTurnBillingIdentity([]byte(body), "")
	require.Equal(t, identity, pending.Identity)
	require.True(t, pending.Images.GenerationSeen)
	require.Len(t, pending.Images.AssetHashes, 1)
	// A separate service instance shares the pending context, even while an
	// admin read is failing and the current tariff has changed.
	prices.tiers["pro"] = 0.09
	prices.err = errors.New("TEST_ONLY settings temporarily unavailable")
	secondHandler := newHandler()
	resumeBody := `{ "conversation_id":"TEST_ONLY_CONVERSATION", "offset":0, "extension":{"kept":true} }`
	for _, tenant := range []struct{ key, user, group int64 }{{24, 21, 17}, {23, 24, 17}, {23, 21, 18}} {
		before := provider.calls
		w := run(secondHandler, "/chatgpt/backend-api/f/conversation/resume", resumeBody, tenant.key, tenant.user, tenant.group)
		require.Equal(t, http.StatusConflict, w.Code)
		require.Equal(t, before, provider.calls)
	}
	for _, bad := range []string{`{}`, `{"conversation_id":"TEST_ONLY_CONVERSATION","model":"gpt-6-pro"}`, `{"conversation_id":"TEST_ONLY_CONVERSATION","messages":[]}`} {
		before := provider.calls
		w := run(secondHandler, "/chatgpt/backend-api/f/conversation/resume", bad, 23, 21, groupID)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Equal(t, before, provider.calls)
	}
	// An incomplete resume costs nothing and leaves the original snapshot for
	// a later successful reconnect.
	provider.responseBody = "data: [DONE]\n\n"
	incomplete := run(secondHandler, "/chatgpt/backend-api/f/conversation/resume", resumeBody, 23, 21, groupID)
	require.Equal(t, http.StatusOK, incomplete.Code)
	require.Empty(t, usage.logs)
	provider.responseBody = "event: error\ndata: {\"message\":\"TEST_ONLY_FAILURE\"}\n\ndata: {\"type\":\"message_stream_complete\",\"conversation_id\":\"TEST_ONLY_CONVERSATION\"}\n\n"
	run(secondHandler, "/chatgpt/backend-api/f/conversation/resume", resumeBody, 23, 21, groupID)
	require.Empty(t, usage.logs)
	provider.responseBody = "data: {\"type\":\"message_stream_complete\",\"conversation_id\":\"WRONG_CONVERSATION\"}\n\n"
	run(secondHandler, "/chatgpt/backend-api/f/conversation/resume", resumeBody, 23, 21, groupID)
	require.Empty(t, usage.logs, "completion of another conversation cannot settle this turn")
	complete := "data: {\"v\":{\"message\":{\"author\":{\"role\":\"assistant\"},\"metadata\":{\"model_slug\":\"gpt-6-pro\"}}}}\n\ndata: {\"type\":\"message_stream_complete\",\"conversation_id\":\"TEST_ONLY_CONVERSATION\"}\n\ndata: [DONE]\n\n"
	complete = imageEvent + complete // a repeated asset on resume must count once
	provider.responseBody = complete
	success := run(secondHandler, "/chatgpt/backend-api/f/conversation/resume?extension=a%2Bb", resumeBody, 23, 21, groupID)
	require.Equal(t, http.StatusOK, success.Code)
	require.Equal(t, complete, success.Body.String())
	require.Equal(t, []byte(resumeBody), provider.body)
	require.Equal(t, "extension=a%2Bb", provider.req.URL.RawQuery)
	require.Equal(t, "Bearer TEST_ONLY_OAUTH", provider.req.Header.Get("Authorization"))
	require.Len(t, usage.logs, 1)
	log := usage.logs[0]
	require.Equal(t, "/chatgpt/backend-api/f/conversation/resume", *log.InboundEndpoint)
	require.True(t, log.IsNativeChatTurn(), "the real wildcard resume route must retain native billing metadata")
	require.Equal(t, identity.RequestID, log.RequestID)
	require.Equal(t, "gpt-6-pro", log.Model)
	require.Nil(t, log.ReasoningEffort)
	require.Zero(t, log.TotalTokens())
	require.Equal(t, 1, log.ImageCount)
	require.Equal(t, "image", *log.MediaType)
	require.InDelta(t, 0.05, log.TotalCost, 1e-12)
	require.InDelta(t, 0.0625, log.ActualCost, 1e-12)
	require.Equal(t, int64(22), log.AccountID)
	require.Equal(t, 1, prices.reads, "delivery retries must not read/reprice the initial turn")
	disconnect = true
	run(newHandler(), "/chatgpt/backend-api/f/conversation/resume", resumeBody, 23, 21, groupID)
	disconnect = false
	require.Len(t, usage.logs, 1, "completed delivery replay cannot bill again")
	other := owner
	other.ID = 24
	accounts.accounts = []service.Account{other}
	before := provider.calls
	unavailable := run(secondHandler, "/chatgpt/backend-api/f/conversation/resume", resumeBody, 23, 21, groupID)
	require.Equal(t, http.StatusServiceUnavailable, unavailable.Code)
	require.Equal(t, before, provider.calls, "resume must not migrate accounts")
}
