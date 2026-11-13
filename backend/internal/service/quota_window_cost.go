package service

import (
	"context"
	"math"
	"time"
)

const (
	quotaFiveHourWindowLength = 5 * time.Hour
	quotaSevenDayWindowLength = 7 * 24 * time.Hour
	// Below this utilization the capacity estimate is dominated by rounding of
	// the upstream percentage, so it is not shown.
	quotaEstimateMinUtilization = 10.0
	// Admin progress and the sampled snapshot describe the same window when
	// their reset times agree within this tolerance.
	quotaResetMatchTolerance = time.Minute
)

// AccountWindowCostQuery asks for the standard-price usage (usage_logs.total_cost,
// all users) of one account in [Start, End), and separately in [Start, SampleEnd).
type AccountWindowCostQuery struct {
	AccountID int64
	Start     time.Time
	SampleEnd time.Time
	End       time.Time
}

// AccountWindowCost is the answer for one AccountWindowCostQuery. Accounts
// without rows in the window report known zeros.
type AccountWindowCost struct {
	UsedCost    float64
	SampledCost float64
}

// AccountWindowCostReader answers many window queries with one SQL statement.
// Results are returned in the same order as the queries.
type AccountWindowCostReader interface {
	GetAccountWindowCosts(ctx context.Context, queries []AccountWindowCostQuery) ([]AccountWindowCost, error)
}

// quotaCostWindow is the current five_hour or seven_day quota window derived
// from the upstream reset time. SampledAt is set only when the utilization
// sample can back a capacity estimate.
type quotaCostWindow struct {
	Key         string
	Start       time.Time
	Reset       time.Time
	Utilization float64
	SampledAt   *time.Time
	// Restart is the stats-only restart moved into Start (nil when none applies).
	Restart *QuotaWindowRestart
}

func quotaWindowLength(key string) (time.Duration, bool) {
	switch key {
	case "five_hour":
		return quotaFiveHourWindowLength, true
	case "seven_day":
		return quotaSevenDayWindowLength, true
	default:
		return 0, false
	}
}

// resolveQuotaCostWindow returns the window [reset - length, reset) containing
// now. A missing, elapsed, or too-far-future reset gives no window: callers
// must report the cost as unknown rather than fall back to a rolling window.
// The scheduler's own window start (GetCurrentWindowStartTime /
// SessionWindowStart) is deliberately not used here.
func resolveQuotaCostWindow(key string, reset *time.Time, now time.Time) (quotaCostWindow, bool) {
	length, ok := quotaWindowLength(key)
	if !ok || reset == nil || !now.Before(*reset) || reset.Sub(now) > length {
		return quotaCostWindow{}, false
	}
	return quotaCostWindow{Key: key, Start: reset.Add(-length), Reset: *reset}, true
}

// withSample records the utilization sample when it is reliable enough for a
// capacity estimate: at least 10%, finite, and taken inside the window. The
// sample may be old: the estimate pairs it only with platform usage up to the
// sample time, so idle accounts keep their estimate until the window resets.
// withRestart moves Start to a recorded mid-window restart (the upstream reset
// the usage to zero but kept the reset time). It applies only to the window
// the restart was recorded for (same reset within the match tolerance) and
// only when it starts later than reset - length. Call it before withSample:
// samples taken before the restart must not back an estimate.
func (w quotaCostWindow) withRestart(restart *QuotaWindowRestart, now time.Time) quotaCostWindow {
	if restart == nil || absDuration(restart.Reset.Sub(w.Reset)) > quotaResetMatchTolerance ||
		!restart.Start.After(w.Start) || restart.Start.After(now) || !restart.Start.Before(w.Reset) {
		return w
	}
	r := *restart
	w.Start = restart.Start
	w.Restart = &r
	return w
}

func (w quotaCostWindow) withSample(utilization float64, sampledAt *time.Time, now time.Time) quotaCostWindow {
	if math.IsNaN(utilization) || math.IsInf(utilization, 0) || utilization < quotaEstimateMinUtilization ||
		sampledAt == nil || sampledAt.After(now) ||
		sampledAt.Before(w.Start) || !sampledAt.Before(w.Reset) {
		return w
	}
	sample := *sampledAt
	w.Utilization = utilization
	w.SampledAt = &sample
	return w
}

func (w quotaCostWindow) query(accountID int64, now time.Time) AccountWindowCostQuery {
	sampleEnd := w.Start
	if w.SampledAt != nil {
		sampleEnd = *w.SampledAt
	}
	return AccountWindowCostQuery{AccountID: accountID, Start: w.Start, SampleEnd: sampleEnd, End: now}
}

// estimate converts the platform usage up to the sample time into a full-window
// capacity. Usage outside the platform is invisible here, so the estimate is a
// lower bound in practice; zero platform usage gives no estimate.
//
// After a restart the utilization still contains whatever was used before
// Start: an automatic restart records that baseline and only the increase
// since it is used (at least quotaEstimateMinUtilization points); a manual
// restart has no trustworthy baseline and gives no estimate.
func (w quotaCostWindow) estimate(sampledCost float64) *float64 {
	if w.SampledAt == nil || math.IsNaN(sampledCost) || sampledCost <= 0 {
		return nil
	}
	used := w.Utilization
	if w.Restart != nil {
		if w.Restart.BaselineUtilization == nil {
			return nil
		}
		used -= *w.Restart.BaselineUtilization * 100
		// Tolerance: 38 − 0.28×100 is 9.999999999999996 in float64.
		if math.IsNaN(used) || used < quotaEstimateMinUtilization-1e-9 {
			return nil
		}
	}
	value := sampledCost / (used / 100)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

// quotaCostWindows derives the five_hour and seven_day cost windows from the
// same snapshot the hosted page displays. Model-scoped weekly windows are not
// included: their cost cannot be attributed from usage_logs.
// restarts holds the account's recorded window restarts (see
// parseQuotaWindowRestarts); they move the window start before sampling.
func quotaCostWindows(snapshot HostedAccount, restarts QuotaWindowRestarts, now time.Time) []quotaCostWindow {
	result := make([]quotaCostWindow, 0, 2)
	for _, w := range snapshot.Windows {
		window, ok := resolveQuotaCostWindow(w.Key, w.ResetsAt, now)
		if !ok {
			continue
		}
		window = window.withRestart(restarts[w.Key], now)
		result = append(result, window.withSample(w.Utilization, snapshot.UpdatedAt, now))
	}
	return result
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
