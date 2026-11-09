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
	reset := weekStart.Add(7 * 24 * time.Hour).Add(time.Minute - time.Nanosecond).Truncate(time.Minute).In(timezone.Location()).Format("2006-01-02 15:04 -07:00")
	require.Equal(t, `weekly usage limit exceeded (subscription group "CC3000"), resets at `+reset, LimitErrorMessage(err))
	require.Equal(t, LimitErrorMessage(err), err.Error())

	// Wrapping twice keeps the first detail.
	require.Same(t, err, withSubscriptionLimitDetail(err, nil, nil))
}

func TestWithSubscriptionLimitDetailOmitsUnknownParts(t *testing.T) {
	// No window start: no reset time; no group: no name.
	err := withSubscriptionLimitDetail(ErrDailyLimitExceeded, &UserSubscription{}, nil)
	require.ErrorIs(t, err, ErrDailyLimitExceeded)
	require.Equal(t, "daily usage limit exceeded", LimitErrorMessage(err))

	// A window that already ended shows no reset time.
	old := time.Now().Add(-6 * time.Hour)
	err = withSubscriptionLimitDetail(ErrFiveHourLimitExceeded, &UserSubscription{FiveHourWindowStart: &old}, &Group{Name: "g"})
	require.Equal(t, `5-hour usage limit exceeded (subscription group "g")`, LimitErrorMessage(err))
}

func TestWithSubscriptionLimitDetailLeavesOtherErrors(t *testing.T) {
	require.Same(t, ErrSubscriptionExpired, withSubscriptionLimitDetail(ErrSubscriptionExpired, &UserSubscription{}, &Group{Name: "g"}))
	require.Equal(t, "subscription has expired", LimitErrorMessage(ErrSubscriptionExpired))
	plain := errors.New("boom")
	require.Equal(t, "boom", LimitErrorMessage(plain))
}

func TestValidateAndCheckLimitsReportsResetTime(t *testing.T) {
	limit := 10.0
	now := time.Now()
	monthStart := now.Add(-24 * time.Hour)
	group := &Group{Name: "CC3000", MonthlyLimitUSD: &limit}
	sub := &UserSubscription{Status: SubscriptionStatusActive, ExpiresAt: now.Add(time.Hour), DailyWindowStart: &now, WeeklyWindowStart: &now, MonthlyWindowStart: &monthStart, FiveHourWindowStart: &now, MonthlyUsageUSD: 11}

	_, err := (&SubscriptionService{}).ValidateAndCheckLimits(sub, group)
	require.ErrorIs(t, err, ErrMonthlyLimitExceeded)
	require.Contains(t, LimitErrorMessage(err), `monthly usage limit exceeded (subscription group "CC3000"), resets at `)

	require.ErrorIs(t, sub.ExhaustedLimitError(group), ErrMonthlyLimitExceeded)
	require.Contains(t, LimitErrorMessage(sub.ExhaustedLimitError(group)), "resets at ")
}

func TestSubscriptionLimitResetRoundsUpToMinute(t *testing.T) {
	reset := time.Date(2030, 1, 2, 12, 0, 59, 0, timezone.Location())
	err := &SubscriptionLimitError{err: ErrFiveHourLimitExceeded, resetAt: &reset}
	require.Equal(t, "5-hour usage limit exceeded, resets at 2030-01-02 12:01 "+reset.Format("-07:00"), err.Message())
}

func TestAPIKeyRateLimitExceededAddsResetTime(t *testing.T) {
	start := time.Now().Add(-2 * 24 * time.Hour)
	err := apiKeyRateLimitExceeded(ErrAPIKeyRateLimit7dExceeded, &start, RateLimitWindow7d)
	require.ErrorIs(t, err, ErrAPIKeyRateLimit7dExceeded)
	require.Equal(t, "api key 7天限额已用完，将于 "+formatLimitResetTime(start.Add(RateLimitWindow7d))+" 重置", LimitErrorMessage(err))

	// Unknown or past window: base text only.
	require.Equal(t, "api key 5小时限额已用完", LimitErrorMessage(apiKeyRateLimitExceeded(ErrAPIKeyRateLimit5hExceeded, nil, RateLimitWindow5h)))
	old := time.Now().Add(-6 * time.Hour)
	require.Equal(t, "api key 5小时限额已用完", LimitErrorMessage(apiKeyRateLimitExceeded(ErrAPIKeyRateLimit5hExceeded, &old, RateLimitWindow5h)))
}
