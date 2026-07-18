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

type apiKeyBreakdownUsageRepoCapture struct {
	service.UsageLogRepository
	called    bool
	userID    int64
	startTime time.Time
	endTime   time.Time
	params    pagination.PaginationParams
	sort      string
}

type dashboardAPIKeyRepoStub struct {
	service.APIKeyRepository
	apiKey *service.APIKey
}

func (r *dashboardAPIKeyRepoStub) GetByID(_ context.Context, _ int64) (*service.APIKey, error) {
	return r.apiKey, nil
}

func (r *apiKeyBreakdownUsageRepoCapture) GetUserAPIKeyUsageBreakdown(
	_ context.Context,
	userID int64,
	startTime, endTime time.Time,
	params pagination.PaginationParams,
	sort string,
) (*usagestats.APIKeyUsageBreakdownResult, error) {
	r.called = true
	r.userID = userID
	r.startTime = startTime
	r.endTime = endTime
	r.params = params
	r.sort = sort
	return &usagestats.APIKeyUsageBreakdownResult{
		Items: []usagestats.APIKeyUsageBreakdownItem{{
			APIKeyID:   7,
			KeyName:    "build-agent",
			Status:     service.StatusAPIKeyActive,
			Requests:   3,
			ActualCost: 1.25,
		}},
		Total: 1,
		Summary: usagestats.APIKeyUsageBreakdownSummary{
			Requests:   3,
			ActualCost: 1.25,
		},
	}, nil
}

func newAPIKeyBreakdownTestRouter(repo *apiKeyBreakdownUsageRepoCapture) *gin.Engine {
	gin.SetMode(gin.TestMode)
	usageSvc := service.NewUsageService(repo, nil, nil, nil)
	handler := NewUsageHandler(usageSvc, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
		c.Next()
	})
	router.GET("/usage/dashboard/api-key-breakdown", handler.DashboardAPIKeyBreakdown)
	return router
}

func TestDashboardAPIKeyBreakdownScopesRangePaginationAndResponse(t *testing.T) {
	repo := &apiKeyBreakdownUsageRepoCapture{}
	router := newAPIKeyBreakdownTestRouter(repo)
	req := httptest.NewRequest(
		http.MethodGet,
		"/usage/dashboard/api-key-breakdown?start_date=2026-03-08&end_date=2026-03-08&timezone=America%2FNew_York&page=2&page_size=500&sort=requests_desc",
		nil,
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, repo.called)
	require.Equal(t, int64(42), repo.userID)
	require.Equal(t, 2, repo.params.Page)
	require.Equal(t, 100, repo.params.PageSize)
	require.Equal(t, "requests_desc", repo.sort)
	require.Equal(t, 23*time.Hour, repo.endTime.Sub(repo.startTime), "calendar-day range must remain DST-safe")
	require.Equal(t, "2026-03-08", repo.startTime.Format("2006-01-02"))
	require.Equal(t, "2026-03-09", repo.endTime.Format("2006-01-02"))

	var payload struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	require.Equal(t, "build-agent", payload.Data.Items[0]["key_name"])
	require.NotContains(t, payload.Data.Items[0], "key")
}

func TestDashboardAPIKeyBreakdownRejectsUnknownSort(t *testing.T) {
	repo := &apiKeyBreakdownUsageRepoCapture{}
	router := newAPIKeyBreakdownTestRouter(repo)
	req := httptest.NewRequest(
		http.MethodGet,
		"/usage/dashboard/api-key-breakdown?sort=actual_cost_desc%3BDROP+TABLE+api_keys",
		nil,
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.False(t, repo.called)
}

func TestDashboardKeyFilterRejectsAnotherUsersKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	usageRepo := &apiKeyBreakdownUsageRepoCapture{}
	usageSvc := service.NewUsageService(usageRepo, nil, nil, nil)
	apiKeySvc := service.NewAPIKeyService(
		&dashboardAPIKeyRepoStub{apiKey: &service.APIKey{ID: 7, UserID: 99}},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	handler := NewUsageHandler(usageSvc, apiKeySvc)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
		c.Next()
	})
	router.GET("/usage/dashboard/trend", handler.DashboardTrend)

	req := httptest.NewRequest(http.MethodGet, "/usage/dashboard/trend?api_key_id=7", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
}
