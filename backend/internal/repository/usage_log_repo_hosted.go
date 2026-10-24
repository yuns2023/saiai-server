package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// hostedScope selects which usage_logs rows a dashboard query covers. A row
// is hosted iff metered_cost IS NOT NULL (see service.applyHostedUsage).
type hostedScope int

const (
	hostedScopeAll hostedScope = iota
	hostedScopeExclude
	hostedScopeOnly
)

// clause returns the SQL predicate (with leading AND) for the scope. column is
// chosen internally, never from a request parameter.
func (s hostedScope) clause(column string) string {
	switch s {
	case hostedScopeExclude:
		return " AND " + column + " IS NULL"
	case hostedScopeOnly:
		return " AND " + column + " IS NOT NULL"
	default:
		return ""
	}
}

// hostedUsage sums hosted rows (metered_cost IS NOT NULL) in the same units as
// the pre-aggregated dashboard buckets, so it can be subtracted from them.
type hostedUsage struct {
	Requests            int64
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	TotalCost           float64
	ActualCost          float64
	DurationMs          int64
	MeteredCost         float64
}

func (h hostedUsage) add(o hostedUsage) hostedUsage {
	return hostedUsage{
		Requests:            h.Requests + o.Requests,
		InputTokens:         h.InputTokens + o.InputTokens,
		OutputTokens:        h.OutputTokens + o.OutputTokens,
		CacheCreationTokens: h.CacheCreationTokens + o.CacheCreationTokens,
		CacheReadTokens:     h.CacheReadTokens + o.CacheReadTokens,
		TotalCost:           h.TotalCost + o.TotalCost,
		ActualCost:          h.ActualCost + o.ActualCost,
		DurationMs:          h.DurationMs + o.DurationMs,
		MeteredCost:         h.MeteredCost + o.MeteredCost,
	}
}

func (h hostedUsage) tokens() int64 {
	return h.InputTokens + h.OutputTokens + h.CacheCreationTokens + h.CacheReadTokens
}

func subtractClampInt(v, d int64) int64 {
	if v -= d; v < 0 {
		return 0
	}
	return v
}

func subtractClampFloat(v, d float64) float64 {
	if v -= d; v < 0 {
		return 0
	}
	return v
}

// hostedHistoryTTL bounds how stale the cached completed-days hosted usage may
// get (late rows near midnight, retention cleanup).
const hostedHistoryTTL = time.Hour

// hostedHistoryCache memoizes per-day hosted usage of completed days
// [HostedUsageMeteringEpoch, todayStart). The admin dashboard refreshes stats
// every ~15s; without this every refresh would range-scan all usage_logs since
// the epoch, while with it only today's rows are scanned per refresh.
type hostedHistoryCache struct {
	mu         sync.Mutex
	dayStart   time.Time
	computedAt time.Time
	byDay      map[string]hostedUsage
}

func (c *hostedHistoryCache) get(dayStart, now time.Time) (map[string]hostedUsage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.computedAt.IsZero() || !c.dayStart.Equal(dayStart) || now.Sub(c.computedAt) > hostedHistoryTTL {
		return nil, false
	}
	return c.byDay, true
}

func (c *hostedHistoryCache) set(dayStart, now time.Time, byDay map[string]hostedUsage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dayStart, c.computedAt, c.byDay = dayStart, now, byDay
}

// minEach returns the field-wise minimum, i.e. how much of h can actually be
// subtracted from bucket b without going below zero.
func minEach(b, h hostedUsage) hostedUsage {
	return hostedUsage{
		Requests:            min(b.Requests, h.Requests),
		InputTokens:         min(b.InputTokens, h.InputTokens),
		OutputTokens:        min(b.OutputTokens, h.OutputTokens),
		CacheCreationTokens: min(b.CacheCreationTokens, h.CacheCreationTokens),
		CacheReadTokens:     min(b.CacheReadTokens, h.CacheReadTokens),
		TotalCost:           min(b.TotalCost, h.TotalCost),
		ActualCost:          min(b.ActualCost, h.ActualCost),
		DurationMs:          min(b.DurationMs, h.DurationMs),
	}
}

