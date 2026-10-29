//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type blockingAnalyticsRepo struct {
	UsageLogRepository
	entered chan struct{}
	release chan struct{}
}

func (r *blockingAnalyticsRepo) GetUserUsageAnalytics(ctx context.Context, _ usagestats.UsageAnalyticsQuery) (*usagestats.UsageAnalyticsResult, error) {
	r.entered <- struct{}{}
	<-r.release
	return &usagestats.UsageAnalyticsResult{}, nil
}

func TestGetUserUsageAnalyticsLimitsConcurrentQueriesPerUser(t *testing.T) {
	repo := &blockingAnalyticsRepo{entered: make(chan struct{}, 4), release: make(chan struct{})}
	svc := NewUsageService(repo, nil, nil, nil)
	const userID = 987654
	q := usagestats.UsageAnalyticsQuery{UserID: userID}

	errs := make(chan error, usageAnalyticsMaxConcurrentPerUser)
	for i := 0; i < usageAnalyticsMaxConcurrentPerUser; i++ {
		go func() {
			_, err := svc.GetUserUsageAnalytics(context.Background(), q)
			errs <- err
		}()
		<-repo.entered
	}

	_, err := svc.GetUserUsageAnalytics(context.Background(), q)
	require.True(t, infraerrors.IsTooManyRequests(err), "got %v", err)

	// Another user is not affected.
	other := usagestats.UsageAnalyticsQuery{UserID: userID + 1}
	go func() { _, _ = svc.GetUserUsageAnalytics(context.Background(), other) }()
	<-repo.entered

	close(repo.release)
	for i := 0; i < usageAnalyticsMaxConcurrentPerUser; i++ {
		require.NoError(t, <-errs)
	}
	// Slots are released afterwards.
	repo.entered = make(chan struct{}, 1)
	_, err = svc.GetUserUsageAnalytics(context.Background(), q)
	require.NoError(t, err)
}

type failingAnalyticsRepo struct {
	UsageLogRepository
	fail bool
}

func (r *failingAnalyticsRepo) GetUserUsageAnalytics(ctx context.Context, _ usagestats.UsageAnalyticsQuery) (*usagestats.UsageAnalyticsResult, error) {
	if r.fail {
		return nil, errors.New("boom")
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestGetUserUsageAnalyticsReleasesSlotsOnErrorAndCancel(t *testing.T) {
	const userID = 876543
	q := usagestats.UsageAnalyticsQuery{UserID: userID}
	failing := NewUsageService(&failingAnalyticsRepo{fail: true}, nil, nil, nil)
	for i := 0; i < usageAnalyticsMaxConcurrentPerUser+2; i++ {
		_, err := failing.GetUserUsageAnalytics(context.Background(), q)
		require.Error(t, err)
		require.False(t, infraerrors.IsTooManyRequests(err), "an error must release its slot")
	}

	waiting := NewUsageService(&failingAnalyticsRepo{}, nil, nil, nil) // separate instance, shared per-user limit
	for i := 0; i < usageAnalyticsMaxConcurrentPerUser+2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := waiting.GetUserUsageAnalytics(ctx, q)
		require.ErrorIs(t, err, context.Canceled)
	}
	_, err := failing.GetUserUsageAnalytics(context.Background(), q)
	require.False(t, infraerrors.IsTooManyRequests(err))
}
