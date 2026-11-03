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

func TestNormalizeUsagePlatformFilter(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"anthropic", service.PlatformAnthropic},
		{"openai", service.PlatformOpenAI},
		{"gemini", service.PlatformGemini},
		{"antigravity", service.PlatformAntigravity},
		{"sora", service.PlatformSora},
		{" OpenAI ", service.PlatformOpenAI},
	} {
		got, err := normalizeUsagePlatformFilter(tc.raw)
		require.NoError(t, err, tc.raw)
		require.Equal(t, tc.want, got, tc.raw)
	}
	for _, raw := range []string{"claude", "unknown", "openai,anthropic", "anthropic'--"} {
		_, err := normalizeUsagePlatformFilter(raw)
		require.Error(t, err, raw)
	}
}

type usagePlatformRepoCapture struct {
	service.UsageLogRepository
	trendPlatforms []string
	modelPlatforms []string
	modelSources   []string
	groupPlatforms []string
}

func (r *usagePlatformRepoCapture) GetUsageTrendWithFilters(_ context.Context, _, _ time.Time, _ string, _, _, _, _ int64, _ string, _ *int16, _ *bool, _ *int8, _, platform string) ([]usagestats.TrendDataPoint, error) {
	r.trendPlatforms = append(r.trendPlatforms, platform)
	return []usagestats.TrendDataPoint{}, nil
}

func (r *usagePlatformRepoCapture) GetModelStatsWithFilters(_ context.Context, _, _ time.Time, _, _, _, _ int64, _ *int16, _ *bool, _ *int8, _, platform string) ([]usagestats.ModelStat, error) {
	r.modelPlatforms = append(r.modelPlatforms, platform)
	r.modelSources = append(r.modelSources, usagestats.ModelSourceRequested)
	return []usagestats.ModelStat{}, nil
}

func (r *usagePlatformRepoCapture) GetModelStatsWithFiltersBySource(_ context.Context, _, _ time.Time, _, _, _, _ int64, _ *int16, _ *bool, _ *int8, source, _, platform string) ([]usagestats.ModelStat, error) {
	r.modelPlatforms = append(r.modelPlatforms, platform)
	r.modelSources = append(r.modelSources, source)
	return []usagestats.ModelStat{}, nil
}

func (r *usagePlatformRepoCapture) GetGroupStatsWithFilters(_ context.Context, _, _ time.Time, _, _, _, _ int64, _ *int16, _ *bool, _ *int8, _, platform string) ([]usagestats.GroupStat, error) {
	r.groupPlatforms = append(r.groupPlatforms, platform)
	return []usagestats.GroupStat{}, nil
}

func newUsagePlatformDashboardRouter(repo *usagePlatformRepoCapture) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	router := gin.New()
	router.GET("/admin/dashboard/trend", handler.GetUsageTrend)
	router.GET("/admin/dashboard/models", handler.GetModelStats)
	router.GET("/admin/dashboard/groups", handler.GetGroupStats)
	router.GET("/admin/dashboard/snapshot-v2", handler.GetSnapshotV2)
	return router
}

func servePlatformRequest(router *gin.Engine, url string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec
}

func TestAdminUsageListAndStatsPassPlatformFilter(t *testing.T) {
	repo := &adminUsageRepoCapture{}
	router := newAdminUsageRequestTypeTestRouter(repo)

	rec := servePlatformRequest(router, "/admin/usage?platform=openai&group_id=7")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.PlatformOpenAI, repo.listFilters.Platform)
	require.Equal(t, int64(7), repo.listFilters.GroupID)

	rec = servePlatformRequest(router, "/admin/usage/stats?platform=anthropic")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.PlatformAnthropic, repo.statsFilters.Platform)

	rec = servePlatformRequest(router, "/admin/usage?platform=")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, repo.listFilters.Platform)
}

func TestAdminUsageRejectsUnknownPlatform(t *testing.T) {
	for _, url := range []string{"/admin/usage?platform=claude", "/admin/usage/stats?platform=claude"} {
		repo := &adminUsageRepoCapture{}
		router := newAdminUsageRequestTypeTestRouter(repo)
		rec := servePlatformRequest(router, url)
		require.Equal(t, http.StatusBadRequest, rec.Code, url)
		require.Empty(t, repo.listFilters.Platform, url)
		require.Empty(t, repo.statsFilters.Platform, url)
	}
}

func TestDashboardEndpointsPassPlatformFilter(t *testing.T) {
	repo := &usagePlatformRepoCapture{}
	router := newUsagePlatformDashboardRouter(repo)
	// Unique ranges keep the package-level snapshot caches from leaking
	// results between tests.
	rangeQuery := "start_time=2031-01-01T00:00:00Z&end_time=2031-01-02T00:00:00Z"

	require.Equal(t, http.StatusOK, servePlatformRequest(router, "/admin/dashboard/trend?platform=gemini&"+rangeQuery).Code)
	require.Equal(t, []string{service.PlatformGemini}, repo.trendPlatforms)

	require.Equal(t, http.StatusOK, servePlatformRequest(router, "/admin/dashboard/models?platform=gemini&"+rangeQuery).Code)
	require.Equal(t, http.StatusOK, servePlatformRequest(router, "/admin/dashboard/models?platform=gemini&model_source=upstream&"+rangeQuery).Code)
	require.Equal(t, []string{service.PlatformGemini, service.PlatformGemini}, repo.modelPlatforms)
	require.Equal(t, []string{usagestats.ModelSourceRequested, usagestats.ModelSourceUpstream}, repo.modelSources)

	require.Equal(t, http.StatusOK, servePlatformRequest(router, "/admin/dashboard/groups?platform=gemini&"+rangeQuery).Code)
	require.Equal(t, []string{service.PlatformGemini}, repo.groupPlatforms)
}

