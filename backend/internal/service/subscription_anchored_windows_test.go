package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSubscriptionWindowStartGrid(t *testing.T) {
	startsAt := time.Date(2026, 10, 25, 21, 55, 35, 0, time.UTC)

	// Before the first boundary: the window starts at StartsAt.
	require.Equal(t, startsAt, subscriptionWindowStart(startsAt, subscriptionMonthlyWindow, startsAt.Add(29*24*time.Hour)))
	// Exactly on a boundary: the new window starts there.
	require.Equal(t, startsAt.Add(30*24*time.Hour), subscriptionWindowStart(startsAt, subscriptionMonthlyWindow, startsAt.Add(30*24*time.Hour)))
	// Long idle: still on the grid, no drift to the day of the next request.
	require.Equal(t, startsAt.Add(21*24*time.Hour), subscriptionWindowStart(startsAt, subscriptionWeeklyWindow, startsAt.Add(27*24*time.Hour+13*time.Hour)))
	// Before StartsAt (future start) and unknown StartsAt.
	require.Equal(t, startsAt, subscriptionWindowStart(startsAt, subscriptionWeeklyWindow, startsAt.Add(-time.Hour)))
	now := time.Now()
	require.Equal(t, now, subscriptionWindowStart(time.Time{}, subscriptionWeeklyWindow, now))
}

func TestEffectiveResetTimeCapsAtExpiry(t *testing.T) {
	startsAt := time.Date(2026, 10, 28, 17, 23, 35, 0, time.UTC)
	sub := &UserSubscription{StartsAt: startsAt, ExpiresAt: startsAt.Add(30 * 24 * time.Hour)}
	require.Equal(t, sub.ExpiresAt, sub.EffectiveResetTime(startsAt, subscriptionMonthlyWindow))
	require.Equal(t, startsAt.Add(7*24*time.Hour), sub.EffectiveResetTime(startsAt, subscriptionWeeklyWindow))
	// The last weekly window of a 30-day subscription ends with it.
	require.Equal(t, sub.ExpiresAt, sub.EffectiveResetTime(startsAt.Add(28*24*time.Hour), subscriptionWeeklyWindow))
}

func TestLimitErrorOmitsResetAtOrAfterExpiry(t *testing.T) {
	now := time.Now()
	monthStart := now.Add(-29 * 24 * time.Hour)
	sub := &UserSubscription{ExpiresAt: monthStart.Add(30 * 24 * time.Hour), MonthlyWindowStart: &monthStart}
	err := withSubscriptionLimitDetail(ErrMonthlyLimitExceeded, sub, &Group{Name: "CC1000"})
	require.Equal(t, `monthly usage limit exceeded (subscription group "CC1000")`, LimitErrorMessage(err))
}

// anchoredWindowRepoStub records the window starts written by activation and
// automatic resets.
type anchoredWindowRepoStub struct {
	userSubRepoNoop

	activated   []time.Time
	resetStarts map[SubscriptionUsageWindow]time.Time
	fiveHour    *time.Time
}

func (r *anchoredWindowRepoStub) ActivateWindows(_ context.Context, _ int64, daily, weekly, monthly time.Time) error {
	r.activated = []time.Time{daily, weekly, monthly}
	return nil
}

func (r *anchoredWindowRepoStub) EnsureFiveHourWindow(_ context.Context, _ int64, start time.Time) error {
	r.fiveHour = &start
	return nil
}

func (r *anchoredWindowRepoStub) ResetEndedUsageWindow(_ context.Context, _ int64, window SubscriptionUsageWindow, _ time.Time, newStart time.Time) error {
	if r.resetStarts == nil {
		r.resetStarts = map[SubscriptionUsageWindow]time.Time{}
	}
	r.resetStarts[window] = newStart
	return nil
}

func TestCheckAndActivateWindowUsesStartsAtGrid(t *testing.T) {
	// First used 9 days and 3 hours after the start.
	startsAt := time.Now().Add(-(9*24 + 3) * time.Hour)
	repo := &anchoredWindowRepoStub{}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	sub := &UserSubscription{ID: 1, StartsAt: startsAt, ExpiresAt: startsAt.Add(30 * 24 * time.Hour)}

	require.NoError(t, svc.CheckAndActivateWindow(context.Background(), sub))
	require.Equal(t, []time.Time{startsAt.Add(9 * 24 * time.Hour), startsAt.Add(7 * 24 * time.Hour), startsAt}, repo.activated)
	require.Equal(t, startsAt, *sub.MonthlyWindowStart)
	require.Equal(t, sub.ExpiresAt, sub.EffectiveResetTime(*sub.MonthlyWindowStart, subscriptionMonthlyWindow))
}

