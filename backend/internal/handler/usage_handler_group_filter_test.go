//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// usageGroupFilterRepo records the scope each user-facing query received.
type usageGroupFilterRepo struct {
	service.UsageLogRepository
	listFilters usagestats.UsageLogFilters
	calls       []string
	userID      int64
	apiKeyID    int64
	accountID   int64
	groupID     int64
}

func (r *usageGroupFilterRepo) record(call string, userID, apiKeyID, accountID, groupID int64) {
	r.calls = append(r.calls, call)
	r.userID, r.apiKeyID, r.accountID, r.groupID = userID, apiKeyID, accountID, groupID
}

func (r *usageGroupFilterRepo) ListWithFilters(_ context.Context, params pagination.PaginationParams, filters usagestats.UsageLogFilters) ([]service.UsageLog, *pagination.PaginationResult, error) {
	r.listFilters = filters
	return []service.UsageLog{}, &pagination.PaginationResult{Page: params.Page, PageSize: params.PageSize}, nil
}

func (r *usageGroupFilterRepo) GetUserStatsAggregatedWithFilters(_ context.Context, userID, apiKeyID, groupID int64, _, _ time.Time) (*usagestats.UsageStats, error) {
	r.record("stats", userID, apiKeyID, 0, groupID)
	return &usagestats.UsageStats{TotalRequests: 3}, nil
}

func (r *usageGroupFilterRepo) GetUserDashboardStatsWithFilters(_ context.Context, userID, apiKeyID, groupID int64) (*usagestats.UserDashboardStats, error) {
	r.record("dashboard", userID, apiKeyID, 0, groupID)
	return &usagestats.UserDashboardStats{TotalAPIKeys: 9, ActiveAPIKeys: 9, TodayRequests: 2}, nil
}

func (r *usageGroupFilterRepo) GetUsageTrendWithFilters(_ context.Context, _, _ time.Time, _ string, userID, apiKeyID, accountID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8, sessionID string) ([]usagestats.TrendDataPoint, error) {
	if model != "" || requestType != nil || stream != nil || billingType != nil || sessionID != "" {
		panic("user trend must not inherit admin filters")
	}
	r.record("trend", userID, apiKeyID, accountID, groupID)
	return []usagestats.TrendDataPoint{}, nil
}

func (r *usageGroupFilterRepo) GetModelStatsWithFilters(_ context.Context, _, _ time.Time, userID, apiKeyID, accountID, groupID int64, requestType *int16, stream *bool, billingType *int8, sessionID string) ([]usagestats.ModelStat, error) {
	if requestType != nil || stream != nil || billingType != nil || sessionID != "" {
		panic("user model stats must not inherit admin filters")
	}
	r.record("models", userID, apiKeyID, accountID, groupID)
	return []usagestats.ModelStat{}, nil
}

func (r *usageGroupFilterRepo) GetGroupStatsWithFilters(_ context.Context, _, _ time.Time, userID, apiKeyID, accountID, groupID int64, _ *int16, _ *bool, _ *int8, _ string) ([]usagestats.GroupStat, error) {
	r.record("groups", userID, apiKeyID, accountID, groupID)
	return []usagestats.GroupStat{
		{GroupID: 31, GroupName: "自建CCMax", Requests: 9},
		{GroupID: 0, GroupName: "", Requests: 4},
		{GroupID: 12, GroupName: "retired", Requests: 1},
	}, nil
}

func newUsageGroupFilterRouter(repo *usageGroupFilterRepo, apiKey *service.APIKey) *gin.Engine {
	gin.SetMode(gin.TestMode)
	usageSvc := service.NewUsageService(repo, nil, nil, nil)
	apiKeySvc := service.NewAPIKeyService(&dashboardAPIKeyRepoStub{apiKey: apiKey}, nil, nil, nil, nil, nil, nil)
	handler := NewUsageHandler(usageSvc, apiKeySvc)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
		c.Next()
	})
	router.GET("/usage", handler.List)
	router.GET("/usage/stats", handler.Stats)
	router.GET("/usage/groups", handler.Groups)
	router.GET("/usage/dashboard/stats", handler.DashboardStats)
	router.GET("/usage/dashboard/trend", handler.DashboardTrend)
	router.GET("/usage/dashboard/models", handler.DashboardModels)
	return router
}

