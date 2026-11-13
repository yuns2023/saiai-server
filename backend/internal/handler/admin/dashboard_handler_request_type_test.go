package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type dashboardUsageRepoCapture struct {
	service.UsageLogRepository
	trendRequestType *int16
	trendStream      *bool
	trendSessionID   string
	trendPlatform    string
	modelRequestType *int16
	modelStream      *bool
	modelSessionID   string
	modelPlatform    string
	rankingLimit     int
	rankingSort      usagestats.SpendingRankingSort
	ranking          []usagestats.UserSpendingRankingItem
	rankingTotal     float64
}

func (s *dashboardUsageRepoCapture) GetUsageTrendWithFilters(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	userID, apiKeyID, accountID, groupID int64,
	model string,
	requestType *int16,
	stream *bool,
	billingType *int8,
	sessionID, platform string,
) ([]usagestats.TrendDataPoint, error) {
	s.trendRequestType = requestType
	s.trendStream = stream
	s.trendSessionID = sessionID
	s.trendPlatform = platform
	return []usagestats.TrendDataPoint{}, nil
}

func (s *dashboardUsageRepoCapture) GetModelStatsWithFilters(
	ctx context.Context,
	startTime, endTime time.Time,
	userID, apiKeyID, accountID, groupID int64,
	requestType *int16,
	stream *bool,
	billingType *int8,
	sessionID, platform string,
) ([]usagestats.ModelStat, error) {
	s.modelRequestType = requestType
	s.modelStream = stream
	s.modelSessionID = sessionID
	s.modelPlatform = platform
	return []usagestats.ModelStat{}, nil
}

func (s *dashboardUsageRepoCapture) GetUserSpendingRanking(
	ctx context.Context,
	startTime, endTime time.Time,
	limit int,
	sort usagestats.SpendingRankingSort,
) (*usagestats.UserSpendingRankingResponse, error) {
	s.rankingLimit = limit
	s.rankingSort = sort
	return &usagestats.UserSpendingRankingResponse{
		Ranking:         s.ranking,
		TotalActualCost: s.rankingTotal,
		TotalRequests:   44,
		TotalTokens:     1234,
		TotalSubscriptionRevenueStats: usagestats.TotalSubscriptionRevenueStats{
			TotalSubscriptionRevenue: 6.4, TotalSubscriptionRevenueUnpricedCost: 2,
			TotalBalanceCost: 30, TotalSubscriptionCost: 12, TotalUnclassifiedCost: 1,
		},
	}, nil
}

func newDashboardRequestTypeTestRouter(repo *dashboardUsageRepoCapture) *gin.Engine {
	gin.SetMode(gin.TestMode)
	dashboardSvc := service.NewDashboardService(repo, nil, nil, nil)
	handler := NewDashboardHandler(dashboardSvc, nil)
	router := gin.New()
	router.GET("/admin/dashboard/trend", handler.GetUsageTrend)
	router.GET("/admin/dashboard/models", handler.GetModelStats)
	router.GET("/admin/dashboard/users-ranking", handler.GetUserSpendingRanking)
	return router
}

func TestDashboardTrendRequestTypePriority(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?request_type=ws_v2&stream=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.trendRequestType)
	require.Equal(t, int16(service.RequestTypeWSV2), *repo.trendRequestType)
	require.Nil(t, repo.trendStream)
}

func TestDashboardTrendSessionIDFilter(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?session_id=%20sess-trend%20", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "sess-trend", repo.trendSessionID)
}

func TestDashboardTrendSessionIDTooShort(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?session_id=abc", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardTrendInvalidRequestType(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?request_type=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardTrendInvalidStream(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/trend?stream=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardModelStatsRequestTypePriority(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?request_type=sync&stream=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.modelRequestType)
	require.Equal(t, int16(service.RequestTypeSync), *repo.modelRequestType)
	require.Nil(t, repo.modelStream)
}

