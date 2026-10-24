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

func TestSubtractTrendPointsClampsAndIgnoresUnknownBuckets(t *testing.T) {
	base := []TrendDataPoint{
		{Date: "2026-10-24", Requests: 10, InputTokens: 100, CacheReadTokens: 5, TotalTokens: 105, Cost: 1, ActualCost: 1},
		{Date: "2026-10-25", Requests: 1, OutputTokens: 5, TotalTokens: 5, Cost: 0.1},
	}
	hosted := []TrendDataPoint{
		{Date: "2026-10-24", Requests: 2, InputTokens: 20, CacheReadTokens: 7, TotalTokens: 27, Cost: 0.5},
		{Date: "2026-10-25", Requests: 3, OutputTokens: 30, TotalTokens: 30, Cost: 0.3},
		{Date: "2026-10-26", Requests: 9, TotalTokens: 9},
	}
	got := subtractTrendPoints(base, hosted)
	require.Equal(t, []TrendDataPoint{
		{Date: "2026-10-24", Requests: 8, InputTokens: 80, CacheReadTokens: 0, TotalTokens: 80, Cost: 0.5, ActualCost: 1},
		{Date: "2026-10-25", Requests: 0, OutputTokens: 0, TotalTokens: 0, Cost: 0},
	}, got)
	require.Equal(t, int64(10), base[0].Requests, "input slice must not be mutated")
	require.Equal(t, base, subtractTrendPoints(base, nil))
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

func TestGetUsageTrendWithFiltersReadsHostedInclusiveAggregatesAsIs(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

	// Only the bucket query: the admin usage page must not subtract or add
	// hosted rows (any further query would fail the mock).
	mock.ExpectQuery(`FROM usage_dashboard_daily`).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows(trendColumns).AddRow("2026-10-24", 6, 60, 0, 0, 0, 60, 0.6, 0.4))

	trend, err := repo.GetUsageTrendWithFilters(context.Background(), start, end, "day", 0, 0, 0, 0, "", nil, nil, nil, "")
	require.NoError(t, err)
	require.Equal(t, int64(6), trend[0].Requests)
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

func TestGetPlatformUsageTrendSubtractsAggregatedHostedRows(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	loc := timezone.Location()
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, loc)
	end := time.Date(2026, 10, 26, 0, 0, 0, 0, loc)
	watermark := time.Date(2026, 10, 25, 12, 0, 0, 0, loc)

	mock.ExpectQuery(`FROM usage_dashboard_aggregation_watermark`).
		WillReturnRows(sqlmock.NewRows([]string{"last_aggregated_at"}).AddRow(watermark))
	mock.ExpectQuery(`FROM usage_dashboard_daily`).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows(trendColumns).
			AddRow("2026-10-23", 5, 50, 0, 0, 0, 50, 0.5, 0.5).
			AddRow("2026-10-24", 6, 60, 0, 0, 0, 60, 0.6, 0.4))
	// Upper bound is the watermark: rows not yet aggregated are not subtracted.
	mock.ExpectQuery(`FROM usage_logs\s+WHERE created_at >= \$1 AND created_at < \$2 AND metered_cost IS NOT NULL GROUP BY date`).
		WithArgs(start, watermark).
		WillReturnRows(sqlmock.NewRows(trendColumns).AddRow("2026-10-24", 2, 20, 0, 0, 0, 20, 0.2, 0))

	trend, err := repo.GetPlatformUsageTrend(context.Background(), start, end, "day")
	require.NoError(t, err)
	require.Len(t, trend, 2)
	require.Equal(t, int64(5), trend[0].Requests)
	require.Equal(t, int64(4), trend[1].Requests)
	require.Equal(t, int64(40), trend[1].TotalTokens)
	require.InDelta(t, 0.4, trend[1].Cost, 1e-9)
	require.InDelta(t, 0.4, trend[1].ActualCost, 1e-9)
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

var hostedUsageCols = []string{"r", "i", "o", "cc", "cr", "tc", "ac", "d", "m", "r2", "i2", "o2", "cc2", "cr2", "tc2", "ac2", "d2", "m2"}