func serveUsageGroupFilter(router *gin.Engine, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestUserUsageGroupFilterRejectsInvalidGroupID(t *testing.T) {
	for _, path := range []string{"/usage", "/usage/stats", "/usage/dashboard/stats", "/usage/dashboard/trend", "/usage/dashboard/models"} {
		for _, raw := range []string{"abc", "0", "-1", "9223372036854775808"} {
			repo := &usageGroupFilterRepo{}
			rec := serveUsageGroupFilter(newUsageGroupFilterRouter(repo, nil), path+"?group_id="+raw)
			require.Equal(t, http.StatusBadRequest, rec.Code, "%s group_id=%s", path, raw)
			require.Empty(t, repo.calls)
		}
	}
}

func TestUserUsageListAndStatsScopeGroupToCurrentUser(t *testing.T) {
	repo := &usageGroupFilterRepo{}
	router := newUsageGroupFilterRouter(repo, nil)

	rec := serveUsageGroupFilter(router, "/usage?group_id=31")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(42), repo.listFilters.UserID)
	require.Equal(t, int64(31), repo.listFilters.GroupID)

	rec = serveUsageGroupFilter(router, "/usage/stats?start_date=2026-10-28&end_date=2026-10-28&group_id=31")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"stats"}, repo.calls)
	require.Equal(t, [3]int64{42, 0, 31}, [3]int64{repo.userID, repo.apiKeyID, repo.groupID})

	// Without a group the same user-scoped aggregate serves the unfiltered view.
	repo.calls = nil
	rec = serveUsageGroupFilter(router, "/usage/stats?period=week")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"stats"}, repo.calls)
	require.Equal(t, [3]int64{42, 0, 0}, [3]int64{repo.userID, repo.apiKeyID, repo.groupID})
}

func TestUserUsageStatsCombinesOwnedAPIKeyAndGroup(t *testing.T) {
	repo := &usageGroupFilterRepo{}
	router := newUsageGroupFilterRouter(repo, &service.APIKey{ID: 7, UserID: 42, Status: service.StatusAPIKeyActive})

	rec := serveUsageGroupFilter(router, "/usage/stats?period=today&api_key_id=7&group_id=31")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, [3]int64{42, 7, 31}, [3]int64{repo.userID, repo.apiKeyID, repo.groupID})
}

func TestUserUsageGroupsListsOnlyRealGroupsFromOwnLogs(t *testing.T) {
	repo := &usageGroupFilterRepo{}
	rec := serveUsageGroupFilter(newUsageGroupFilterRouter(repo, nil), "/usage/groups?start_date=2026-10-01&end_date=2026-10-28")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"groups"}, repo.calls)
	require.Equal(t, [4]int64{42, 0, 0, 0}, [4]int64{repo.userID, repo.apiKeyID, repo.accountID, repo.groupID})

	var payload struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, []map[string]any{
		{"id": float64(31), "name": "自建CCMax"},
		{"id": float64(12), "name": "retired"},
	}, payload.Data)
}

func TestUserDashboardGroupFilterKeepsUserScopeAndKeyState(t *testing.T) {
	repo := &usageGroupFilterRepo{}
	router := newUsageGroupFilterRouter(repo, &service.APIKey{ID: 7, UserID: 42, Status: "disabled"})

	rec := serveUsageGroupFilter(router, "/usage/dashboard/stats?api_key_id=7&group_id=31")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, [3]int64{42, 7, 31}, [3]int64{repo.userID, repo.apiKeyID, repo.groupID})
	var payload struct {
		Data usagestats.UserDashboardStats `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, int64(1), payload.Data.TotalAPIKeys)
	require.Equal(t, int64(0), payload.Data.ActiveAPIKeys, "a disabled key must not turn active under a group filter")

	for _, path := range []string{"/usage/dashboard/trend", "/usage/dashboard/models"} {
		repo.calls = nil
		rec = serveUsageGroupFilter(router, path+"?start_date=2026-10-22&end_date=2026-10-28&group_id=31")
		require.Equal(t, http.StatusOK, rec.Code, path)
		require.Len(t, repo.calls, 1, path)
		require.Equal(t, [4]int64{42, 0, 0, 31}, [4]int64{repo.userID, repo.apiKeyID, repo.accountID, repo.groupID}, path)
	}
}

func TestDashboardAPIKeyBreakdownPassesGroupFilter(t *testing.T) {
	repo := &apiKeyBreakdownUsageRepoCapture{}
	router := newAPIKeyBreakdownTestRouter(repo)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/usage/dashboard/api-key-breakdown?group_id=31", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(42), repo.userID)
	require.Equal(t, int64(31), repo.groupID)

	repo = &apiKeyBreakdownUsageRepoCapture{}
	router = newAPIKeyBreakdownTestRouter(repo)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/usage/dashboard/api-key-breakdown?group_id=0", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.False(t, repo.called)
}
