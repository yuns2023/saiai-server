package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func zstdCompressForTest(t *testing.T, payload []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed)
	require.NoError(t, err)
	_, err = encoder.Write(payload)
	require.NoError(t, err)
	require.NoError(t, encoder.Close())
	return compressed.Bytes()
}

// newOpenAIBodyBytesHandler wires a simple-mode OpenAI handler whose usage
// rows are captured synchronously (no worker pool).
func newOpenAIBodyBytesHandler(t *testing.T, account service.Account, upstream *chatGPTReplayUpstream) (*OpenAIGatewayHandler, *chatGPTUsageLogCapture) {
	t.Helper()
	usageRepo := &chatGPTUsageLogCapture{}
	slots := &chatGPTSlotCache{active: make(map[string]int64), attempts: make(map[int64]int)}
	concurrency := service.NewConcurrencyService(slots)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, cfg)
	t.Cleanup(billingCache.Stop)
	svc := service.NewOpenAIGatewayService(&chatGPTAccountRepo{account: account}, usageRepo, nil, nil, nil, nil,
		nil, cfg, nil, concurrency, service.NewBillingService(cfg, nil), nil, billingCache, upstream, &service.DeferredService{}, nil)
	return NewOpenAIGatewayHandler(svc, concurrency, billingCache, &service.APIKeyService{}, nil, nil, nil, cfg, nil), usageRepo
}

func openAIBodyBytesContext(t *testing.T, path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	groupID := int64(17)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{ID: 23, GroupID: &groupID,
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, RateMultiplier: 1}, User: &service.User{ID: 21}})
	c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 21})
	return c, w
}

// The Responses HTTP size is the body as received: a zstd-compressed request
// counts its compressed bytes, not the decoded JSON the gateway forwards.
func TestOpenAIResponsesHTTPRecordsCompressedInboundBodyBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := service.Account{ID: 22, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "TEST_ONLY_API_KEY", "base_url": "https://responses.example.test/v1"}}
	upstream := &chatGPTReplayUpstream{contentType: "application/json",
		responseBody: `{"id":"resp_body_bytes","object":"response","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":7}}`}
	h, usageRepo := newOpenAIBodyBytesHandler(t, account, upstream)

	payload := []byte(`{"model":"gpt-5.4","input":"` + string(bytes.Repeat([]byte("TEST_ONLY "), 200)) + `"}`)
	wire := zstdCompressForTest(t, payload)
	require.Less(t, len(wire), len(payload))
	c, w := openAIBodyBytesContext(t, "/v1/responses", wire)
	c.Request.Header.Set("Content-Encoding", "zstd")

	h.Responses(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, usageRepo.logs, 1)
	require.NotNil(t, usageRepo.logs[0].RequestBodyBytes)
	require.Equal(t, int64(len(wire)), *usageRepo.logs[0].RequestBodyBytes)
	require.NotEqual(t, int64(len(upstream.body)), *usageRepo.logs[0].RequestBodyBytes, "never the forwarded (decoded) body")
}

func TestOpenAIImagesRecordsInboundBodyBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Run("api_key_json", func(t *testing.T) {
		account := service.Account{ID: 22, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 1,
			Credentials: map[string]any{"api_key": "TEST_ONLY_API_KEY", "base_url": "https://images.example.test/custom/v1"}}
		upstream := &chatGPTReplayUpstream{contentType: "application/json",
			responseBody: `{"data":[{"b64_json":"TEST_ONLY"}],"usage":{"input_tokens":5,"output_tokens":21}}`}
		h, usageRepo := newOpenAIBodyBytesHandler(t, account, upstream)
		body := []byte(`{ "model":"gpt-image-2", "prompt":"TEST_ONLY" }`)
		c, w := openAIBodyBytesContext(t, "/v1/images/generations", body)
		h.ImagesGenerations(c)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Len(t, usageRepo.logs, 1)
		require.NotNil(t, usageRepo.logs[0].RequestBodyBytes)
		require.Equal(t, int64(len(body)), *usageRepo.logs[0].RequestBodyBytes)
	})
	t.Run("native_codex_zstd", func(t *testing.T) {
		account := service.Account{ID: 22, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Status: service.StatusActive, Schedulable: true, Concurrency: 1,
			Credentials: map[string]any{"access_token": "TEST_ONLY_OAUTH", "chatgpt_account_id": "TEST_ONLY_ACCOUNT"}}
		upstream := &chatGPTReplayUpstream{contentType: "application/json",
			responseBody: `{"data":[{"b64_json":"TEST_ONLY"}],"usage":{"input_tokens":5,"output_tokens":21}}`}
		h, usageRepo := newOpenAIBodyBytesHandler(t, account, upstream)
		payload := []byte(`{"model":"gpt-image-2","prompt":"` + string(bytes.Repeat([]byte("TEST_ONLY "), 100)) + `"}`)
		wire := zstdCompressForTest(t, payload)
		c, w := openAIBodyBytesContext(t, "/v1/codex/images/generations", wire)
		c.Request.Header.Set("Content-Encoding", "zstd")
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.160.0")
		c.Request.Header.Set("originator", "codex_cli_rs")
		h.CodexImagesGenerations(c)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Len(t, usageRepo.logs, 1)
		require.NotNil(t, usageRepo.logs[0].RequestBodyBytes)
		require.Equal(t, int64(len(wire)), *usageRepo.logs[0].RequestBodyBytes)
		require.NotEqual(t, int64(len(payload)), *usageRepo.logs[0].RequestBodyBytes)
	})
}

