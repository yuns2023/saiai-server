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

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type ownerMarketRepo struct {
	service.MarketListingRepository
	owner   int64
	listing *service.MarketListing
	sets    int
}

func (r *ownerMarketRepo) account() *service.Account {
	owner := r.owner
	return &service.Account{ID: 11, Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken, Concurrency: 3,
		Status: service.StatusActive, Schedulable: true, OwnerUserID: &owner, Extra: map[string]any{}}
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
	return r.listing, nil
}

func (r *ownerMarketRepo) ListTierOptions(context.Context, []int64) ([]service.MarketTierOption, error) {
	return []service.MarketTierOption{{GroupID: 3, Name: "T1", RateMultiplier: 1.2, Available: true}}, nil
}

func (r *ownerMarketRepo) OwnerSet(ctx context.Context, p service.MarketOwnerSetParams) (*service.MarketListing, error) {
	if _, err := r.GetOwnedAccountFacts(ctx, p.OwnerUserID, p.AccountID); err != nil {
		return nil, err
	}
	r.sets++
	selected := p.GroupID
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
	repo := &ownerMarketRepo{owner: 7, listing: &service.MarketListing{AccountID: 11, OwnerUserID: 7, AllowedGroupIDs: []int64{3},
		Status: service.MarketListingStatusUnlisted, Version: 1,
		Protection: service.MarketProtectionParams{ReservedConcurrency: 1, ReservedDevices: 2, ReservedAdmissions: 1, Cap5hPct: 70, Cap7dPct: 60, SampleMaxAgeMinutes: 30}}}

	w := httptest.NewRecorder()
	newOwnerMarketRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/hosted/owned-accounts/11/market", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, []string{"account_id", "allowed_tiers", "can_list", "market_enabled", "protection", "selected_group_id",
		"status", "unavailable_reasons", "version"}, jsonKeys(t, body.Data))
	var view struct {
		AllowedTiers []json.RawMessage `json:"allowed_tiers"`
		Protection   json.RawMessage   `json:"protection"`
		CanList      bool              `json:"can_list"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &view))
	require.True(t, view.CanList)
	require.Equal(t, []string{"admin_member", "available", "group_id", "name", "rate_multiplier"}, jsonKeys(t, view.AllowedTiers[0]))
	require.Equal(t, []string{"cap_5h_pct", "cap_7d_pct", "reserved_admissions", "reserved_concurrency", "reserved_devices",
		"sample_max_age_minutes"}, jsonKeys(t, view.Protection))

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

	// Body validation.
	for _, raw := range []string{`{}`, `{"listed":true}`, `{"group_id":3}`, `not json`} {
		w = httptest.NewRecorder()
		newOwnerMarketRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/hosted/owned-accounts/11/market", bytes.NewBufferString(raw)))
		require.Equal(t, http.StatusBadRequest, w.Code, raw)
	}
	w = httptest.NewRecorder()
	newOwnerMarketRouter(repo, 7).ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/hosted/owned-accounts/11/market", bytes.NewBufferString(`{"group_id":3,"listed":true}`)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, repo.sets)
	require.Contains(t, w.Body.String(), `"status":"listed"`)
}
