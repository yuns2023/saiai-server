package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type hostedRepairRepoStub struct {
	dashboardAggregationRepoTestStub
	ranges [][2]time.Time
}

func (s *hostedRepairRepoStub) AggregateRange(ctx context.Context, start, end time.Time) error {
	s.ranges = append(s.ranges, [2]time.Time{start, end})
	return s.dashboardAggregationRepoTestStub.AggregateRange(ctx, start, end)
}

type markerStoreStub struct {
	values map[string]string
	getErr error
	sets   int
}

func (m *markerStoreStub) GetValue(ctx context.Context, key string) (string, error) {
	if m.getErr != nil {
		return "", m.getErr
	}
	v, ok := m.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}

func (m *markerStoreStub) Set(ctx context.Context, key, value string) error {
	if m.values == nil {
		m.values = map[string]string{}
	}
	m.values[key] = value
	m.sets++
	return nil
}

func newHostedRepairService(repo DashboardAggregationRepository, markers dashboardAggregationMarkerStore, usageLogsDays int) *DashboardAggregationService {
	return newHostedRepairServiceWithHourly(repo, markers, usageLogsDays, 0)
}

func newHostedRepairServiceWithHourly(repo DashboardAggregationRepository, markers dashboardAggregationMarkerStore, usageLogsDays, hourlyDays int) *DashboardAggregationService {
	svc := &DashboardAggregationService{
		repo: repo,
		cfg: config.DashboardAggregationConfig{
			Enabled:   true,
			Retention: config.DashboardAggregationRetentionConfig{UsageLogsDays: usageLogsDays, HourlyDays: hourlyDays},
		},
	}
	svc.SetMarkerStore(markers)
	return svc
}

func TestRepairHostedExclusion_ReaggregatesPerDaySinceEpochAndMarksDone(t *testing.T) {
	repo := &hostedRepairRepoStub{}
	markers := &markerStoreStub{}
	svc := newHostedRepairService(repo, markers, 90)
	now := usagestats.HostedUsageMeteringEpoch.Add(4*24*time.Hour + 5*time.Hour)

	require.NoError(t, svc.repairHostedExclusion(context.Background(), now))

	require.NotEmpty(t, repo.ranges)
	first := repo.ranges[0]
	require.True(t, first[0].Equal(timezone.StartOfDay(usagestats.HostedUsageMeteringEpoch)), first[0])
	for i, r := range repo.ranges {
		require.True(t, r[1].After(r[0]))
		require.LessOrEqual(t, r[1].Sub(r[0]), 25*time.Hour)
		if i > 0 {
			require.True(t, r[0].Equal(repo.ranges[i-1][1]), "chunks must be contiguous")
		}
	}
	require.True(t, repo.ranges[len(repo.ranges)-1][1].Equal(now))
	require.Contains(t, markers.values, DashboardHostedExclusionRepairSettingKey)
	require.Equal(t, int32(0), svc.running, "running flag released")

	// Idempotent across restarts: the marker prevents a second sweep.
	calls := len(repo.ranges)
	require.NoError(t, svc.repairHostedExclusion(context.Background(), now.Add(time.Hour)))
	require.Len(t, repo.ranges, calls)
	require.Equal(t, 1, markers.sets)
}

func TestRepairHostedExclusion_ClampsToUsageLogRetention(t *testing.T) {
	repo := &hostedRepairRepoStub{}
	svc := newHostedRepairService(repo, &markerStoreStub{}, 2)
	now := usagestats.HostedUsageMeteringEpoch.Add(30 * 24 * time.Hour)

	require.NoError(t, svc.repairHostedExclusion(context.Background(), now))
	// The partially purged boundary day is never re-aggregated.
	require.True(t, repo.ranges[0][0].After(now.AddDate(0, 0, -2)), repo.ranges[0][0])
	require.True(t, repo.ranges[0][0].Equal(timezone.StartOfDay(repo.ranges[0][0])))
	require.LessOrEqual(t, len(repo.ranges), 2)
}

func TestRepairHostedExclusion_FailureLeavesMarkerUnset(t *testing.T) {
	repo := &hostedRepairRepoStub{}
	repo.aggregateErr = errors.New("db down")
	markers := &markerStoreStub{}
	svc := newHostedRepairService(repo, markers, 90)

	err := svc.repairHostedExclusion(context.Background(), usagestats.HostedUsageMeteringEpoch.Add(48*time.Hour))
	require.Error(t, err)
	require.NotContains(t, markers.values, DashboardHostedExclusionRepairSettingKey)
	require.Equal(t, int32(0), svc.running)
}

func TestRepairHostedExclusion_MarkerReadErrorSkipsSweep(t *testing.T) {
	repo := &hostedRepairRepoStub{}
	markers := &markerStoreStub{getErr: errors.New("settings unavailable")}
	svc := newHostedRepairService(repo, markers, 90)

	require.Error(t, svc.repairHostedExclusion(context.Background(), usagestats.HostedUsageMeteringEpoch.Add(48*time.Hour)))
	require.Empty(t, repo.ranges)
}

func TestRepairHostedExclusion_NoMarkerStoreIsNoop(t *testing.T) {
	repo := &hostedRepairRepoStub{}
	svc := &DashboardAggregationService{repo: repo}
	require.NoError(t, svc.repairHostedExclusion(context.Background(), time.Now().UTC()))
	require.Empty(t, repo.ranges)
}

func TestRepairHostedExclusion_ClampsToHourlyRetention(t *testing.T) {
	repo := &hostedRepairRepoStub{}
	svc := newHostedRepairServiceWithHourly(repo, &markerStoreStub{}, 90, 1)
	now := usagestats.HostedUsageMeteringEpoch.Add(10 * 24 * time.Hour)

	require.NoError(t, svc.repairHostedExclusion(context.Background(), now))
	require.True(t, repo.ranges[0][0].After(now.AddDate(0, 0, -1)), repo.ranges[0][0])
}

func TestRunStartupRecompute_RetriesFailedRepair(t *testing.T) {
	prev := dashboardHostedRepairRetryDelay
	dashboardHostedRepairRetryDelay = time.Millisecond
	t.Cleanup(func() { dashboardHostedRepairRetryDelay = prev })

	repo := &hostedRepairRepoStub{}
	markers := &flakyMarkerStore{failures: 2}
	svc := newHostedRepairService(repo, markers, 90)
	svc.runStartupRecompute()
	require.Equal(t, 3, markers.gets)
	require.Contains(t, markers.values, DashboardHostedExclusionRepairSettingKey)
}

type flakyMarkerStore struct {
	markerStoreStub
	failures int
	gets     int
}

func (m *flakyMarkerStore) GetValue(ctx context.Context, key string) (string, error) {
	m.gets++
	if m.gets <= m.failures {
		return "", errors.New("transient")
	}
	return m.markerStoreStub.GetValue(ctx, key)
}
