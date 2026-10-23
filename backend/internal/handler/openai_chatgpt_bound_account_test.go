package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type chatGPTBoundFixture struct {
	t        *testing.T
	gid      int64
	accounts *chatGPTAccountRepo
	cache    *chatGPTSharedTurnCache
	h        *OpenAIGatewayHandler
	calls    []string
	scope    service.ChatGPTTurnScope

	providerAccounts []string
}

const (
	chatGPTBoundDeviceID = "TEST_ONLY_DEVICE"
	chatGPTBoundCookie   = "_devicecheck=TEST_ONLY_PROOF"
)

func newChatGPTBoundFixture(t *testing.T) *chatGPTBoundFixture {
	gin.SetMode(gin.TestMode)
	f := &chatGPTBoundFixture{t: t, gid: 48}
	owner := service.Account{ID: 248, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{f.gid}, Credentials: map[string]any{"access_token": "TEST_ONLY_OAUTH", "chatgpt_account_id": "TEST_ONLY_ACCOUNT"}}
	f.accounts = &chatGPTAccountRepo{accounts: []service.Account{owner}}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		f.calls = append(f.calls, r.URL.Path)
		f.providerAccounts = append(f.providerAccounts, r.Header.Get("ChatGPT-Account-ID"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/backend-api/devicecheck":
			w.Header().Add("Set-Cookie", "_devicecheck=TEST_ONLY_NEW_PROOF; Domain=.chatgpt.com; Path=/; Max-Age=600; Secure; HttpOnly")
			_, _ = io.WriteString(w, `{}`)
		case "/backend-api/f/conversation":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"message_stream_complete\",\"conversation_id\":\"TEST_ONLY_CONVERSATION\"}\n\ndata: [DONE]\n\n")
		default:
			_, _ = io.WriteString(w, `{"models":[]}`)
		}
	}))
	t.Cleanup(provider.Close)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.OpenAIChatEnabled = true
	cfg.Gateway.OpenAIChatUpstreamBaseURL = provider.URL
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	f.cache = &chatGPTSharedTurnCache{&chatGPTStickyCache{bindings: make(map[string]int64)}, service.NewChatGPTMemoryTurnCache()}
	slots := &chatGPTSlotCache{active: make(map[string]int64), attempts: make(map[int64]int)}
	concurrency := service.NewConcurrencyService(slots)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, cfg)
	t.Cleanup(billing.Stop)
	network := &chatGPTLocalNetwork{&http.Client{Transport: &http.Transport{Proxy: nil, DisableCompression: true}}}
	svc := service.NewOpenAIGatewayService(f.accounts, &chatGPTLockedUsage{}, nil, nil, nil, nil, f.cache, cfg, nil, concurrency, nil, nil, billing, network, &service.DeferredService{}, nil)
	f.h = NewOpenAIGatewayHandler(svc, concurrency, billing, nil, nil, nil, nil, cfg, nil)
	f.h.chatGPTBillingSettings = &chatGPTPriceReader{price: .01}
	f.scope = service.ChatGPTTurnScope{UserID: 21, APIKeyID: 23, GroupID: f.gid}
	// The device proof was issued by account 248 (the incident's state).
	_, err := f.cache.ClaimChatGPTUploadOwner(context.Background(), f.cookieKey(), owner.ID, time.Hour)
	require.NoError(t, err)
	return f
}

func (f *chatGPTBoundFixture) cookieKey() string {
	return f.scope.DeviceCookieKey(chatGPTBoundDeviceID, "TEST_ONLY_PROOF")
}

func (f *chatGPTBoundFixture) binding() int64 {
	owner, err := f.cache.GetChatGPTUploadOwner(context.Background(), f.cookieKey())
	require.NoError(f.t, err)
	return owner
}

func (f *chatGPTBoundFixture) setOwner(mutate func(*service.Account)) {
	mutate(&f.accounts.accounts[0])
}