func sumHostedDays(byDay map[string]hostedUsage) hostedUsage {
	var total hostedUsage
	for _, u := range byDay {
		total = total.add(u)
	}
	return total
}

const hostedUsageColumns = `
			COUNT(*) FILTER (WHERE %[1]s),
			COALESCE(SUM(input_tokens) FILTER (WHERE %[1]s), 0),
			COALESCE(SUM(output_tokens) FILTER (WHERE %[1]s), 0),
			COALESCE(SUM(cache_creation_tokens) FILTER (WHERE %[1]s), 0),
			COALESCE(SUM(cache_read_tokens) FILTER (WHERE %[1]s), 0),
			COALESCE(SUM(total_cost) FILTER (WHERE %[1]s), 0),
			COALESCE(SUM(actual_cost) FILTER (WHERE %[1]s), 0),
			COALESCE(SUM(COALESCE(duration_ms, 0)) FILTER (WHERE %[1]s), 0),
			COALESCE(SUM(metered_cost) FILTER (WHERE %[1]s), 0)`

func (h *hostedUsage) scanTargets() []any {
	return []any{&h.Requests, &h.InputTokens, &h.OutputTokens, &h.CacheCreationTokens, &h.CacheReadTokens, &h.TotalCost, &h.ActualCost, &h.DurationMs, &h.MeteredCost}
}

// queryHostedUsage sums hosted rows in [start, end), clamped to the hosted
// metering epoch so the scan stays on the created_at index range. It returns
// all rows (for the hosted line) and the subset created before aggregatedUntil
// (what the pre-aggregated buckets already contain, for subtraction).
func (r *usageLogRepository) queryHostedUsage(ctx context.Context, start, end, aggregatedUntil time.Time) (all, aggregated hostedUsage, err error) {
	if start.Before(usagestats.HostedUsageMeteringEpoch) {
		start = usagestats.HostedUsageMeteringEpoch
	}
	if !end.After(start) {
		return all, aggregated, nil
	}
	query := `
		SELECT` + fmt.Sprintf(hostedUsageColumns, "TRUE") + `,` + fmt.Sprintf(hostedUsageColumns, "created_at < $3") + `
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
			AND metered_cost IS NOT NULL
	`
	dest := append(all.scanTargets(), aggregated.scanTargets()...)
	err = scanSingleRow(ctx, r.sql, query, []any{start, end, aggregatedUntil}, dest...)
	return all, aggregated, err
}

// dashboardAggregationWatermark returns the time up to which the scheduled
// aggregation has folded usage_logs into the buckets (epoch if never run).
func (r *usageLogRepository) dashboardAggregationWatermark(ctx context.Context) (time.Time, error) {
	var ts time.Time
	err := scanSingleRow(ctx, r.sql, "SELECT last_aggregated_at FROM usage_dashboard_aggregation_watermark WHERE id = 1", nil, &ts)
	if err == sql.ErrNoRows {
		return time.Unix(0, 0).UTC(), nil
	}
	return ts, err
}

// hostedLocalDayKey formats t as the app-timezone calendar day, matching both
// TO_CHAR(created_at, 'YYYY-MM-DD') (session TimeZone = app timezone) and
// usage_dashboard_daily.bucket_date.
func hostedLocalDayKey(t time.Time) string {
	return t.In(timezone.Location()).Format("2006-01-02")
}

// queryHostedUsageByDay groups hosted rows in [start, end) by app-timezone day.
func (r *usageLogRepository) queryHostedUsageByDay(ctx context.Context, start, end time.Time) (byDay map[string]hostedUsage, err error) {
	byDay = map[string]hostedUsage{}
	if start.Before(usagestats.HostedUsageMeteringEpoch) {
		start = usagestats.HostedUsageMeteringEpoch
	}
	if !end.After(start) {
		return byDay, nil
	}
	query := `
		SELECT TO_CHAR(created_at, 'YYYY-MM-DD') AS day,` + fmt.Sprintf(hostedUsageColumns, "TRUE") + `
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
			AND metered_cost IS NOT NULL
		GROUP BY day
	`
	rows, err := r.sql.QueryContext(ctx, query, start, end)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err, byDay = closeErr, nil
		}
	}()
	for rows.Next() {
		var day string
		var u hostedUsage
		if err = rows.Scan(append([]any{&day}, u.scanTargets()...)...); err != nil {
			return nil, err
		}
		byDay[day] = u
	}
	return byDay, rows.Err()
}

