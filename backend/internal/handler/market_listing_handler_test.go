//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type ownerMarketRepo struct {
	service.MarketListingRepository
	owner     int64
	listing   *service.MarketListing
	sets      int
	lastSet   service.MarketOwnerSetParams
	setErr    error
	next      *time.Time
	sessionAt *time.Time
}

func (r *ownerMarketRepo) account() *service.Account {
	owner := r.owner
	a := &service.Account{ID: 11, Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken, Concurrency: 3,
		Status: service.StatusActive, Schedulable: true, OwnerUserID: &owner, Extra: map[string]any{}}
	if r.sessionAt != nil {
		a.SessionWindowEnd = r.sessionAt
		a.Extra["session_window_utilization"] = 0.5
	}
	return a
}

func (r *ownerMarketRepo) GetOwnedAccountFacts(_ context.Context, owner, accountID int64) (*service.Account, error) {
	if owner != r.owner || accountID != 11 {
		return nil, service.ErrHostedOwnedAccountNotFound
	}
	return r.account(), nil
}

func (r *ownerMarketRepo) ListAdminMemberGroupIDs(context.Context, int64, []int64) ([]int64, error) {
	return nil, nil
}

func (r *ownerMarketRepo) GetListing(context.Context, int64) (*service.MarketListing, error) {
	if r.listing == nil {
		return nil, nil
	}
	l := *r.listing
	return &l, nil
}

func (r *ownerMarketRepo) ListPlanTierOptions(context.Context, string, string, *int64) ([]service.MarketTierOption, error) {
	plan := "max"
	return []service.MarketTierOption{{GroupID: 3, Name: "T2-Max", RateMultiplier: 0.3, Plan: &plan, Available: true}}, nil
}

func (r *ownerMarketRepo) NextOwnerSwitchAt(context.Context, int64, int) (*time.Time, error) {
	return r.next, nil
}

func (r *ownerMarketRepo) OwnerSet(ctx context.Context, p service.MarketOwnerSetParams) (*service.MarketListing, error) {
	if _, err := r.GetOwnedAccountFacts(ctx, p.OwnerUserID, p.AccountID); err != nil {
		return nil, err
	}
	r.lastSet = p
	if r.setErr != nil {
		return nil, r.setErr
	}
	r.sets++
	selected := int64(3)
	r.listing.Status = service.MarketListingStatusListed
	r.listing.SelectedGroupID = &selected
	return r.listing, nil
}

type ownerMarketSettings struct {
	service.SettingRepository
}

func (ownerMarketSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{service.SettingKeyMarketEnabled: "true"}, nil
}

func newOwnerMarketRouter(repo *ownerMarketRepo, userID int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewMarketListingHandler(service.NewMarketListingService(repo, ownerMarketSettings{}))
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
		c.Next()
	})
	r.GET("/hosted/owned-accounts/:id/market", h.Get)
	r.PUT("/hosted/owned-accounts/:id/market", h.Put)
	return r
}

