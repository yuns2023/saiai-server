package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

var trendColumns = []string{"date", "requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_tokens", "cost", "actual_cost"}

func TestHostedScopeClause(t *testing.T) {
	require.Equal(t, "", hostedScopeAll.clause("metered_cost"))
	require.Equal(t, " AND ul.metered_cost IS NULL", hostedScopeExclude.clause("ul.metered_cost"))
	require.Equal(t, " AND metered_cost IS NOT NULL", hostedScopeOnly.clause("metered_cost"))
}

func TestMergeTrendPointsSumsAndSortsUnion(t *testing.T) {
	base := []TrendDataPoint{
		{Date: "2026-10-24", Requests: 10, InputTokens: 100, TotalTokens: 100, Cost: 1, ActualCost: 1},
		{Date: "2026-10-26", Requests: 1, TotalTokens: 5},
	}
	extra := []TrendDataPoint{
		{Date: "2026-10-25", Requests: 3, OutputTokens: 30, TotalTokens: 30, Cost: 0.3},
		{Date: "2026-10-24", Requests: 2, InputTokens: 20, CacheReadTokens: 7, TotalTokens: 27, Cost: 0.5},
	}
	merged := mergeTrendPoints(base, extra)
	require.Equal(t, []TrendDataPoint{
		{Date: "2026-10-24", Requests: 12, InputTokens: 120, CacheReadTokens: 7, TotalTokens: 127, Cost: 1.5, ActualCost: 1},
		{Date: "2026-10-25", Requests: 3, OutputTokens: 30, TotalTokens: 30, Cost: 0.3},
		{Date: "2026-10-26", Requests: 1, TotalTokens: 5},
	}, merged)
	require.Equal(t, base, mergeTrendPoints(base, nil))
}

func TestHostedTrendBoundsMatchAggregateBuckets(t *testing.T) {
	loc := timezone.Location()
	start := time.Date(2026, 10, 24, 10, 15, 0, 0, loc)
	end := time.Date(2026, 10, 25, 10, 15, 0, 0, loc)

	// Hourly buckets are selected by bucket_start in [start, end): the partial
	// first hour is not part of any selected bucket, the partial last one is.
	lower, upper := hostedTrendBounds(start, end, "hour")
	require.True(t, lower.Equal(time.Date(2026, 10, 24, 11, 0, 0, 0, loc)), lower)
	require.True(t, upper.Equal(time.Date(2026, 10, 25, 11, 0, 0, 0, loc)), upper)

	// Daily buckets are selected by bucket_date in [start::date, end::date).
	lower, upper = hostedTrendBounds(start, end, "day")
	require.True(t, lower.Equal(time.Date(2026, 10, 24, 0, 0, 0, 0, loc)), lower)
	require.True(t, upper.Equal(time.Date(2026, 10, 25, 0, 0, 0, 0, loc)), upper)

	// Daily dates follow each parameter's own calendar date (lib/pq text
	// encoding + ::date), interpreted as app-timezone days.
	other := time.FixedZone("UTC-11", -11*3600)
	lower, upper = hostedTrendBounds(time.Date(2026, 10, 24, 23, 0, 0, 0, other), time.Date(2026, 10, 26, 23, 0, 0, 0, other), "day")
	require.True(t, lower.Equal(time.Date(2026, 10, 24, 0, 0, 0, 0, loc)), lower)
	require.True(t, upper.Equal(time.Date(2026, 10, 26, 0, 0, 0, 0, loc)), upper)

	// Never look before the hosted metering epoch.
	lower, _ = hostedTrendBounds(time.Date(2026, 1, 1, 0, 0, 0, 0, loc), end, "day")
	require.True(t, lower.Equal(usagestats.HostedUsageMeteringEpoch), lower)
}