func TestDashboardModelStatsSessionIDFilter(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?session_id=sess-model", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "sess-model", repo.modelSessionID)
}

func TestDashboardModelStatsSessionIDTooShort(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?session_id=abc", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardModelStatsInvalidRequestType(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?request_type=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardModelStatsInvalidStream(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?stream=bad", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardModelStatsInvalidModelSource(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?model_source=invalid", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardModelStatsValidModelSource(t *testing.T) {
	repo := &dashboardUsageRepoCapture{}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/models?model_source=upstream", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestDashboardUsersRankingLimitAndCache(t *testing.T) {
	dashboardUsersRankingCache = newSnapshotCache(5 * time.Minute)
	repo := &dashboardUsageRepoCapture{
		ranking: []usagestats.UserSpendingRankingItem{
			{UserID: 7, Email: "rank@example.com", ActualCost: 10.5, Requests: 3, Tokens: 300},
		},
		rankingTotal: 88.8,
	}
	router := newDashboardRequestTypeTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/users-ranking?limit=100&start_date=2025-01-01&end_date=2025-01-02", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 50, repo.rankingLimit)
	require.Contains(t, rec.Body.String(), "\"total_actual_cost\":88.8")
	require.Contains(t, rec.Body.String(), "\"total_requests\":44")
	require.Contains(t, rec.Body.String(), "\"total_tokens\":1234")
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))

	req2 := httptest.NewRequest(http.MethodGet, "/admin/dashboard/users-ranking?limit=100&start_date=2025-01-01&end_date=2025-01-02", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)

	require.Equal(t, http.StatusOK, rec2.Code)
	require.Equal(t, "hit", rec2.Header().Get("X-Snapshot-Cache"))
	// Revenue totals (pre-LIMIT) feed the Others row; they survive the cache.
	for _, body := range []string{rec.Body.String(), rec2.Body.String()} {
		for _, field := range []string{
			`"total_subscription_revenue":6.4`, `"total_subscription_revenue_unpriced_cost":2`,
			`"total_balance_cost":30`, `"total_subscription_cost":12`, `"total_unclassified_cost":1`,
		} {
			require.Contains(t, body, field)
		}
	}
}

func TestDashboardUsersRankingSortParamAndCacheKey(t *testing.T) {
	dashboardUsersRankingCache = newSnapshotCache(5 * time.Minute)
	repo := &dashboardUsageRepoCapture{
		ranking: []usagestats.UserSpendingRankingItem{{
			UserID: 7, Email: "rank@example.com", Requests: 3,
			RequestBodySizeStats: usagestats.RequestBodySizeStats{RequestBodyBytesTotal: 0},
		}},
	}
	router := newDashboardRequestTypeTestRouter(repo)
	base := "/admin/dashboard/users-ranking?start_date=2025-01-01&end_date=2025-01-02"
	get := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+query, nil))
		return rec
	}

	rec := get("")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, usagestats.SpendingRankingSortCost, repo.rankingSort)
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
	// Unknown sizes: total 0, avg/max explicit null.
	require.Contains(t, rec.Body.String(), `"request_body_bytes_total":0`)
	require.Contains(t, rec.Body.String(), `"request_body_bytes_avg":null`)
	require.Contains(t, rec.Body.String(), `"request_body_bytes_max":null`)
	require.Contains(t, rec.Body.String(), `"request_body_bytes_known_count":0`)

	// The sort is part of the cache key: a cost snapshot never serves a size sort.
	rec = get("&sort=request_body_bytes")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, usagestats.SpendingRankingSortRequestBodyBytes, repo.rankingSort)
	rec = get("&sort=request_body_bytes")
	require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"))
	// Explicit cost equals the default order and cache entry.
	rec = get("&sort=cost")
	require.Equal(t, "hit", rec.Header().Get("X-Snapshot-Cache"))

	repo.rankingSort = ""
	rec = get("&sort=tokens")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, repo.rankingSort)
}