func TestDashboardEndpointsRejectUnknownPlatform(t *testing.T) {
	for _, path := range []string{"trend", "models", "groups", "snapshot-v2"} {
		repo := &usagePlatformRepoCapture{}
		router := newUsagePlatformDashboardRouter(repo)
		rec := servePlatformRequest(router, "/admin/dashboard/"+path+"?platform=bogus&include_stats=false")
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		require.Empty(t, repo.trendPlatforms, path)
		require.Empty(t, repo.modelPlatforms, path)
		require.Empty(t, repo.groupPlatforms, path)
	}
}

func TestDashboardCachesKeyByPlatform(t *testing.T) {
	repo := &usagePlatformRepoCapture{}
	router := newUsagePlatformDashboardRouter(repo)
	rangeQuery := "start_time=2031-02-01T00:00:00Z&end_time=2031-02-02T00:00:00Z"

	for _, platform := range []string{"", "openai", "anthropic", "openai"} {
		require.Equal(t, http.StatusOK, servePlatformRequest(router, "/admin/dashboard/trend?platform="+platform+"&"+rangeQuery).Code)
		require.Equal(t, http.StatusOK, servePlatformRequest(router, "/admin/dashboard/groups?platform="+platform+"&"+rangeQuery).Code)
	}
	// The repeated openai request is a cache hit; the others must not share entries.
	require.Equal(t, []string{"", service.PlatformOpenAI, service.PlatformAnthropic}, repo.trendPlatforms)
	require.Equal(t, []string{"", service.PlatformOpenAI, service.PlatformAnthropic}, repo.groupPlatforms)
}

func TestDashboardSnapshotV2PassesPlatformFilter(t *testing.T) {
	repo := &usagePlatformRepoCapture{}
	router := newUsagePlatformDashboardRouter(repo)
	base := "/admin/dashboard/snapshot-v2?start_time=2031-03-01T00:00:00Z&end_time=2031-03-02T00:00:00Z&include_stats=false&include_trend=true&include_model_stats=true&include_group_stats=true"

	rec := servePlatformRequest(router, base+"&platform=openai")
	require.Equal(t, http.StatusOK, rec.Code)
	rec = servePlatformRequest(router, base+"&platform=anthropic")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{service.PlatformOpenAI, service.PlatformAnthropic}, repo.trendPlatforms)
	require.Equal(t, []string{service.PlatformOpenAI, service.PlatformAnthropic}, repo.modelPlatforms)
	require.Equal(t, []string{service.PlatformOpenAI, service.PlatformAnthropic}, repo.groupPlatforms)

	// The hosted-free platform view cannot be narrowed by a platform filter.
	rec = servePlatformRequest(router, "/admin/dashboard/snapshot-v2?include_stats=false&exclude_hosted=true&platform=openai")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGetUserBreakdownPlatformFilter(t *testing.T) {
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)

	rec := servePlatformRequest(router, "/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&platform=antigravity")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.PlatformAntigravity, repo.capturedDim.Platform)

	repo = &userBreakdownRepoCapture{}
	router = newUserBreakdownRouter(repo)
	rec = servePlatformRequest(router, "/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16&platform=nope")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, repo.capturedDim.Platform)
}

func TestUsageEndpointsRejectRepeatedPlatform(t *testing.T) {
	for _, query := range []string{"platform=openai&platform=anthropic", "platform=&platform=openai", "platform=openai&platform=openai"} {
		usageRepo := &adminUsageRepoCapture{}
		usageRouter := newAdminUsageRequestTypeTestRouter(usageRepo)
		for _, path := range []string{"/admin/usage", "/admin/usage/stats"} {
			rec := servePlatformRequest(usageRouter, path+"?"+query)
			require.Equal(t, http.StatusBadRequest, rec.Code, path+"?"+query)
			require.Contains(t, rec.Body.String(), "at most once", path+"?"+query)
		}
		require.Empty(t, usageRepo.listFilters.Platform, query)
		require.Empty(t, usageRepo.statsFilters.Platform, query)

		dashboardRepo := &usagePlatformRepoCapture{}
		dashboardRouter := newUsagePlatformDashboardRouter(dashboardRepo)
		for _, path := range []string{"trend", "models", "groups", "snapshot-v2"} {
			rec := servePlatformRequest(dashboardRouter, "/admin/dashboard/"+path+"?include_stats=false&"+query)
			require.Equal(t, http.StatusBadRequest, rec.Code, path+"?"+query)
		}
		require.Empty(t, dashboardRepo.trendPlatforms, query)
		require.Empty(t, dashboardRepo.modelPlatforms, query)
		require.Empty(t, dashboardRepo.groupPlatforms, query)

		breakdownRepo := &userBreakdownRepoCapture{}
		rec := servePlatformRequest(newUserBreakdownRouter(breakdownRepo), "/admin/dashboard/user-breakdown?"+query)
		require.Equal(t, http.StatusBadRequest, rec.Code, "user-breakdown?"+query)
		require.Empty(t, breakdownRepo.capturedDim.Platform, query)
	}
}
