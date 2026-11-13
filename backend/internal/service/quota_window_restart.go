package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// A quota window restart records that the upstream reset an account's usage
// to zero in the middle of a window while keeping the window's reset time
// (e.g. a plan upgrade). Window statistics (platform usage, A/U, capacity
// estimate) then start at the restart instead of reset - length. It is
// statistics only: scheduling and rate limiting never read it.
//
// Stored in accounts.extra as
//
//	"quota_window_restarts": {"seven_day": {"start": <unix>, "reset": <unix>,
//	    "source": "auto"|"manual", "baseline_utilization": <ratio, auto only>}}
//
// A restart applies only while the window it was recorded for is current: once
// the reset time moves, it is ignored and later overwritten.
const QuotaWindowRestartsExtraKey = "quota_window_restarts"

const (
	QuotaWindowRestartSourceAuto   = "auto"
	QuotaWindowRestartSourceManual = "manual"

	// QuotaWindowRestartMinDrop is the drop of the shared 7d utilization (ratio)
	// under an unchanged reset that counts as an upstream reset. Within one
	// window the upstream utilization never decreases; the gateway samples it
	// from response headers as they arrive, so concurrent responses differ by
	// rounding only.
	QuotaWindowRestartMinDrop = 0.05
)

// QuotaWindowRestartWindows are the windows that support restarts.
var QuotaWindowRestartWindows = map[string]struct{}{"seven_day": {}}

// ErrQuotaWindowRestartConflict: the window the admin looked at is no longer
// the current one (its reset time changed).
var ErrQuotaWindowRestartConflict = infraerrors.Conflict("QUOTA_WINDOW_RESET_CHANGED", "quota window reset time changed; reload and retry")

func quotaWindowRestartInvalid(message string) error {
	return infraerrors.BadRequest("QUOTA_WINDOW_RESTART_INVALID", message)
}

type QuotaWindowRestart struct {
	Start  time.Time
	Reset  time.Time
	Source string
	// BaselineUtilization is the upstream utilization (ratio) observed at Start
	// by automatic detection; nil for manual restarts.
	BaselineUtilization *float64
}

// QuotaWindowRestarts maps window keys (seven_day) to their restart.
type QuotaWindowRestarts map[string]*QuotaWindowRestart

// QuotaWindowRestartInfo is the admin-facing description of an applied restart.
type QuotaWindowRestartInfo struct {
	Start  time.Time `json:"start"`
	Source string    `json:"source"`
}

func (r *QuotaWindowRestart) info() *QuotaWindowRestartInfo {
	if r == nil {
		return nil
	}
	return &QuotaWindowRestartInfo{Start: r.Start, Source: r.Source}
}

// extraValue is the JSON object stored under the window key.
func (r *QuotaWindowRestart) extraValue() map[string]any {
	value := map[string]any{
		"start":  r.Start.Unix(),
		"reset":  r.Reset.Unix(),
		"source": r.Source,
	}
	if r.BaselineUtilization != nil {
		value["baseline_utilization"] = *r.BaselineUtilization
	}
	return value
}

// parseQuotaWindowRestarts reads the restarts from accounts.extra; malformed
// entries are skipped.
func parseQuotaWindowRestarts(extra map[string]any) QuotaWindowRestarts {
	raw, ok := extra[QuotaWindowRestartsExtraKey].(map[string]any)
	if !ok {
		return nil
	}
	result := make(QuotaWindowRestarts, len(raw))
	for key := range QuotaWindowRestartWindows {
		entry, ok := raw[key].(map[string]any)
		if !ok {
			continue
		}
		start, okStart := restartUnixSeconds(entry["start"])
		reset, okReset := restartUnixSeconds(entry["reset"])
		source, _ := entry["source"].(string)
		if !okStart || !okReset || (source != QuotaWindowRestartSourceAuto && source != QuotaWindowRestartSourceManual) {
			continue
		}
		restart := &QuotaWindowRestart{Start: time.Unix(start, 0), Reset: time.Unix(reset, 0), Source: source}
		if source == QuotaWindowRestartSourceAuto {
			if baseline, ok := restartFloat(entry["baseline_utilization"]); ok && baseline >= 0 && baseline <= anthropicUtilizationMaxRatio {
				restart.BaselineUtilization = &baseline
			}
		}
		result[key] = restart
	}
	return result
}