func (f *chatGPTBoundFixture) run(method, path, body, cookie string) (*httptest.ResponseRecorder, *gin.Context) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	c.Request.Header.Set("OAI-DID", chatGPTBoundDeviceID)
	if cookie != "" {
		c.Request.Header.Set("Cookie", cookie)
	}
	gid := f.gid
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 23, GroupID: &gid, Group: &service.Group{ID: gid, Platform: service.PlatformOpenAI, RateMultiplier: 1}, User: &service.User{ID: 21, Balance: 100}})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 21, Concurrency: 2})
	switch c.Request.URL.Path {
	case "/chatgpt/backend-api/models":
		f.h.ChatGPTModels(c)
	case "/chatgpt/backend-api/devicecheck":
		f.h.ChatGPTDeviceCheck(c)
	default:
		f.h.ChatGPTConversation(c)
	}
	return w, c
}

func chatGPTBoundErrorBody(t *testing.T, w *httptest.ResponseRecorder) (string, string, string) {
	var payload struct {
		Error struct{ Type, Code, Message string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload), w.Body.String())
	return payload.Error.Type, payload.Error.Code, payload.Error.Message
}

func chatGPTBoundOpsDetail(c *gin.Context) string {
	v, _ := c.Get(opsGatewayErrorDetailKey)
	detail, _ := v.(string)
	return detail
}

func TestChatGPTModelsBoundToHealthyAccountIsUnchanged(t *testing.T) {
	f := newChatGPTBoundFixture(t)
	w, c := f.run("GET", "/chatgpt/backend-api/models", "", chatGPTBoundCookie)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, `{"models":[]}`, w.Body.String())
	require.Equal(t, []string{"/backend-api/models"}, f.calls)
	require.Empty(t, w.Header().Values("Set-Cookie"))
	require.Empty(t, chatGPTBoundOpsDetail(c))
	require.EqualValues(t, 248, f.binding())
}

func TestChatGPTModelsBoundToRateLimitedAccountReturns429AndKeepsBinding(t *testing.T) {
	f := newChatGPTBoundFixture(t)
	reset := time.Now().Add(2 * time.Hour)
	f.setOwner(func(a *service.Account) { a.RateLimitResetAt = &reset })
	w, c := f.run("GET", "/chatgpt/backend-api/models", "", chatGPTBoundCookie)
	require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
	errType, code, message := chatGPTBoundErrorBody(t, w)
	require.Equal(t, "rate_limit_error", errType)
	require.Equal(t, "bound_account_unavailable", code)
	require.Contains(t, message, "temporarily rate limited; retry after "+reset.UTC().Format(time.RFC3339))
	require.NotContains(t, message, "248")
	retryAfter, err := strconv.Atoi(w.Header().Get("Retry-After"))
	require.NoError(t, err)
	require.InDelta(t, 7200, retryAfter, 5)
	require.Empty(t, w.Header().Values("Set-Cookie"))
	require.Empty(t, f.calls)
	require.EqualValues(t, 248, f.binding(), "a temporary cooldown keeps the device binding")
	require.Contains(t, chatGPTBoundOpsDetail(c), "bound account 248 unavailable (temporary: rate_limited until")
	accountID, _ := c.Get(opsAccountIDKey)
	require.EqualValues(t, 248, accountID)

	// Very long cooldowns are reported exactly but Retry-After stays bounded.
	far := time.Now().Add(5 * 24 * time.Hour)
	f.setOwner(func(a *service.Account) { a.RateLimitResetAt = &far })
	w, _ = f.run("GET", "/chatgpt/backend-api/models", "", chatGPTBoundCookie)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, strconv.Itoa(int(chatGPTBoundRetryAfterMax.Seconds())), w.Header().Get("Retry-After"))

	// Once the cooldown ends the same proof serves again on the same account.
	f.setOwner(func(a *service.Account) { a.RateLimitResetAt = nil })
	w, _ = f.run("GET", "/chatgpt/backend-api/models", "", chatGPTBoundCookie)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestChatGPTModelsBoundToDisabledAccountRequiresRegistration(t *testing.T) {
	for name, mutate := range map[string]func(*service.Account){
		"admin unschedulable": func(a *service.Account) { a.Schedulable = false },
		"disabled":            func(a *service.Account) { a.Status = service.StatusDisabled },
		"removed from group":  func(a *service.Account) { a.GroupIDs = []int64{99} },
		"deleted":             func(a *service.Account) { a.ID = 9999 },
	} {
		t.Run(name, func(t *testing.T) {
			f := newChatGPTBoundFixture(t)
			if name == "deleted" {
				f.accounts.getByIDNotFound = true
			}
			f.setOwner(mutate)
			w, c := f.run("GET", "/chatgpt/backend-api/models", "", chatGPTBoundCookie)
			require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
			errType, _, message := chatGPTBoundErrorBody(t, w)
			require.Equal(t, "device_registration_required", errType)
			require.Contains(t, message, "register this device again")
			require.Equal(t, []string{clearChatGPTDeviceCookieHeader}, w.Header().Values("Set-Cookie"))
			require.Empty(t, w.Header().Get("Retry-After"))
			require.Empty(t, f.calls)
			require.Zero(t, f.binding(), "the stale ownership key is deleted")
			require.Contains(t, chatGPTBoundOpsDetail(c), "bound account 248 unavailable (permanent:")

			// The client registers again without the stale proof and is
			// scheduled on any available account of the group.
			f.setOwner(func(a *service.Account) {
				*a = service.Account{ID: 245, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{f.gid}, Credentials: map[string]any{"access_token": "TEST_ONLY_OAUTH", "chatgpt_account_id": "TEST_ONLY_ACCOUNT"}}
			})
			f.accounts.getByIDNotFound = false
			w, _ = f.run("GET", "/chatgpt/backend-api/models", "", "")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		})
	}
}

