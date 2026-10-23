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
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// End-to-end handler proof for upstream stop_reason evidence: account A fails
// with a failover status, account B answers with a zero-output refusal. Only
// B's stop_reason may reach the usage row and the WARN must fire exactly once.
const (
	stopReasonGroupID  = int64(41)
	stopReasonAccountA = int64(4101)
	stopReasonAccountB = int64(4102)
	stopReasonModel    = "claude-sonnet-4-5"
)

type stopReasonLab struct {
	mu       sync.Mutex
	accounts []service.Account
	calls    map[string]int
	usage    []service.UsageLog
}

func (l *stopReasonLab) upstream(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("x-api-key")
	l.mu.Lock()
	l.calls[key]++
	l.mu.Unlock()
	_, _ = io.Copy(io.Discard, r.Body)
	w.Header().Set("Content-Type", "application/json")
	if key == "TEST_ONLY_UPSTREAM_A" {
		w.Header().Set("request-id", "req_upstream_A")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"permission_error","message":"mock failure"},"stop_reason":"end_turn"}`))
		return
	}
	w.Header().Set("request-id", "req_upstream_B")
	_, _ = w.Write([]byte(`{"id":"msg_B","type":"message","role":"assistant","model":"` + stopReasonModel + `","content":[],"stop_reason":"refusal","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":0,"cache_creation_input_tokens":200000,"cache_read_input_tokens":0}}`))
}

type stopReasonAccountRepo struct {
	service.AccountRepository
	lab *stopReasonLab
}

func (r stopReasonAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	for _, account := range r.lab.accounts {
		if account.ID == id {
			copied := account
			return &copied, nil
		}
	}
	return nil, service.ErrAccountNotFound
}

func (r stopReasonAccountRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]service.Account, error) {
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	return append([]service.Account(nil), r.lab.accounts...), nil
}

func (stopReasonAccountRepo) SetError(context.Context, int64, string) error { return nil }

func (r stopReasonAccountRepo) ListByGroup(context.Context, int64) ([]service.Account, error) {
	return r.ListSchedulableByGroupIDAndPlatform(context.Background(), 0, "")
}

func (r stopReasonAccountRepo) ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]service.Account, error) {
	return r.ListSchedulableByGroupIDAndPlatform(context.Background(), 0, "")
}

type stopReasonUsageRepo struct {
	service.UsageLogRepository
	lab *stopReasonLab
}

func (r stopReasonUsageRepo) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	r.lab.usage = append(r.lab.usage, *log)
	return true, nil
}

func (r stopReasonUsageRepo) CreateBestEffort(ctx context.Context, log *service.UsageLog) error {
	_, err := r.Create(ctx, log)
	return err
}

type stopReasonUserRepo struct {
	service.UserRepository
	user *service.User
}

func (r stopReasonUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	copied := *r.user
	return &copied, nil
}

type stopReasonGroupRepo struct {
	service.GroupRepository
	group *service.Group
}

func (r stopReasonGroupRepo) GetByID(context.Context, int64) (*service.Group, error) {
	copied := *r.group
	return &copied, nil
}

func (r stopReasonGroupRepo) GetByIDLite(ctx context.Context, id int64) (*service.Group, error) {
	return r.GetByID(ctx, id)
}

type stopReasonBillingRepo struct{}

func (stopReasonBillingRepo) Apply(context.Context, *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	return &service.UsageBillingApplyResult{Applied: true}, nil
}

type stopReasonGatewayCache struct{ service.GatewayCache }

func (stopReasonGatewayCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, nil
}
func (stopReasonGatewayCache) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}
func (stopReasonGatewayCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}
func (stopReasonGatewayCache) DeleteSessionAccountID(context.Context, int64, string) error {
	return nil
}

type stopReasonConcurrency struct{ service.ConcurrencyCache }

func (stopReasonConcurrency) AcquireUserSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}
func (stopReasonConcurrency) ReleaseUserSlot(context.Context, int64, string) error { return nil }
func (stopReasonConcurrency) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}
func (stopReasonConcurrency) ReleaseAccountSlot(context.Context, int64, string) error { return nil }
func (stopReasonConcurrency) IncrementWaitCount(context.Context, int64, int) (bool, error) {
	return true, nil
}
func (stopReasonConcurrency) DecrementWaitCount(context.Context, int64) error { return nil }
func (stopReasonConcurrency) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	return true, nil
}
func (stopReasonConcurrency) DecrementAccountWaitCount(context.Context, int64) error { return nil }
func (stopReasonConcurrency) GetAccountsLoadBatch(_ context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	out := make(map[int64]*service.AccountLoadInfo, len(accounts))
	for _, account := range accounts {
		out[account.ID] = &service.AccountLoadInfo{AccountID: account.ID}
	}
	return out, nil
}

func TestGatewayHandlerMessages_FailoverRecordsOnlyFinalStopReasonAndWarnsOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	lab := &stopReasonLab{calls: map[string]int{}}
	upstream := httptest.NewServer(http.HandlerFunc(lab.upstream))
	t.Cleanup(upstream.Close)
	account := func(id int64, priority int, key string) service.Account {
		return service.Account{ID: id, Name: key, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 5, Priority: priority, GroupIDs: []int64{stopReasonGroupID},
			Credentials: map[string]any{"api_key": key, "base_url": upstream.URL}}
	}
	// A has the better priority so it is tried first.
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
		nil, concurrency, service.NewBillingService(cfg, pricing), rateLimit, billingCache, nil, repository.NewHTTPUpstream(cfg),
		&service.DeferredService{}, nil, nil, nil, nil, nil)
	h := NewGatewayHandler(gateway, nil, nil, nil, concurrency, billingCache, nil, nil, nil, nil, nil, nil, nil, nil, cfg, nil)

	apiKey := &service.APIKey{ID: 801, UserID: user.ID, Key: "TEST_ONLY_KEY", Status: service.StatusActive, GroupID: &groupID, User: user,
		Group: group}

	core, logs := observer.New(zap.WarnLevel)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := `{"model":"` + stopReasonModel + `","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), ctxkey.RequestID, "local-req-1")
	c.Request = req.WithContext(logger.IntoContext(ctx, zap.New(core)))
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user.ID, Concurrency: user.Concurrency})

	h.Messages(c)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	lab.mu.Lock()
	require.Equal(t, 1, lab.calls["TEST_ONLY_UPSTREAM_A"], "account A attempted first")
	require.Equal(t, 1, lab.calls["TEST_ONLY_UPSTREAM_B"], "account B served after failover")
	lab.mu.Unlock()

	// No worker pool is wired, so usage is recorded synchronously.
	lab.mu.Lock()
	usage := append([]service.UsageLog(nil), lab.usage...)
	lab.mu.Unlock()
	require.Len(t, usage, 1)
	require.Equal(t, stopReasonAccountB, usage[0].AccountID)
	require.NotNil(t, usage[0].StopReason)
	require.Equal(t, "refusal", *usage[0].StopReason)
	require.Equal(t, "req_upstream_B", usage[0].RequestID)

	warns := logs.FilterMessage("gateway.zero_output_usage").All()
	require.Len(t, warns, 1)
	fields := warns[0].ContextMap()
	require.Equal(t, stopReasonAccountB, fields["account_id"])
	require.Equal(t, "refusal", fields["stop_reason"])
	require.Equal(t, "req_upstream_B", fields["upstream_request_id"])
	require.Equal(t, int64(0), fields["output_tokens"])
	require.Equal(t, int64(200000), fields["cache_creation_tokens"])
}
