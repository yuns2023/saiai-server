package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type apiKeyRankingRepoCapture struct {
	service.UsageLogRepository
	userID    int64
	limit     int
	sort      usagestats.SpendingRankingSort
	order     usagestats.SpendingRankingOrder
	startTime time.Time
	endTime   time.Time
	calls     int
	err       error
}

func (r *apiKeyRankingRepoCapture) GetAPIKeySpendingRanking(_ context.Context, start, end time.Time, userID int64, limit int, sort usagestats.SpendingRankingSort, order usagestats.SpendingRankingOrder) (*usagestats.APIKeySpendingRankingResponse, error) {
	r.startTime, r.endTime, r.userID, r.limit, r.sort, r.order = start, end, userID, limit, sort, order
	r.calls++
	return &usagestats.APIKeySpendingRankingResponse{
		Ranking: []usagestats.APIKeySpendingRankingItem{{
			APIKeyID: 11, KeyName: "Work", UserID: userID, Email: "user@example.com",
			ActualCost: 12.5, Requests: 9, Tokens: 200, InputTokens: 100,
			OutputTokens: 50, CacheCreationTokens: 25, CacheReadTokens: 25,
		}},
		TotalActualCost: 40, TotalRequests: 30, TotalTokens: 2600,
		TotalSubscriptionRevenueStats: usagestats.TotalSubscriptionRevenueStats{
			TotalSubscriptionRevenue: 12.8, TotalSubscriptionRevenueUnpricedCost: 3,
			TotalBalanceCost: 17, TotalSubscriptionCost: 23, TotalUnclassifiedCost: 0.5,
		},
	}, r.err
}

func newAPIKeyRankingRouter(t *testing.T, repo *apiKeyRankingRepoCapture) *gin.Engine {
	t.Helper()
	previous := dashboardAPIKeysRankingCache
	dashboardAPIKeysRankingCache = newSnapshotCache(30 * time.Second)
	t.Cleanup(func() { dashboardAPIKeysRankingCache = previous })
	gin.SetMode(gin.TestMode)
	h := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	r := gin.New()
	r.GET("/admin/dashboard/api-keys-ranking", h.GetAPIKeySpendingRanking)
	return r
}