func TestChatGPTModelsBoundAccountStoreFailureStays503(t *testing.T) {
	f := newChatGPTBoundFixture(t)
	f.accounts.accounts = []service.Account{} // GetByID fails with a non-NotFound error
	w, c := f.run("GET", "/chatgpt/backend-api/models", "", chatGPTBoundCookie)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	errType, _, message := chatGPTBoundErrorBody(t, w)
	require.Equal(t, "service_unavailable", errType)
	require.Equal(t, "Bound ChatGPT account state is temporarily unavailable; retry later", message)
	require.Empty(t, w.Header().Values("Set-Cookie"))
	require.EqualValues(t, 248, f.binding())
	require.Contains(t, chatGPTBoundOpsDetail(c), "account_store_error")
}

func TestChatGPTModelRequestBoundConversationTemporaryAndPermanent(t *testing.T) {
	const followUp = `{"model":"auto","conversation_id":"TEST_ONLY_CONVERSATION","messages":[{"id":"TEST_ONLY_FOLLOW_UP","author":{"role":"user"},"content":{"parts":["TEST_ONLY_TEXT"]}}]}`
	seed := func(f *chatGPTBoundFixture) {
		require.NoError(t, f.cache.BindChatGPTDeliveryOwner(context.Background(), f.scope.ConversationOwnerKey("TEST_ONLY_CONVERSATION"), &service.ChatGPTDeliveryOwner{AccountID: 248}))
	}

	t.Run("healthy", func(t *testing.T) {
		f := newChatGPTBoundFixture(t)
		seed(f)
		w, _ := f.run("POST", "/chatgpt/backend-api/f/conversation", followUp, chatGPTBoundCookie)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, []string{"/backend-api/f/conversation"}, f.calls)
	})

	t.Run("temporary", func(t *testing.T) {
		f := newChatGPTBoundFixture(t)
		seed(f)
		until := time.Now().Add(10 * time.Minute)
		f.setOwner(func(a *service.Account) { a.OverloadUntil = &until })
		w, c := f.run("POST", "/chatgpt/backend-api/f/conversation", followUp, "")
		require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
		errType, _, message := chatGPTBoundErrorBody(t, w)
		require.Equal(t, "rate_limit_error", errType)
		require.Contains(t, message, "temporarily overloaded")
		require.NotEmpty(t, w.Header().Get("Retry-After"))
		require.Empty(t, f.calls)
		require.Contains(t, chatGPTBoundOpsDetail(c), "bound context conversation")
		owner, _, err := service.ResolveChatGPTConversationOwner(context.Background(), f.cache, f.scope, "TEST_ONLY_CONVERSATION")
		require.NoError(t, err)
		require.EqualValues(t, 248, owner.AccountID, "the conversation is never moved")
	})

	t.Run("permanent without device proof", func(t *testing.T) {
		f := newChatGPTBoundFixture(t)
		seed(f)
		f.setOwner(func(a *service.Account) { a.Status = service.StatusError })
		w, _ := f.run("POST", "/chatgpt/backend-api/f/conversation", followUp, "")
		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		errType, _, message := chatGPTBoundErrorBody(t, w)
		require.Equal(t, "conversation_context_unavailable", errType)
		require.Contains(t, message, "start a new conversation")
		require.Empty(t, w.Header().Values("Set-Cookie"))
		require.Empty(t, f.calls)
		require.EqualValues(t, 248, f.binding(), "an unrelated proof is not touched")
	})

	t.Run("permanent with device proof", func(t *testing.T) {
		f := newChatGPTBoundFixture(t)
		seed(f)
		f.setOwner(func(a *service.Account) { a.Schedulable = false })
		w, _ := f.run("POST", "/chatgpt/backend-api/f/conversation", followUp, chatGPTBoundCookie)
		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		errType, _, _ := chatGPTBoundErrorBody(t, w)
		require.Equal(t, "conversation_context_unavailable", errType)
		require.Equal(t, []string{clearChatGPTDeviceCookieHeader}, w.Header().Values("Set-Cookie"))
		require.Zero(t, f.binding())
		require.Empty(t, f.calls)
	})

	t.Run("new conversation bound only by device", func(t *testing.T) {
		f := newChatGPTBoundFixture(t)
		reset := time.Now().Add(time.Hour)
		f.setOwner(func(a *service.Account) { a.RateLimitResetAt = &reset })
		const fresh = `{"model":"auto","messages":[{"id":"TEST_ONLY_NEW","author":{"role":"user"},"content":{"parts":["TEST_ONLY_TEXT"]}}]}`
		w, _ := f.run("POST", "/chatgpt/backend-api/f/conversation", fresh, chatGPTBoundCookie)
		require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
		f.setOwner(func(a *service.Account) { a.RateLimitResetAt = nil; a.Schedulable = false })
		w, _ = f.run("POST", "/chatgpt/backend-api/f/conversation", fresh, chatGPTBoundCookie)
		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		errType, _, _ := chatGPTBoundErrorBody(t, w)
		require.Equal(t, "device_registration_required", errType)
		require.Zero(t, f.binding())
		require.Empty(t, f.calls)
	})
}

