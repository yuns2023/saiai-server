//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type adminMarketRepo struct {
	service.MarketListingRepository
	listing *service.MarketListing
	cfg     *service.MarketAdminConfigureParams
}

func (r *adminMarketRepo) account() *service.Account {
	owner := int64(7)
	return &service.Account{ID: 11, Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken, Concurrency: 3,
		Status: service.StatusActive, Schedulable: true, OwnerUserID: &owner, Extra: map[string]any{}}
}

func (r *adminMarketRepo) GetAccountFacts(_ context.Context, id int64) (*service.Account, error) {
	if id != 11 {
		return nil, service.ErrAccountNotFound
	}
	return r.account(), nil
}

func (r *adminMarketRepo) ListAdminMemberGroupIDs(context.Context, int64, []int64) ([]int64, error) {
	return nil, nil
}

func (r *adminMarketRepo) GetListing(context.Context, int64) (*service.MarketListing, error) {
	return r.listing, nil
}

func (r *adminMarketRepo) ListPlanTierOptions(_ context.Context, plan, _ string, _ *int64) ([]service.MarketTierOption, error) {
	if plan == "" {
		return []service.MarketTierOption{}, nil
	}
	return []service.MarketTierOption{{GroupID: 3, Name: "T2-Max", RateMultiplier: 0.3, Plan: &plan, Available: true}}, nil
}

func (r *adminMarketRepo) AdminConfigure(_ context.Context, p service.MarketAdminConfigureParams) (*service.MarketListing, error) {
	r.cfg = &p
	if err := p.Validate(r.account()); err != nil {
		return nil, err
	}
	if p.Plan == "unknown" {
		return nil, service.ErrMarketPlanUnknown
	}
	plan := p.Plan
	r.listing = &service.MarketListing{AccountID: 11, OwnerUserID: 7, Plan: &plan, Status: service.MarketListingStatusUnlisted, Version: 1}
	return r.listing, nil
}

func (r *adminMarketRepo) AdminRevoke(context.Context, int64, int64) (*service.MarketListing, error) {
	if r.listing == nil {
		return nil, service.ErrMarketListingNotFound
	}
	r.listing.Status = service.MarketListingStatusRevoked
	r.listing.Plan = nil
	return r.listing, nil
}

type adminMarketSettings struct {
	service.SettingRepository
}

func (adminMarketSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func newAdminMarketRouter(repo *adminMarketRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewMarketListingHandler(service.NewMarketListingService(repo, adminMarketSettings{}))
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Next()
	})
	r.GET("/admin/accounts/:id/market-listing", h.Get)
	r.PUT("/admin/accounts/:id/market-listing", h.Put)
	r.DELETE("/admin/accounts/:id/market-listing", h.Delete)
	return r
}

