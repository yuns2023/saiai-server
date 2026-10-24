package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// ErrPlatformUsageUnavailable is returned when the usage repository cannot
// produce hosted-free platform aggregates. Callers must not fall back to the
// hosted-inclusive queries.
var ErrPlatformUsageUnavailable = errors.New("platform usage statistics are unavailable")

// PlatformUsageRepository is the admin-dashboard capability for platform
// (hosted rows excluded) trend and model distribution.
type PlatformUsageRepository interface {
	GetPlatformUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string) ([]usagestats.TrendDataPoint, error)
	GetPlatformModelStats(ctx context.Context, startTime, endTime time.Time) ([]usagestats.ModelStat, error)
}

func (s *DashboardService) platformUsageRepo() (PlatformUsageRepository, error) {
	repo, ok := s.usageRepo.(PlatformUsageRepository)
	if !ok {
		return nil, ErrPlatformUsageUnavailable
	}
	return repo, nil
}

// GetPlatformUsageTrend returns the admin-dashboard usage trend without hosted rows.
func (s *DashboardService) GetPlatformUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string) ([]usagestats.TrendDataPoint, error) {
	repo, err := s.platformUsageRepo()
	if err != nil {
		return nil, err
	}
	trend, err := repo.GetPlatformUsageTrend(ctx, startTime, endTime, granularity)
	if err != nil {
		return nil, fmt.Errorf("get platform usage trend: %w", err)
	}
	return trend, nil
}

// GetPlatformModelStats returns the admin-dashboard model distribution without hosted rows.
func (s *DashboardService) GetPlatformModelStats(ctx context.Context, startTime, endTime time.Time) ([]usagestats.ModelStat, error) {
	repo, err := s.platformUsageRepo()
	if err != nil {
		return nil, err
	}
	stats, err := repo.GetPlatformModelStats(ctx, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("get platform model stats: %w", err)
	}
	return stats, nil
}
