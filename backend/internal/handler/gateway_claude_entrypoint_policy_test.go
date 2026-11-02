//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Handler-level proof for the Claude entrypoint allowlist on POST
// /v1/messages: enforce rejects before any upstream call, observe passes and
// records, off is untouched, and smart-switch / claude_code_only fallback
// target groups are checked with their own policy.
const (
	entrypointModel     = "claude-sonnet-4-5"
	entrypointT3CodeUA  = "claude-cli/2.1.316 (external, t3-code, agent-sdk/0.3.276)"
	entrypointGroupA    = int64(51)
	entrypointGroupB    = int64(52)
	entrypointAccountID = int64(5201)
)

func entrypointBody(entrypoint string) string {
	return `{"model":"` + entrypointModel + `","max_tokens":16,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.316.abc; cc_entrypoint=` +
		entrypoint + `; cch=12345;"}],"messages":[{"role":"user","content":"hi"}]}`
}

type entrypointLab struct {
	mu             sync.Mutex
	calls          int
	accounts       []service.Account
	groups         map[int64]*service.Group
	groupReads     map[int64]int
	failGroupReads map[int64]int
}

func (l *entrypointLab) upstream(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	l.calls++
	l.mu.Unlock()
	_, _ = io.Copy(io.Discard, r.Body)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"` + entrypointModel + `","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":1}}`))
}

func (l *entrypointLab) upstreamCalls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

type entrypointAccountRepo struct {
	service.AccountRepository
	lab *entrypointLab
}

func (r entrypointAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for _, account := range r.lab.accounts {
		if account.ID == id {
			copied := account
			return &copied, nil
		}
	}
	return nil, service.ErrAccountNotFound
}

func (r entrypointAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, _ string) ([]service.Account, error) {
	var out []service.Account
	for _, account := range r.lab.accounts {
		for _, id := range account.GroupIDs {
			if id == groupID {
				out = append(out, account)
			}
		}
	}
	return out, nil
}

func (r entrypointAccountRepo) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, _ []string) ([]service.Account, error) {
	return r.ListSchedulableByGroupIDAndPlatform(ctx, groupID, "")
}

func (r entrypointAccountRepo) ListByGroup(ctx context.Context, groupID int64) ([]service.Account, error) {
	return r.ListSchedulableByGroupIDAndPlatform(ctx, groupID, "")
}

func (entrypointAccountRepo) SetError(context.Context, int64, string) error { return nil }

type entrypointGroupRepo struct {
	service.GroupRepository
	lab *entrypointLab
}

func (r entrypointGroupRepo) GetByID(_ context.Context, id int64) (*service.Group, error) {
	r.lab.mu.Lock()
	r.lab.groupReads[id]++
	failing := r.lab.failGroupReads[id] > 0
	if failing {
		r.lab.failGroupReads[id]--
	}
	r.lab.mu.Unlock()
	if failing {
		return nil, errors.New("TEST_ONLY transient group read failure")
	}
	group, ok := r.lab.groups[id]
	if !ok {
		return nil, service.ErrGroupNotFound
	}
	copied := *group
	return &copied, nil
}

func (r entrypointGroupRepo) GetByIDLite(ctx context.Context, id int64) (*service.Group, error) {
	return r.GetByID(ctx, id)
}

// entrypointHTTPUpstream forwards to the local httptest upstream.
type entrypointHTTPUpstream struct{}

func (entrypointHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

func (entrypointHTTPUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ bool) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

type entrypointObservationRepo struct {
	mu   sync.Mutex
	rows []service.ClaudeEntrypointObservation
}

func (r *entrypointObservationRepo) UpsertClaudeEntrypointObservations(_ context.Context, rows []service.ClaudeEntrypointObservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, rows...)
	return nil
}

func (r *entrypointObservationRepo) DeleteClaudeEntrypointObservationsBefore(context.Context, string) (int64, error) {
	return 0, nil
}

func (r *entrypointObservationRepo) ListClaudeEntrypointObservations(context.Context, int64, string, int) ([]service.ClaudeEntrypointObservationSummary, error) {
	return nil, nil
}

type entrypointHarness struct {
	lab       *entrypointLab
	handler   *GatewayHandler
	observer  *service.ClaudeEntrypointObservationService
	obsRepo   *entrypointObservationRepo
	user      *service.User
	billing   *service.BillingCacheService
	upstreamU string
}

func anthropicGroup(id int64, policy string) *service.Group {
	return &service.Group{ID: id, Platform: service.PlatformAnthropic, Status: service.StatusActive, Hydrated: true,
		RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeStandard, ClaudeEntrypointPolicy: policy,
		ClaudeEntrypointAllowlist: service.DefaultClaudeEntrypointAllowlist()}
}

// newEntrypointHarness serves accounts only in accountGroup.
func newEntrypointHarness(t *testing.T, groups map[int64]*service.Group, accountGroup int64) *entrypointHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	lab := &entrypointLab{groups: groups, groupReads: map[int64]int{}, failGroupReads: map[int64]int{}}
	upstream := httptest.NewServer(http.HandlerFunc(lab.upstream))
	t.Cleanup(upstream.Close)
	lab.accounts = []service.Account{{ID: entrypointAccountID, Name: "TEST_ONLY_UPSTREAM", Platform: service.PlatformAnthropic,
		Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 5, Priority: 1,
		GroupIDs: []int64{accountGroup}, Credentials: map[string]any{"api_key": "TEST_ONLY_UPSTREAM", "base_url": upstream.URL}}}

	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.MaxBodySize = 1 << 20
	cfg.Gateway.MaxAccountSwitches = 3
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Pricing.DataDir, cfg.Pricing.UpdateIntervalHours = t.TempDir(), 24
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Pricing.DataDir, "model_pricing.json"),
		[]byte(`{"`+entrypointModel+`":{"input_cost_per_token":0.000003,"output_cost_per_token":0.000015}}`), 0o600))
	pricing := service.NewPricingService(cfg, nil)
	require.NoError(t, pricing.Initialize())
	t.Cleanup(pricing.Stop)

	user := &service.User{ID: 711, Status: service.StatusActive, Role: service.RoleUser, Balance: 100, Concurrency: 5}
	users := stopReasonUserRepo{user: user}
	accounts := entrypointAccountRepo{lab: lab}
	usageRepo := stopReasonUsageRepo{lab: &stopReasonLab{}}
	concurrency := service.NewConcurrencyService(stopReasonConcurrency{})
	billingCache := service.NewBillingCacheService(nil, users, nil, nil, cfg)
	t.Cleanup(billingCache.Stop)
	rateLimit := service.NewRateLimitService(accounts, usageRepo, cfg, nil, nil)
	gateway := service.NewGatewayService(accounts, entrypointGroupRepo{lab: lab}, usageRepo, stopReasonBillingRepo{}, users, nil, nil, stopReasonGatewayCache{}, cfg,
		nil, concurrency, service.NewBillingService(cfg, pricing), rateLimit, billingCache, nil, entrypointHTTPUpstream{},
		&service.DeferredService{}, nil, nil, nil, nil, nil)
	obsRepo := &entrypointObservationRepo{}
	observer := service.NewClaudeEntrypointObservationService(obsRepo)
	h := NewGatewayHandler(gateway, nil, nil, nil, concurrency, billingCache, nil, nil, nil, nil, nil, observer, nil, nil, nil, cfg, nil)
	return &entrypointHarness{lab: lab, handler: h, observer: observer, obsRepo: obsRepo, user: user, billing: billingCache, upstreamU: upstream.URL}
}

func (h *entrypointHarness) key(groupID int64) *service.APIKey {
	id := groupID
	return &service.APIKey{ID: 900 + groupID, UserID: h.user.ID, Key: "TEST_ONLY_KEY", Status: service.StatusActive,
		GroupID: &id, User: h.user, Group: h.lab.groups[groupID]}
}

func (h *entrypointHarness) serve(t *testing.T, apiKey *service.APIKey, ua, body string, setup func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ua)
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: h.user.ID, Concurrency: h.user.Concurrency})
	if setup != nil {
		setup(c)
	}
	h.handler.Messages(c)
	return rec
}

func (h *entrypointHarness) observations() []service.ClaudeEntrypointObservation {
	h.observer.Flush(context.Background())
	h.obsRepo.mu.Lock()
	defer h.obsRepo.mu.Unlock()
	return append([]service.ClaudeEntrypointObservation(nil), h.obsRepo.rows...)
}

func requireEntrypointRejection(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	var payload struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "error", payload.Type)
	require.Equal(t, "invalid_request_error", payload.Error.Type)
	require.Equal(t, "claude_entrypoint_not_allowed", payload.Error.Code)
	require.Equal(t, "This group does not allow this Claude client entrypoint (UA: t3-code, body: t3-code). Allowed: cli, claude-vscode, sdk-cli.", payload.Error.Message)
}

func TestMessagesClaudeEntrypointEnforceRejectsBeforeUpstream(t *testing.T) {
	h := newEntrypointHarness(t, map[int64]*service.Group{entrypointGroupA: anthropicGroup(entrypointGroupA, service.ClaudeEntrypointPolicyEnforce)}, entrypointGroupA)
	rec := h.serve(t, h.key(entrypointGroupA), entrypointT3CodeUA, entrypointBody("t3-code"), nil)
	requireEntrypointRejection(t, rec)
	require.Zero(t, h.lab.upstreamCalls())

	rows := h.observations()
	require.Len(t, rows, 1)
	require.Equal(t, entrypointGroupA, rows[0].GroupID)
	require.Equal(t, service.ClaudeEntrypointResultRejected, rows[0].Result)
	require.Equal(t, service.ClaudeEntrypointReasonUANotAllowed, rows[0].Reason)
	require.Equal(t, "t3-code", rows[0].UAEntrypoint)
	require.Equal(t, "t3-code", rows[0].BodyEntrypoint)
	require.Equal(t, "2.1.316", rows[0].CLIVersion)
	require.Equal(t, entrypointT3CodeUA, rows[0].SampleUserAgent)

	// A compliant client is forwarded and recorded as allowed.
	rec = h.serve(t, h.key(entrypointGroupA), "claude-cli/2.1.316 (external, cli)", entrypointBody("cli"), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, h.lab.upstreamCalls())
	rows = h.observations()
	require.Len(t, rows, 2)
	require.Equal(t, service.ClaudeEntrypointResultAllowed, rows[1].Result)
}

func TestMessagesClaudeEntrypointObservePassesAndRecords(t *testing.T) {
	h := newEntrypointHarness(t, map[int64]*service.Group{entrypointGroupA: anthropicGroup(entrypointGroupA, service.ClaudeEntrypointPolicyObserve)}, entrypointGroupA)
	rec := h.serve(t, h.key(entrypointGroupA), entrypointT3CodeUA, entrypointBody("t3-code"), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, h.lab.upstreamCalls())
	rows := h.observations()
	require.Len(t, rows, 1)
	require.Equal(t, service.ClaudeEntrypointResultViolationObserved, rows[0].Result)
	require.Equal(t, service.ClaudeEntrypointPolicyObserve, rows[0].Policy)
	require.Equal(t, int64(1), rows[0].Requests)
}

func TestMessagesClaudeEntrypointOffUntouched(t *testing.T) {
	h := newEntrypointHarness(t, map[int64]*service.Group{entrypointGroupA: anthropicGroup(entrypointGroupA, service.ClaudeEntrypointPolicyOff)}, entrypointGroupA)
	rec := h.serve(t, h.key(entrypointGroupA), entrypointT3CodeUA, entrypointBody("t3-code"), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, h.lab.upstreamCalls())
	require.Empty(t, h.observations())
}

func TestMessagesClaudeEntrypointSmartSwitchTargetEnforced(t *testing.T) {
	groups := map[int64]*service.Group{
		entrypointGroupA: anthropicGroup(entrypointGroupA, service.ClaudeEntrypointPolicyOff),
		entrypointGroupB: anthropicGroup(entrypointGroupB, service.ClaudeEntrypointPolicyEnforce),
	}
	// A has no accounts, so selection fails and smart switch moves to B.
	h := newEntrypointHarness(t, groups, entrypointGroupB)
	switched := 0
	rec := h.serve(t, h.key(entrypointGroupA), entrypointT3CodeUA, entrypointBody("t3-code"), func(c *gin.Context) {
		c.Set("smart_switch_next", middleware2.SmartSwitchNext(func(excluded map[int64]bool) (*service.SmartSwitchSelection, error) {
			switched++
			require.True(t, excluded[entrypointGroupA])
			return &service.SmartSwitchSelection{Key: h.key(entrypointGroupB)}, nil
		}))
	})
	require.Equal(t, 1, switched)
	requireEntrypointRejection(t, rec)
	require.Zero(t, h.lab.upstreamCalls())
	rows := h.observations()
	require.Len(t, rows, 1, "group A is off: only B is recorded")
	require.Equal(t, entrypointGroupB, rows[0].GroupID)
	require.Equal(t, service.ClaudeEntrypointResultRejected, rows[0].Result)
}

func TestMessagesClaudeEntrypointSmartSwitchTargetObserveForwards(t *testing.T) {
	groups := map[int64]*service.Group{
		entrypointGroupA: anthropicGroup(entrypointGroupA, service.ClaudeEntrypointPolicyObserve),
		entrypointGroupB: anthropicGroup(entrypointGroupB, service.ClaudeEntrypointPolicyObserve),
	}
	h := newEntrypointHarness(t, groups, entrypointGroupB)
	rec := h.serve(t, h.key(entrypointGroupA), entrypointT3CodeUA, entrypointBody("t3-code"), func(c *gin.Context) {
		c.Set("smart_switch_next", middleware2.SmartSwitchNext(func(map[int64]bool) (*service.SmartSwitchSelection, error) {
			return &service.SmartSwitchSelection{Key: h.key(entrypointGroupB)}, nil
		}))
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, h.lab.upstreamCalls())
	rows := h.observations()
	require.Len(t, rows, 2, "one observation per (request, group)")
	seen := map[int64]bool{}
	for _, row := range rows {
		seen[row.GroupID] = true
		require.Equal(t, int64(1), row.Requests)
	}
	require.True(t, seen[entrypointGroupA] && seen[entrypointGroupB])
}

func TestMessagesClaudeEntrypointClaudeCodeOnlyFallbackEnforced(t *testing.T) {
	fallback := entrypointGroupB
	groupA := anthropicGroup(entrypointGroupA, service.ClaudeEntrypointPolicyOff)
	groupA.ClaudeCodeOnly = true
	groupA.FallbackGroupID = &fallback
	groups := map[int64]*service.Group{
		entrypointGroupA: groupA,
		entrypointGroupB: anthropicGroup(entrypointGroupB, service.ClaudeEntrypointPolicyEnforce),
	}
	// The request is not recognized as Claude Code, so scheduling resolves
	// A → B; B's enforce policy must reject before any upstream call.
	h := newEntrypointHarness(t, groups, entrypointGroupB)
	rec := h.serve(t, h.key(entrypointGroupA), entrypointT3CodeUA, entrypointBody("t3-code"), nil)
	requireEntrypointRejection(t, rec)
	require.Zero(t, h.lab.upstreamCalls())
	rows := h.observations()
	require.Len(t, rows, 1)
	require.Equal(t, entrypointGroupB, rows[0].GroupID)
}

func TestWriteClaudeEntrypointRejectionAfterStreamStart(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	writeClaudeEntrypointRejection(c, "denied", true)
	require.Equal(t, `data: {"error":{"code":"claude_entrypoint_not_allowed","message":"denied","type":"invalid_request_error"},"type":"error"}`+"\n\n", rec.Body.String())
}

func TestClaudeEntrypointPolicyStateParsesOnce(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", entrypointT3CodeUA)
	body := []byte(entrypointBody("t3-code"))
	state := newClaudeEntrypointPolicyState(c, body, 1, 2)
	require.False(t, state.parsed, "no parsing until a group with an active policy is checked")
	h := &GatewayHandler{}
	require.True(t, h.applyClaudeEntrypointPolicy(c, state, anthropicGroup(1, service.ClaudeEntrypointPolicyOff), false))
	require.False(t, state.parsed, "off skips parsing")
	require.True(t, h.applyClaudeEntrypointPolicy(c, state, anthropicGroup(1, service.ClaudeEntrypointPolicyObserve), false))
	require.True(t, state.parsed)
	require.Equal(t, claudebilling.EntrypointBodyValid, state.facts.BodyStatus)
}

func TestMessagesClaudeEntrypointFallbackResolutionErrorFailsClosed(t *testing.T) {
	fallback := entrypointGroupB
	groupA := anthropicGroup(entrypointGroupA, service.ClaudeEntrypointPolicyOff)
	groupA.ClaudeCodeOnly = true
	groupA.FallbackGroupID = &fallback
	groups := map[int64]*service.Group{
		entrypointGroupA: groupA,
		entrypointGroupB: anthropicGroup(entrypointGroupB, service.ClaudeEntrypointPolicyEnforce),
	}
	h := newEntrypointHarness(t, groups, entrypointGroupB)
	// The first read of B fails transiently; later reads (account selection)
	// would succeed. The check must stop the request instead of letting
	// selection reach the unchecked enforce group B.
	h.lab.failGroupReads[entrypointGroupB] = 1
	rec := h.serve(t, h.key(entrypointGroupA), entrypointT3CodeUA, entrypointBody("t3-code"), nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "No available accounts")
	require.Zero(t, h.lab.upstreamCalls())
	h.lab.mu.Lock()
	require.Equal(t, 1, h.lab.groupReads[entrypointGroupB], "stopped at the failed resolution")
	h.lab.mu.Unlock()
	require.Empty(t, h.observations())
}