func hostedUsageRow(all, agg hostedUsage) *sqlmock.Rows {
	return sqlmock.NewRows(hostedUsageCols).AddRow(
		all.Requests, all.InputTokens, all.OutputTokens, all.CacheCreationTokens, all.CacheReadTokens, all.TotalCost, all.ActualCost, all.DurationMs, all.MeteredCost,
		agg.Requests, agg.InputTokens, agg.OutputTokens, agg.CacheCreationTokens, agg.CacheReadTokens, agg.TotalCost, agg.ActualCost, agg.DurationMs, agg.MeteredCost)
}

func hostedDayRows(days map[string]hostedUsage) *sqlmock.Rows {
	rows := sqlmock.NewRows(append([]string{"day"}, hostedUsageCols[:9]...))
	for day, u := range days {
		rows.AddRow(day, u.Requests, u.InputTokens, u.OutputTokens, u.CacheCreationTokens, u.CacheReadTokens, u.TotalCost, u.ActualCost, u.DurationMs, u.MeteredCost)
	}
	return rows
}

func dailyBucketRows(days map[string]hostedUsage) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"day", "r", "i", "o", "cc", "cr", "tc", "ac", "d"})
	for day, b := range days {
		rows.AddRow(day, b.Requests, b.InputTokens, b.OutputTokens, b.CacheCreationTokens, b.CacheReadTokens, b.TotalCost, b.ActualCost, b.DurationMs)
	}
	return rows
}

