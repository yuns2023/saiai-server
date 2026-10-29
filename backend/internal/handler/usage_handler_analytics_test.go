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

type usageAnalyticsRepoCapture struct {
	service.UsageLogRepository
	called bool
	query  usagestats.UsageAnalyticsQuery
	result *usagestats.UsageAnalyticsResult
}

func (r *usageAnalyticsRepoCapture) GetUserUsageAnalytics(ctx context.Context, q usagestats.UsageAnalyticsQuery) (*usagestats.UsageAnalyticsResult, error) {
	r.called = true
	r.query = q
	if _, ok := ctx.Deadline(); !ok {
		panic("analytics query must run with a deadline")
	}
	if r.result != nil {
		return r.result, nil
	}
	return &usagestats.UsageAnalyticsResult{Items: []usagestats.UsageAnalyticsItem{}}, nil
}

func newUsageAnalyticsTestRouter(repo *usageAnalyticsRepoCapture) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewUsageHandler(service.NewUsageService(repo, nil, nil, nil), nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
		c.Next()
	})
	router.GET("/usage/analytics", handler.Analytics)
	return router
}

func serveUsageAnalytics(t *testing.T, repo *usageAnalyticsRepoCapture, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	newUsageAnalyticsTestRouter(repo).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/usage/analytics?"+query, nil))
	return rec
}

func TestUsageAnalyticsParsesFiltersAndScopesUser(t *testing.T) {
	repo := &usageAnalyticsRepoCapture{result: &usagestats.UsageAnalyticsResult{
		Items: []usagestats.UsageAnalyticsItem{{APIKeyID: 7, APIKeyName: "ci", Model: "gpt"}},
		Total: 101,
	}}
	rec := serveUsageAnalytics(t, repo,
		"start_date=2026-03-08&end_date=2026-03-08&timezone=America%2FNew_York&group_by=api_key_model&sort=tokens_desc"+
			"&page=3&page_size=50&api_key_id=7&group_id=9&model=%20gpt%20&reasoning_effort=%20HIGH%20&request_type=stream&billing_type=1")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	q := repo.query
	require.Equal(t, int64(42), q.UserID)
	require.Equal(t, usagestats.UsageAnalyticsByAPIKeyModel, q.GroupBy)
	require.Equal(t, usagestats.UsageAnalyticsSortTokens, q.Sort)
	require.Equal(t, 3, q.Page)
	require.Equal(t, 50, q.PageSize)
	require.Equal(t, int64(7), q.APIKeyID)
	require.Equal(t, int64(9), q.GroupID)
	require.Equal(t, "gpt", q.Model)
	require.Equal(t, "high", q.ReasoningEffort)
	require.NotNil(t, q.RequestType)
	require.Equal(t, int16(service.RequestTypeStream), *q.RequestType)
	require.NotNil(t, q.BillingType)
	require.Equal(t, service.BillingTypeSubscription, *q.BillingType)
	require.Equal(t, 23*time.Hour, q.EndTime.Sub(q.StartTime), "calendar-day range must stay DST-safe")

	var payload struct {
		Data struct {
			Pages    int    `json:"pages"`
			Timezone string `json:"timezone"`
			End      string `json:"end_exclusive"`
			Items    []map[string]any
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, 3, payload.Data.Pages)
	require.Equal(t, "America/New_York", payload.Data.Timezone)
	require.Equal(t, "2026-03-09T00:00:00-04:00", payload.Data.End)
	require.Equal(t, "ci", payload.Data.Items[0]["api_key_name"])
	require.NotContains(t, payload.Data.Items[0], "reasoning_effort")
	require.NotContains(t, payload.Data.Items[0], "key")
}

func TestUsageAnalyticsDefaultsToToday(t *testing.T) {
	repo := &usageAnalyticsRepoCapture{}
	rec := serveUsageAnalytics(t, repo, "timezone=UTC")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	today := time.Now().UTC().Format("2006-01-02")
	require.Equal(t, today, repo.query.StartTime.Format("2006-01-02"))
	require.Equal(t, 24*time.Hour, repo.query.EndTime.Sub(repo.query.StartTime))
	require.Equal(t, usagestats.UsageAnalyticsByModel, repo.query.GroupBy)
	require.Equal(t, usagestats.UsageAnalyticsSortActualCost, repo.query.Sort)
	require.Equal(t, 50, repo.query.PageSize)
}

func TestUsageAnalyticsAcceptsNinetyCalendarDaysAndMissingEffort(t *testing.T) {
	repo := &usageAnalyticsRepoCapture{}
	// 90 calendar days across a DST change.
	rec := serveUsageAnalytics(t, repo, "start_date=2026-08-04&end_date=2026-11-01&timezone=America%2FNew_York&reasoning_effort_missing=true&group_by=reasoning_effort")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, repo.query.ReasoningEffortMissing)
	require.Empty(t, repo.query.ReasoningEffort)
}