func TestCheckAndResetWindowsRestartsOnGrid(t *testing.T) {
	// Renewed once (60 days); now 31 days and 2 hours in, nothing used for
	// the last 10 days.
	startsAt := time.Now().Add(-(31*24 + 2) * time.Hour)
	weekly := startsAt.Add(14 * 24 * time.Hour)
	monthly := startsAt
	repo := &anchoredWindowRepoStub{}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	sub := &UserSubscription{
		ID: 1, StartsAt: startsAt, ExpiresAt: startsAt.Add(60 * 24 * time.Hour),
		WeeklyWindowStart: &weekly, MonthlyWindowStart: &monthly,
		WeeklyUsageUSD: 5, MonthlyUsageUSD: 50,
	}

	require.NoError(t, svc.CheckAndResetWindows(context.Background(), sub))
	require.Equal(t, startsAt.Add(28*24*time.Hour), repo.resetStarts[SubscriptionUsageWindowWeekly])
	require.Equal(t, startsAt.Add(30*24*time.Hour), repo.resetStarts[SubscriptionUsageWindowMonthly])
	require.Equal(t, sub.ExpiresAt, sub.EffectiveResetTime(*sub.MonthlyWindowStart, subscriptionMonthlyWindow))
	require.Zero(t, sub.MonthlyUsageUSD)
}

// periodRestartRepoStub models a renewal: GetByUserIDAndGroupID/GetByID return
// the subscription; RestartPeriod and ExtendExpiry record what was written.
type periodRestartRepoStub struct {
	userSubRepoNoop

	sub        *UserSubscription
	restarted  bool
	restartAt  time.Time
	extendedTo time.Time
	status     string
}

func (r *periodRestartRepoStub) GetByUserIDAndGroupID(context.Context, int64, int64) (*UserSubscription, error) {
	cp := *r.sub
	return &cp, nil
}

func (r *periodRestartRepoStub) GetByID(context.Context, int64) (*UserSubscription, error) {
	cp := *r.sub
	return &cp, nil
}

func (r *periodRestartRepoStub) RestartPeriod(_ context.Context, _ int64, startsAt, expiresAt time.Time) error {
	r.restarted, r.restartAt, r.extendedTo = true, startsAt, expiresAt
	return nil
}

func (r *periodRestartRepoStub) ExtendExpiry(_ context.Context, _ int64, expiresAt time.Time) error {
	r.extendedTo = expiresAt
	return nil
}

func (r *periodRestartRepoStub) UpdateStatus(_ context.Context, _ int64, status string) error {
	r.status = status
	return nil
}

type subscriptionGroupRepoStub struct{ groupRepoNoop }

func (subscriptionGroupRepoStub) GetByID(context.Context, int64) (*Group, error) {
	return &Group{ID: 1, SubscriptionType: SubscriptionTypeSubscription}, nil
}

func TestRenewExpiredSubscriptionRestartsPeriod(t *testing.T) {
	startsAt := time.Now().Add(-31 * 24 * time.Hour)
	repo := &periodRestartRepoStub{sub: &UserSubscription{ID: 7, UserID: 1, GroupID: 1, StartsAt: startsAt, ExpiresAt: startsAt.Add(30 * 24 * time.Hour), Status: SubscriptionStatusExpired}}
	svc := NewSubscriptionService(subscriptionGroupRepoStub{}, repo, nil, nil, nil)

	before := time.Now()
	_, renewed, err := svc.AssignOrExtendSubscription(context.Background(), &AssignSubscriptionInput{UserID: 1, GroupID: 1, ValidityDays: 30})
	require.NoError(t, err)
	require.True(t, renewed)
	require.True(t, repo.restarted)
	require.False(t, repo.restartAt.Before(before))
	require.Equal(t, repo.restartAt.AddDate(0, 0, 30), repo.extendedTo)
	require.Equal(t, SubscriptionStatusActive, repo.status)
}

func TestRenewActiveSubscriptionKeepsPeriod(t *testing.T) {
	startsAt := time.Now().Add(-20 * 24 * time.Hour)
	expiresAt := startsAt.Add(30 * 24 * time.Hour)
	repo := &periodRestartRepoStub{sub: &UserSubscription{ID: 7, UserID: 1, GroupID: 1, StartsAt: startsAt, ExpiresAt: expiresAt, Status: SubscriptionStatusActive}}
	svc := NewSubscriptionService(subscriptionGroupRepoStub{}, repo, nil, nil, nil)

	_, renewed, err := svc.AssignOrExtendSubscription(context.Background(), &AssignSubscriptionInput{UserID: 1, GroupID: 1, ValidityDays: 30})
	require.NoError(t, err)
	require.True(t, renewed)
	require.False(t, repo.restarted)
	require.Equal(t, expiresAt.AddDate(0, 0, 30), repo.extendedTo)
}