// queryDailyBucketsSinceHostedEpoch loads the (hosted-inclusive) daily buckets
// that can contain hosted rows, keyed like hostedLocalDayKey.
func (r *usageLogRepository) queryDailyBucketsSinceHostedEpoch(ctx context.Context) (buckets map[string]hostedUsage, err error) {
	buckets = map[string]hostedUsage{}
	// Bind a plain calendar date: a time.Time would be cast with its own offset.
	rows, err := r.sql.QueryContext(ctx, `
		SELECT TO_CHAR(bucket_date::timestamp, 'YYYY-MM-DD'), total_requests, input_tokens, output_tokens,
			cache_creation_tokens, cache_read_tokens, total_cost, actual_cost, total_duration_ms
		FROM usage_dashboard_daily
		WHERE bucket_date >= $1::date
	`, hostedLocalDayKey(usagestats.HostedUsageMeteringEpoch))
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err, buckets = closeErr, nil
		}
	}()
	for rows.Next() {
		var day string
		var b hostedUsage
		if err = rows.Scan(&day, &b.Requests, &b.InputTokens, &b.OutputTokens, &b.CacheCreationTokens, &b.CacheReadTokens, &b.TotalCost, &b.ActualCost, &b.DurationMs); err != nil {
			return nil, err
		}
		buckets[day] = b
	}
	return buckets, rows.Err()
}

// applyDashboardHostedStatsAggregated turns the hosted-inclusive bucket totals
// read by fillDashboardUsageStatsAggregated into platform figures and fills
// the separate hosted line:
//
//	platform = buckets - Σ_day min(daily bucket, hosted rows of that day
//	                              already aggregated, i.e. created_at < watermark)
//
// watermark must be read BEFORE the buckets: buckets can then only contain
// more than the watermark says (rows aggregated after it was read), which
// briefly overstates platform usage until the next refresh but never
// subtracts hosted usage the fetched buckets do not contain. Per-day clamping
// keeps a day whose bucket is missing or incomplete from eating other days'
// platform usage.
//
// Hosted rows come from usage_logs, so once raw logs are purged by retention
// the subtraction for those old buckets is 0 and their platform figures fall
// back to hosted-inclusive (the pre-hosted-split behaviour). Likewise the
// hosted "total" only covers the raw-log retention window. Active users keep
// counting hosted users.
func (r *usageLogRepository) applyDashboardHostedStatsAggregated(ctx context.Context, stats *DashboardStats, todayStart, watermark time.Time, bucketDurationMs int64) error {
	now := time.Now()
	tomorrow := timezone.StartOfDay(todayStart.AddDate(0, 0, 1).Add(time.Hour))

	historyAll, ok := r.hostedHistory.get(todayStart, now)
	if !ok {
		var err error
		if historyAll, err = r.queryHostedUsageByDay(ctx, usagestats.HostedUsageMeteringEpoch, todayStart); err != nil {
			return err
		}
		r.hostedHistory.set(todayStart, now, historyAll)
	}
	historyAgg := historyAll
	if watermark.Before(todayStart) {
		// Aggregation is behind: only subtract what it has folded in.
		var err error
		if historyAgg, err = r.queryHostedUsageByDay(ctx, usagestats.HostedUsageMeteringEpoch, watermark); err != nil {
			return err
		}
	}
	todayAll, todayAgg, err := r.queryHostedUsage(ctx, todayStart, tomorrow, watermark)
	if err != nil {
		return err
	}

	var appliedTotal, appliedToday hostedUsage
	if hostedAny := len(historyAgg) > 0 || todayAgg.Requests > 0; hostedAny {
		buckets, err := r.queryDailyBucketsSinceHostedEpoch(ctx)
		if err != nil {
			return err
		}
		for day, h := range historyAgg {
			if b, ok := buckets[day]; ok {
				appliedTotal = appliedTotal.add(minEach(b, h))
			}
		}
		if b, ok := buckets[hostedLocalDayKey(todayStart)]; ok {
			appliedToday = minEach(b, todayAgg)
			appliedTotal = appliedTotal.add(appliedToday)
		}
	}

	stats.TotalRequests = subtractClampInt(stats.TotalRequests, appliedTotal.Requests)
	stats.TotalInputTokens = subtractClampInt(stats.TotalInputTokens, appliedTotal.InputTokens)
	stats.TotalOutputTokens = subtractClampInt(stats.TotalOutputTokens, appliedTotal.OutputTokens)
	stats.TotalCacheCreationTokens = subtractClampInt(stats.TotalCacheCreationTokens, appliedTotal.CacheCreationTokens)
	stats.TotalCacheReadTokens = subtractClampInt(stats.TotalCacheReadTokens, appliedTotal.CacheReadTokens)
	stats.TotalCost = subtractClampFloat(stats.TotalCost, appliedTotal.TotalCost)
	stats.TotalActualCost = subtractClampFloat(stats.TotalActualCost, appliedTotal.ActualCost)
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens
	stats.AverageDurationMs = 0
	if stats.TotalRequests > 0 {
		stats.AverageDurationMs = float64(subtractClampInt(bucketDurationMs, appliedTotal.DurationMs)) / float64(stats.TotalRequests)
	}

	stats.TodayRequests = subtractClampInt(stats.TodayRequests, appliedToday.Requests)
	stats.TodayInputTokens = subtractClampInt(stats.TodayInputTokens, appliedToday.InputTokens)
	stats.TodayOutputTokens = subtractClampInt(stats.TodayOutputTokens, appliedToday.OutputTokens)
	stats.TodayCacheCreationTokens = subtractClampInt(stats.TodayCacheCreationTokens, appliedToday.CacheCreationTokens)
	stats.TodayCacheReadTokens = subtractClampInt(stats.TodayCacheReadTokens, appliedToday.CacheReadTokens)
	stats.TodayCost = subtractClampFloat(stats.TodayCost, appliedToday.TotalCost)
	stats.TodayActualCost = subtractClampFloat(stats.TodayActualCost, appliedToday.ActualCost)
	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens

	// The hosted line is live (not limited to the aggregation watermark).
	totalAll := sumHostedDays(historyAll).add(todayAll)
	stats.HostedTotalRequests = totalAll.Requests
	stats.HostedTotalTokens = totalAll.tokens()
	stats.HostedTotalMeteredCost = totalAll.MeteredCost
	stats.HostedTodayRequests = todayAll.Requests
	stats.HostedTodayTokens = todayAll.tokens()
	stats.HostedTodayMeteredCost = todayAll.MeteredCost
	return nil
}