func TestChatGPTBoundRetryAfterBounds(t *testing.T) {
	now := time.Now()
	require.Equal(t, 1, chatGPTBoundRetryAfter(now.Add(-time.Hour), now))
	require.Equal(t, 1, chatGPTBoundRetryAfter(now.Add(200*time.Millisecond), now))
	require.Equal(t, 91, chatGPTBoundRetryAfter(now.Add(90*time.Second+time.Millisecond), now))
	require.Equal(t, int(chatGPTBoundRetryAfterMax.Seconds()), chatGPTBoundRetryAfter(now.Add(72*time.Hour), now))
}

func TestOpsErrorLoggerRecordsBoundAccountReason(t *testing.T) {
	queue := opsFailureTestQueue(t)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	f := newChatGPTBoundFixture(t)
	reset := time.Now().Add(time.Hour)
	f.setOwner(func(a *service.Account) { a.RateLimitResetAt = &reset })
	r := gin.New()
	gid := f.gid
	r.GET("/chatgpt/backend-api/models", OpsErrorLoggerMiddleware(ops), func(c *gin.Context) {
		c.Request.Header.Set("OAI-DID", chatGPTBoundDeviceID)
		c.Request.Header.Set("Cookie", chatGPTBoundCookie)
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 23, GroupID: &gid, Group: &service.Group{ID: gid, Platform: service.PlatformOpenAI, RateMultiplier: 1}, User: &service.User{ID: 21, Balance: 100}})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 21, Concurrency: 2})
		f.h.ChatGPTModels(c)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/chatgpt/backend-api/models", nil))
	require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "bound account 248", "operator detail never reaches the client")
	select {
	case job := <-queue:
		require.Equal(t, http.StatusTooManyRequests, job.entry.StatusCode)
		require.EqualValues(t, 248, *job.entry.AccountID)
		require.Equal(t, "routing", job.entry.ErrorPhase)
		require.Contains(t, job.entry.ErrorMessage, "Bound ChatGPT account is temporarily rate limited")
		require.Contains(t, job.entry.ErrorMessage, "[bound account 248 unavailable (temporary: rate_limited until "+reset.UTC().Format(time.RFC3339)+"); bound context device]")
	default:
		t.Fatal("bound account failure was not queued")
	}
}