// Native Chat settles from the turn snapshot, possibly long after the
// original POST (resume, updates or async completion). The size always comes
// from the snapshot; whatever the triggering request supplies is ignored, and
// a snapshot written before the field existed records NULL.
func TestChatGPTSettlementTakesRequestBodyBytesFromSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		snapshot *int64
		want     *int64
	}{
		{"snapshot_size", int64Pointer(12345), int64Pointer(12345)},
		{"legacy_snapshot", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Default.RateMultiplier = 1
			usage := &chatGPTLockedUsage{}
			svc := service.NewOpenAIGatewayService(nil, usage, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
			h := NewOpenAIGatewayHandler(svc, nil, nil, nil, nil, nil, nil, cfg, nil)
			scope := service.ChatGPTTurnScope{UserID: 1, APIKeyID: 2, GroupID: 3}
			cache := service.NewChatGPTMemoryTurnCache()
			turn := &service.ChatGPTTurnSnapshot{Identity: service.ChatGPTTurnBillingIdentity{RequestID: "TEST_ONLY", PayloadHash: "TEST_ONLY"},
				AccountID: 4, BasePriceUSD: .03, StartedAt: time.Now(), RequestBodyBytes: tc.snapshot}
			_, err := cache.PutChatGPTTurnIfAbsent(ctx, scope.TurnKey(turn.Identity), turn, time.Minute)
			require.NoError(t, err)
			key := &service.APIKey{ID: 2, GroupID: &scope.GroupID, Group: &service.Group{ID: 3, RateMultiplier: 1}, User: &service.User{ID: 1}}
			// A resume/poll request must never contribute its own size.
			require.NoError(t, h.settleChatGPTTurn(ctx, cache, scope, turn, &service.OpenAIChatGPTTurnUsageInput{
				APIKey: key, User: key.User, Account: &service.Account{ID: 4}, RequestBodyBytes: int64Pointer(7),
			}))
			require.Equal(t, 1, usage.count())
			require.Equal(t, tc.want, usage.logs[0].RequestBodyBytes)
		})
	}
}

// A snapshot JSON written by an older Gateway has no RequestBodyBytes field;
// a new snapshot with a nil size omits it, and 0 stays 0.
func TestChatGPTTurnSnapshotRequestBodyBytesJSONCompatibility(t *testing.T) {
	var legacy service.ChatGPTTurnSnapshot
	require.NoError(t, json.Unmarshal([]byte(`{"AccountID":4,"BasePriceUSD":0.03}`), &legacy))
	require.Nil(t, legacy.RequestBodyBytes)

	raw, err := json.Marshal(service.ChatGPTTurnSnapshot{AccountID: 4})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "RequestBodyBytes")

	raw, err = json.Marshal(service.ChatGPTTurnSnapshot{AccountID: 4, RequestBodyBytes: int64Pointer(0)})
	require.NoError(t, err)
	var decoded service.ChatGPTTurnSnapshot
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.NotNil(t, decoded.RequestBodyBytes)
	require.Zero(t, *decoded.RequestBodyBytes)
}

// End to end: the model POST stores its size in the turn snapshot and the
// synchronous completion records it.
func TestChatGPTConversationRecordsOriginalPostBodyBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(17)
	user := &service.User{ID: 21, Balance: 100}
	account := service.Account{ID: 22, Name: "oauth-fixed-turn", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID},
		Credentials: map[string]any{"access_token": "oauth-upstream-token", "chatgpt_account_id": "upstream-account"}}
	upstream := &chatGPTReplayUpstream{}
	usageRepo := &chatGPTUsageLogCapture{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.OpenAIChatEnabled = true
	cfg.Gateway.OpenAIChatSuccessTurnPriceUSD = 0.07
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, cfg)
	t.Cleanup(billingCache.Stop)
	svc := service.NewOpenAIGatewayService(&chatGPTAccountRepo{account: account}, usageRepo, nil, nil, nil, nil,
		&chatGPTStickyCache{bindings: make(map[string]int64)}, cfg, nil, nil, nil, nil, billingCache, upstream, &service.DeferredService{}, nil)
	h := NewOpenAIGatewayHandler(svc, nil, billingCache, nil, nil, nil, nil, cfg, nil)
	h.chatGPTBillingSettings = &chatGPTPriceReader{price: 0.02}
	body := []byte(`{ "action" : "next", "messages" : [ { "id" : "m1" } ], "model" : "auto" }`)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/chatgpt/backend-api/f/conversation", bytes.NewReader(body))
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestID, "chatgpt-body-bytes"))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{ID: 23, GroupID: &groupID,
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, RateMultiplier: 1}, User: user})
	c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: user.ID, Concurrency: 2})
	h.ChatGPTConversation(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, usageRepo.logs, 1)
	require.NotNil(t, usageRepo.logs[0].RequestBodyBytes)
	require.Equal(t, int64(len(body)), *usageRepo.logs[0].RequestBodyBytes)
}

func int64Pointer(value int64) *int64 {
	return &value
}