func jsonKeys(t *testing.T, raw json.RawMessage) []string {
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

func TestOwnerMarketHandlerJSONAndAuth(t *testing.T) {
	plan := "max"
	sessionEnd := time.Date(2026, 11, 29, 10, 0, 0, 0, time.UTC)
	repo := &ownerMarketRepo{owner: 7, sessionAt: &sessionEnd, listing: &service.MarketListing{AccountID: 11, OwnerUserID: 7, Plan: &plan,
		Status: service.MarketListingStatusUnlisted, Version: 1}}

	w := httptest.NewRecorder()
	newOwnerMarketRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/hosted/owned-accounts/11/market", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, []string{"account_id", "allowed_tiers", "can_list", "can_unlist", "cooldown_hours", "market_enabled", "mode",
		"next_switch_at", "plan", "selected_group_id", "status", "unavailable_reasons", "upstream_windows", "version"}, jsonKeys(t, body.Data))
	var view struct {
		AllowedTiers    []json.RawMessage `json:"allowed_tiers"`
		UpstreamWindows []json.RawMessage `json:"upstream_windows"`
		CanList         bool              `json:"can_list"`
		CanUnlist       bool              `json:"can_unlist"`
		Mode            string            `json:"mode"`
		Plan            *string           `json:"plan"`
		CooldownHours   int               `json:"cooldown_hours"`
		NextSwitchAt    *string           `json:"next_switch_at"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &view))
	require.True(t, view.CanList)
	require.False(t, view.CanUnlist)
	require.Equal(t, "self_use", view.Mode)
	require.Equal(t, "max", *view.Plan)
	require.Equal(t, 24, view.CooldownHours)
	require.Nil(t, view.NextSwitchAt)
	require.Equal(t, []string{"admin_member", "available", "group_id", "name", "plan", "rate_multiplier"}, jsonKeys(t, view.AllowedTiers[0]))
	require.Len(t, view.UpstreamWindows, 1)
	require.Equal(t, []string{"estimate_sampled_at", "estimated_capacity_usd", "expired", "key", "resets_at", "used_cost_usd", "utilization"},
		jsonKeys(t, view.UpstreamWindows[0]), "the owned-accounts window objects")
	require.NotContains(t, w.Body.String(), "protection")
	require.NotContains(t, w.Body.String(), "reserved")

	// Another user: 404 on read and write, nothing changed.
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/hosted/owned-accounts/11/market", nil),
		httptest.NewRequest(http.MethodPut, "/hosted/owned-accounts/11/market", bytes.NewBufferString(`{"group_id":3,"listed":true}`)),
	} {
		w = httptest.NewRecorder()
		newOwnerMarketRouter(repo, 8).ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	}
	require.Zero(t, repo.sets)

	// Body validation: listed is required; group_id is optional.
	for _, raw := range []string{`{}`, `{"group_id":3}`, `{"listed":true,"group_id":-1}`, `not json`} {
		w = httptest.NewRecorder()
		newOwnerMarketRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/hosted/owned-accounts/11/market", bytes.NewBufferString(raw)))
		require.Equal(t, http.StatusBadRequest, w.Code, raw)
	}
	w = httptest.NewRecorder()
	newOwnerMarketRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/hosted/owned-accounts/11/market", bytes.NewBufferString(`{"listed":true}`)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, repo.sets)
	require.Zero(t, repo.lastSet.GroupID, "omitted group_id: the plan's only tier")
	require.Contains(t, w.Body.String(), `"status":"listed"`)
	require.Contains(t, w.Body.String(), `"mode":"shared"`)
	w = httptest.NewRecorder()
	newOwnerMarketRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/hosted/owned-accounts/11/market", bytes.NewBufferString(`{"listed":false,"group_id":3}`)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Zero(t, repo.lastSet.GroupID, "unlisting ignores group_id")
	require.False(t, repo.lastSet.Listed)

	// Cooldown view and 429.
	next := time.Date(2026, 11, 29, 8, 0, 0, 0, time.UTC)
	repo.next = &next
	w = httptest.NewRecorder()
	newOwnerMarketRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/hosted/owned-accounts/11/market", nil))
	require.Contains(t, w.Body.String(), `"next_switch_at":"2026-11-29T08:00:00Z"`)
	require.Contains(t, w.Body.String(), `"switch_cooldown"`)
	repo.setErr = service.NewMarketSwitchCooldownError(next, 3600)
	w = httptest.NewRecorder()
	newOwnerMarketRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/hosted/owned-accounts/11/market", bytes.NewBufferString(`{"listed":false}`)))
	require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
	require.Equal(t, "3600", w.Header().Get("Retry-After"))
	var errBody struct {
		Reason   string            `json:"reason"`
		Metadata map[string]string `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errBody))
	require.Equal(t, "MARKET_SWITCH_COOLDOWN", errBody.Reason)
	require.Equal(t, "2026-11-29T08:00:00Z", errBody.Metadata["next_switch_at"])
	// Other errors carry no Retry-After.
	repo.setErr = service.ErrMarketTierRequired
	w = httptest.NewRecorder()
	newOwnerMarketRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/hosted/owned-accounts/11/market", bytes.NewBufferString(`{"listed":true}`)))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "MARKET_TIER_REQUIRED")
	require.Empty(t, w.Header().Get("Retry-After"))
}
