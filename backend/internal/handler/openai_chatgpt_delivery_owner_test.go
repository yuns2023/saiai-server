package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestChatGPTHistoricalDeliveryAndContinuationRetainOwnerWithoutRebilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	const conversation = "TEST_ONLY_HISTORY"
	const snapshot = `{"conversation_id":"TEST_ONLY_HISTORY","current_node":"image","mapping":{"user":{"parent":null,"message":{"id":"user","author":{"role":"user"}}},"image":{"parent":"user","message":{"id":"image","author":{"role":"tool","name":"image_gen"},"status":"finished_successfully","content":{"content_type":"multimodal_text","parts":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://file_TEST_ONLY_HISTORY","width":2048,"height":2048}]}}}}}`
	const nextSnapshot = `{"conversation_id":"TEST_ONLY_HISTORY","current_node":"assistant","mapping":{"next":{"parent":null,"message":{"id":"next","author":{"role":"user"}}},"assistant":{"parent":"next","message":{"id":"assistant","author":{"role":"assistant"},"status":"finished_successfully","end_turn":true,"content":{"content_type":"text","parts":["MOCK_ONLY_SUCCESS"]}}}}}`
	var reads, downloads, generations atomic.Int64
	var generationBody atomic.Value
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer TEST_ONLY_OAUTH" || r.Header.Get("Chatgpt-Account-Id") != "TEST_ONLY_ACCOUNT" {
			t.Error("historical delivery or continuation changed the original OAuth owner")
		}
		switch r.URL.Path {
		case "/backend-api/conversation/TEST_ONLY_HISTORY":
			if reads.Add(1) == 1 {
				if r.URL.RawQuery != "x=a%2Fb&x=a+b" {
					t.Error("history read changed the original encoded query")
				}
			} else if r.URL.RawQuery != "" {
				t.Error("completion read unexpectedly inherited the history query")
			}
			w.Header().Set("Content-Type", "application/json")
			if generations.Load() > 0 {
				_, _ = io.WriteString(w, nextSnapshot)
			} else {
				_, _ = io.WriteString(w, snapshot)
			}
		case "/backend-api/files/download/file_TEST_ONLY_HISTORY":
			downloads.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"download_url":"MOCK_ONLY_DOWNLOAD","retry":false}`)
		case "/backend-api/f/conversation":
			generations.Add(1)
			raw, _ := io.ReadAll(r.Body)
			generationBody.Store(string(raw))
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"conversation_id\":\"TEST_ONLY_HISTORY\",\"message\":{\"id\":\"assistant\",\"author\":{\"role\":\"assistant\"},\"status\":\"finished_successfully\",\"end_turn\":true,\"content\":{\"content_type\":\"text\",\"parts\":[\"MOCK_ONLY_SUCCESS\"]}}}\n\ndata: {\"type\":\"message_stream_complete\",\"conversation_id\":\"TEST_ONLY_HISTORY\"}\n\ndata: [DONE]\n\n")
		default:
			t.Error("unexpected local mock request")
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.Close)
	groupID := int64(17)
	user := &service.User{ID: 21, Balance: 100}
	account := service.Account{ID: 22, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{"access_token": "TEST_ONLY_OAUTH", "chatgpt_account_id": "TEST_ONLY_ACCOUNT"}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.OpenAIChatEnabled, cfg.Gateway.OpenAIChatUpdatesEnabled = true, true
	cfg.Gateway.OpenAIChatUpstreamBaseURL = provider.URL
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cache := &chatGPTSharedTurnCache{&chatGPTStickyCache{bindings: make(map[string]int64)}, service.NewChatGPTMemoryTurnCache()}
	usage := &chatGPTLockedUsage{}
	billing := service.NewBillingCacheService(nil, nil, nil, nil, cfg)
	t.Cleanup(billing.Stop)
	network := &chatGPTLocalNetwork{&http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	svc := service.NewOpenAIGatewayService(&chatGPTAccountRepo{account: account}, usage, nil, nil, nil, nil, cache, cfg, nil, nil, nil, nil, billing, network, &service.DeferredService{}, nil)
	h := NewOpenAIGatewayHandler(svc, nil, billing, nil, nil, nil, nil, cfg, nil)
	h.chatGPTBillingSettings = &chatGPTPriceReader{price: .01}
	scope := service.ChatGPTTurnScope{UserID: user.ID, APIKeyID: 23, GroupID: groupID}
	old := &service.ChatGPTTurnSnapshot{Identity: service.ChatGPTTurnBillingIdentity{RequestID: "TEST_ONLY_EXPIRED", PayloadHash: "TEST_ONLY_HASH"}, AccountID: account.ID, BasePriceUSD: .01, StartedAt: time.Now().Add(-time.Hour), Completed: true, TerminalSeen: true, UserMessageHash: service.ChatGPTMessageIDHash("user")}
	_, err := cache.PutChatGPTTurnIfAbsent(ctx, scope.TurnKey(old.Identity), old, 20*time.Millisecond)
	require.NoError(t, err)
	require.NoError(t, service.BindChatGPTConversationDelivery(ctx, cache, scope, old, conversation))
	require.Eventually(t, func() bool {
		turn, err := cache.GetChatGPTResume(ctx, scope.ResumeKey(conversation))
		return err == nil && turn == nil
	}, time.Second, 5*time.Millisecond)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		keyID := int64(23)
		if value := c.GetHeader("TEST-Key-ID"); value != "" {
			keyID, _ = strconv.ParseInt(value, 10, 64)
		}
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: keyID, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, RateMultiplier: 1}, User: user})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: user.ID, Concurrency: 2})
	})
	router.GET("/chatgpt/backend-api/conversation/:conversation_id", h.ChatGPTConversationRead)
	router.GET("/chatgpt/backend-api/files/download/:file_id", h.ChatGPTFileDownload)
	router.POST("/chatgpt/backend-api/f/conversation", h.ChatGPTConversation)
	request := func(method, path, body string, keyID int64) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("TEST-Key-ID", strconv.FormatInt(keyID, 10))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	w := request("GET", "/chatgpt/backend-api/conversation/TEST_ONLY_HISTORY?x=a%2Fb&x=a+b", "", 23)
	require.Equal(t, 200, w.Code)
	require.Equal(t, snapshot, w.Body.String())
	require.Zero(t, usage.count(), "history and final images cannot recreate a completed bill")
	w = request("GET", "/chatgpt/backend-api/files/download/file_TEST_ONLY_HISTORY", "", 23)
	require.Equal(t, 200, w.Code)
	require.Equal(t, int64(1), downloads.Load())
	w = request("GET", "/chatgpt/backend-api/conversation/TEST_ONLY_HISTORY", "", 24)
	require.Equal(t, 404, w.Code)
	w = request("GET", "/chatgpt/backend-api/files/download/file_TEST_ONLY_HISTORY", "", 24)
	require.Equal(t, 404, w.Code)
	require.Equal(t, int64(1), reads.Load())
	require.Equal(t, int64(1), downloads.Load())
	const next = `{"action":"next","conversation_id":"TEST_ONLY_HISTORY","model":"gpt-5-6-thinking","messages":[{"id":"next","author":{"role":"user"},"content":{"parts":["MOCK_ONLY next turn"]}}]}`
	w = request("POST", "/chatgpt/backend-api/f/conversation", next, 23)
	require.Equal(t, 200, w.Code)
	require.Equal(t, next, generationBody.Load())
	require.Equal(t, int64(1), generations.Load())
	require.Equal(t, 1, usage.count(), "only the new successful generation is billable")
	require.InDelta(t, .01, usage.logs[0].ActualCost, 1e-9)
}
