package service

import (
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	anthropicUnifiedRateLimitHeaderPrefix = "anthropic-ratelimit-unified-"
	anthropicFiveHourHeaderPrefix         = anthropicUnifiedRateLimitHeaderPrefix + "5h-"

	// passive_usage_5h_absent is true while the latest reliable observation
	// carried the shared 7d window but no 5h window at all (Claude Ultra since
	// 2026-10-30). It is an observation, not a plan capability: it is set back
	// to false as soon as a valid 5h header returns. UpdateExtra writes nil as
	// JSON null instead of deleting the key, so readers must check for true.
	anthropicFiveHourAbsentExtraKey = "passive_usage_5h_absent"

	anthropicWeeklyResetMaxFuture   = 8 * 24 * time.Hour
	anthropicUtilizationMaxRatio    = 2.0
	anthropicHeaderDiagInterval     = time.Hour
	anthropicHeaderDiagMaxHeaders   = 32
	anthropicHeaderDiagMaxValueLen  = 64
	anthropicUnknownWindowLogMaxLen = 32
	anthropicUnknownWindowLogLimit  = 64
)

// Windows the Gateway understands. Anything else is only logged so a new
// upstream window shows up in the logs without being guessed into scheduling.
var anthropicKnownRateLimitWindows = map[string]struct{}{
	"5h":        {},
	"7d":        {},
	"7d-sonnet": {},
	"7d_oi":     {},
}

var anthropicRateLimitWindowFieldSuffixes = []string{
	"-surpassed-threshold",
	"-utilization",
	"-status",
	"-reset",
}

// Aggregate headers that share the prefix and suffix shape but are not windows.
var anthropicNonWindowRateLimitNames = map[string]struct{}{
	"overage": {},
}

type anthropicPassiveUsageSample struct {
	updates map[string]any

	fiveHourUtilizationPercent float64
	hasFiveHourUtilization     bool
	// hasFiveHourHeader is true when any 5h-* header is present, valid or not.
	hasFiveHourHeader bool
	// hasValidFiveHour is true when 5h-status carries a known value.
	hasValidFiveHour  bool
	hasValidSevenDay  bool
	unknownWindows    []string
	hasUnifiedHeaders bool
}

// hasUsageData reports whether any utilization or reset value was sampled, as
// opposed to bookkeeping updates such as clearing a stale value.
func (s anthropicPassiveUsageSample) hasUsageData() bool {
	for key, value := range s.updates {
		if value != nil && key != anthropicFiveHourAbsentExtraKey {
			return true
		}
	}
	return false
}

var anthropicKnownWindowStatuses = map[string]struct{}{
	"allowed":         {},
	"allowed_warning": {},
	"rejected":        {},
}

// sampleAnthropicPassiveUsageHeaders reads the usage windows from a response
// without side effects, so success and 429 responses share one parser. The 5h
// and weekly windows are sampled independently: Claude Ultra responses carry
// no 5h headers at all.
func sampleAnthropicPassiveUsageHeaders(headers http.Header, now time.Time) anthropicPassiveUsageSample {
	sample := anthropicPassiveUsageSample{updates: make(map[string]any, 8)}
	if len(headers) == 0 {
		return sample
	}

	// 5h utilization is only meaningful together with the 5h window that
	// UpdateSessionWindow derives from 5h-status/5h-reset.
	fiveHourStatus := strings.ToLower(strings.TrimSpace(headers.Get(anthropicFiveHourHeaderPrefix + "status")))
	if _, known := anthropicKnownWindowStatuses[fiveHourStatus]; known {
		sample.hasValidFiveHour = true
		if util, ok := parseAnthropicUtilizationHeader(headers.Get(anthropicFiveHourHeaderPrefix + "utilization")); ok {
			sample.updates["session_window_utilization"] = util
			sample.fiveHourUtilizationPercent = normalizeUtilizationPercent(util)
			sample.hasFiveHourUtilization = true
		}
	}

	for _, window := range []struct{ header, key string }{
		{"7d", "passive_usage_7d"},
		{"7d-sonnet", "passive_usage_7d_sonnet"},
	} {
		prefix := anthropicUnifiedRateLimitHeaderPrefix + window.header + "-"
		if util, ok := parseAnthropicUtilizationHeader(headers.Get(prefix + "utilization")); ok {
			sample.updates[window.key+"_utilization"] = util
			if window.header == "7d" {
				sample.hasValidSevenDay = true
			}
		}
		if resetAt, ok := parseAnthropicFutureReset(headers.Get(prefix+"reset"), now, anthropicWeeklyResetMaxFuture); ok {
			sample.updates[window.key+"_reset"] = resetAt.Unix()
		}
	}
	addAnthropicFableUsageUpdates(sample.updates, headers)

	for name := range headers {
		lower := strings.ToLower(strings.TrimSpace(name))
		if !strings.HasPrefix(lower, anthropicUnifiedRateLimitHeaderPrefix) {
			continue
		}
		sample.hasUnifiedHeaders = true
		if strings.HasPrefix(lower, anthropicFiveHourHeaderPrefix) {
			sample.hasFiveHourHeader = true
		}
		if window, ok := anthropicRateLimitWindowName(lower); ok {
			if _, known := anthropicKnownRateLimitWindows[window]; !known {
				sample.unknownWindows = append(sample.unknownWindows, window)
			}
		}
	}
	sort.Strings(sample.unknownWindows)
	return sample
}