// GetPlatformUsageTrend returns the admin-dashboard trend of platform usage
// (hosted rows excluded). The pre-aggregated buckets are hosted-inclusive, so
// hosted rows of exactly the same buckets (already aggregated, i.e. created
// before the watermark) are subtracted on read; buckets whose raw logs were
// purged by retention get nothing subtracted and stay hosted-inclusive.
func (r *usageLogRepository) GetPlatformUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string) ([]TrendDataPoint, error) {
	if shouldUsePreaggregatedTrend(granularity, 0, 0, 0, 0, "", nil, nil, nil, "") {
		// Watermark first (see applyDashboardHostedStatsAggregated).
		watermark, err := r.dashboardAggregationWatermark(ctx)
		if err != nil {
			return nil, err
		}
		aggregated, err := r.getUsageTrendFromAggregates(ctx, startTime, endTime, granularity)
		if err == nil && len(aggregated) > 0 {
			hosted, hostedErr := r.getHostedTrendForAggregateBuckets(ctx, startTime, endTime, granularity, watermark)
			if hostedErr != nil {
				return nil, hostedErr
			}
			return subtractTrendPoints(aggregated, hosted), nil
		}
	}
	return r.queryUsageTrendFromLogs(ctx, startTime, endTime, granularity, 0, 0, 0, 0, "", nil, nil, nil, "", hostedScopeExclude)
}

// GetPlatformModelStats returns the admin-dashboard model distribution of
// platform usage (hosted rows excluded, requested model dimension).
func (r *usageLogRepository) GetPlatformModelStats(ctx context.Context, startTime, endTime time.Time) ([]ModelStat, error) {
	return r.getModelStatsWithFiltersBySource(ctx, startTime, endTime, 0, 0, 0, 0, nil, nil, nil, usagestats.ModelSourceRequested, "", hostedScopeExclude)
}

