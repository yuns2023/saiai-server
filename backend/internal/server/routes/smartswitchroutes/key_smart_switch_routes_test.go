//go:build unit

// Package smartswitchroutes_test runs in its own test binary: the gateway's
// process-wide extra-CA pool is loaded once, and sharing a process with the
// native Codex route proof would freeze that pool before its mock CA is set.
package smartswitchroutes_test

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
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/server/routes"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Smart switching acceptance through the public Responses route: real auth
// middleware, handler, gateway service and HTTP transport against a loopback
// upstream. Repositories are in-memory doubles; no provider is contacted.
const (
	smartSubscriptionGroup = int64(11)
	smartPaygGroup         = int64(12)
	smartSubscriptionAcct  = int64(111)
	smartPaygAcct          = int64(121)
	smartTestKey           = "TEST_ONLY_SMART_SWITCH_KEY"
)

type smartRoutesLab struct {
	mu sync.Mutex

	key      service.APIKey
	user     service.User
	groups   map[int64]*service.Group
	sub      *service.UserSubscription
	accounts map[int64][]service.Account

	upstreamStatus map[string]int // by upstream API key; default 200
	upstreamCalls  map[string]int
	usage          []service.UsageLog
	billing        []service.UsageBillingCommand
}

func newSmartRoutesLab(upstreamURL string) *smartRoutesLab {
	dailyLimit := 5.0
	now := time.Now()
	baseGroup := smartSubscriptionGroup
	account := func(id, group int64, apiKey string) service.Account {
		return service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 5, GroupIDs: []int64{group},
			Credentials: map[string]any{"api_key": apiKey, "base_url": upstreamURL}}
	}
	lab := &smartRoutesLab{
		user: service.User{ID: 501, Status: service.StatusActive, Role: service.RoleUser, Balance: 100, Concurrency: 5},
		groups: map[int64]*service.Group{
			smartSubscriptionGroup: {ID: smartSubscriptionGroup, Platform: service.PlatformOpenAI, Status: service.StatusActive,
				Hydrated: true, RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeSubscription, DailyLimitUSD: &dailyLimit},
			smartPaygGroup: {ID: smartPaygGroup, Platform: service.PlatformOpenAI, Status: service.StatusActive,
				Hydrated: true, RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeStandard},
		},
		sub: &service.UserSubscription{ID: 901, UserID: 501, GroupID: smartSubscriptionGroup, Status: service.SubscriptionStatusActive,
			StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(30 * 24 * time.Hour),
			DailyWindowStart: &now, WeeklyWindowStart: &now, MonthlyWindowStart: &now},
		accounts: map[int64][]service.Account{
			smartSubscriptionGroup: {account(smartSubscriptionAcct, smartSubscriptionGroup, "TEST_ONLY_UPSTREAM_SUB")},
			smartPaygGroup:         {account(smartPaygAcct, smartPaygGroup, "TEST_ONLY_UPSTREAM_PAYG")},
		},
		upstreamStatus: map[string]int{},
		upstreamCalls:  map[string]int{},
	}
	lab.key = service.APIKey{ID: 301, UserID: lab.user.ID, Key: smartTestKey, Status: service.StatusActive,
		GroupID: &baseGroup, SmartSwitchVersion: 1,
		SmartSwitch: &domain.KeySmartSwitchPolicy{Enabled: true, GroupIDs: []int64{smartSubscriptionGroup, smartPaygGroup}, PaygConfirmed: true, Version: 1}}
	return lab
}

// snapshot returns hydrated copies so callers can never mutate lab state.
func (l *smartRoutesLab) snapshot() *service.APIKey {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := l.key
	user := l.user
	group := *l.groups[*key.GroupID]
	key.User, key.Group = &user, &group
	if key.SmartSwitch != nil {
		key.SmartSwitch = key.SmartSwitch.Clone()
	}
	return &key
}

