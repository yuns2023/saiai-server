//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type usageTokenTotalsRepoStub struct{ service.UsageLogRepository }

func (usageTokenTotalsRepoStub) GetAPIKeyDashboardStats(context.Context, int64) (*usagestats.UserDashboardStats, error) {
	return &usagestats.UserDashboardStats{}, nil
}

func (usageTokenTotalsRepoStub) GetAPIKeyStatsAggregated(context.Context, int64, time.Time, time.Time) (*usagestats.UsageStats, error) {
	return &usagestats.UsageStats{
		TotalInputTokens: 14906, TotalOutputTokens: 54, TotalCacheReadTokens: 6500,
		TotalCacheCreationTokens: 160, TotalCacheTokens: 6660, TotalTokens: 21620,
	}, nil
}

func (s usageTokenTotalsRepoStub) GetUserStatsAggregated(ctx context.Context, id int64, start, end time.Time) (*usagestats.UsageStats, error) {
	return s.GetAPIKeyStatsAggregated(ctx, id, start, end)
}

func (s usageTokenTotalsRepoStub) GetUserStatsAggregatedWithFilters(ctx context.Context, id, _, _ int64, start, end time.Time) (*usagestats.UsageStats, error) {
	return s.GetAPIKeyStatsAggregated(ctx, id, start, end)
}

func TestGatewayUsageRangeIncludesCacheBreakdown(t *testing.T) {
	svc := service.NewUsageService(usageTokenTotalsRepoStub{}, nil, nil, nil)
	h := &GatewayHandler{usageService: svc}
	data := h.buildUsageData(context.Background(), 7, time.Now().Add(-time.Hour), time.Now())
	stats := data["range"].(gin.H)
	require.Equal(t, int64(6500), stats["cache_read_tokens"])
	require.Equal(t, int64(160), stats["cache_creation_tokens"])
	require.Equal(t, int64(6660), stats["cache_tokens"])
	require.Equal(t, int64(21620), stats["total_tokens"])
}

func TestUserUsageStatsIncludesCacheBreakdown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := service.NewUsageService(usageTokenTotalsRepoStub{}, nil, nil, nil)
	h := NewUsageHandler(svc, nil)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/usage/stats?period=week", nil)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
	h.Stats(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data service.UsageStats `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	stats := response.Data
	require.Equal(t, int64(6500), stats.TotalCacheReadTokens)
	require.Equal(t, int64(160), stats.TotalCacheCreationTokens)
	require.Equal(t, int64(21620), stats.TotalTokens)
	require.Equal(t, stats.TotalTokens, stats.TotalInputTokens+stats.TotalOutputTokens+stats.TotalCacheReadTokens+stats.TotalCacheCreationTokens)
}

type usageHostedTotalsRepoStub struct {
	usageTokenTotalsRepoStub
	metered *float64
}

func (s usageHostedTotalsRepoStub) GetUserStatsAggregatedWithFilters(context.Context, int64, int64, int64, time.Time, time.Time) (*usagestats.UsageStats, error) {
	return &usagestats.UsageStats{TotalRequests: 1, TotalCost: 1.25, TotalMeteredCost: s.metered}, nil
}

func TestUserUsageStatsExposesHostedMeteredTotalOnlyWhenPresent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	metered := 1.25
	for name, value := range map[string]*float64{"hosted": &metered, "none": nil} {
		t.Run(name, func(t *testing.T) {
			svc := service.NewUsageService(usageHostedTotalsRepoStub{metered: value}, nil, nil, nil)
			h := NewUsageHandler(svc, nil)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/usage/stats?period=week", nil)
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
			h.Stats(c)
			require.Equal(t, http.StatusOK, recorder.Code)
			var response struct {
				Data map[string]any `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			got, ok := response.Data["total_metered_cost"]
			if value == nil {
				require.False(t, ok, "total_metered_cost must be omitted without hosted rows")
				return
			}
			require.True(t, ok)
			require.InDelta(t, 1.25, got, 1e-9)
		})
	}
}