func marketJSONKeys(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func marketData(t *testing.T, w *httptest.ResponseRecorder) json.RawMessage {
	t.Helper()
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body.Data
}

func TestAdminMarketListingHandlerJSON(t *testing.T) {
	repo := &adminMarketRepo{}
	r := newAdminMarketRouter(repo)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/accounts/11/market-listing", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	viewKeys := []string{"account_id", "allowed_tiers", "ineligible_reason", "listing", "market_enabled", "owner_user_id"}
	require.Equal(t, viewKeys, marketJSONKeys(t, marketData(t, w)))
	require.Contains(t, w.Body.String(), `"listing":null`)

	w = httptest.NewRecorder()
	// Legacy fields an old tab may still send (allowed tiers, reservations,
	// retired caps) are ignored.
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/admin/accounts/11/market-listing",
		bytes.NewBufferString(`{"plan":"max","allowed_group_ids":[9],"reserved_concurrency":2,"cap_5h_pct":50}`)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "max", repo.cfg.Plan)
	data := marketData(t, w)
	require.Equal(t, viewKeys, marketJSONKeys(t, data))
	var view struct {
		Listing      json.RawMessage   `json:"listing"`
		AllowedTiers []json.RawMessage `json:"allowed_tiers"`
	}
	require.NoError(t, json.Unmarshal(data, &view))
	require.Equal(t, []string{"account_id", "allowed_group_ids", "created_at", "listed_at", "owner_user_id", "plan",
		"selected_group_id", "status", "updated_at", "version"}, marketJSONKeys(t, view.Listing))
	require.Contains(t, string(view.Listing), `"plan":"max"`)
	require.Contains(t, string(view.Listing), `"allowed_group_ids":[3]`, "derived from the plan")
	require.Len(t, view.AllowedTiers, 1)
	require.NotContains(t, w.Body.String(), "protection")

	for _, raw := range []string{`{}`, `{"plan":""}`, `{"plan":null}`, `{"allowed_group_ids":[3]}`, `nope`} {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/admin/accounts/11/market-listing", bytes.NewBufferString(raw)))
		require.Equal(t, http.StatusBadRequest, w.Code, raw)
	}
	for raw, reason := range map[string]string{`{"plan":"Max"}`: "MARKET_PLAN_INVALID", `{"plan":"  "}`: "MARKET_PLAN_INVALID",
		`{"plan":" max "}`: "MARKET_PLAN_INVALID", `{"plan":"unknown"}`: "MARKET_PLAN_UNKNOWN"} {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/admin/accounts/11/market-listing", bytes.NewBufferString(raw)))
		require.Equal(t, http.StatusBadRequest, w.Code, raw)
		require.Contains(t, w.Body.String(), reason, raw)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/admin/accounts/11/market-listing", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"status":"revoked"`)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/accounts/99/market-listing", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestResolveMarketSettings(t *testing.T) {
	prevMarket := service.ParseMarketSettings(nil)
	prev := &service.SystemSettings{Market: &prevMarket}
	f := func(v float64) *float64 { return &v }
	i := func(v int) *int { return &v }

	for _, tc := range []struct {
		rate float64
		ok   bool
	}{{0, true}, {1, true}, {0.1234, true}, {1.00001, false}, {-0.2, false}, {0.12345, false}, {math.Inf(1), false}} {
		got, msg := resolveMarketSettings(&marketSettingsRequest{MarketPlatformCutRate: f(tc.rate)}, prev)
		if tc.ok {
			require.Empty(t, msg, "cut %v", tc.rate)
			require.True(t, got.PlatformCutRate.Equal(decimal.NewFromFloat(tc.rate)), "explicit %v kept", tc.rate)
		} else {
			require.NotEmpty(t, msg, "cut %v", tc.rate)
		}
	}
	got, msg := resolveMarketSettings(&marketSettingsRequest{MarketEarningFreezeDays: i(0), MarketModeSwitchCooldownHours: i(0)}, prev)
	require.Empty(t, msg)
	require.Equal(t, 0, got.EarningFreezeDays)
	require.Equal(t, 0, got.ModeSwitchCooldownHours, "explicit 0 = no cooldown")
	require.True(t, got.PlatformCutRate.Equal(decimal.RequireFromString("0.2")), "omitted keeps stored")
	got, msg = resolveMarketSettings(&marketSettingsRequest{MarketModeSwitchCooldownHours: i(168)}, prev)
	require.Empty(t, msg)
	require.Equal(t, 168, got.ModeSwitchCooldownHours)
	for _, req := range []marketSettingsRequest{
		{MarketEarningFreezeDays: i(91)}, {MarketMinConvertUSD: f(0)}, {MarketMinConvertUSD: f(1.005)},
		{MarketModeSwitchCooldownHours: i(169)}, {MarketModeSwitchCooldownHours: i(-1)},
	} {
		_, msg := resolveMarketSettings(&req, prev)
		require.NotEmpty(t, msg, "%+v", req)
	}

	require.False(t, (&marketSettingsRequest{}).present(), "unrelated saves do not write the market block")
	require.True(t, (&marketSettingsRequest{MarketModeSwitchCooldownHours: i(2)}).present())
	require.Equal(t, []string{service.SettingKeyMarketModeSwitchCooldownHours}, (&marketSettingsRequest{MarketModeSwitchCooldownHours: i(2)}).providedKeys())
	cool := prevMarket
	cool.ModeSwitchCooldownHours = 6
	require.Equal(t, []string{"market_mode_switch_cooldown_hours"}, diffMarketSettings(&service.SystemSettings{Market: &prevMarket}, &service.SystemSettings{Market: &cool}))
	// Audit: an unrelated save (no market block written) reports no market change.
	enabled := service.ParseMarketSettings(map[string]string{service.SettingKeyMarketEnabled: "true"})
	require.Empty(t, diffMarketSettings(&service.SystemSettings{Market: &enabled}, &service.SystemSettings{}))
	off := enabled
	off.Enabled = false
	require.Equal(t, []string{"market_enabled"}, diffMarketSettings(&service.SystemSettings{Market: &enabled}, &service.SystemSettings{Market: &off}))

	// The embedded request block decodes from the flat settings body.
	var req UpdateSettingsRequest
	require.NoError(t, json.Unmarshal([]byte(`{"market_enabled":true,"market_platform_cut_rate":0,"market_earning_freeze_days":3,
		"market_mode_switch_cooldown_hours":12,"market_default_reserved_concurrency":2}`), &req))
	require.Equal(t, 12, *req.MarketModeSwitchCooldownHours)
	require.NotContains(t, req.providedKeys(), "market_default_reserved_concurrency", "removed keys are ignored")
	require.NotNil(t, req.MarketEnabled)
	require.True(t, *req.MarketEnabled)
	require.NotNil(t, req.MarketPlatformCutRate)
	require.Zero(t, *req.MarketPlatformCutRate)
	require.Equal(t, 3, *req.MarketEarningFreezeDays)
}
