package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type chatGPTLocalNetwork struct{ client *http.Client }

func (n *chatGPTLocalNetwork) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return n.client.Do(req)
}
func (n *chatGPTLocalNetwork) DoWithTLS(req *http.Request, p string, id int64, c int, _ bool) (*http.Response, error) {
	return n.Do(req, p, id, c)
}

type chatGPTLockedUsage struct {
	service.UsageLogRepository
	sync.Mutex
	logs []*service.UsageLog
}

func (r *chatGPTLockedUsage) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	r.Lock()
	defer r.Unlock()
	r.logs = append(r.logs, log)
	return true, nil
}
func (r *chatGPTLockedUsage) CreateBestEffort(ctx context.Context, log *service.UsageLog) error {
	_, err := r.Create(ctx, log)
	return err
}
func (r *chatGPTLockedUsage) count() int { r.Lock(); defer r.Unlock(); return len(r.logs) }

func TestChatGPTAsyncSettlementKeepsOriginalSubscription(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	usage := &chatGPTLockedUsage{}
	svc := service.NewOpenAIGatewayService(nil, usage, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	h := NewOpenAIGatewayHandler(svc, nil, nil, nil, nil, nil, nil, cfg, nil)
	scope := service.ChatGPTTurnScope{UserID: 1, APIKeyID: 2, GroupID: 3}
	cache := service.NewChatGPTMemoryTurnCache()
	turn := &service.ChatGPTTurnSnapshot{Identity: service.ChatGPTTurnBillingIdentity{RequestID: "TEST_ONLY", PayloadHash: "TEST_ONLY"}, AccountID: 4, BasePriceUSD: .03, StartedAt: time.Now(), BillingSnapshotVersion: 1, SubscriptionID: 55}
	_, err := cache.PutChatGPTTurnIfAbsent(ctx, scope.TurnKey(turn.Identity), turn, time.Minute)
	require.NoError(t, err)
	key := &service.APIKey{ID: 2, GroupID: &scope.GroupID, Group: &service.Group{ID: 3, RateMultiplier: 1}, User: &service.User{ID: 1}}
	require.NoError(t, h.settleChatGPTTurn(ctx, cache, scope, turn, &service.OpenAIChatGPTTurnUsageInput{APIKey: key, User: key.User, Account: &service.Account{ID: 4}, Subscription: &service.UserSubscription{ID: 99}}))
	require.Equal(t, 1, usage.count())
	require.Equal(t, int64(55), *usage.logs[0].SubscriptionID)
	require.Equal(t, service.BillingTypeSubscription, usage.logs[0].BillingType)
	require.False(t, key.Group.IsSubscriptionType(), "settlement must not mutate shared auth objects")
}

func TestChatGPTAsyncImageDeliverySettlesOnlyOwnedCompletedTurnOnce(t *testing.T) {
	for _, handoff := range []string{
		"data: {\"type\":\"stream_handoff\",\"options\":[{\"type\":\"subscribe_ws_topic\",\"topic_id\":\"conv-turn-low-ttl-TEST_ONLY\"}]}\n\n",
		"data: {\"type\":\"resume_conversation_token\",\"conversation_id\":\"TEST_ONLY_CONVERSATION\",\"token\":\"TEST_ONLY_SECRET\"}\n\n",
	} {
		t.Run(strings.Split(handoff, `"`)[3], func(t *testing.T) { testChatGPTAsyncImageDelivery(t, handoff) })
	}
}

func testChatGPTAsyncImageDelivery(t *testing.T, handoff string) {
	gin.SetMode(gin.TestMode)
	const conversation = "TEST_ONLY_CONVERSATION"
	const terminal = "data: {\"type\":\"message_stream_complete\",\"conversation_id\":\"TEST_ONLY_CONVERSATION\"}\n\ndata: [DONE]\n\n"
	const original = `{"action":"next","model":"gpt-5-6-thinking","thinking_effort":"extended","messages":[{"id":"user","author":{"role":"user"},"content":{"parts":["TEST_ONLY draw"]}}],"client_extension":{"keep":true}}`
	snapshot := `{"conversation_id":"TEST_ONLY_CONVERSATION","async_status":4,"current_node":"final","mapping":{"user":{"parent":null,"message":{"id":"user","author":{"role":"user"}}},"image":{"parent":"user","message":{"id":"image","author":{"role":"tool","name":"image_gen"},"status":"finished_successfully","content":{"content_type":"multimodal_text","parts":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://file_TEST_ONLY","width":3840,"height":2160}]}}},"final":{"parent":"image","message":{"id":"final","author":{"role":"assistant"},"status":"finished_successfully","end_turn":true,"metadata":{"model_slug":"gpt-5-6-thinking"}}}}}`
	var modelCalls, snapshotCalls, bootstrapCalls atomic.Int64
	var snapshotBody atomic.Value
	snapshotBody.Store(strings.ReplaceAll(strings.ReplaceAll(snapshot, `"async_status":4`, `"async_status":5`), `"status":"finished_successfully"`, `"status":"in_progress"`))
	var assetCalls atomic.Int64
	var capturedBody string
	var capturedMu sync.Mutex
	ready := make(chan struct{}, 1)
	providerFrames := make(chan string, 4)
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/f/conversation":
			modelCalls.Add(1)
			raw, _ := io.ReadAll(r.Body)
			capturedMu.Lock()
			capturedBody = string(raw)
			capturedMu.Unlock()
			if r.Header.Get("Authorization") != "Bearer TEST_ONLY_OAUTH" || r.Header.Get("Cookie") != "" {
				t.Error("invalid provider credential boundary")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, handoff+terminal)
		case "/backend-api/f/conversation/resume":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, terminal)
		case "/backend-api/conversation/TEST_ONLY_CONVERSATION":
			readNumber := snapshotCalls.Add(1)
			if r.Header.Get("Sec-Websocket-Key") != "" || (readNumber == 1 && r.Header.Get("Accept-Encoding") != "identity") {
				t.Error("internal snapshot must not copy WebSocket negotiation or encoding")
			}
			w.Header().Set("Content-Type", "application/json")
			body, ok := snapshotBody.Load().(string)
			if !ok {
				t.Error("snapshot fixture is missing")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(w, body)
		case "/backend-api/celsius/ws/user":
			bootstrapCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"websocket_url": "ws" + strings.TrimPrefix(provider.URL, "http") + "/updates?token=TEST_ONLY_PROVIDER_SECRET"})
		case "/backend-api/files/download/file_TEST_ONLY", "/backend-api/files/download/file_RECOVERED_DELIVERY":
			assetCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer TEST_ONLY_OAUTH" {
				t.Error("asset changed OAuth owner")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"download_url":"/backend-api/estuary/content?id=file_TEST_ONLY","retry":false}`)
		case "/updates":
			conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					_, raw, err := conn.Read(r.Context())
					if err != nil {
						return
					}
					if bytes.Contains(raw, []byte(`"subscribe"`)) {
						select {
						case ready <- struct{}{}:
						default:
						}
					}
				}
			}()
			for {
				select {
				case <-done:
					return
				case frame := <-providerFrames:
					if conn.Write(r.Context(), coderws.MessageText, []byte(frame)) != nil {
						return
					}
				case <-r.Context().Done():
					return
				}
			}
		default:
			t.Errorf("unexpected mock provider path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.Close)
	groupID := int64(17)
	user := &service.User{ID: 21, Balance: 100}
	owner := service.Account{ID: 22, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{"access_token": "TEST_ONLY_OAUTH", "chatgpt_account_id": "TEST_ONLY_ACCOUNT"}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.OpenAIChatEnabled = true
	cfg.Gateway.OpenAIChatUpdatesEnabled = true
	cfg.Gateway.OpenAIChatUpstreamBaseURL = provider.URL
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cache := &chatGPTSharedTurnCache{&chatGPTStickyCache{bindings: make(map[string]int64)}, service.NewChatGPTMemoryTurnCache()}
	usage := &chatGPTLockedUsage{}
	billing := service.NewBillingCacheService(nil, nil, nil, nil, cfg)
	t.Cleanup(billing.Stop)
	network := &chatGPTLocalNetwork{&http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	svc := service.NewOpenAIGatewayService(&chatGPTAccountRepo{account: owner}, usage, nil, nil, nil, nil, cache, cfg, nil, nil, nil, nil, billing, network, &service.DeferredService{}, nil)
	h := NewOpenAIGatewayHandler(svc, nil, billing, nil, nil, nil, nil, cfg, nil)
	h.chatGPTBillingSettings = &chatGPTPriceReader{price: .03, images: map[string]float64{"1K": .02, "2K": .04, "4K": .08, "unknown": .02}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		id := int64(23)
		if v := c.GetHeader("TEST-Key-ID"); v != "" {
			id, _ = strconv.ParseInt(v, 10, 64)
		}
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: id, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, RateMultiplier: 1.25}, User: user})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: user.ID, Concurrency: 2})
	})
	router.GET("/chatgpt/backend-api/celsius/ws/user", h.ChatGPTUpdatesBootstrap)
	router.GET("/chatgpt/backend-api/saiai/chat-updates", h.ChatGPTUpdatesWebSocket)
	router.GET("/chatgpt/backend-api/conversation/:conversation_id", h.ChatGPTConversationRead)
	router.GET("/chatgpt/backend-api/files/download/:file_id", h.ChatGPTFileDownload)
	router.POST("/chatgpt/backend-api/f/conversation", h.ChatGPTConversation)
	router.POST("/chatgpt/backend-api/f/conversation/resume", h.ChatGPTConversation)
	gateway := httptest.NewServer(router)
	t.Cleanup(gateway.Close)
	request := func(method, path, body string, key int64) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("TEST-Key-ID", strconv.FormatInt(key, 10))
		r.Header.Set("User-Agent", "TEST_ONLY_Desktop")
		router.ServeHTTP(w, r)
		return w
	}
	bootstrap := request("GET", "/chatgpt/backend-api/celsius/ws/user", "", 23)
	require.Equal(t, 200, bootstrap.Code)
	require.Contains(t, bootstrap.Body.String(), "/backend-api/saiai/chat-updates")
	require.NotContains(t, bootstrap.Body.String(), "SECRET")
	require.Equal(t, int64(0), bootstrapCalls.Load(), "bootstrap before selection must not subscribe a random account")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http")+"/chatgpt/backend-api/saiai/chat-updates", nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()
	// Exact Desktop startup includes both catalogs. An auxiliary subscription
	// in the same batch must not take down the owned conversation transport.
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`[{"id":1,"command":{"type":"connect","presence":{"type":"presence","state":"foreground"}}},{"id":2,"command":{"type":"subscribe","topic_id":"conversations"}},{"id":3,"command":{"type":"subscribe","topic_id":"alder-conversations"}},{"id":4,"command":{"type":"subscribe","topic_id":"app_notifications"}}]`)))
	for i := 0; i < 4; i++ {
		_, _, err := client.Read(ctx)
		require.NoError(t, err)
	}
	w := request("POST", "/chatgpt/backend-api/f/conversation", original, 23)
	require.Equal(t, 200, w.Code)
	require.Equal(t, 0, usage.count())
	capturedMu.Lock()
	require.Equal(t, original, capturedBody)
	capturedMu.Unlock()
	scope := service.ChatGPTTurnScope{UserID: user.ID, APIKeyID: 23, GroupID: groupID}
	pending, err := cache.GetChatGPTResume(ctx, scope.ResumeKey(conversation))
	require.NoError(t, err)
	require.True(t, pending.AsyncPending)
	require.False(t, pending.TerminalSeen)
	topicOwner, err := cache.GetChatGPTResume(ctx, scope.TopicKey("conv-turn-low-ttl-TEST_ONLY"))
	require.NoError(t, err)
	if strings.Contains(handoff, "stream_handoff") {
		require.NotNil(t, topicOwner)
	} else {
		require.Nil(t, topicOwner)
	}
	previewOwner, err := cache.GetChatGPTResume(ctx, scope.AssetKey(service.ChatGPTAssetLookupHashes("file_TEST_ONLY")[0]))
	require.NoError(t, err)
	require.NotNil(t, previewOwner, "an owned preview must be downloadable before completion")
	require.Empty(t, previewOwner.Images.AssetHashes, "preview ownership must not add a billable image")
	w = request("POST", "/chatgpt/backend-api/f/conversation/resume", `{"conversation_id":"TEST_ONLY_CONVERSATION","offset":0}`, 23)
	require.Equal(t, 200, w.Code)
	require.Equal(t, 0, usage.count(), "a delivery leg omitting async status cannot erase pending state")
	w = request("GET", "/chatgpt/backend-api/conversation/TEST_ONLY_CONVERSATION", "", 24)
	require.Equal(t, 404, w.Code)
	require.Equal(t, int64(2), snapshotCalls.Load())
	snapshotBody.Store(snapshot)
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("mock update subscription not established")
	}
	owned := `{"type":"message","topic_id":"conversations","offset":"ACCOUNT_CURSOR","payload":{"type":"conversation-update","payload":{"conversation_id":"TEST_ONLY_CONVERSATION","update_type":"async-task-completed"}}}`
	foreign := strings.ReplaceAll(owned, "TEST_ONLY_CONVERSATION", "FOREIGN_CONVERSATION")
	providerFrames <- `[` + foreign + `,` + owned + `]`
	_, raw, err := client.Read(ctx)
	require.NoError(t, err)
	require.Contains(t, string(raw), conversation)
	require.NotContains(t, string(raw), "FOREIGN")
	require.NotContains(t, string(raw), "ACCOUNT_CURSOR")
	require.NotContains(t, string(raw), "PROVIDER_SECRET")
	require.Eventually(t, func() bool { return usage.count() == 1 }, 3*time.Second, 10*time.Millisecond)
	usage.Lock()
	log := *usage.logs[0]
	usage.Unlock()
	require.InDelta(t, .11, log.TotalCost, 1e-12)
	require.InDelta(t, .1375, log.ActualCost, 1e-12)
	require.Equal(t, 1, log.ImageCount)
	require.Equal(t, "3840x2160", *log.ImageSize)
	require.Equal(t, "gpt-5-6-thinking", log.Model)
	require.Equal(t, "extended", *log.ReasoningEffort)
	require.Equal(t, pending.Identity.RequestID, log.RequestID)
	providerFrames <- `[` + owned + `]`
	_, _, err = client.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(3), snapshotCalls.Load())
	require.Equal(t, 1, usage.count())
	require.Equal(t, int64(1), modelCalls.Load())
	snapshotBody.Store(strings.ReplaceAll(snapshot, "file_TEST_ONLY", "file_COMPLETED_DELIVERY"))
	w = request("GET", "/chatgpt/backend-api/conversation/TEST_ONLY_CONVERSATION", "", 23)
	require.Equal(t, 200, w.Code)
	downloadOwner, err := cache.GetChatGPTResume(ctx, scope.AssetKey(service.ChatGPTAssetLookupHashes("file_COMPLETED_DELIVERY")[0]))
	require.NoError(t, err)
	require.NotNil(t, downloadOwner, "completed turn delivery still binds verified files")
	require.Equal(t, 1, usage.count(), "late delivery cannot reopen a sealed bill")
	// A missing file binding can be recovered only from an already-owned
	// conversation, including after billing has frozen.
	snapshotBody.Store(strings.ReplaceAll(snapshot, "file_TEST_ONLY", "file_RECOVERED_DELIVERY"))
	w = request("GET", "/chatgpt/backend-api/files/download/file_TEST_ONLY?conversation_id=TEST_ONLY_CONVERSATION", "", 24)
	require.Equal(t, 404, w.Code)
	require.Equal(t, int64(4), snapshotCalls.Load(), "foreign keys must not trigger an owner snapshot")
	w = request("GET", "/chatgpt/backend-api/files/download/file_RECOVERED_DELIVERY?conversation_id=TEST_ONLY_CONVERSATION", "", 23)
	require.Equal(t, 200, w.Code)
	downloadOwner, err = cache.GetChatGPTResume(ctx, scope.AssetKey(service.ChatGPTAssetLookupHashes("file_RECOVERED_DELIVERY")[0]))
	require.NoError(t, err)
	require.NotNil(t, downloadOwner)
	require.Equal(t, 1, usage.count())
	w = request("GET", "/chatgpt/backend-api/files/download/file_FOREIGN?conversation_id=TEST_ONLY_CONVERSATION", "", 23)
	require.Equal(t, 404, w.Code, "an owned conversation cannot authorize an unobserved file")
	require.Equal(t, int64(1), assetCalls.Load())
	w = request("GET", "/chatgpt/backend-api/files/download/file_TEST_ONLY", "", 24)
	require.Equal(t, 404, w.Code)
	require.Equal(t, int64(1), assetCalls.Load())
	w = request("GET", "/chatgpt/backend-api/files/download/file_TEST_ONLY", "", 23)
	require.Equal(t, 200, w.Code)
	require.Equal(t, int64(2), assetCalls.Load())
	require.Equal(t, 1, usage.count(), "downloads are not new billable turns")
	require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
	require.Eventually(t, func() bool {
		h.chatGPTUpdatesMu.Lock()
		defer h.chatGPTUpdatesMu.Unlock()
		return h.chatGPTUpdatesActive == 0
	}, 2*time.Second, 10*time.Millisecond)
	malformed, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http")+"/chatgpt/backend-api/saiai/chat-updates", nil)
	require.NoError(t, err)
	defer func() { _ = malformed.CloseNow() }()
	require.NoError(t, malformed.Write(ctx, coderws.MessageText, []byte(`{"command":"invalid"}`)))
	_, _, err = malformed.Read(ctx)
	require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err), "protocol rejection must send a close frame, not reset the transport")
}

func TestChatGPTUpdatesCapacityIncludesOfficialAuxiliaryTransports(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	scope := service.ChatGPTTurnScope{UserID: 1, APIKeyID: 2, GroupID: 3}
	for range 4 {
		release, ok := h.reserveChatGPTUpdates(scope)
		require.True(t, ok)
		t.Cleanup(release)
	}
	_, ok := h.reserveChatGPTUpdates(scope)
	require.False(t, ok)
	release, ok := h.reserveChatGPTUpdates(service.ChatGPTTurnScope{UserID: 2, APIKeyID: 2, GroupID: 3})
	require.True(t, ok, "one scope cannot exhaust a different user's reservation")
	t.Cleanup(release)
}
