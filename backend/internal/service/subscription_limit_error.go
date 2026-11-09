package service

import (
	"errors"
	"fmt"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// SubscriptionLimitError is a subscription usage-limit error (5-hour, daily,
// weekly or monthly) that also names the subscription group and the reset time
// of the exhausted window, so the client message says what to wait for.
// errors.Is still matches the wrapped Err*LimitExceeded value.
type SubscriptionLimitError struct {
	err       error
	groupName string
	resetAt   *time.Time
}

func (e *SubscriptionLimitError) Error() string { return e.Message() }

func (e *SubscriptionLimitError) Unwrap() error { return e.err }

// Message is the client-facing text, e.g. `weekly usage limit exceeded
// (subscription group "CC3000"), resets at 2026-11-11 00:00 +08:00`.
func (e *SubscriptionLimitError) Message() string {
	msg := infraerrors.Message(e.err)
	if e.groupName != "" {
		msg += fmt.Sprintf(" (subscription group %q)", e.groupName)
	}
	if e.resetAt != nil {
		msg += ", resets at " + formatLimitResetTime(*e.resetAt)
	}
	return msg
}

// formatLimitResetTime rounds up to the minute, so the shown time is never
// before the actual reset, in the configured timezone.
func formatLimitResetTime(t time.Time) string {
	return t.Add(time.Minute - time.Nanosecond).Truncate(time.Minute).In(timezone.Location()).Format("2006-01-02 15:04 -07:00")
}

// APIKeyRateLimitError is an API Key 5h/1d/7d limit error that also carries
// the reset time of the exhausted window. errors.Is still matches the wrapped
// ErrAPIKeyRateLimit*Exceeded value.
type APIKeyRateLimitError struct {
	err     error
	resetAt *time.Time
}

func (e *APIKeyRateLimitError) Error() string { return e.Message() }

func (e *APIKeyRateLimitError) Unwrap() error { return e.err }

// Message is the client-facing text, e.g. `api key 7天限额已用完，将于
// 2026-11-11 00:00 +08:00 重置`.
func (e *APIKeyRateLimitError) Message() string {
	msg := infraerrors.Message(e.err)
	if e.resetAt != nil {
		msg += "，将于 " + formatLimitResetTime(*e.resetAt) + " 重置"
	}
	return msg
}

// apiKeyRateLimitExceeded wraps limitErr with the reset time of the window
// that started at windowStart (omitted when unknown or already past).
func apiKeyRateLimitExceeded(limitErr error, windowStart *time.Time, window time.Duration) error {
	wrapped := &APIKeyRateLimitError{err: limitErr}
	if windowStart != nil {
		if reset := windowStart.Add(window); reset.After(time.Now()) {
			wrapped.resetAt = &reset
		}
	}
	return wrapped
}

func isSubscriptionUsageLimitError(err error) bool {
	return errors.Is(err, ErrFiveHourLimitExceeded) || errors.Is(err, ErrDailyLimitExceeded) ||
		errors.Is(err, ErrWeeklyLimitExceeded) || errors.Is(err, ErrMonthlyLimitExceeded)
}

// withSubscriptionLimitDetail wraps a usage-limit error with group's name and
// the reset time of sub's exhausted window. Other errors, and a nil sub or
// group, are returned unchanged (the name or time is then left out).
func withSubscriptionLimitDetail(err error, sub *UserSubscription, group *Group) error {
	if err == nil || !isSubscriptionUsageLimitError(err) {
		return err
	}
	var already *SubscriptionLimitError
	if errors.As(err, &already) {
		return err
	}
	limitErr := &SubscriptionLimitError{err: err}
	if group != nil {
		limitErr.groupName = group.Name
	}
	if sub != nil {
		switch {
		case errors.Is(err, ErrFiveHourLimitExceeded):
			limitErr.resetAt = sub.FiveHourResetTime()
		case errors.Is(err, ErrDailyLimitExceeded):
			limitErr.resetAt = sub.DailyResetTime()
		case errors.Is(err, ErrWeeklyLimitExceeded):
			limitErr.resetAt = sub.WeeklyResetTime()
		case errors.Is(err, ErrMonthlyLimitExceeded):
			limitErr.resetAt = sub.MonthlyResetTime()
		}
		// A window that already ended (cached usage not yet reset) has no
		// meaningful reset time to show.
		if limitErr.resetAt != nil && !limitErr.resetAt.After(time.Now()) {
			limitErr.resetAt = nil
		}
	}
	return limitErr
}

// LimitErrorMessage is the client-facing text of a subscription or API Key
// limit error: the detailed text when it carries one, otherwise the error's
// plain message.
func LimitErrorMessage(err error) string {
	var limitErr *SubscriptionLimitError
	if errors.As(err, &limitErr) {
		return limitErr.Message()
	}
	var keyLimitErr *APIKeyRateLimitError
	if errors.As(err, &keyLimitErr) {
		return keyLimitErr.Message()
	}
	var appErr *infraerrors.ApplicationError
	if errors.As(err, &appErr) && appErr.Message != "" {
		return appErr.Message
	}
	return err.Error()
}
