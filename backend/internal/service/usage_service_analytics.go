package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// usageAnalyticsQueryTimeout bounds one analytics aggregate. A 90-day range
// for a heavy user scans several hundred thousand raw usage rows.
const usageAnalyticsQueryTimeout = 20 * time.Second

// UsageAnalyticsRepository is kept separate from UsageLogRepository, like
// APIKeyUsageBreakdownRepository, so test doubles need not implement it.
type UsageAnalyticsRepository interface {
	GetUserUsageAnalytics(ctx context.Context, q usagestats.UsageAnalyticsQuery) (*usagestats.UsageAnalyticsResult, error)
}

// GetUserUsageAnalytics aggregates the user's own usage by one dimension.
func (s *UsageService) GetUserUsageAnalytics(ctx context.Context, q usagestats.UsageAnalyticsQuery) (*usagestats.UsageAnalyticsResult, error) {
	if q.UserID <= 0 {
		return nil, errors.New("usage analytics requires a user scope")
	}
	repo, ok := s.usageRepo.(UsageAnalyticsRepository)
	if !ok {
		return nil, errors.New("usage analytics repository is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, usageAnalyticsQueryTimeout)
	defer cancel()
	result, err := repo.GetUserUsageAnalytics(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("get usage analytics: %w", err)
	}
	return result, nil
}