func TestApplyDashboardHostedStatsSubtractsPerDayAndMemoizesHistory(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	loc := timezone.Location()
	todayStart := time.Date(2026, 10, 26, 0, 0, 0, 0, loc)
	tomorrow := time.Date(2026, 10, 27, 0, 0, 0, 0, loc)
	watermark := todayStart.Add(10 * time.Hour)
	byDayQuery := `SELECT TO_CHAR\(created_at, 'YYYY-MM-DD'\) AS day`
	splitQuery := `FROM usage_logs\s+WHERE created_at >= \$1 AND created_at < \$2\s+AND metered_cost IS NOT NULL\s*$`
	bucketQuery := `FROM usage_dashboard_daily\s+WHERE bucket_date >= \$1::date`

	history := map[string]hostedUsage{
		"2026-10-24": {Requests: 6, InputTokens: 400, OutputTokens: 200, TotalCost: 1.0, DurationMs: 600, MeteredCost: 1.0},
		// Bucket for this day is smaller than its hosted rows (incomplete
		// coverage): only what the bucket holds may be subtracted.
		"2026-10-25": {Requests: 4, InputTokens: 200, OutputTokens: 200, TotalCost: 0.5, DurationMs: 400, MeteredCost: 0.5},
	}
	todayAll := hostedUsage{Requests: 3, InputTokens: 200, OutputTokens: 100, TotalCost: 0.3, DurationMs: 300, MeteredCost: 0.3}
	todayAgg := hostedUsage{Requests: 2, InputTokens: 150, OutputTokens: 50, TotalCost: 0.2, DurationMs: 200, MeteredCost: 0.2}
	buckets := map[string]hostedUsage{
		"2026-10-24": {Requests: 10, InputTokens: 500, OutputTokens: 300, TotalCost: 2, ActualCost: 1, DurationMs: 2000},
		"2026-10-25": {Requests: 1, InputTokens: 50, OutputTokens: 0, TotalCost: 0.1, DurationMs: 100},
		"2026-10-26": {Requests: 5, InputTokens: 300, OutputTokens: 100, TotalCost: 0.5, ActualCost: 0.3, DurationMs: 900},
	}

	mock.ExpectQuery(byDayQuery).WithArgs(usagestats.HostedUsageMeteringEpoch, todayStart).WillReturnRows(hostedDayRows(history))
	mock.ExpectQuery(splitQuery).WithArgs(todayStart, tomorrow, watermark).WillReturnRows(hostedUsageRow(todayAll, todayAgg))
	mock.ExpectQuery(bucketQuery).WithArgs(hostedLocalDayKey(usagestats.HostedUsageMeteringEpoch)).WillReturnRows(dailyBucketRows(buckets))
	// Second refresh: history from memory.
	mock.ExpectQuery(splitQuery).WithArgs(todayStart, tomorrow, watermark).WillReturnRows(hostedUsageRow(todayAll, todayAgg))
	mock.ExpectQuery(bucketQuery).WillReturnRows(dailyBucketRows(buckets))

	for i := 0; i < 2; i++ {
		stats := &DashboardStats{ // = Σ buckets
			TotalRequests: 16, TotalInputTokens: 850, TotalOutputTokens: 400, TotalCost: 2.6, TotalActualCost: 1.3,
			TodayRequests: 5, TodayInputTokens: 300, TodayOutputTokens: 100, TodayCost: 0.5, TodayActualCost: 0.3,
		}
		require.NoError(t, repo.applyDashboardHostedStatsAggregated(context.Background(), stats, todayStart, watermark, 3000))
		// applied: 10-24 {6,400,200,1.0,600} + 10-25 min {1,50,0,0.1,100} + today {2,150,50,0.2,200}
		require.Equal(t, int64(16-9), stats.TotalRequests)
		require.Equal(t, int64(850-600), stats.TotalInputTokens)
		require.Equal(t, int64(400-250), stats.TotalOutputTokens)
		require.InDelta(t, 2.6-1.3, stats.TotalCost, 1e-9)
		require.InDelta(t, 1.3, stats.TotalActualCost, 1e-9)
		require.InDelta(t, float64(3000-900)/7.0, stats.AverageDurationMs, 1e-9)
		require.Equal(t, int64(3), stats.TodayRequests)
		require.Equal(t, int64(150+50), stats.TodayTokens)
		require.InDelta(t, 0.3, stats.TodayCost, 1e-9)
		require.Equal(t, int64(13), stats.HostedTotalRequests) // live, not watermark-bound
		require.Equal(t, int64(1300), stats.HostedTotalTokens)
		require.InDelta(t, 1.8, stats.HostedTotalMeteredCost, 1e-9)
		require.Equal(t, int64(3), stats.HostedTodayRequests)
		require.InDelta(t, 0.3, stats.HostedTodayMeteredCost, 1e-9)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyDashboardHostedStatsBehindWatermarkOnlySubtractsAggregatedRows(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	todayStart := time.Date(2026, 10, 26, 0, 0, 0, 0, timezone.Location())
	watermark := todayStart.Add(-12 * time.Hour)
	byDayQuery := `SELECT TO_CHAR\(created_at, 'YYYY-MM-DD'\) AS day`

	mock.ExpectQuery(byDayQuery).WithArgs(usagestats.HostedUsageMeteringEpoch, todayStart).
		WillReturnRows(hostedDayRows(map[string]hostedUsage{"2026-10-25": {Requests: 4}}))
	mock.ExpectQuery(byDayQuery).WithArgs(usagestats.HostedUsageMeteringEpoch, watermark).
		WillReturnRows(hostedDayRows(map[string]hostedUsage{"2026-10-25": {Requests: 1}}))
	mock.ExpectQuery(`FROM usage_logs`).WillReturnRows(hostedUsageRow(hostedUsage{}, hostedUsage{}))
	mock.ExpectQuery(`FROM usage_dashboard_daily`).
		WillReturnRows(dailyBucketRows(map[string]hostedUsage{"2026-10-25": {Requests: 3}}))

	stats := &DashboardStats{TotalRequests: 3}
	require.NoError(t, repo.applyDashboardHostedStatsAggregated(context.Background(), stats, todayStart, watermark, 0))
	require.Equal(t, int64(2), stats.TotalRequests)
	require.Equal(t, int64(4), stats.HostedTotalRequests)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyDashboardHostedStatsWithoutHostedRowsSkipsBucketRead(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	todayStart := time.Date(2026, 10, 26, 0, 0, 0, 0, timezone.Location())
	mock.ExpectQuery(`SELECT TO_CHAR\(created_at`).WillReturnRows(hostedDayRows(nil))
	mock.ExpectQuery(`FROM usage_logs`).WillReturnRows(hostedUsageRow(hostedUsage{}, hostedUsage{}))

	stats := &DashboardStats{TotalRequests: 5, TotalInputTokens: 10}
	require.NoError(t, repo.applyDashboardHostedStatsAggregated(context.Background(), stats, todayStart, todayStart.Add(time.Hour), 50))
	require.Equal(t, int64(5), stats.TotalRequests)
	require.Equal(t, int64(10), stats.TotalTokens)
	require.InDelta(t, 10.0, stats.AverageDurationMs, 1e-9)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueryHostedUsageSkipsRangesBeforeEpoch(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	all, agg, err := repo.queryHostedUsage(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), usagestats.HostedUsageMeteringEpoch, time.Now())
	require.NoError(t, err)
	require.Equal(t, hostedUsage{}, all)
	require.Equal(t, hostedUsage{}, agg)
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