type chatGPTBoundUpdatesCache struct {
	service.ChatGPTTurnCache
	service.ChatGPTUploadCache
	accounts   []int64
	releaseErr error
}

func (c *chatGPTBoundUpdatesCache) ListChatGPTUpdateAccounts(ctx context.Context, key string) ([]int64, error) {
	if c.accounts == nil {
		return c.ChatGPTTurnCache.ListChatGPTUpdateAccounts(ctx, key)
	}
	return c.accounts, nil
}

func (c *chatGPTBoundUpdatesCache) ReleaseChatGPTUploadOwner(ctx context.Context, key string, owner int64) error {
	if c.releaseErr != nil {
		return c.releaseErr
	}
	return c.ChatGPTUploadCache.ReleaseChatGPTUploadOwner(ctx, key, owner)
}

func TestChatGPTDeviceRegistrationSkipsPermanentlyUnavailableConversationAffinity(t *testing.T) {
	f := newChatGPTBoundFixture(t)
	healthy := f.accounts.accounts[0]
	healthy.ID, healthy.Credentials = 245, map[string]any{"access_token": "TEST_ONLY_OAUTH_245", "chatgpt_account_id": "TEST_ONLY_ACCOUNT_245"}
	f.setOwner(func(a *service.Account) { a.Schedulable = false })
	f.accounts.accounts = append(f.accounts.accounts, healthy)
	memory := f.cache.ChatGPTTurnCache
	// The only live conversation of this scope is on the stale account 248.
	uploads, ok := memory.(service.ChatGPTUploadCache)
	require.True(t, ok)
	f.cache.ChatGPTTurnCache = &chatGPTBoundUpdatesCache{ChatGPTTurnCache: memory, ChatGPTUploadCache: uploads, accounts: []int64{248}}

	w, _ := f.run("GET", "/chatgpt/backend-api/models", "", chatGPTBoundCookie)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Zero(t, f.binding())

	const registration = `{"device_token":"TEST_ONLY_APPLE_TOKEN","bundle_id":"com.openai.codex"}`
	w, _ = f.run("POST", "/chatgpt/backend-api/devicecheck", registration, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []string{"TEST_ONLY_ACCOUNT_245"}, f.providerAccounts, "re-registration must not return to the stale owner")
	owner, err := f.cache.GetChatGPTUploadOwner(context.Background(), f.scope.DeviceCookieKey(chatGPTBoundDeviceID, "TEST_ONLY_NEW_PROOF"))
	require.NoError(t, err)
	require.EqualValues(t, 245, owner)

}

