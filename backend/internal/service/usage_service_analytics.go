package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

const (
	// usageAnalyticsQueryTimeout bounds one analytics aggregate or records
	// page. A 90-day range for a heavy user scans several hundred thousand
	// raw usage rows.
	usageAnalyticsQueryTimeout = 20 * time.Second
	// usageAnalyticsMaxConcurrentPerUser caps simultaneous analytics
	// aggregates of one user across the Web UI and all of the user's Keys.
	usageAnalyticsMaxConcurrentPerUser = 3
)

// ErrUsageAnalyticsBusy is returned when a user already runs the maximum
// number of analytics aggregates.
var ErrUsageAnalyticsBusy = infraerrors.TooManyRequests("USAGE_ANALYTICS_BUSY", "too many concurrent usage analytics queries, retry shortly")

// UsageAnalyticsRepository is kept separate from UsageLogRepository, like
// APIKeyUsageBreakdownRepository, so test doubles need not implement it.
type UsageAnalyticsRepository interface {
	GetUserUsageAnalytics(ctx context.Context, q usagestats.UsageAnalyticsQuery) (*usagestats.UsageAnalyticsResult, error)
}

// UsageRecordsRepository lists usage records with keyset pagination.
type UsageRecordsRepository interface {
	ListUserUsageRecordsPage(ctx context.Context, q usagestats.UsageAnalyticsQuery, after *usagestats.UsageRecordPosition, limit int) ([]UsageLog, error)
}

// usageAnalyticsGate counts running aggregates per user in this process.
type usageAnalyticsGate struct {
	mu      sync.Mutex
	running map[int64]int
}

var analyticsGate = &usageAnalyticsGate{running: map[int64]int{}}

func (g *usageAnalyticsGate) acquire(userID int64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running[userID] >= usageAnalyticsMaxConcurrentPerUser {
		return false
	}
	g.running[userID]++
	return true
}

func (g *usageAnalyticsGate) release(userID int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running[userID] <= 1 {
		delete(g.running, userID)
		return
	}
	g.running[userID]--
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
	if !analyticsGate.acquire(q.UserID) {
		return nil, ErrUsageAnalyticsBusy
	}
	defer analyticsGate.release(q.UserID)
	ctx, cancel := context.WithTimeout(ctx, usageAnalyticsQueryTimeout)
	defer cancel()
	result, err := repo.GetUserUsageAnalytics(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("get usage analytics: %w", err)
	}
	return result, nil
}

// ListUserUsageRecordsPage returns one keyset page of the user's records.
func (s *UsageService) ListUserUsageRecordsPage(ctx context.Context, q usagestats.UsageAnalyticsQuery, after *usagestats.UsageRecordPosition, limit int) ([]UsageLog, error) {
	if q.UserID <= 0 {
		return nil, errors.New("usage records require a user scope")
	}
	repo, ok := s.usageRepo.(UsageRecordsRepository)
	if !ok {
		return nil, errors.New("usage records repository is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, usageAnalyticsQueryTimeout)
	defer cancel()
	logs, err := repo.ListUserUsageRecordsPage(ctx, q, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list usage records: %w", err)
	}
	return logs, nil
}