func TestUsageAnalyticsRejectsInvalidInput(t *testing.T) {
	cases := map[string]string{
		"bad timezone":         "timezone=Not%2FAZone",
		"bad start":            "start_date=2026-02-30",
		"bad end":              "end_date=yesterday",
		"inverted":             "start_date=2026-03-09&end_date=2026-03-08",
		"91 days":              "start_date=2026-08-03&end_date=2026-11-01&timezone=UTC",
		"bad group_by":         "group_by=account",
		"bad sort":             "sort=name_asc",
		"page zero":            "page=0",
		"page too large":       "page=10001",
		"page overflow":        "page=9223372036854775807",
		"page size too large":  "page_size=1001",
		"bad key":              "api_key_id=-1",
		"bad group":            "group_id=abc",
		"effort too long":      "reasoning_effort=abcdefghijklmnopqrstu",
		"effort and missing":   "reasoning_effort=high&reasoning_effort_missing=true",
		"bad missing flag":     "reasoning_effort_missing=maybe",
		"bad request type":     "request_type=batch",
		"billing type 2":       "billing_type=2",
		"billing type garbage": "billing_type=x",
	}
	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &usageAnalyticsRepoCapture{}
			rec := serveUsageAnalytics(t, repo, query)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.False(t, repo.called)
		})
	}
}

func TestUsageAnalyticsHandlesSkippedLocalMidnight(t *testing.T) {
	// America/Santiago skips 00:00-01:00 on 2026-09-06.
	repo := &usageAnalyticsRepoCapture{}
	rec := serveUsageAnalytics(t, repo, "start_date=2026-09-06&end_date=2026-09-06&timezone=America%2FSantiago")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "2026-09-06T01:00:00-03:00", repo.query.StartTime.Format(time.RFC3339))
	require.Equal(t, "2026-09-07T00:00:00-03:00", repo.query.EndTime.Format(time.RFC3339))
	require.Equal(t, 23*time.Hour, repo.query.EndTime.Sub(repo.query.StartTime))

	// The day before ends where the skipped day starts.
	repo = &usageAnalyticsRepoCapture{}
	rec = serveUsageAnalytics(t, repo, "start_date=2026-09-05&end_date=2026-09-05&timezone=America%2FSantiago")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "2026-09-06T01:00:00-03:00", repo.query.EndTime.Format(time.RFC3339))
	require.Equal(t, 24*time.Hour, repo.query.EndTime.Sub(repo.query.StartTime))

	repo = &usageAnalyticsRepoCapture{}
	rec = serveUsageAnalytics(t, repo, "start_date=2026-06-09&end_date=2026-09-06&timezone=America%2FSantiago")
	require.Equal(t, http.StatusOK, rec.Code, "exactly 90 calendar days: %s", rec.Body.String())
	rec = serveUsageAnalytics(t, &usageAnalyticsRepoCapture{}, "start_date=2026-06-08&end_date=2026-09-06&timezone=America%2FSantiago")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
