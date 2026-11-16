package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type dashboardPlatformUsageRepoProbe struct {
	dashboardUsageRepoCacheProbe
	platformTrendCalls atomic.Int32
	platformModelCalls atomic.Int32
	modelCalls         atomic.Int32
}

func (r *dashboardPlatformUsageRepoProbe) GetPlatformUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string) ([]usagestats.TrendDataPoint, error) {
	r.platformTrendCalls.Add(1)
	return []usagestats.TrendDataPoint{{Date: "2026-03-11", Requests: 9}}, nil
}

func (r *dashboardPlatformUsageRepoProbe) GetPlatformModelStats(ctx context.Context, startTime, endTime time.Time) ([]usagestats.ModelStat, error) {
	r.platformModelCalls.Add(1)
	return []usagestats.ModelStat{{Model: "platform-model", Requests: 9}}, nil
}

func (r *dashboardPlatformUsageRepoProbe) GetModelStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, accountID, groupID int64, requestType *int16, stream *bool, billingType *int8, sessionID, platform string) ([]usagestats.ModelStat, error) {
	r.modelCalls.Add(1)
	return []usagestats.ModelStat{{Model: "all-model", Requests: 10}}, nil
}

func newHostedSnapshotRouter(repo service.UsageLogRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	router := gin.New()
	router.GET("/snapshot", handler.GetSnapshotV2)
	return router
}

func TestDashboardSnapshotExcludeHostedUsesPlatformQueriesAndSeparateCache(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()
	repo := &dashboardPlatformUsageRepoProbe{}
	router := newHostedSnapshotRouter(repo)
	base := "/snapshot?start_date=2026-03-01&end_date=2026-03-07&include_stats=false&granularity=day"

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+"&exclude_hosted=true", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "platform-model")
	require.Equal(t, int32(1), repo.platformTrendCalls.Load())
	require.Equal(t, int32(1), repo.platformModelCalls.Load())
	require.Equal(t, int32(0), repo.trendCalls.Load())
	require.Equal(t, int32(0), repo.modelCalls.Load())

	// Same range without the flag (admin usage page) must not reuse the
	// hosted-free entries.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "all-model")
	require.Equal(t, int32(1), repo.trendCalls.Load())
	require.Equal(t, int32(1), repo.modelCalls.Load())
}

func TestDashboardSnapshotExcludeHostedRejectsFiltersAndGroups(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()
	repo := &dashboardPlatformUsageRepoProbe{}
	router := newHostedSnapshotRouter(repo)
	for _, extra := range []string{"&user_id=3", "&model=gpt-5", "&include_group_stats=true"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/snapshot?start_date=2026-03-01&end_date=2026-03-07&include_stats=false&exclude_hosted=true"+extra, nil))
		require.Equal(t, http.StatusBadRequest, rec.Code, extra)
	}
	require.Equal(t, int32(0), repo.platformTrendCalls.Load())
}

func TestDashboardSnapshotExcludeHostedNeverFallsBackToInclusiveQueries(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	resetDashboardReadCachesForTest()
	repo := &dashboardUsageRepoCacheProbe{} // no platform capability
	router := newHostedSnapshotRouter(repo)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/snapshot?start_date=2026-03-01&end_date=2026-03-07&include_stats=false&include_model_stats=false&exclude_hosted=true", nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, int32(0), repo.trendCalls.Load())
}

type dashboardStatsRepoProbe struct {
	service.UsageLogRepository
	stats *usagestats.DashboardStats
}

func (r *dashboardStatsRepoProbe) GetDashboardStats(context.Context) (*usagestats.DashboardStats, error) {
	return r.stats, nil
}

// GetStats builds its payload by hand; the hosted service fee fields must be
// serialized there (P2a).
func TestDashboardStatsSerializesHostedServiceFee(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &dashboardStatsRepoProbe{stats: &usagestats.DashboardStats{
		HostedTotalMeteredCost: 12, HostedTodayMeteredCost: 2, HostedTotalServiceFee: 1.2, HostedTodayServiceFee: 0.2,
	}}
	handler := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	router := gin.New()
	router.GET("/stats", handler.GetStats)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, 1.2, body.Data["hosted_total_service_fee"])
	require.Equal(t, 0.2, body.Data["hosted_today_service_fee"])
	require.Equal(t, 12.0, body.Data["hosted_total_metered_cost"])
}