func (l *smartRoutesLab) upstream(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	upstreamKey := auth[len("Bearer "):]
	l.mu.Lock()
	l.upstreamCalls[upstreamKey]++
	status := l.upstreamStatus[upstreamKey]
	l.mu.Unlock()
	_, _ = io.Copy(io.Discard, r.Body)
	if status != 0 && status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"type":"server_error","message":"mock failure"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"resp_mock","object":"response","status":"completed","model":"gpt-5.5","output":[],"usage":{"input_tokens":1000,"output_tokens":500,"total_tokens":1500}}`))
}

func (l *smartRoutesLab) resetObservations() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.upstreamCalls = map[string]int{}
	l.upstreamStatus = map[string]int{}
	l.usage, l.billing = nil, nil
}

func (l *smartRoutesLab) calls(upstreamKey string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.upstreamCalls[upstreamKey]
}

func (l *smartRoutesLab) observed() ([]service.UsageLog, []service.UsageBillingCommand) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]service.UsageLog(nil), l.usage...), append([]service.UsageBillingCommand(nil), l.billing...)
}

type smartRoutesKeyRepo struct {
	service.APIKeyRepository
	lab *smartRoutesLab
}

func (r smartRoutesKeyRepo) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	if key != smartTestKey {
		return nil, service.ErrAPIKeyNotFound
	}
	return r.lab.snapshot(), nil
}
func (r smartRoutesKeyRepo) GetByID(context.Context, int64) (*service.APIKey, error) {
	return r.lab.snapshot(), nil
}
func (smartRoutesKeyRepo) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

type smartRoutesUserRepo struct {
	service.UserRepository
	lab *smartRoutesLab
}

func (r smartRoutesUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	user := r.lab.user
	return &user, nil
}

type smartRoutesGroupRepo struct {
	service.GroupRepository
	lab *smartRoutesLab
}

func (r smartRoutesGroupRepo) GetByID(_ context.Context, id int64) (*service.Group, error) {
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	group := r.lab.groups[id]
	if group == nil {
		return nil, service.ErrGroupNotFound
	}
	copied := *group
	return &copied, nil
}

func (r smartRoutesGroupRepo) GetByIDLite(ctx context.Context, id int64) (*service.Group, error) {
	return r.GetByID(ctx, id)
}

type smartRoutesSubRepo struct {
	service.UserSubscriptionRepository
	lab *smartRoutesLab
}

func (r smartRoutesSubRepo) GetActiveByUserIDAndGroupID(_ context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	if r.lab.sub == nil || r.lab.sub.UserID != userID || r.lab.sub.GroupID != groupID {
		return nil, service.ErrSubscriptionNotFound
	}
	copied := *r.lab.sub
	return &copied, nil
}

// Window maintenance writes through to the in-memory row like the database would.
func (r smartRoutesSubRepo) ResetDailyUsage(_ context.Context, _ int64, start time.Time) error {
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	r.lab.sub.DailyUsageUSD, r.lab.sub.DailyWindowStart = 0, &start
	return nil
}
func (r smartRoutesSubRepo) ResetWeeklyUsage(_ context.Context, _ int64, start time.Time) error {
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	r.lab.sub.WeeklyUsageUSD, r.lab.sub.WeeklyWindowStart = 0, &start
	return nil
}
func (r smartRoutesSubRepo) ResetMonthlyUsage(_ context.Context, _ int64, start time.Time) error {
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	r.lab.sub.MonthlyUsageUSD, r.lab.sub.MonthlyWindowStart = 0, &start
	return nil
}

type smartRoutesAccountRepo struct {
	service.AccountRepository
	lab *smartRoutesLab
}

func (r smartRoutesAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	for _, accounts := range r.lab.accounts {
		for _, account := range accounts {
			if account.ID == id {
				copied := account
				return &copied, nil
			}
		}
	}
	return nil, service.ErrAccountNotFound
}
func (r smartRoutesAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, _ string) ([]service.Account, error) {
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	return append([]service.Account(nil), r.lab.accounts[groupID]...), nil
}

type smartRoutesUsageRepo struct {
	service.UsageLogRepository
	lab *smartRoutesLab
}

func (r smartRoutesUsageRepo) CreateBestEffort(_ context.Context, log *service.UsageLog) error {
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	r.lab.usage = append(r.lab.usage, *log)
	return nil
}

type smartRoutesBillingRepo struct{ lab *smartRoutesLab }

func (r smartRoutesBillingRepo) Apply(_ context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	r.lab.mu.Lock()
	defer r.lab.mu.Unlock()
	r.lab.billing = append(r.lab.billing, *cmd)
	return &service.UsageBillingApplyResult{Applied: true}, nil
}

type smartRoutesCache struct {
	service.GatewayCache
}

func (smartRoutesCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, nil
}
func (smartRoutesCache) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}
func (smartRoutesCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}
func (smartRoutesCache) DeleteSessionAccountID(context.Context, int64, string) error { return nil }

type smartRoutesConcurrency struct {
	service.ConcurrencyCache
}

func (smartRoutesConcurrency) AcquireUserSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}
func (smartRoutesConcurrency) ReleaseUserSlot(context.Context, int64, string) error { return nil }
func (smartRoutesConcurrency) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}
func (smartRoutesConcurrency) ReleaseAccountSlot(context.Context, int64, string) error { return nil }
func (smartRoutesConcurrency) GetAccountsLoadBatch(_ context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	result := make(map[int64]*service.AccountLoadInfo, len(accounts))
	for _, account := range accounts {
		result[account.ID] = &service.AccountLoadInfo{AccountID: account.ID}
	}
	return result, nil
}

func newSmartRoutesGateway(t *testing.T, lab *smartRoutesLab) *httptest.Server {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.MaxBodySize = 1 << 20
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Pricing.DataDir, cfg.Pricing.UpdateIntervalHours = t.TempDir(), 24
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Pricing.DataDir, "model_pricing.json"),
		[]byte(`{"gpt-5.5":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`), 0o600))
	pricing := service.NewPricingService(cfg, nil)
	require.NoError(t, pricing.Initialize())
	t.Cleanup(pricing.Stop)

	keys := smartRoutesKeyRepo{lab: lab}
	users := smartRoutesUserRepo{lab: lab}
	subs := smartRoutesSubRepo{lab: lab}
	keyService := service.NewAPIKeyService(keys, users, smartRoutesGroupRepo{lab: lab}, subs, nil, nil, cfg)
	billingCache := service.NewBillingCacheService(nil, users, subs, keys, cfg)
	t.Cleanup(billingCache.Stop)
	subscriptions := service.NewSubscriptionService(smartRoutesGroupRepo{lab: lab}, subs, billingCache, nil, cfg)
	concurrency := service.NewConcurrencyService(smartRoutesConcurrency{})
	svc := service.NewOpenAIGatewayService(smartRoutesAccountRepo{lab: lab}, smartRoutesUsageRepo{lab: lab}, smartRoutesBillingRepo{lab: lab},
		nil, nil, nil, smartRoutesCache{}, cfg, nil, concurrency, service.NewBillingService(cfg, pricing), nil, billingCache,
		repository.NewHTTPUpstream(cfg), &service.DeferredService{}, nil)
	t.Cleanup(svc.CloseOpenAIWSPool)
	gatewayHandler := handler.NewOpenAIGatewayHandler(svc, concurrency, billingCache, keyService, nil, nil, nil, cfg, nil)

	router := gin.New()
	routes.RegisterGatewayRoutes(router, &handler.Handlers{Gateway: &handler.GatewayHandler{}, OpenAIGateway: gatewayHandler},
		middleware.NewAPIKeyAuthMiddleware(keyService, subscriptions, nil, cfg), keyService, nil, nil, nil, cfg)
	gateway := httptest.NewServer(router)
	t.Cleanup(gateway.Close)
	return gateway
}

func smartRoutesPost(t *testing.T, gateway *httptest.Server, body string) (int, string) {
	t.Helper()
	return smartRoutesPostPath(t, gateway, "/v1/responses", body)
}

func smartRoutesPostPath(t *testing.T, gateway *httptest.Server, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, gateway.URL+path, bytes.NewReader([]byte(body)))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+smartTestKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(data)
}

// waitObserved waits for asynchronous usage recording after the response.
func waitObserved(t *testing.T, lab *smartRoutesLab, n int) ([]service.UsageLog, []service.UsageBillingCommand) {
	t.Helper()
	var usage []service.UsageLog
	var billing []service.UsageBillingCommand
	require.Eventually(t, func() bool {
		usage, billing = lab.observed()
		return len(usage) >= n && len(billing) >= n
	}, 3*time.Second, 10*time.Millisecond)
	return usage, billing
}

func TestSmartSwitchResponsesRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(nil)
	t.Cleanup(upstream.Close)
	lab := newSmartRoutesLab(upstream.URL)
	upstream.Config.Handler = http.HandlerFunc(lab.upstream)
	gateway := newSmartRoutesGateway(t, lab)
	const body = `{"model":"gpt-5.5","input":"hi","stream":false}`

	setDailyUsage := func(usage float64, windowStart time.Time) {
		lab.mu.Lock()
		defer lab.mu.Unlock()
		lab.sub.DailyUsageUSD = usage
		lab.sub.DailyWindowStart = &windowStart
	}

	t.Run("subscription_group_serves_first", func(t *testing.T) {
		lab.resetObservations()
		setDailyUsage(0, time.Now())
		status, resp := smartRoutesPost(t, gateway, body)
		require.Equal(t, http.StatusOK, status, resp)
		require.Equal(t, 1, lab.calls("TEST_ONLY_UPSTREAM_SUB"))
		require.Zero(t, lab.calls("TEST_ONLY_UPSTREAM_PAYG"))
		usage, billing := waitObserved(t, lab, 1)
		require.Equal(t, smartSubscriptionGroup, *usage[0].GroupID)
		require.Equal(t, lab.sub.ID, *usage[0].SubscriptionID)
		require.Equal(t, service.BillingTypeSubscription, usage[0].BillingType)
		require.Equal(t, &domain.KeySmartSwitchTrace{OriginalGroupID: smartSubscriptionGroup, PolicyVersion: 1}, usage[0].SmartSwitch)
		require.Zero(t, billing[0].SmartSwitchPaygCost)
	})

	t.Run("exhausted_subscription_falls_back_to_payg", func(t *testing.T) {
		lab.resetObservations()
		setDailyUsage(5, time.Now())
		status, resp := smartRoutesPost(t, gateway, body)
		require.Equal(t, http.StatusOK, status, resp)
		require.Zero(t, lab.calls("TEST_ONLY_UPSTREAM_SUB"))
		require.Equal(t, 1, lab.calls("TEST_ONLY_UPSTREAM_PAYG"))
		usage, billing := waitObserved(t, lab, 1)
		require.Equal(t, smartPaygGroup, *usage[0].GroupID)
		require.Nil(t, usage[0].SubscriptionID)
		require.Equal(t, service.BillingTypeBalance, usage[0].BillingType)
		require.Equal(t, "subscription_limit_or_expiry", usage[0].SmartSwitch.Reason)
		require.Greater(t, billing[0].BalanceCost, 0.0)
		require.Equal(t, billing[0].BalanceCost, billing[0].SmartSwitchPaygCost)
	})

	t.Run("recovered_window_returns_to_subscription", func(t *testing.T) {
		lab.resetObservations()
		setDailyUsage(5, time.Now().Add(-25*time.Hour))
		status, resp := smartRoutesPost(t, gateway, body)
		require.Equal(t, http.StatusOK, status, resp)
		require.Equal(t, 1, lab.calls("TEST_ONLY_UPSTREAM_SUB"))
		require.Zero(t, lab.calls("TEST_ONLY_UPSTREAM_PAYG"))
		usage, _ := waitObserved(t, lab, 1)
		require.Equal(t, smartSubscriptionGroup, *usage[0].GroupID)
	})

	t.Run("exhausted_payg_budget_rejects_without_upstream", func(t *testing.T) {
		lab.resetObservations()
		setDailyUsage(5, time.Now())
		budget := 1.0
		lab.mu.Lock()
		lab.key.SmartSwitch.PaygBudgetUSD = &budget
		lab.key.SmartSwitchPaygUsed = budget
		lab.mu.Unlock()
		t.Cleanup(func() {
			lab.mu.Lock()
			lab.key.SmartSwitch.PaygBudgetUSD, lab.key.SmartSwitchPaygUsed = nil, 0
			lab.mu.Unlock()
		})
		status, resp := smartRoutesPost(t, gateway, body)
		require.Equal(t, http.StatusServiceUnavailable, status, resp)
		require.Contains(t, resp, "SMART_SWITCH_UNAVAILABLE")
		require.Zero(t, lab.calls("TEST_ONLY_UPSTREAM_SUB")+lab.calls("TEST_ONLY_UPSTREAM_PAYG"))
		usage, billing := lab.observed()
		require.Empty(t, usage)
		require.Empty(t, billing)
	})

	t.Run("group_without_accounts_advances_before_upstream", func(t *testing.T) {
		lab.resetObservations()
		setDailyUsage(0, time.Now())
		lab.mu.Lock()
		saved := lab.accounts[smartSubscriptionGroup]
		lab.accounts[smartSubscriptionGroup] = nil
		lab.mu.Unlock()
		t.Cleanup(func() { lab.mu.Lock(); lab.accounts[smartSubscriptionGroup] = saved; lab.mu.Unlock() })
		status, resp := smartRoutesPost(t, gateway, body)
		require.Equal(t, http.StatusOK, status, resp)
		require.Equal(t, 1, lab.calls("TEST_ONLY_UPSTREAM_PAYG"))
		usage, _ := waitObserved(t, lab, 1)
		require.Equal(t, smartPaygGroup, *usage[0].GroupID)
		require.Equal(t, "group_unavailable", usage[0].SmartSwitch.Reason)
	})

	t.Run("failure_after_upstream_send_does_not_switch_groups", func(t *testing.T) {
		lab.resetObservations()
		setDailyUsage(0, time.Now())
		lab.mu.Lock()
		lab.upstreamStatus["TEST_ONLY_UPSTREAM_SUB"] = http.StatusInternalServerError
		lab.mu.Unlock()
		status, resp := smartRoutesPost(t, gateway, body)
		require.GreaterOrEqual(t, status, 500, resp)
		require.GreaterOrEqual(t, lab.calls("TEST_ONLY_UPSTREAM_SUB"), 1)
		require.Zero(t, lab.calls("TEST_ONLY_UPSTREAM_PAYG"))
	})

	t.Run("disabled_policy_keeps_single_group_behavior", func(t *testing.T) {
		lab.resetObservations()
		setDailyUsage(5, time.Now())
		lab.mu.Lock()
		lab.key.SmartSwitch.Enabled = false
		lab.mu.Unlock()
		t.Cleanup(func() { lab.mu.Lock(); lab.key.SmartSwitch.Enabled = true; lab.mu.Unlock() })
		status, resp := smartRoutesPost(t, gateway, body)
		// Same rejection as main: the bound subscription's billing error.
		require.Equal(t, http.StatusForbidden, status, resp)
		require.Contains(t, resp, "daily usage limit exceeded")
		require.Zero(t, lab.calls("TEST_ONLY_UPSTREAM_SUB")+lab.calls("TEST_ONLY_UPSTREAM_PAYG"))
	})

	// Compact shares the Responses handler and billing; it must follow the
	// policy instead of the Key's bound group.
	t.Run("responses_subpath_follows_policy", func(t *testing.T) {
		lab.resetObservations()
		setDailyUsage(5, time.Now())
		status, resp := smartRoutesPostPath(t, gateway, "/v1/responses/compact", body)
		require.Equal(t, http.StatusOK, status, resp)
		require.Zero(t, lab.calls("TEST_ONLY_UPSTREAM_SUB"))
		require.Equal(t, 1, lab.calls("TEST_ONLY_UPSTREAM_PAYG"))
		status, resp = smartRoutesPostPath(t, gateway, "/v1/responses/compact", `{"model":"gpt-5.5","input":"hi","previous_response_id":"resp_prev"}`)
		require.Equal(t, http.StatusBadRequest, status, resp)
		require.Contains(t, resp, "SMART_SWITCH_CONTINUATION_UNSUPPORTED")
		require.Equal(t, 1, lab.calls("TEST_ONLY_UPSTREAM_PAYG"))
	})

	t.Run("stateful_continuation_is_rejected_before_routing", func(t *testing.T) {
		lab.resetObservations()
		setDailyUsage(0, time.Now())
		status, resp := smartRoutesPost(t, gateway, `{"model":"gpt-5.5","input":"hi","previous_response_id":"resp_prev"}`)
		require.Equal(t, http.StatusBadRequest, status, resp)
		require.Contains(t, resp, "SMART_SWITCH_CONTINUATION_UNSUPPORTED")
		require.Zero(t, lab.calls("TEST_ONLY_UPSTREAM_SUB")+lab.calls("TEST_ONLY_UPSTREAM_PAYG"))
	})
}
