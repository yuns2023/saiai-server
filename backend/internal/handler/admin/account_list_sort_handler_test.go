package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccountListSortParamsReachService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/api/v1/admin/accounts", handler.List)

	get := func(query string) (*httptest.ResponseRecorder, service.AccountListSort) {
		adminSvc.lastListAccountsSort = nil
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts"+query, nil))
		require.NotNil(t, adminSvc.lastListAccountsSort, "service not called for %q", query)
		return rec, *adminSvc.lastListAccountsSort
	}

	rec, got := get("?page=1&page_size=20&sort_by=last_used_at&sort_order=asc")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.AccountListSort{By: service.AccountSortByLastUsedAt}, got)
	ascETag := rec.Header().Get("ETag")

	rec, got = get("?page=1&page_size=20&sort_by=last_used_at&sort_order=desc")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.AccountListSort{By: service.AccountSortByLastUsedAt, Desc: true}, got)
	require.NotEqual(t, ascETag, rec.Header().Get("ETag"), "different sort must not share an ETag")

	// Unknown / hostile sort_by never errors and falls back to the default.
	for _, q := range []string{"?sort_by=bogus&sort_order=asc", "?sort_by=name%3BDROP%20TABLE%20accounts", "?sort_order=asc", ""} {
		rec, got = get(q)
		require.Equal(t, http.StatusOK, rec.Code, q)
		require.Equal(t, service.AccountListSort{}, got, q)
	}

	// A conditional request carrying the ETag of another sort order is not 304.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&sort_by=last_used_at&sort_order=desc", nil)
	req.Header.Set("If-None-Match", ascETag)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestSortOwnedAccountsMatchesRepositoryOrder(t *testing.T) {
	base := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	at := func(h int) *time.Time { v := base.Add(time.Duration(h) * time.Hour); return &v }
	rate := func(v float64) *float64 { return &v }
	accounts := func() []service.Account {
		return []service.Account{
			{ID: 1, Name: "Bravo", Status: "active", Priority: 10, RateMultiplier: rate(2), LastUsedAt: at(2), Schedulable: true},
			{ID: 2, Name: "alpha", Status: "disabled", Priority: 10, LastUsedAt: nil, ExpiresAt: at(5)},
			{ID: 3, Name: "alpha", Status: "active", Priority: 1, RateMultiplier: rate(0.5), LastUsedAt: at(2), Schedulable: true},
			{ID: 4, Name: "charlie", Status: "error", Priority: 50, LastUsedAt: at(1), ExpiresAt: at(3)},
		}
	}
	ids := func(s service.AccountListSort) []int64 {
		list := accounts()
		sortOwnedAccounts(list, s)
		out := make([]int64, len(list))
		for i := range list {
			out[i] = list[i].ID
		}
		return out
	}

	require.Equal(t, []int64{4, 3, 2, 1}, ids(service.AccountListSort{}))
	require.Equal(t, []int64{1, 2, 3, 4}, ids(service.AccountListSort{By: service.AccountSortByID}))
	require.Equal(t, []int64{2, 3, 1, 4}, ids(service.AccountListSort{By: service.AccountSortByName}))
	require.Equal(t, []int64{4, 1, 3, 2}, ids(service.AccountListSort{By: service.AccountSortByName, Desc: true}))
	require.Equal(t, []int64{1, 3, 2, 4}, ids(service.AccountListSort{By: service.AccountSortByStatus}))
	require.Equal(t, []int64{2, 4, 1, 3}, ids(service.AccountListSort{By: service.AccountSortBySchedulable}))
	require.Equal(t, []int64{3, 1, 2, 4}, ids(service.AccountListSort{By: service.AccountSortByPriority}))
	// nil rate multiplier = stored default 1.0
	require.Equal(t, []int64{3, 2, 4, 1}, ids(service.AccountListSort{By: service.AccountSortByRateMultiplier}))
	// NULLs last in both directions; equal timestamps tie-break on id.
	require.Equal(t, []int64{4, 1, 3, 2}, ids(service.AccountListSort{By: service.AccountSortByLastUsedAt}))
	require.Equal(t, []int64{3, 1, 4, 2}, ids(service.AccountListSort{By: service.AccountSortByLastUsedAt, Desc: true}))
	require.Equal(t, []int64{4, 2, 1, 3}, ids(service.AccountListSort{By: service.AccountSortByExpiresAt}))
	require.Equal(t, []int64{2, 4, 3, 1}, ids(service.AccountListSort{By: service.AccountSortByExpiresAt, Desc: true}))
}