func anthropicRateLimitWindowName(lowerHeaderName string) (string, bool) {
	rest := strings.TrimPrefix(lowerHeaderName, anthropicUnifiedRateLimitHeaderPrefix)
	for _, suffix := range anthropicRateLimitWindowFieldSuffixes {
		if !strings.HasSuffix(rest, suffix) {
			continue
		}
		window := strings.TrimSuffix(rest, suffix)
		if window == "" {
			return "", false
		}
		if _, skip := anthropicNonWindowRateLimitNames[window]; skip {
			return "", false
		}
		return window, true
	}
	return "", false
}

func parseAnthropicUtilizationHeader(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	util, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(util) || math.IsInf(util, 0) || util < 0 || util > anthropicUtilizationMaxRatio {
		return 0, false
	}
	return util, true
}

// applyAnthropicFiveHourAbsence records whether the account still reports a
// 5h window. Only a success response with a valid shared 7d window and no 5h
// header at all marks it absent; partial or aggregate-only responses decide
// nothing. A valid 5h status always writes false, independent of the request's
// account snapshot, so a concurrent Ultra-shaped response cannot leave a
// stale true behind.
func applyAnthropicFiveHourAbsence(account *Account, sample anthropicPassiveUsageSample) {
	if account == nil || !account.IsAnthropicOAuthOrSetupToken() || sample.updates == nil {
		return
	}
	switch {
	case sample.hasValidFiveHour:
		sample.updates[anthropicFiveHourAbsentExtraKey] = false
	case !sample.hasFiveHourHeader && sample.hasValidSevenDay:
		sample.updates[anthropicFiveHourAbsentExtraKey] = true
		// A stale 5h utilization plus a future window end would otherwise keep
		// tripping the new-session 5h guard.
		sample.updates["session_window_utilization"] = nil
	}
}

// IsAnthropicFiveHourWindowAbsent reports whether the latest reliable
// response showed no 5h window for this Anthropic OAuth/setup-token account.
func (a *Account) IsAnthropicFiveHourWindowAbsent() bool {
	if a == nil || a.Extra == nil || !a.IsAnthropicOAuthOrSetupToken() {
		return false
	}
	absent, ok := a.Extra[anthropicFiveHourAbsentExtraKey].(bool)
	return ok && absent
}

type anthropicHeaderDiagnostics struct {
	mu             sync.Mutex
	missingLogged  map[int64]time.Time
	unknownWindows map[string]struct{}
}

var anthropicRateLimitHeaderDiag = &anthropicHeaderDiagnostics{
	missingLogged:  make(map[int64]time.Time),
	unknownWindows: make(map[string]struct{}),
}

// logAnthropicRateLimitHeaderShape surfaces upstream header-shape changes:
// a response without any 5h header (at most hourly per account) and each new
// window name (once per process, bounded).
func logAnthropicRateLimitHeaderShape(account *Account, headers http.Header, sample anthropicPassiveUsageSample, source string, now time.Time) {
	if account == nil || !account.IsAnthropicOAuthOrSetupToken() || !sample.hasUnifiedHeaders {
		return
	}
	d := anthropicRateLimitHeaderDiag
	var logMissing bool
	var newWindows []string

	d.mu.Lock()
	if !sample.hasFiveHourHeader {
		if last, ok := d.missingLogged[account.ID]; !ok || now.Sub(last) >= anthropicHeaderDiagInterval {
			d.missingLogged[account.ID] = now
			logMissing = true
		}
	}
	for _, window := range sample.unknownWindows {
		if len(window) > anthropicUnknownWindowLogMaxLen {
			window = window[:anthropicUnknownWindowLogMaxLen]
		}
		if _, seen := d.unknownWindows[window]; seen || len(d.unknownWindows) >= anthropicUnknownWindowLogLimit {
			continue
		}
		d.unknownWindows[window] = struct{}{}
		newWindows = append(newWindows, window)
	}
	d.mu.Unlock()

	if logMissing {
		slog.Warn("anthropic_ratelimit_5h_headers_absent",
			"account_id", account.ID,
			"source", source,
			"shared_7d_present", sample.hasValidSevenDay,
			"headers", anthropicUnifiedHeaderSummary(headers))
	}
	if len(newWindows) > 0 {
		slog.Warn("anthropic_ratelimit_unknown_window",
			"account_id", account.ID,
			"source", source,
			"windows", newWindows,
			"headers", anthropicUnifiedHeaderSummary(headers))
	}
}

func anthropicUnifiedHeaderSummary(headers http.Header) []string {
	out := make([]string, 0, 16)
	for name := range headers {
		lower := strings.ToLower(strings.TrimSpace(name))
		if !strings.HasPrefix(lower, anthropicUnifiedRateLimitHeaderPrefix) {
			continue
		}
		short := strings.TrimPrefix(lower, anthropicUnifiedRateLimitHeaderPrefix)
		if len(short) > anthropicHeaderDiagMaxValueLen {
			short = short[:anthropicHeaderDiagMaxValueLen]
		}
		value := strings.TrimSpace(headers.Get(name))
		if len(value) > anthropicHeaderDiagMaxValueLen {
			value = value[:anthropicHeaderDiagMaxValueLen]
		}
		out = append(out, short+"="+value)
	}
	sort.Strings(out)
	if len(out) > anthropicHeaderDiagMaxHeaders {
		out = out[:anthropicHeaderDiagMaxHeaders]
	}
	return out
}
