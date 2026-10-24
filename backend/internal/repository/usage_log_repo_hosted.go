package repository

import (
	"context"
	"sort"
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

type hostedTotals struct {
	Requests    int64
	Tokens      int64
	MeteredCost float64
}

func (t hostedTotals) add(o hostedTotals) hostedTotals {
	return hostedTotals{Requests: t.Requests + o.Requests, Tokens: t.Tokens + o.Tokens, MeteredCost: t.MeteredCost + o.MeteredCost}
}

// hostedHistoryTTL bounds how stale the cached completed-days hosted total may
// get (late rows near midnight, retention cleanup).
const hostedHistoryTTL = time.Hour

// hostedHistoryCache memoizes hosted totals of completed days
// [HostedUsageMeteringEpoch, todayStart). The admin dashboard refreshes stats
// every ~15s; without this every refresh would range-scan all usage_logs since
// the epoch, while with it only today's rows are scanned per refresh.
type hostedHistoryCache struct {
	mu         sync.Mutex
	dayStart   time.Time
	computedAt time.Time
	totals     hostedTotals
}

func (c *hostedHistoryCache) get(dayStart, now time.Time) (hostedTotals, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.computedAt.IsZero() || !c.dayStart.Equal(dayStart) || now.Sub(c.computedAt) > hostedHistoryTTL {
		return hostedTotals{}, false
	}
	return c.totals, true
}

func (c *hostedHistoryCache) set(dayStart, now time.Time, totals hostedTotals) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dayStart, c.computedAt, c.totals = dayStart, now, totals
}

// queryHostedTotals sums hosted rows in [start, end), clamped to the hosted
// metering epoch so the scan stays on the created_at index range.
func (r *usageLogRepository) queryHostedTotals(ctx context.Context, start, end time.Time) (hostedTotals, error) {
	var totals hostedTotals
	if start.Before(usagestats.HostedUsageMeteringEpoch) {
		start = usagestats.HostedUsageMeteringEpoch
	}
	if !end.After(start) {
		return totals, nil
	}
	query := `
		SELECT
			COUNT(*),
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0),
			COALESCE(SUM(metered_cost), 0)
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
			AND metered_cost IS NOT NULL
	`
	err := scanSingleRow(ctx, r.sql, query, []any{start, end}, &totals.Requests, &totals.Tokens, &totals.MeteredCost)
	return totals, err
}

// fillDashboardHostedStatsAggregated fills the hosted line for the
// pre-aggregated dashboard path. The aggregate tables carry no hosted columns
// (no migration), so hosted totals come from usage_logs: completed days are
// memoized, today is queried live. "Total" therefore covers the usage_logs
// retention window since the hosted epoch.
func (r *usageLogRepository) fillDashboardHostedStatsAggregated(ctx context.Context, stats *DashboardStats, todayStart time.Time) error {
	now := time.Now()
	history, ok := r.hostedHistory.get(todayStart, now)
	if !ok {
		var err error
		history, err = r.queryHostedTotals(ctx, usagestats.HostedUsageMeteringEpoch, todayStart)
		if err != nil {
			return err
		}
		r.hostedHistory.set(todayStart, now, history)
	}
	today, err := r.queryHostedTotals(ctx, todayStart, timezone.StartOfDay(todayStart.AddDate(0, 0, 1).Add(time.Hour)))
	if err != nil {
		return err
	}
	total := history.add(today)
	stats.HostedTotalRequests = total.Requests
	stats.HostedTotalTokens = total.Tokens
	stats.HostedTotalMeteredCost = total.MeteredCost
	stats.HostedTodayRequests = today.Requests
	stats.HostedTodayTokens = today.Tokens
	stats.HostedTodayMeteredCost = today.MeteredCost
	return nil
}

// GetPlatformUsageTrend returns the admin-dashboard trend of platform usage,
// i.e. hosted rows excluded. Pre-aggregated buckets are already hosted-free.
func (r *usageLogRepository) GetPlatformUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string) ([]TrendDataPoint, error) {
	if shouldUsePreaggregatedTrend(granularity, 0, 0, 0, 0, "", nil, nil, nil, "") {
		aggregated, err := r.getUsageTrendFromAggregates(ctx, startTime, endTime, granularity)
		if err == nil && len(aggregated) > 0 {
			return aggregated, nil
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
// the buckets an aggregate trend query covers, so they can be merged back.
func (r *usageLogRepository) getHostedTrendForAggregateBuckets(ctx context.Context, startTime, endTime time.Time, granularity string) ([]TrendDataPoint, error) {
	lower, upper := hostedTrendBounds(startTime, endTime, granularity)
	if !upper.After(lower) {
		return nil, nil
	}
	return r.queryUsageTrendFromLogs(ctx, lower, upper, granularity, 0, 0, 0, 0, "", nil, nil, nil, "", hostedScopeOnly)
}

// mergeTrendPoints adds extra into base by date key and returns the union
// sorted by date (all trend date formats sort lexicographically).
func mergeTrendPoints(base, extra []TrendDataPoint) []TrendDataPoint {
	if len(extra) == 0 {
		return base
	}
	index := make(map[string]int, len(base))
	merged := make([]TrendDataPoint, 0, len(base)+len(extra))
	for _, p := range base {
		index[p.Date] = len(merged)
		merged = append(merged, p)
	}
	for _, p := range extra {
		i, ok := index[p.Date]
		if !ok {
			index[p.Date] = len(merged)
			merged = append(merged, p)
			continue
		}
		m := &merged[i]
		m.Requests += p.Requests
		m.InputTokens += p.InputTokens
		m.OutputTokens += p.OutputTokens
		m.CacheCreationTokens += p.CacheCreationTokens
		m.CacheReadTokens += p.CacheReadTokens
		m.TotalTokens += p.TotalTokens
		m.Cost += p.Cost
		m.ActualCost += p.ActualCost
	}
	sort.SliceStable(merged, func(a, b int) bool { return merged[a].Date < merged[b].Date })
	return merged
}

var _ service.PlatformUsageRepository = (*usageLogRepository)(nil)