func restartFloat(v any) (float64, bool) {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case json.Number:
		parsed, err := n.Float64()
		if err != nil {
			return 0, false
		}
		f = parsed
	case int64:
		f = float64(n)
	case int:
		f = float64(n)
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func restartUnixSeconds(v any) (int64, bool) {
	f, ok := restartFloat(v)
	if !ok || f <= 0 || f >= 253402300800 {
		return 0, false
	}
	return int64(f), true
}

// WeeklyRestartObservation is one fresh observation of the shared Anthropic
// 7d window: both the utilization (ratio) and the reset from the same
// response, taken at ObservedAt.
type WeeklyRestartObservation struct {
	Utilization float64
	Reset       time.Time
	ObservedAt  time.Time
}

// AccountQuotaWindowRestartRepository is implemented by the account repository.
// Callers fall back to UpdateExtra when the repository does not implement it.
type AccountQuotaWindowRestartRepository interface {
	// UpdateExtraDetectingWeeklyRestart merges updates into extra like
	// UpdateExtra and, in the same statement and against the stored values,
	// records an automatic seven_day restart when the stored shared 7d sample
	// has the same reset (within a minute) and a utilization at least
	// QuotaWindowRestartMinDrop higher than obs. A manual restart for the same
	// window is never replaced. Returns the restart it wrote, or nil.
	UpdateExtraDetectingWeeklyRestart(ctx context.Context, id int64, updates map[string]any, obs WeeklyRestartObservation) (*QuotaWindowRestart, error)
	// SetQuotaWindowRestart stores (restart != nil) or removes the restart of
	// one window without touching other extra keys.
	SetQuotaWindowRestart(ctx context.Context, id int64, window string, restart *QuotaWindowRestart) error
}

// weeklyRestartObservationFromUpdates returns the observation when updates
// carry a complete, valid shared 7d sample (utilization and reset together).
func weeklyRestartObservationFromUpdates(updates map[string]any, observedAt time.Time) (WeeklyRestartObservation, bool) {
	util, okUtil := restartFloat(updates["passive_usage_7d_utilization"])
	reset, okReset := restartUnixSeconds(updates["passive_usage_7d_reset"])
	if !okUtil || !okReset || util < 0 || util > anthropicUtilizationMaxRatio {
		return WeeklyRestartObservation{}, false
	}
	return WeeklyRestartObservation{Utilization: util, Reset: time.Unix(reset, 0), ObservedAt: observedAt.Truncate(time.Second)}, true
}

// updateExtraDetectingWeeklyRestart writes a passive/active usage sample. For
// Anthropic OAuth/setup-token accounts with a complete shared 7d sample it
// also runs the restart detection; otherwise it is plain UpdateExtra. A
// detected restart is merged into updates so callers that merge updates into
// their in-memory account see it.
func updateExtraDetectingWeeklyRestart(ctx context.Context, repo AccountRepository, account *Account, updates map[string]any, observedAt time.Time) error {
	if account == nil || !account.IsAnthropicOAuthOrSetupToken() {
		return repo.UpdateExtra(ctx, accountIDOrZero(account), updates)
	}
	detector, ok := repo.(AccountQuotaWindowRestartRepository)
	obs, complete := weeklyRestartObservationFromUpdates(updates, observedAt)
	if !ok || !complete {
		return repo.UpdateExtra(ctx, account.ID, updates)
	}
	restart, err := detector.UpdateExtraDetectingWeeklyRestart(ctx, account.ID, updates, obs)
	if err != nil {
		return err
	}
	if restart != nil {
		merged := map[string]any{}
		if current, ok := account.Extra[QuotaWindowRestartsExtraKey].(map[string]any); ok {
			for k, v := range current {
				merged[k] = v
			}
		}
		merged["seven_day"] = restart.extraValue()
		updates[QuotaWindowRestartsExtraKey] = merged
	}
	return nil
}

func accountIDOrZero(account *Account) int64 {
	if account == nil {
		return 0
	}
	return account.ID
}

// currentQuotaWindowReset returns the reset time the admin usage view shows
// for the window (Anthropic passive/active 7d sample, Codex 7d snapshot).
func currentQuotaWindowReset(account *Account, window string, now time.Time) *time.Time {
	if account == nil || window != "seven_day" {
		return nil
	}
	var progress *UsageProgress
	switch {
	case account.IsAnthropicOAuthOrSetupToken():
		progress = buildPassiveUsageWindow(account.Extra, "passive_usage_7d_utilization", "passive_usage_7d_reset", now)
	case account.Platform == PlatformOpenAI && account.IsOAuth():
		progress = buildCodexUsageProgressFromExtra(account.Extra, "7d", now)
	}
	if progress == nil {
		return nil
	}
	return progress.ResetsAt
}

// SetQuotaWindowRestartInput is the admin request to restart window statistics.
type SetQuotaWindowRestartInput struct {
	Window        string
	ExpectedReset time.Time
	// Start defaults to now; it must lie in [reset - window length, now].
	Start *time.Time
}

// SetManualQuotaWindowRestart records a manual restart for the window the
// admin was looking at. ExpectedReset must match the current reset (within a
// minute), otherwise ErrQuotaWindowRestartConflict: the window moved on.
func (s *AccountUsageService) SetManualQuotaWindowRestart(ctx context.Context, accountID int64, input SetQuotaWindowRestartInput) (*QuotaWindowRestartInfo, error) {
	repo, ok := s.accountRepo.(AccountQuotaWindowRestartRepository)
	if !ok {
		return nil, fmt.Errorf("quota window restart not supported")
	}
	if _, ok := QuotaWindowRestartWindows[input.Window]; !ok {
		return nil, quotaWindowRestartInvalid(fmt.Sprintf("unsupported window %q", input.Window))
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	reset := currentQuotaWindowReset(account, input.Window, now)
	window, valid := resolveQuotaCostWindow(input.Window, reset, now)
	if !valid {
		return nil, quotaWindowRestartInvalid("the account has no current window with a valid reset time")
	}
	if absDuration(window.Reset.Sub(input.ExpectedReset)) > quotaResetMatchTolerance {
		return nil, ErrQuotaWindowRestartConflict
	}
	start := now
	if input.Start != nil {
		start = *input.Start
	}
	start = start.Truncate(time.Second)
	// The window start itself is no restart: withRestart would ignore it.
	if !start.After(window.Start) || start.After(now) {
		return nil, quotaWindowRestartInvalid(fmt.Sprintf("start must be after %s and not in the future", window.Start.UTC().Format(time.RFC3339)))
	}
	restart := &QuotaWindowRestart{Start: start, Reset: window.Reset, Source: QuotaWindowRestartSourceManual}
	if err := repo.SetQuotaWindowRestart(ctx, accountID, input.Window, restart); err != nil {
		return nil, err
	}
	return restart.info(), nil
}

// ClearQuotaWindowRestart removes the window's restart (manual or automatic).
// Like SetManualQuotaWindowRestart it is bound to the window the admin was
// looking at: a moved window returns ErrQuotaWindowRestartConflict so a stale
// dialog cannot remove the next window's restart.
func (s *AccountUsageService) ClearQuotaWindowRestart(ctx context.Context, accountID int64, window string, expectedReset time.Time) error {
	repo, ok := s.accountRepo.(AccountQuotaWindowRestartRepository)
	if !ok {
		return fmt.Errorf("quota window restart not supported")
	}
	window = strings.TrimSpace(window)
	if _, ok := QuotaWindowRestartWindows[window]; !ok {
		return quotaWindowRestartInvalid(fmt.Sprintf("unsupported window %q", window))
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return err
	}
	reset := currentQuotaWindowReset(account, window, time.Now())
	if reset == nil || absDuration(reset.Sub(expectedReset)) > quotaResetMatchTolerance {
		return ErrQuotaWindowRestartConflict
	}
	return repo.SetQuotaWindowRestart(ctx, accountID, window, nil)
}
