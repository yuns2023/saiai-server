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
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestChatGPTMacDeviceRegistrationKeepsProviderProofAndAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gid := int64(17)
	owner := service.Account{ID: 22, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{gid}, Credentials: map[string]any{"access_token": "TEST_ONLY_OAUTH", "chatgpt_account_id": "TEST_ONLY_ACCOUNT"}}
	other := owner
	other.ID, other.Credentials = 24, map[string]any{"access_token": "TEST_ONLY_OTHER_OAUTH", "chatgpt_account_id": "TEST_ONLY_OTHER_ACCOUNT"}
	accounts := &chatGPTAccountRepo{accounts: []service.Account{owner}}
	type capture struct{ method, path, body, account, cookie, integrity string }
	var captured []capture
	const proofHeader = "_devicecheck=TEST_ONLY_PROOF; Domain=.chatgpt.com; Path=/; Max-Age=600; Secure; HttpOnly; SameSite=Lax"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured = append(captured, capture{r.Method, r.URL.RequestURI(), string(body), r.Header.Get("ChatGPT-Account-ID"), r.Header.Get("Cookie"), r.Header.Get("X-Sentinel-DC")})
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/backend-api/devicecheck":
			if r.URL.Query().Get("failure") != "" {
				w.WriteHeader(403)
				_, _ = io.WriteString(w, `{"detail":"TEST_ONLY_PROVIDER_DENIAL"}`)
				return
			}
			w.Header().Add("Set-Cookie", proofHeader)
			w.Header().Add("Set-Cookie", "session=TEST_ONLY_PRIVATE; Path=/; Secure; HttpOnly")
			_, _ = io.WriteString(w, `{ "future":null }`)
		case "/backend-api/ios/attestation_challenge":
			_, _ = io.WriteString(w, `{ "attestation_challenge":"TEST_ONLY_CHALLENGE", "future":null }`)
		case "/backend-api/f/conversation":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"message_stream_complete\",\"conversation_id\":\"TEST_ONLY_CONVERSATION\"}\n\ndata: [DONE]\n\n")
		default:
			_, _ = io.WriteString(w, `{ "models":[] }`)
		}
	}))
	defer provider.Close()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.OpenAIChatEnabled = true
	cfg.Gateway.OpenAIChatUpstreamBaseURL = provider.URL
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cache := &chatGPTSharedTurnCache{&chatGPTStickyCache{bindings: make(map[string]int64)}, service.NewChatGPTMemoryTurnCache()}
	usage := &chatGPTLockedUsage{}
	slots := &chatGPTSlotCache{active: make(map[string]int64), attempts: make(map[int64]int)}
	concurrency := service.NewConcurrencyService(slots)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, cfg)
	defer billing.Stop()
	network := &chatGPTLocalNetwork{&http.Client{Transport: &http.Transport{Proxy: nil, DisableCompression: true}}}
	svc := service.NewOpenAIGatewayService(accounts, usage, nil, nil, nil, nil, cache, cfg, nil, concurrency, nil, nil, billing, network, &service.DeferredService{}, nil)
	h := NewOpenAIGatewayHandler(svc, concurrency, billing, nil, nil, nil, nil, cfg, nil)
	h.chatGPTBillingSettings = &chatGPTPriceReader{price: .01}
	run := func(method, path, body, cookie, device string, uid, kid, group int64) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		c.Request.Header.Set("OAI-DID", device)
		c.Request.Header.Set("X-Sentinel-DC", "TEST_ONLY_APPLE_HEADER")
		c.Request.Header.Set("Cookie", cookie)
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: kid, GroupID: &group, Group: &service.Group{ID: group, Platform: service.PlatformOpenAI, RateMultiplier: 1}, User: &service.User{ID: uid, Balance: 100}})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: uid, Concurrency: 2})
		switch pathName := c.Request.URL.Path; pathName {
		case "/chatgpt/backend-api/devicecheck":
			h.ChatGPTDeviceCheck(c)
		case "/chatgpt/backend-api/ios/attestation_challenge":
			h.ChatGPTAttestationChallenge(c)
		case "/chatgpt/backend-api/models":
			h.ChatGPTModels(c)
		default:
			h.ChatGPTConversation(c)
		}
		require.Empty(t, slots.active)
		require.Empty(t, slots.users)
		return w
	}
	const registration = `{ "device_token":"TEST_ONLY_APPLE_TOKEN", "bundle_id":"com.openai.codex", "future":[null,true] }`
	w := run("POST", "/chatgpt/backend-api/devicecheck?x=a%2Fb&x=a+b", registration, "session=TEST_ONLY_UNRELATED", "TEST_ONLY_DEVICE", 21, 23, gid)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, `{ "future":null }`, w.Body.String())
	require.Equal(t, []string{proofHeader}, w.Header().Values("Set-Cookie"))
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Zero(t, usage.count())
	require.Equal(t, registration, captured[0].body)
	require.Equal(t, "/backend-api/devicecheck?x=a%2Fb&x=a+b", captured[0].path)
	require.Empty(t, captured[0].cookie)
	accounts.accounts = []service.Account{other, owner}
	const cookie = "session=TEST_ONLY_UNRELATED; _devicecheck=TEST_ONLY_PROOF; another=TEST_ONLY_UNRELATED"
	w = run("GET", "/chatgpt/backend-api/ios/attestation_challenge", "", cookie, "TEST_ONLY_DEVICE", 21, 23, gid)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, captured[0].account, captured[1].account)
	require.Equal(t, "_devicecheck=TEST_ONLY_PROOF", captured[1].cookie)
	require.Equal(t, "TEST_ONLY_APPLE_HEADER", captured[1].integrity)
	w = run("GET", "/chatgpt/backend-api/models", "", cookie, "TEST_ONLY_DEVICE", 21, 23, gid)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, captured[0].account, captured[2].account)
	const body = `{ "model":"auto", "app_attest_challenge":"TEST_ONLY_CHALLENGE", "messages":[{"id":"TEST_ONLY_USER","author":{"role":"user"},"content":{"parts":["TEST_ONLY_TEXT"]}}], "future":null }`
	w = run("POST", "/chatgpt/backend-api/f/conversation", body, cookie, "TEST_ONLY_DEVICE", 21, 23, gid)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, captured[0].account, captured[3].account)
	require.Equal(t, body, captured[3].body)
	require.Equal(t, "_devicecheck=TEST_ONLY_PROOF", captured[3].cookie)
	require.Equal(t, 1, usage.count(), "only the successful synthetic model turn is billed")
	calls := len(captured)
	for _, foreign := range [][3]int64{{22, 23, gid}, {21, 24, gid}, {21, 23, gid + 1}} {
		w = run("POST", "/chatgpt/backend-api/f/conversation", body, cookie, "TEST_ONLY_DEVICE", foreign[0], foreign[1], foreign[2])
		require.Equal(t, 409, w.Code)
		require.Contains(t, w.Header().Get("Set-Cookie"), "Max-Age=0")
		require.Len(t, captured, calls)
	}
	for _, invalid := range []string{"_devicecheck=TEST_ONLY_UNKNOWN", "_devicecheck=TEST_ONLY_PROOF; _devicecheck=TEST_ONLY_SECOND", "_devicecheck=saiai-local-proxy"} {
		w = run("POST", "/chatgpt/backend-api/f/conversation", body, invalid, "TEST_ONLY_DEVICE", 21, 23, gid)
		require.Equal(t, 409, w.Code)
		require.Len(t, captured, calls)
	}
	w = run("POST", "/chatgpt/backend-api/f/conversation", body, cookie, "TEST_ONLY_OTHER_DEVICE", 21, 23, gid)
	require.Equal(t, 409, w.Code)
	require.Len(t, captured, calls)
	scope := service.ChatGPTTurnScope{UserID: 21, APIKeyID: 23, GroupID: gid}
	_, err := cache.ClaimChatGPTUploadOwner(context.Background(), scope.DeviceCookieKey("TEST_ONLY_DEVICE", "TEST_ONLY_EXPIRED"), owner.ID, time.Nanosecond)
	require.NoError(t, err)
	w = run("POST", "/chatgpt/backend-api/f/conversation", body, "_devicecheck=TEST_ONLY_EXPIRED", "TEST_ONLY_DEVICE", 21, 23, gid)
	require.Equal(t, 409, w.Code)
	require.Len(t, captured, calls)
	w = run("POST", "/chatgpt/backend-api/devicecheck?failure=1", registration, "", "TEST_ONLY_DEVICE", 21, 23, gid)
	require.Equal(t, 403, w.Code)
	require.Equal(t, `{"detail":"TEST_ONLY_PROVIDER_DENIAL"}`, w.Body.String())
	require.Empty(t, w.Header().Values("Set-Cookie"))
	require.Equal(t, 1, usage.count())
	cfg.Gateway.OpenAIChatEnabled = false
	calls = len(captured)
	w = run("POST", "/chatgpt/backend-api/devicecheck", registration, "", "TEST_ONLY_DEVICE", 21, 23, gid)
	require.Equal(t, 404, w.Code)
	require.Len(t, captured, calls)
}
