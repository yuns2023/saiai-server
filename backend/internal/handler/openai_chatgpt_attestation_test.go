package handler

import (
	"bytes"
	"compress/gzip"
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

func TestChatGPTMacAttestationPreservesWireAndBindsModelOwner(t *testing.T) {
	for _, encoding := range []string{"identity", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			groupID := int64(17)
			owner := service.Account{ID: 22, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{"access_token": "TEST_ONLY_OAUTH", "chatgpt_account_id": "TEST_ONLY_ACCOUNT"}}
			other := owner
			other.ID, other.Credentials = 24, map[string]any{"access_token": "TEST_ONLY_OTHER_OAUTH", "chatgpt_account_id": "TEST_ONLY_OTHER_ACCOUNT"}
			accounts := &chatGPTAccountRepo{accounts: []service.Account{owner, other}}
			const metadata = `{ "attestation_challenge":"TEST_ONLY_CHALLENGE", "future": {"keep":true} }`
			wire := []byte(metadata)
			if encoding == "gzip" {
				var compressed bytes.Buffer
				encoder := gzip.NewWriter(&compressed)
				_, err := encoder.Write(wire)
				require.NoError(t, err)
				require.NoError(t, encoder.Close())
				wire = compressed.Bytes()
			}
			type capture struct {
				method, path, body, account, auth string
				integrity                         []string
			}
			var captured []capture
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				captured = append(captured, capture{r.Method, r.URL.RequestURI(), string(raw), r.Header.Get("ChatGPT-Account-ID"), r.Header.Get("Authorization"), r.Header.Values("X-Test-Devicecheck")})
				if r.URL.Path == "/backend-api/ios/attestation_challenge" {
					if r.URL.Query().Get("failure") != "" {
						w.Header().Set("Content-Type", "text/html")
						w.WriteHeader(403)
						_, _ = io.WriteString(w, "<html>TEST_ONLY_PROVIDER_DENIAL</html>")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Content-Encoding", encoding)
					w.Header().Add("X-Provider-Future", "first")
					w.Header().Add("X-Provider-Future", "second")
					_, _ = w.Write(wire)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"message_stream_complete\",\"conversation_id\":\"TEST_ONLY_CONVERSATION\"}\n\ndata: [DONE]\n\n")
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
			run := func(method, path, body string, keyID, uid, gid int64) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
				c.Request.Header.Set("User-Agent", "TEST_ONLY_Mac_26.930")
				c.Request.Header.Set("OAI-Device-ID", "TEST_ONLY_DEVICE")
				c.Request.Header.Set("Accept-Encoding", encoding)
				c.Request.Header.Add("X-Test-Devicecheck", "first")
				c.Request.Header.Add("X-Test-Devicecheck", "second")
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: keyID, GroupID: &gid, Group: &service.Group{ID: gid, Platform: service.PlatformOpenAI, RateMultiplier: 1}, User: &service.User{ID: uid, Balance: 100}})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: uid, Concurrency: 2})
				if method == "GET" {
					h.ChatGPTAttestationChallenge(c)
				} else {
					h.ChatGPTConversation(c)
				}
				require.Empty(t, slots.active)
				require.Empty(t, slots.users)
				return w
			}
			path := "/chatgpt/backend-api/ios/attestation_challenge?x=a%2Fb&x=a+b"
			w := run("GET", path, "", 23, 21, groupID)
			require.Equal(t, 200, w.Code)
			require.Equal(t, wire, w.Body.Bytes())
			require.Equal(t, encoding, w.Header().Get("Content-Encoding"))
			require.Equal(t, []string{"first", "second"}, w.Header().Values("X-Provider-Future"))
			require.Zero(t, usage.count(), "integrity preparation is not a model turn")
			require.Equal(t, "GET", captured[0].method)
			require.Equal(t, "/backend-api/ios/attestation_challenge?x=a%2Fb&x=a+b", captured[0].path)
			require.Empty(t, captured[0].body)
			require.Equal(t, []string{"first", "second"}, captured[0].integrity)
			// Bound selection must win even if generic scheduling now favors a
			// different account; the caller's body remains byte-for-byte intact.
			accounts.accounts = []service.Account{other, owner}
			body := `{ "model":"auto", "app_attest_challenge":"TEST_ONLY_CHALLENGE", "messages":[{"id":"TEST_ONLY_USER","author":{"role":"user"},"content":{"parts":["TEST_ONLY_TEXT"]}}], "future":null }`
			w = run("POST", "/chatgpt/backend-api/f/conversation", body, 23, 21, groupID)
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, captured[0].account, captured[1].account)
			require.Equal(t, captured[0].auth, captured[1].auth)
			require.Equal(t, body, captured[1].body)
			require.Equal(t, 1, usage.count())
			scope := service.ChatGPTTurnScope{UserID: 21, APIKeyID: 23, GroupID: groupID}
			liveAccounts, err := cache.ListChatGPTUpdateAccounts(context.Background(), scope.UpdatesKey())
			require.NoError(t, err)
			require.Len(t, liveAccounts, 1)
			// After upload affinity expires, the next challenge must still use
			// the one account owning this scope's existing conversation.
			require.NoError(t, cache.BindChatGPTUploadOwner(context.Background(), []string{scope.UploadSessionKey("TEST_ONLY_DEVICE")}, liveAccounts[0], time.Nanosecond))
			w = run("GET", path, "", 23, 21, groupID)
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, captured[0].account, captured[2].account)
			require.Equal(t, 1, usage.count())
			calls := len(captured)
			for _, scope := range [][3]int64{{25, 21, groupID}, {23, 25, groupID}, {23, 21, 25}} {
				w = run("POST", "/chatgpt/backend-api/f/conversation", body, scope[0], scope[1], scope[2])
				require.Equal(t, 409, w.Code)
				require.Len(t, captured, calls, "foreign scope must not reach the provider")
			}
			require.NoError(t, cache.BindChatGPTUploadOwner(context.Background(), []string{scope.AttestationKey("TEST_ONLY_EXPIRED")}, owner.ID, time.Nanosecond))
			for _, invalid := range []string{`{"model":"auto","app_attest_challenge":"TEST_ONLY_EXPIRED"}`, `{"model":"auto","app_attest_challenge":"TEST_ONLY_MISSING"}`, `{"model":"auto","app_attest_challenge":"first","app_attest_challenge":"second"}`} {
				w = run("POST", "/chatgpt/backend-api/f/conversation", invalid, 23, 21, groupID)
				require.True(t, w.Code == 400 || w.Code == 409)
				require.Len(t, captured, calls)
			}
			w = run("GET", path+"&failure=1", "", 23, 21, groupID)
			require.Equal(t, 403, w.Code)
			require.Equal(t, "<html>TEST_ONLY_PROVIDER_DENIAL</html>", w.Body.String())
			cfg.Gateway.OpenAIChatEnabled = false
			calls = len(captured)
			w = run("GET", path, "", 23, 21, groupID)
			require.Equal(t, 404, w.Code)
			require.Len(t, captured, calls)
		})
	}
}
