package service

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// marketFiveHourResetMaxFuture bounds a 5h window end (a few minutes of
// slack over the window length).
const marketFiveHourResetMaxFuture = 5*time.Hour + 10*time.Minute

// buildMarketUsageObservations turns one response's rate-limit headers into
// per-window observations. A window is observed only with a valid
// utilization AND a plausible future reset; the "no 5h window" flag only
// from a success response (like passive_usage_5h_absent).
func buildMarketUsageObservations(headers http.Header, sample anthropicPassiveUsageSample, now time.Time, success bool) MarketUsageObservations {
	var out MarketUsageObservations
	at := now.Unix()
	if sample.hasValidFiveHour && sample.hasFiveHourUtilization {
		if util, ok := sample.updates["session_window_utilization"].(float64); ok {
			if resetAt, ok := parseAnthropicFutureReset(headers.Get(anthropicFiveHourHeaderPrefix+"reset"), now, marketFiveHourResetMaxFuture); ok {
				out.FiveHour = &MarketUsageObservation{Utilization: util, ResetAt: resetAt.Unix(), ObservedAt: at}
			}
		}
	}
	if util, ok := sample.updates["passive_usage_7d_utilization"].(float64); ok {
		if reset, ok := sample.updates["passive_usage_7d_reset"].(int64); ok && reset > 0 {
			out.SevenDay = &MarketUsageObservation{Utilization: util, ResetAt: reset, ObservedAt: at}
		}
	}
	if success {
		switch {
		case sample.hasValidFiveHour:
			out.FiveHourAbsent = &MarketFiveHourAbsence{Absent: false, ObservedAt: at}
		case !sample.hasFiveHourHeader && sample.hasValidSevenDay:
			out.FiveHourAbsent = &MarketFiveHourAbsence{Absent: true, ObservedAt: at}
		}
	}
	return out
}

// recordMarketUsageObservations stores the observations of a listed
// account (no-op for every other account).
func (s *RateLimitService) recordMarketUsageObservations(ctx context.Context, account *Account, headers http.Header, sample anthropicPassiveUsageSample, now time.Time, success bool) {
	if s == nil || !s.market.ShouldRecordUsage(account) {
		return
	}
	obs := buildMarketUsageObservations(headers, sample, now, success)
	if obs.FiveHour == nil && obs.SevenDay == nil && obs.FiveHourAbsent == nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := s.market.usage.RecordMarketUsageObservations(writeCtx, account.ID, obs); err != nil {
		slog.Warn("market_usage_observation_write_failed", "account_id", account.ID, "error", err)
	}
}
