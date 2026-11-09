//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The Anthropic /v1/messages usage row carries the inbound body size frozen
// at read time, through a failover, independent of what the gateway sends
// upstream.
func TestGatewayHandlerMessages_RecordsInboundRequestBodyBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	lab := &stopReasonLab{calls: map[string]int{}}
	var upstreamMu sync.Mutex
	var upstreamBodies [][]byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-api-key")
		raw, _ := io.ReadAll(r.Body)
		upstreamMu.Lock()
		upstreamBodies = append(upstreamBodies, raw)
		upstreamMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if key == "TEST_ONLY_UPSTREAM_A" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"permission_error","message":"mock failure"}}`))
			return
		}
		w.Header().Set("request-id", "req_upstream_B")
		_, _ = w.Write([]byte(`{"id":"msg_B","type":"message","role":"assistant","model":"` + stopReasonModel + `","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	t.Cleanup(upstream.Close)
	account := func(id int64, priority int, key string) service.Account {
		return service.Account{ID: id, Name: key, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 5, Priority: priority, GroupIDs: []int64{stopReasonGroupID},
			Credentials: map[string]any{"api_key": key, "base_url": upstream.URL}}
	}
	lab.accounts = []service.Account{
		account(stopReasonAccountA, 1, "TEST_ONLY_UPSTREAM_A"),
		account(stopReasonAccountB, 50, "TEST_ONLY_UPSTREAM_B"),
	}

	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.MaxBodySize = 1 << 20
	cfg.Gateway.MaxAccountSwitches = 3
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Pricing.DataDir, cfg.Pricing.UpdateIntervalHours = t.TempDir(), 24
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Pricing.DataDir, "model_pricing.json"),
		[]byte(`{"`+stopReasonModel+`":{"input_cost_per_token":0.000003,"output_cost_per_token":0.000015,"cache_creation_input_token_cost":0.00000375,"cache_read_input_token_cost":0.0000003}}`), 0o600))
	pricing := service.NewPricingService(cfg, nil)
	require.NoError(t, pricing.Initialize())
	t.Cleanup(pricing.Stop)

	user := &service.User{ID: 701, Status: service.StatusActive, Role: service.RoleUser, Balance: 100, Concurrency: 5}
	users := stopReasonUserRepo{user: user}
	groupID := stopReasonGroupID
	group := &service.Group{ID: groupID, Platform: service.PlatformAnthropic, Status: service.StatusActive, Hydrated: true,
		RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeStandard}
	accounts := stopReasonAccountRepo{lab: lab}
	usageRepo := stopReasonUsageRepo{lab: lab}
	concurrency := service.NewConcurrencyService(stopReasonConcurrency{})
	billingCache := service.NewBillingCacheService(nil, users, nil, nil, cfg)
	t.Cleanup(billingCache.Stop)
	rateLimit := service.NewRateLimitService(accounts, usageRepo, cfg, nil, nil)
	gateway := service.NewGatewayService(accounts, stopReasonGroupRepo{group: group}, usageRepo, stopReasonBillingRepo{}, users, nil, nil, stopReasonGatewayCache{}, cfg,
		nil, concurrency, service.NewBillingService(cfg, pricing), rateLimit, billingCache, nil, &chatGPTLocalNetwork{client: upstream.Client()},
		&service.DeferredService{}, nil, nil, nil, nil, nil)
	h := NewGatewayHandler(gateway, nil, nil, nil, concurrency, billingCache, nil, nil, nil, nil, nil, nil, nil, nil, nil, cfg, nil)
	apiKey := &service.APIKey{ID: 801, UserID: user.ID, Key: "TEST_ONLY_KEY", Status: service.StatusActive, GroupID: &groupID, User: user, Group: group}

	// Insignificant whitespace makes the client body differ from anything the
	// gateway re-serializes for upstream.
	body := `{ "model" : "` + stopReasonModel + `" ,   "max_tokens" : 64 , "messages" : [ { "role" : "user" , "content" : "` + strings.Repeat("hi ", 50) + `" } ] }`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req.WithContext(context.WithValue(req.Context(), ctxkey.RequestID, "local-req-body-bytes"))
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user.ID, Concurrency: user.Concurrency})

	h.Messages(c)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	upstreamMu.Lock()
	require.Len(t, upstreamBodies, 2, "failover A -> B")
	upstreamMu.Unlock()
	lab.mu.Lock()
	usage := append([]service.UsageLog(nil), lab.usage...)
	lab.mu.Unlock()
	require.Len(t, usage, 1)
	require.Equal(t, stopReasonAccountB, usage[0].AccountID)
	require.NotNil(t, usage[0].RequestBodyBytes)
	require.Equal(t, int64(len(body)), *usage[0].RequestBodyBytes)
}

