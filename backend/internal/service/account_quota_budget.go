package service

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const newSessionFiveHourUtilizationThreshold = 0.80

// NewSessionQuotaGuardPolicy controls the optional admission guard for new
// conversations. Existing sticky or pinned conversations are not affected.
type NewSessionQuotaGuardPolicy struct {
	Enabled          bool
	ThresholdPercent float64
}

// OpenAINewSessionQuotaGuardError distinguishes a deliberate new-session
// admission hold from a genuinely empty or unhealthy account pool.
type OpenAINewSessionQuotaGuardError struct {
	RetryAfter time.Duration
}

func (e *OpenAINewSessionQuotaGuardError) Error() string {
	if e == nil || e.RetryAfter <= 0 {
		return "OpenAI new-session quota guard is active"
	}
	return fmt.Sprintf("OpenAI new-session quota guard is active; retry after %s", e.RetryAfter.Round(time.Second))
}

func defaultNewSessionQuotaGuardPolicy() NewSessionQuotaGuardPolicy {
	return NewSessionQuotaGuardPolicy{Enabled: true, ThresholdPercent: newSessionFiveHourUtilizationThreshold * 100}
}

// shouldRejectNewSessionForHighFiveHourUsage reports whether an OAuth-backed
// account should be reserved for its existing sessions. The gate deliberately
// ignores 7d usage and does not rank accounts by remaining quota: it only keeps
// a new session off an account whose current, unexpired 5h window is over 80%.
// Missing or malformed usage/reset data fails open.
func shouldRejectNewSessionForHighFiveHourUsage(account *Account, now time.Time) bool {
	return shouldRejectNewSessionForHighFiveHourUsageWithPolicy(account, now, defaultNewSessionQuotaGuardPolicy())
}

func shouldRejectNewSessionForHighFiveHourUsageWithPolicy(account *Account, now time.Time, policy NewSessionQuotaGuardPolicy) bool {
	if !policy.Enabled {
		return false
	}
	if policy.ThresholdPercent < 1 || policy.ThresholdPercent > 100 {
		policy = defaultNewSessionQuotaGuardPolicy()
	}
	if account == nil {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}

	var (
		usedRatio float64
		resetAt   time.Time
		ok        bool
	)
	switch {
	case account.IsAnthropicOAuthOrSetupToken():
		usedRatio, ok = validUtilization(account.Extra, "session_window_utilization", 1)
		if !ok || account.SessionWindowEnd == nil || account.SessionWindowEnd.IsZero() {
			return false
		}
		resetAt = account.SessionWindowEnd.UTC()
	case account.IsOpenAIOAuth():
		usedPercent, valid := validUtilization(account.Extra, "codex_5h_used_percent", 100)
		if !valid {
			return false
		}
		usedRatio = usedPercent / 100
		resetAt, ok = openAIFiveHourResetAt(account.Extra)
		if !ok {
			return false
		}
	default:
		return false
	}

	return resetAt.After(now) && usedRatio > policy.ThresholdPercent/100
}

func openAINewSessionQuotaGuardRetryAfter(account *Account, now time.Time, policy NewSessionQuotaGuardPolicy) time.Duration {
	if !shouldRejectNewSessionForHighFiveHourUsageWithPolicy(account, now, policy) {
		return 0
	}
	resetAt, ok := openAIFiveHourResetAt(account.Extra)
	if !ok {
		return 0
	}
	return resetAt.Sub(now)
}

func validUtilization(extra map[string]any, key string, max float64) (float64, bool) {
	value, ok := extraNumber(extra, key)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > max {
		return 0, false
	}
	return value, true
}

func openAIFiveHourResetAt(extra map[string]any) (time.Time, bool) {
	if resetAt, ok := extraTime(extra, "codex_5h_reset_at"); ok && !resetAt.IsZero() {
		return resetAt, true
	}

	seconds, ok := extraNumber(extra, "codex_5h_reset_after_seconds")
	if !ok || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return time.Time{}, false
	}
	updatedAt, ok := extraTime(extra, "codex_usage_updated_at")
	if !ok || updatedAt.IsZero() {
		return time.Time{}, false
	}
	maxDurationSeconds := float64(math.MaxInt64) / float64(time.Second)
	if seconds > maxDurationSeconds {
		return time.Time{}, false
	}
	return updatedAt.Add(time.Duration(seconds * float64(time.Second))), true
}

func extraNumber(extra map[string]any, key string) (float64, bool) {
	if len(extra) == 0 || key == "" {
		return 0, false
	}
	raw, ok := extra[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint64:
		return float64(v), true
	case uint32:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func extraTime(extra map[string]any, key string) (time.Time, bool) {
	if len(extra) == 0 || key == "" {
		return time.Time{}, false
	}
	raw, ok := extra[key]
	if !ok || raw == nil {
		return time.Time{}, false
	}
	switch v := raw.(type) {
	case time.Time:
		return v.UTC(), true
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return time.Time{}, false
		}
		if t, err := time.Parse(time.RFC3339, trimmed); err == nil {
			return t.UTC(), true
		}
		if ts, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return unixTimestampToTime(ts), true
		}
	case json.Number:
		if ts, err := v.Int64(); err == nil {
			return unixTimestampToTime(ts), true
		}
	default:
		if f, ok := extraNumber(map[string]any{key: raw}, key); ok {
			return unixTimestampToTime(int64(f)), true
		}
	}
	return time.Time{}, false
}

func unixTimestampToTime(ts int64) time.Time {
	if ts > 1e11 {
		ts = ts / 1000
	}
	return time.Unix(ts, 0).UTC()
}
