package service

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// DashboardHostedExclusionRepairSettingKey records that the pre-aggregated
// dashboard buckets were re-aggregated after hosted usage rows started being
// excluded from platform totals. Delete the setting and restart to re-run.
const DashboardHostedExclusionRepairSettingKey = "dashboard_agg_hosted_exclusion_repair_v1"

const (
	dashboardHostedRepairLockRetries  = 12
	dashboardHostedRepairLockInterval = 5 * time.Second
	dashboardHostedRepairAttempts     = 5
)

// dashboardHostedRepairRetryDelay is a var so tests can shorten it.
var dashboardHostedRepairRetryDelay = time.Minute

// dashboardAggregationMarkerStore is the subset of SettingRepository used to
// make the one-time repair idempotent across restarts.
type dashboardAggregationMarkerStore interface {
	GetValue(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// SetMarkerStore enables the one-time startup repair of aggregate buckets.
func (s *DashboardAggregationService) SetMarkerStore(store dashboardAggregationMarkerStore) {
	if s != nil {
		s.markers = store
	}
}

// runStartupRecompute repairs hosted-inflated buckets once, then applies the
// regular recompute_days refresh.
func (s *DashboardAggregationService) runStartupRecompute() {
	for attempt := 1; attempt <= dashboardHostedRepairAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), defaultDashboardAggregationBackfillTimeout)
		err := s.repairHostedExclusion(ctx, time.Now().UTC())
		cancel()
		if err == nil {
			break
		}
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 托管用量剔除重算失败 (attempt %d/%d，未完成时下次启动重试): %v", attempt, dashboardHostedRepairAttempts, err)
		if attempt < dashboardHostedRepairAttempts {
			time.Sleep(dashboardHostedRepairRetryDelay)
		}
	}
	if s.cfg.RecomputeDays > 0 {
		s.recomputeRecentDays()
	}
}

// repairHostedExclusion re-aggregates every bucket that may contain hosted
// rows, i.e. [HostedUsageMeteringEpoch, now). The upsert is non-destructive
// (buckets without raw rows are left untouched) and re-running it yields the
// same result. It only touches days whose raw usage_logs and hourly buckets
// are still fully retained: re-aggregating a partially purged day would
// shrink legitimate platform history. Older buckets (only possible when the
// upgrade lands more than the retention window after the epoch) are logged
// as unrepairable.
func (s *DashboardAggregationService) repairHostedExclusion(ctx context.Context, now time.Time) error {
	if s == nil || s.repo == nil || s.markers == nil {
		return nil
	}
	if _, err := s.markers.GetValue(ctx, DashboardHostedExclusionRepairSettingKey); err == nil {
		return nil
	} else if !errors.Is(err, ErrSettingNotFound) {
		return err
	}

	loc := timezone.Location()
	epochDay := timezone.StartOfDay(usagestats.HostedUsageMeteringEpoch.In(loc))
	start := epochDay
	for _, days := range []int{s.cfg.Retention.UsageLogsDays, s.cfg.Retention.HourlyDays} {
		if days <= 0 {
			continue
		}
		// First local day that is entirely inside the retention window.
		floor := timezone.StartOfDay(now.AddDate(0, 0, -days).In(loc).AddDate(0, 0, 1).Add(time.Hour))
		if start.Before(floor) {
			start = floor
		}
	}
	if start.After(epochDay) {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 托管用量剔除重算: %s 之前的聚合桶已超出原始日志保留期，无法重算",
			start.Format(time.RFC3339))
	}
	if !now.After(start) {
		return s.markers.Set(ctx, DashboardHostedExclusionRepairSettingKey, now.Format(time.RFC3339))
	}

	if err := s.acquireRunningForRepair(ctx); err != nil {
		return err
	}
	defer atomic.StoreInt32(&s.running, 0)

	jobStart := time.Now()
	cursor := start
	for cursor.Before(now) {
		next := timezone.StartOfDay(cursor.AddDate(0, 0, 1).Add(time.Hour))
		windowEnd := next
		if windowEnd.After(now) {
			windowEnd = now
		}
		if err := s.aggregateRange(ctx, cursor, windowEnd); err != nil {
			return err
		}
		cursor = next
	}
	if err := s.markers.Set(ctx, DashboardHostedExclusionRepairSettingKey, now.Format(time.RFC3339)); err != nil {
		return err
	}
	logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 托管用量剔除重算完成 (start=%s end=%s duration=%s)",
		start.Format(time.RFC3339), now.Format(time.RFC3339), time.Since(jobStart).String())
	return nil
}

func (s *DashboardAggregationService) acquireRunningForRepair(ctx context.Context) error {
	for i := 0; ; i++ {
		if atomic.CompareAndSwapInt32(&s.running, 0, 1) {
			return nil
		}
		if i >= dashboardHostedRepairLockRetries {
			return errDashboardAggregationRunning
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dashboardHostedRepairLockInterval):
		}
	}
}