type bodyBytesGeminiCache struct{ stopReasonGatewayCache }

type bodyBytesAccountRepo struct{ stopReasonAccountRepo }

func (r bodyBytesAccountRepo) ListSchedulableByPlatform(ctx context.Context, _ string) ([]service.Account, error) {
	return r.ListSchedulableByGroupIDAndPlatform(ctx, 0, "")
}

func (r bodyBytesAccountRepo) ListSchedulableByPlatforms(ctx context.Context, _ []string) ([]service.Account, error) {
	return r.ListSchedulableByGroupIDAndPlatform(ctx, 0, "")
}

func (bodyBytesGeminiCache) FindGeminiSession(context.Context, int64, string, string) (string, int64, string, bool) {
	return "", 0, "", false
}

func (bodyBytesGeminiCache) SaveGeminiSession(context.Context, int64, string, string, string, int64, string) error {
	return nil
}

// Gemini native: the request carries a thoughtSignature without a session
// binding, so the handler cleans (rewrites) the body before forwarding. The
// usage row still reports the size of the body as received.
func TestGeminiV1BetaModels_RecordsInboundSizeBeforeSignatureCleaning(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var upstreamMu sync.Mutex
	var upstreamBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		upstreamMu.Lock()
		upstreamBody = raw
		upstreamMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`))
	}))
	t.Cleanup(upstream.Close)

	const geminiGroupID = int64(42)
	const geminiModel = "gemini-2.5-pro"
	lab := &stopReasonLab{calls: map[string]int{}}
	lab.accounts = []service.Account{{ID: 4201, Name: "gemini-key", Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 5, GroupIDs: []int64{geminiGroupID},
		Credentials: map[string]any{"api_key": "TEST_ONLY_GEMINI_KEY", "base_url": upstream.URL}}}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.MaxBodySize = 1 << 20
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	user := &service.User{ID: 702, Status: service.StatusActive, Role: service.RoleUser, Balance: 100, Concurrency: 5}
	users := stopReasonUserRepo{user: user}
	groupID := geminiGroupID
	group := &service.Group{ID: groupID, Platform: service.PlatformGemini, Status: service.StatusActive, Hydrated: true,
		RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeStandard}
	accounts := bodyBytesAccountRepo{stopReasonAccountRepo{lab: lab}}
	usageRepo := stopReasonUsageRepo{lab: lab}
	cache := bodyBytesGeminiCache{}
	concurrency := service.NewConcurrencyService(stopReasonConcurrency{})
	billingCache := service.NewBillingCacheService(nil, users, nil, nil, cfg)
	t.Cleanup(billingCache.Stop)
	rateLimit := service.NewRateLimitService(accounts, usageRepo, cfg, nil, nil)
	httpUpstream := &chatGPTLocalNetwork{client: upstream.Client()}
	gateway := service.NewGatewayService(accounts, stopReasonGroupRepo{group: group}, usageRepo, stopReasonBillingRepo{}, users, nil, nil, cache, cfg,
		nil, concurrency, service.NewBillingService(cfg, nil), rateLimit, billingCache, nil, httpUpstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil)
	geminiCompat := service.NewGeminiMessagesCompatService(accounts, stopReasonGroupRepo{group: group}, cache, nil, nil, rateLimit, httpUpstream, nil, cfg)
	h := NewGatewayHandler(gateway, geminiCompat, nil, nil, concurrency, billingCache, nil, nil, nil, nil, nil, nil, nil, nil, nil, cfg, nil)
	apiKey := &service.APIKey{ID: 802, UserID: user.ID, Key: "TEST_ONLY_KEY", Status: service.StatusActive, GroupID: &groupID, User: user, Group: group}

	signature := strings.Repeat("S", 4096)
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"text":"x","thoughtSignature":"` + signature + `"}]},{"role":"user","parts":[{"text":"again"}]}]}`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+geminiModel+":generateContent", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req.WithContext(context.WithValue(req.Context(), ctxkey.RequestID, "local-req-gemini-body-bytes"))
	c.Params = gin.Params{{Key: "modelAction", Value: "/" + geminiModel + ":generateContent"}}
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user.ID, Concurrency: user.Concurrency})

	h.GeminiV1BetaModels(c)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	upstreamMu.Lock()
	forwarded := append([]byte(nil), upstreamBody...)
	upstreamMu.Unlock()
	require.NotContains(t, string(forwarded), signature, "the forwarded body was cleaned")
	lab.mu.Lock()
	usage := append([]service.UsageLog(nil), lab.usage...)
	lab.mu.Unlock()
	require.Len(t, usage, 1)
	require.NotNil(t, usage[0].RequestBodyBytes)
	require.Equal(t, int64(len(body)), *usage[0].RequestBodyBytes)
}