func (f *chatGPTBoundFixture) addHealthy245() {
	healthy := f.accounts.accounts[0]
	healthy.Schedulable, healthy.Status, healthy.RateLimitResetAt = true, service.StatusActive, nil
	healthy.ID, healthy.Credentials = 245, map[string]any{"access_token": "TEST_ONLY_OAUTH_245", "chatgpt_account_id": "TEST_ONLY_ACCOUNT_245"}
	f.accounts.accounts = append(f.accounts.accounts, healthy)
}

func (f *chatGPTBoundFixture) failingReleases() *chatGPTBoundUpdatesCache {
	memory := f.cache.ChatGPTTurnCache
	uploads, ok := memory.(service.ChatGPTUploadCache)
	require.True(f.t, ok)
	wrapped := &chatGPTBoundUpdatesCache{ChatGPTTurnCache: memory, ChatGPTUploadCache: uploads, releaseErr: errors.New("TEST_ONLY_REDIS_DOWN")}
	f.cache.ChatGPTTurnCache = wrapped
	return wrapped
}

// A normal registration seeds both the 5-minute device affinity and the
// proof ownership. After the owner is disabled, the client must recover on
// another account: first the proof is rejected (409 + clear), then the next
// registration skips the stale affinity instead of failing with a second 409.
func TestChatGPTReRegistrationAfterPermanentOwnerLandsOnAvailableAccount(t *testing.T) {
	f := newChatGPTBoundFixture(t)
	const registration = `{"device_token":"TEST_ONLY_APPLE_TOKEN","bundle_id":"com.openai.codex"}`
	w, _ := f.run("POST", "/chatgpt/backend-api/devicecheck", registration, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []string{"TEST_ONLY_ACCOUNT"}, f.providerAccounts)
	sessionKey := f.scope.UploadSessionKey(chatGPTBoundDeviceID)
	proofKey := f.scope.DeviceCookieKey(chatGPTBoundDeviceID, "TEST_ONLY_NEW_PROOF")
	for _, key := range []string{sessionKey, proofKey} {
		owner, err := f.cache.GetChatGPTUploadOwner(context.Background(), key)
		require.NoError(t, err)
		require.EqualValues(t, 248, owner)
	}

	f.setOwner(func(a *service.Account) { a.Schedulable = false })
	f.addHealthy245()
	f.providerAccounts = nil
	w, _ = f.run("GET", "/chatgpt/backend-api/models", "", "_devicecheck=TEST_ONLY_NEW_PROOF")
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Equal(t, []string{clearChatGPTDeviceCookieHeader}, w.Header().Values("Set-Cookie"))

	w, c := f.run("POST", "/chatgpt/backend-api/devicecheck", registration, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Empty(t, chatGPTBoundOpsDetail(c))
	require.Equal(t, []string{"TEST_ONLY_ACCOUNT_245"}, f.providerAccounts)
	for _, key := range []string{sessionKey, proofKey} {
		owner, err := f.cache.GetChatGPTUploadOwner(context.Background(), key)
		require.NoError(t, err)
		require.EqualValues(t, 245, owner)
	}
}

func TestChatGPTBoundReleaseFailuresAreStoreErrors(t *testing.T) {
	t.Run("device proof", func(t *testing.T) {
		f := newChatGPTBoundFixture(t)
		f.setOwner(func(a *service.Account) { a.Schedulable = false })
		f.failingReleases()
		w, c := f.run("GET", "/chatgpt/backend-api/models", "", chatGPTBoundCookie)
		require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
		_, _, message := chatGPTBoundErrorBody(t, w)
		require.Equal(t, chatGPTBoundStoreEnvelope.Message, message)
		require.Empty(t, w.Header().Values("Set-Cookie"), "the cookie stays while its ownership key survives")
		require.EqualValues(t, 248, f.binding())
		require.Contains(t, chatGPTBoundOpsDetail(c), "device proof release failed: native Chat device ownership is unavailable: TEST_ONLY_REDIS_DOWN")
	})
	t.Run("short affinity", func(t *testing.T) {
		f := newChatGPTBoundFixture(t)
		_, err := f.cache.ClaimChatGPTUploadOwner(context.Background(), f.scope.UploadSessionKey(chatGPTBoundDeviceID), 248, service.ChatGPTUploadSessionTTL)
		require.NoError(t, err)
		f.setOwner(func(a *service.Account) { a.Status = service.StatusDisabled })
		f.addHealthy245()
		f.failingReleases()
		w, c := f.run("POST", "/chatgpt/backend-api/devicecheck", `{"device_token":"TEST_ONLY_APPLE_TOKEN","bundle_id":"com.openai.codex"}`, "")
		require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
		require.Empty(t, f.calls)
		require.Contains(t, chatGPTBoundOpsDetail(c), "affinity release failed: TEST_ONLY_REDIS_DOWN")
	})
}

// Once an SSE response is committed (e.g. by user-slot wait pings), the
// classification is delivered as an SSE error event: no Retry-After and no
// cookie clearing are possible. The proof ownership is still released, so
// the client's next HTTP request gets the 409 + cookie clear.
func TestChatGPTBoundAccountAfterStreamStartedIsTwoPhase(t *testing.T) {
	f := newChatGPTBoundFixture(t)
	f.setOwner(func(a *service.Account) { a.Schedulable = false })
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/chatgpt/backend-api/f/conversation", nil)
	c.Request.Header.Set("OAI-DID", chatGPTBoundDeviceID)
	c.Request.Header.Set("Cookie", chatGPTBoundCookie)
	owner, ok := f.h.chatGPTDeviceCookieOwner(c, f.scope)
	require.True(t, ok)
	require.EqualValues(t, 248, owner)
	c.Header("Content-Type", "text/event-stream")
	c.Status(http.StatusOK)
	c.Writer.WriteHeaderNow()
	gid := f.gid
	_, err := f.h.gatewayService.GetChatGPTBoundAccount(context.Background(), &gid, owner)
	f.h.writeChatGPTBoundAccountUnavailable(c, err, chatGPTBoundDevice, f.scope, true, chatGPTNoOAuthAccountEnvelope)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "event: error")
	require.Contains(t, w.Body.String(), `"device_registration_required"`)
	require.Empty(t, w.Header().Values("Set-Cookie"))
	require.Empty(t, w.Header().Get("Retry-After"))
	require.Zero(t, f.binding(), "the stale proof ownership is released during the stream")

	w2, _ := f.run("GET", "/chatgpt/backend-api/models", "", chatGPTBoundCookie)
	require.Equal(t, http.StatusConflict, w2.Code, w2.Body.String())
	errType, _, _ := chatGPTBoundErrorBody(t, w2)
	require.Equal(t, "device_registration_required", errType)
	require.Equal(t, []string{clearChatGPTDeviceCookieHeader}, w2.Header().Values("Set-Cookie"))

	// A temporary cooldown during a committed stream keeps the binding.
	f2 := newChatGPTBoundFixture(t)
	reset := time.Now().Add(time.Hour)
	f2.setOwner(func(a *service.Account) { a.RateLimitResetAt = &reset })
	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	c3.Request = httptest.NewRequest("POST", "/chatgpt/backend-api/f/conversation", nil)
	c3.Status(http.StatusOK)
	c3.Writer.WriteHeaderNow()
	_, err = f2.h.gatewayService.GetChatGPTBoundAccount(context.Background(), &gid, 248)
	f2.h.writeChatGPTBoundAccountUnavailable(c3, err, chatGPTBoundConversation, f2.scope, true, chatGPTNoOAuthAccountEnvelope)
	require.Contains(t, w3.Body.String(), `"rate_limit_error"`)
	require.Empty(t, w3.Header().Get("Retry-After"))
	require.EqualValues(t, 248, f2.binding())
}