func TestAPIKeySpendingRankingScopeCacheAndResponse(t *testing.T) {
	repo := &apiKeyRankingRepoCapture{}
	router := newAPIKeyRankingRouter(t, repo)
	rangeQuery := "?start_time=2026-03-01T00:00:00.123Z&end_time=2026-03-02T00:00:00.456Z"
	for _, tc := range []struct {
		query string
		user  int64
		limit int
		cache string
		calls int
	}{
		{"", 0, 12, "miss", 1},
		{"", 0, 12, "hit", 1},
		{"&user_id=7", 7, 12, "miss", 2},
		{"&user_id=7", 7, 12, "hit", 2},
		{"&user_id=8", 8, 12, "miss", 3},
		{"&user_id=7&limit=1000", 7, 50, "miss", 4},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/dashboard/api-keys-ranking"+rangeQuery+tc.query, nil))
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, tc.cache, w.Header().Get("X-Snapshot-Cache"))
		require.Equal(t, tc.calls, repo.calls)
		var body struct {
			Data struct {
				usagestats.APIKeySpendingRankingResponse
				StartDate string `json:"start_date"`
				EndDate   string `json:"end_date"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, tc.user, body.Data.Ranking[0].UserID)
		require.Equal(t, 40.0, body.Data.TotalActualCost)
		// Revenue totals reach the client on misses and cache hits alike.
		require.Equal(t, usagestats.TotalSubscriptionRevenueStats{
			TotalSubscriptionRevenue: 12.8, TotalSubscriptionRevenueUnpricedCost: 3,
			TotalBalanceCost: 17, TotalSubscriptionCost: 23, TotalUnclassifiedCost: 0.5,
		}, body.Data.TotalSubscriptionRevenueStats)
		require.Equal(t, "2026-03-01", body.Data.StartDate)
		require.Equal(t, "2026-03-02", body.Data.EndDate)
		require.NotContains(t, w.Body.String(), `"key":`)
		require.NotContains(t, w.Body.String(), `"api_key":`)
		require.Equal(t, tc.limit, repo.limit)
	}
	require.Equal(t, 123000000, repo.startTime.Nanosecond())
	require.Equal(t, 456000000, repo.endTime.Nanosecond())
}

func TestAPIKeySpendingRankingRejectsInvalidFilters(t *testing.T) {
	repo := &apiKeyRankingRepoCapture{}
	router := newAPIKeyRankingRouter(t, repo)
	for _, query := range []string{
		"user_id=bad", "user_id=0", "user_id=-7", "user_id=1.5", "user_id=9223372036854775808",
		"start_time=bad&end_time=bad", "start_time=2026-03-01T00:00:00Z",
		"start_time=2026-03-02T00:00:00Z&end_time=2026-03-01T00:00:00Z",
		"sort=tokens", "sort=request_body_bytes%3B",
		"order=asc", "sort=cost&order=asc", "sort=request_body_bytes&order=asc",
		"sort=cache_read_rate&order=up", "sort=cache_read_rate&order=ASC",
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/dashboard/api-keys-ranking?"+query, nil))
		require.Equal(t, http.StatusBadRequest, w.Code, query)
	}
	require.Zero(t, repo.calls)
}

func TestAPIKeySpendingRankingCalendarRangeAndErrorRetry(t *testing.T) {
	repo := &apiKeyRankingRepoCapture{err: errors.New("private database detail")}
	router := newAPIKeyRankingRouter(t, repo)
	url := "/admin/dashboard/api-keys-ranking?start_date=2026-03-08&end_date=2026-03-08&timezone=America%2FNew_York&user_id=7"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.NotContains(t, w.Body.String(), "private database detail")
	require.Equal(t, 23*time.Hour, repo.endTime.Sub(repo.startTime))
	repo.err = nil
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 2, repo.calls)
	require.Equal(t, "miss", w.Header().Get("X-Snapshot-Cache"))
}

func TestAPIKeySpendingRankingSortIsPartOfCacheKey(t *testing.T) {
	repo := &apiKeyRankingRepoCapture{}
	router := newAPIKeyRankingRouter(t, repo)
	base := "/admin/dashboard/api-keys-ranking?start_time=2026-03-01T00:00:00Z&end_time=2026-03-02T00:00:00Z&user_id=7"
	for _, tc := range []struct {
		query string
		sort  usagestats.SpendingRankingSort
		cache string
		calls int
	}{
		{"", usagestats.SpendingRankingSortCost, "miss", 1},
		{"&sort=request_body_bytes", usagestats.SpendingRankingSortRequestBodyBytes, "miss", 2},
		{"&sort=request_body_bytes", usagestats.SpendingRankingSortRequestBodyBytes, "hit", 2},
		{"&sort=cost", usagestats.SpendingRankingSortCost, "hit", 2},
		{"&sort=cost&order=desc", usagestats.SpendingRankingSortCost, "hit", 2},
		{"&sort=cache_read_rate", usagestats.SpendingRankingSortCacheReadRate, "miss", 3},
		{"&sort=cache_read_rate&order=asc", usagestats.SpendingRankingSortCacheReadRate, "hit", 3},
		{"&sort=cache_read_rate&order=desc", usagestats.SpendingRankingSortCacheReadRate, "miss", 4},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, base+tc.query, nil))
		require.Equal(t, http.StatusOK, w.Code, tc.query)
		require.Equal(t, tc.cache, w.Header().Get("X-Snapshot-Cache"), tc.query)
		require.Equal(t, tc.calls, repo.calls, tc.query)
		if tc.cache == "miss" {
			require.Equal(t, tc.sort, repo.sort, tc.query)
		}
		require.Contains(t, w.Body.String(), `"request_body_bytes_avg":null`)
		require.Contains(t, w.Body.String(), `"request_body_bytes_known_count":0`)
	}
}

func TestAPIKeySpendingRankingCacheReadRateOrderAndPayload(t *testing.T) {
	repo := &apiKeyRankingRepoCapture{}
	router := newAPIKeyRankingRouter(t, repo)
	base := "/admin/dashboard/api-keys-ranking?start_time=2026-03-01T00:00:00Z&end_time=2026-03-02T00:00:00Z&limit=20"
	for _, tc := range []struct {
		query string
		order usagestats.SpendingRankingOrder
	}{
		{"&sort=cache_read_rate", usagestats.SpendingRankingOrderAsc},
		{"&sort=cache_read_rate&order=desc", usagestats.SpendingRankingOrderDesc},
		{"", usagestats.SpendingRankingOrderDesc},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, base+tc.query, nil))
		require.Equal(t, http.StatusOK, w.Code, tc.query)
		require.Equal(t, tc.order, repo.order, tc.query)
		require.Equal(t, 20, repo.limit, tc.query)
		var body struct {
			Data struct {
				Sort                   string `json:"sort"`
				Order                  string `json:"order"`
				CacheReadRateMinTokens int64  `json:"cache_read_rate_min_tokens"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), tc.query)
		require.Equal(t, string(repo.sort), body.Data.Sort, tc.query)
		require.Equal(t, string(tc.order), body.Data.Order, tc.query)
		require.Equal(t, usagestats.CacheReadRateMinTokens, body.Data.CacheReadRateMinTokens, tc.query)
	}
}

func TestUserSpendingRankingRejectsCacheReadRateSort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x?sort=cache_read_rate", nil)
	_, ok := parseRankingSort(c)
	require.False(t, ok)
	for query, want := range map[string]bool{"sort=cost&order=asc": false, "order=up": false, "sort=cost&order=desc": true, "": true} {
		c, _ = gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/x?"+query, nil)
		_, ok = parseRankingSort(c)
		require.Equal(t, want, ok, query)
	}
}