func TestGetUsageTrendWithFiltersAddsHostedRowsBackToAggregates(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	loc := timezone.Location()
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, loc)
	end := time.Date(2026, 10, 26, 0, 0, 0, 0, loc)

	mock.ExpectQuery(`FROM usage_dashboard_daily`).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows(trendColumns).
			AddRow("2026-10-23", 5, 50, 0, 0, 0, 50, 0.5, 0.5).
			AddRow("2026-10-24", 4, 40, 0, 0, 0, 40, 0.4, 0.4))
	mock.ExpectQuery(`FROM usage_logs\s+WHERE created_at >= \$1 AND created_at < \$2 AND metered_cost IS NOT NULL GROUP BY date`).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows(trendColumns).
			AddRow("2026-10-24", 2, 20, 0, 0, 0, 20, 0.2, 0).
			AddRow("2026-10-25", 1, 10, 0, 0, 0, 10, 0.1, 0))

	trend, err := repo.GetUsageTrendWithFilters(context.Background(), start, end, "day", 0, 0, 0, 0, "", nil, nil, nil, "")
	require.NoError(t, err)
	require.Len(t, trend, 3)
	require.Equal(t, int64(5), trend[0].Requests)
	require.Equal(t, int64(6), trend[1].Requests)
	require.InDelta(t, 0.6, trend[1].Cost, 1e-9)
	require.InDelta(t, 0.4, trend[1].ActualCost, 1e-9)
	require.Equal(t, "2026-10-25", trend[2].Date)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetUsageTrendWithFiltersFilteredQueryKeepsHostedRows(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery(`FROM usage_logs\s+WHERE created_at >= \$1 AND created_at < \$2 AND user_id = \$3 GROUP BY date`).
		WithArgs(start, end, int64(7)).
		WillReturnRows(sqlmock.NewRows(trendColumns))

	_, err := repo.GetUsageTrendWithFilters(context.Background(), start, end, "day", 7, 0, 0, 0, "", nil, nil, nil, "")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetPlatformUsageTrendUsesAggregatesWithoutHostedMerge(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery(`FROM usage_dashboard_hourly`).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows(trendColumns).AddRow("2026-10-24 01:00", 3, 30, 0, 0, 0, 30, 0.3, 0.3))

	trend, err := repo.GetPlatformUsageTrend(context.Background(), start, end, "hour")
	require.NoError(t, err)
	require.Len(t, trend, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetPlatformUsageTrendFallbackExcludesHosted(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	// 15m granularity never uses the aggregates.
	mock.ExpectQuery(`FROM usage_logs\s+WHERE created_at >= \$1 AND created_at < \$2 AND metered_cost IS NULL GROUP BY date`).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows(trendColumns))

	_, err := repo.GetPlatformUsageTrend(context.Background(), start, end, "15m")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetPlatformModelStatsExcludesHosted(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery(`FROM usage_logs\s+WHERE created_at >= \$1 AND created_at < \$2 AND metered_cost IS NULL GROUP BY`).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"model", "requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_tokens", "cost", "actual_cost"}))

	_, err := repo.GetPlatformModelStats(context.Background(), start, end)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFillDashboardHostedStatsAggregatedMemoizesCompletedDays(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	todayStart := timezone.StartOfDay(time.Date(2026, 10, 26, 12, 0, 0, 0, timezone.Location()))
	todayEnd := timezone.StartOfDay(todayStart.AddDate(0, 0, 1).Add(time.Hour))
	hostedCols := []string{"count", "tokens", "metered"}
	hostedQuery := `FROM usage_logs\s+WHERE created_at >= \$1 AND created_at < \$2\s+AND metered_cost IS NOT NULL`

	mock.ExpectQuery(hostedQuery).
		WithArgs(usagestats.HostedUsageMeteringEpoch, todayStart).
		WillReturnRows(sqlmock.NewRows(hostedCols).AddRow(10, 1000, 1.5))
	mock.ExpectQuery(hostedQuery).
		WithArgs(todayStart, todayEnd).
		WillReturnRows(sqlmock.NewRows(hostedCols).AddRow(2, 200, 0.25))
	// Second refresh: history comes from memory, only today is queried.
	mock.ExpectQuery(hostedQuery).
		WithArgs(todayStart, todayEnd).
		WillReturnRows(sqlmock.NewRows(hostedCols).AddRow(3, 300, 0.5))

	stats := &DashboardStats{}
	require.NoError(t, repo.fillDashboardHostedStatsAggregated(context.Background(), stats, todayStart))
	require.Equal(t, int64(12), stats.HostedTotalRequests)
	require.Equal(t, int64(1200), stats.HostedTotalTokens)
	require.InDelta(t, 1.75, stats.HostedTotalMeteredCost, 1e-9)
	require.Equal(t, int64(2), stats.HostedTodayRequests)
	require.Equal(t, int64(200), stats.HostedTodayTokens)
	require.InDelta(t, 0.25, stats.HostedTodayMeteredCost, 1e-9)

	stats = &DashboardStats{}
	require.NoError(t, repo.fillDashboardHostedStatsAggregated(context.Background(), stats, todayStart))
	require.Equal(t, int64(13), stats.HostedTotalRequests)
	require.Equal(t, int64(3), stats.HostedTodayRequests)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueryHostedTotalsSkipsRangesBeforeEpoch(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	totals, err := repo.queryHostedTotals(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), usagestats.HostedUsageMeteringEpoch)
	require.NoError(t, err)
	require.Equal(t, hostedTotals{}, totals)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetUserBreakdownStatsExcludeHosted(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery(`AND ul.metered_cost IS NULL GROUP BY ul.user_id, u.email`).
		WithArgs(start, end, "gpt-5").
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "email", "requests", "total_tokens", "cost", "actual_cost"}))

	_, err := repo.GetUserBreakdownStats(context.Background(), start, end, usagestats.UserBreakdownDimension{Model: "gpt-5", ModelType: usagestats.ModelSourceRequested, ExcludeHosted: true}, 10)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCeilToLocalHourKeepsRepeatedDSTHour(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	// 2026-11-01 01:30 EST (second occurrence, UTC-5) = 06:30Z.
	second := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC).In(ny)
	require.True(t, ceilToLocalHour(second).Equal(time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)))
	// First occurrence 01:30 EDT (UTC-4) = 05:30Z.
	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC).In(ny)
	require.True(t, ceilToLocalHour(first).Equal(time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)))
	aligned := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC).In(ny)
	require.True(t, ceilToLocalHour(aligned).Equal(aligned))
}
