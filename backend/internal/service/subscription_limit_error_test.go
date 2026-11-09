package service

import (
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func TestWithSubscriptionLimitDetailNamesGroupAndReset(t *testing.T) {
	weekStart := time.Now().Add(-48 * time.Hour)
	sub := &UserSubscription{WeeklyWindowStart: &weekStart}
	group := &Group{Name: "CC3000"}

	err := withSubscriptionLimitDetail(ErrWeeklyLimitExceeded, sub, group)
	require.ErrorIs(t, err, ErrWeeklyLimitExceeded)
	reset := weekStart.Add(7 * 24 * time.Hour).In(timezone.Location()).Format("2006-01-02 15:04 -07:00")
	require.Equal(t, `weekly usage limit exceeded (subscription group "CC3000"), resets at `+reset, SubscriptionErrorMessage(err))
	require.Equal(t, SubscriptionErrorMessage(err), err.Error())

	// Wrapping twice keeps the first detail.
	require.Same(t, err, withSubscriptionLimitDetail(err, nil, nil))
}

func TestWithSubscriptionLimitDetailOmitsUnknownParts(t *testing.T) {
	// No window start: no reset time; no group: no name.
	err := withSubscriptionLimitDetail(ErrDailyLimitExceeded, &UserSubscription{}, nil)
	require.ErrorIs(t, err, ErrDailyLimitExceeded)
	require.Equal(t, "daily usage limit exceeded", SubscriptionErrorMessage(err))

	// A window that already ended shows no reset time.
	old := time.Now().Add(-6 * time.Hour)
	err = withSubscriptionLimitDetail(ErrFiveHourLimitExceeded, &UserSubscription{FiveHourWindowStart: &old}, &Group{Name: "g"})
	require.Equal(t, `5-hour usage limit exceeded (subscription group "g")`, SubscriptionErrorMessage(err))
}

func TestWithSubscriptionLimitDetailLeavesOtherErrors(t *testing.T) {
	require.Same(t, ErrSubscriptionExpired, withSubscriptionLimitDetail(ErrSubscriptionExpired, &UserSubscription{}, &Group{Name: "g"}))
	require.Equal(t, "subscription has expired", SubscriptionErrorMessage(ErrSubscriptionExpired))
	plain := errors.New("boom")
	require.Equal(t, "boom", SubscriptionErrorMessage(plain))
}

func TestValidateAndCheckLimitsReportsResetTime(t *testing.T) {
	limit := 10.0
	now := time.Now()
	monthStart := now.Add(-24 * time.Hour)
	group := &Group{Name: "CC3000", MonthlyLimitUSD: &limit}
	sub := &UserSubscription{Status: SubscriptionStatusActive, ExpiresAt: now.Add(time.Hour), DailyWindowStart: &now, WeeklyWindowStart: &now, MonthlyWindowStart: &monthStart, FiveHourWindowStart: &now, MonthlyUsageUSD: 11}

	_, err := (&SubscriptionService{}).ValidateAndCheckLimits(sub, group)
	require.ErrorIs(t, err, ErrMonthlyLimitExceeded)
	require.Contains(t, SubscriptionErrorMessage(err), `monthly usage limit exceeded (subscription group "CC3000"), resets at `)

	require.ErrorIs(t, sub.ExhaustedLimitError(group), ErrMonthlyLimitExceeded)
	require.Contains(t, SubscriptionErrorMessage(sub.ExhaustedLimitError(group)), "resets at ")
}
