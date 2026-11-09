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
		msg += ", resets at " + e.resetAt.In(timezone.Location()).Format("2006-01-02 15:04 -07:00")
	}
	return msg
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

// SubscriptionErrorMessage is the client-facing text of a subscription
// validation error: the detailed text of a usage-limit error, otherwise the
// error's plain message.
func SubscriptionErrorMessage(err error) string {
	var limitErr *SubscriptionLimitError
	if errors.As(err, &limitErr) {
		return limitErr.Message()
	}
	var appErr *infraerrors.ApplicationError
	if errors.As(err, &appErr) && appErr.Message != "" {
		return appErr.Message
	}
	return err.Error()
}