// hostedTrendBounds maps a trend range to the raw created_at interval covered
// by the aggregate buckets that getUsageTrendFromAggregates selects:
//   - hour: bucket_start in [start, end) (instant comparison), i.e. raw rows in
//     [ceilHour(start), ceilHour(end)) on app-timezone hour boundaries;
//   - day: bucket_date in [$1::date, $2::date). lib/pq sends time.Time as text
//     with its own offset and the ::date cast keeps that calendar date, so the
//     dates come from each parameter's own location; bucket_date itself is an
//     app-timezone calendar day.
func hostedTrendBounds(startTime, endTime time.Time, granularity string) (time.Time, time.Time) {
	loc := timezone.Location()
	var lower, upper time.Time
	switch granularity {
	case "hour":
		lower, upper = ceilToLocalHour(startTime.In(loc)), ceilToLocalHour(endTime.In(loc))
	default:
		lower = time.Date(startTime.Year(), startTime.Month(), startTime.Day(), 0, 0, 0, 0, loc)
		upper = time.Date(endTime.Year(), endTime.Month(), endTime.Day(), 0, 0, 0, 0, loc)
	}
	if lower.Before(usagestats.HostedUsageMeteringEpoch) {
		lower = usagestats.HostedUsageMeteringEpoch
	}
	return lower, upper
}

// ceilToLocalHour rounds up to the next local hour boundary. It subtracts the
// local minute/second components instead of rebuilding the wall clock with
// time.Date, so the repeated hour of a DST fall-back keeps its own offset.
func ceilToLocalHour(t time.Time) time.Time {
	floor := t.Add(-(time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second + time.Duration(t.Nanosecond())))
	if floor.Equal(t) {
		return floor
	}
	return floor.Add(time.Hour)
}

// getHostedTrendForAggregateBuckets returns hosted per-bucket sums for exactly
// the buckets an aggregate trend query covers, limited to rows the aggregation
// has already folded in (created_at < watermark).
func (r *usageLogRepository) getHostedTrendForAggregateBuckets(ctx context.Context, startTime, endTime time.Time, granularity string, watermark time.Time) ([]TrendDataPoint, error) {
	lower, upper := hostedTrendBounds(startTime, endTime, granularity)
	if watermark.Before(upper) {
		upper = watermark
	}
	if !upper.After(lower) {
		return nil, nil
	}
	return r.queryUsageTrendFromLogs(ctx, lower, upper, granularity, 0, 0, 0, 0, "", nil, nil, nil, "", hostedScopeOnly)
}

// subtractTrendPoints subtracts hosted from the matching base buckets (by date
// key), clamping every field at zero. Hosted entries without a base bucket
// are ignored: there is nothing hosted-inclusive to correct there.
func subtractTrendPoints(base, hosted []TrendDataPoint) []TrendDataPoint {
	if len(hosted) == 0 {
		return base
	}
	byDate := make(map[string]TrendDataPoint, len(hosted))
	for _, h := range hosted {
		byDate[h.Date] = h
	}
	out := make([]TrendDataPoint, len(base))
	for i, p := range base {
		if h, ok := byDate[p.Date]; ok {
			p.Requests = subtractClampInt(p.Requests, h.Requests)
			p.InputTokens = subtractClampInt(p.InputTokens, h.InputTokens)
			p.OutputTokens = subtractClampInt(p.OutputTokens, h.OutputTokens)
			p.CacheCreationTokens = subtractClampInt(p.CacheCreationTokens, h.CacheCreationTokens)
			p.CacheReadTokens = subtractClampInt(p.CacheReadTokens, h.CacheReadTokens)
			p.TotalTokens = p.InputTokens + p.OutputTokens + p.CacheCreationTokens + p.CacheReadTokens
			p.Cost = subtractClampFloat(p.Cost, h.Cost)
			p.ActualCost = subtractClampFloat(p.ActualCost, h.ActualCost)
		}
		out[i] = p
	}
	return out
}

var _ service.PlatformUsageRepository = (*usageLogRepository)(nil)
