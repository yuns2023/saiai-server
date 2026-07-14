package service

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	quotaBudgetNeutralScore    = 0.50
	quotaBudgetWindowGap       = 0.08
	quotaBudgetScoreEpsilon    = 0.000001
	quotaBudgetMinTimeRatio    = 0.02
	quotaBudgetRemainingWeight = 0.50
	quotaBudgetSpendWeight     = 0.50
)

type accountQuotaBudgetScore struct {
	score float64
	known bool
}

func accountQuotaBudgetForScheduling(account *Account, requestedModel string, now time.Time) accountQuotaBudgetScore {
	if account == nil {
		return accountQuotaBudgetScore{score: quotaBudgetNeutralScore}
	}
	if now.IsZero() {
		now = time.Now()
	}

	switch {
	case account.IsOpenAI() && account.Type == AccountTypeOAuth:
		return openAIQuotaBudgetForScheduling(account, now)
	case account.IsAnthropicOAuthOrSetupToken():
		return claudeQuotaBudgetForScheduling(account, requestedModel, now)
	default:
		return accountQuotaBudgetScore{score: quotaBudgetNeutralScore}
	}
}

func openAIQuotaBudgetForScheduling(account *Account, now time.Time) accountQuotaBudgetScore {
	extra := account.Extra
	score7d := quotaWindowBudgetFromExtraPercent(extra, "codex_7d_used_percent", "codex_7d_reset_at", "codex_7d_reset_after_seconds", "codex_usage_updated_at", 7*24*time.Hour, now)
	score5h := quotaWindowBudgetFromExtraPercent(extra, "codex_5h_used_percent", "codex_5h_reset_at", "codex_5h_reset_after_seconds", "codex_usage_updated_at", 5*time.Hour, now)
	return combineQuotaWindowScores(score7d, score5h)
}

func claudeQuotaBudgetForScheduling(account *Account, requestedModel string, now time.Time) accountQuotaBudgetScore {
	extra := account.Extra

	used7dKey := "passive_usage_7d_utilization"
	reset7dKey := "passive_usage_7d_reset"
	if isClaudeSonnetModel(requestedModel) {
		if _, ok := extraNumber(extra, "passive_usage_7d_sonnet_utilization"); ok {
			used7dKey = "passive_usage_7d_sonnet_utilization"
			reset7dKey = "passive_usage_7d_sonnet_reset"
		}
	}

	score7d := quotaWindowBudgetFromExtraRatio(extra, used7dKey, reset7dKey, "", "passive_usage_sampled_at", 7*24*time.Hour, now)
	score5h := quotaWindowBudgetFromExtraRatio(extra, "session_window_utilization", "", "", "passive_usage_sampled_at", 5*time.Hour, now)
	if account.SessionWindowEnd != nil {
		score5h = quotaWindowBudgetFromUsageAndReset(extraNumberOrNaN(extra, "session_window_utilization"), account.SessionWindowEnd, 5*time.Hour, now)
	}
	return combineQuotaWindowScores(score7d, score5h)
}

func combineQuotaWindowScores(score7d, score5h accountQuotaBudgetScore) accountQuotaBudgetScore {
	known := score7d.known || score5h.known
	if !score7d.known {
		score7d.score = quotaBudgetNeutralScore
	}
	if !score5h.known {
		score5h.score = quotaBudgetNeutralScore
	}
	return accountQuotaBudgetScore{
		score: clampFloat(score7d.score*0.75+score5h.score*0.25, 0, 1),
		known: known,
	}
}

func quotaWindowBudgetFromExtraPercent(extra map[string]any, usedKey, resetAtKey, resetAfterKey, updatedAtKey string, window time.Duration, now time.Time) accountQuotaBudgetScore {
	usedPercent, ok := extraNumber(extra, usedKey)
	if !ok {
		return accountQuotaBudgetScore{score: quotaBudgetNeutralScore}
	}
	usedRatio := usedPercent / 100.0
	resetAt := extraResetTime(extra, resetAtKey, resetAfterKey, updatedAtKey, now)
	return quotaWindowBudgetFromUsageAndReset(usedRatio, resetAt, window, now)
}

func quotaWindowBudgetFromExtraRatio(extra map[string]any, usedKey, resetAtKey, resetAfterKey, updatedAtKey string, window time.Duration, now time.Time) accountQuotaBudgetScore {
	usedRatio, ok := extraNumber(extra, usedKey)
	if !ok {
		return accountQuotaBudgetScore{score: quotaBudgetNeutralScore}
	}
	resetAt := extraResetTime(extra, resetAtKey, resetAfterKey, updatedAtKey, now)
	return quotaWindowBudgetFromUsageAndReset(usedRatio, resetAt, window, now)
}

func quotaWindowBudgetFromUsageAndReset(usedRatio float64, resetAt *time.Time, window time.Duration, now time.Time) accountQuotaBudgetScore {
	if math.IsNaN(usedRatio) || math.IsInf(usedRatio, 0) {
		return accountQuotaBudgetScore{score: quotaBudgetNeutralScore}
	}
	usedRatio = clampFloat(usedRatio, 0, 1)
	remaining := 1 - usedRatio
	if resetAt == nil {
		return accountQuotaBudgetScore{score: remaining, known: true}
	}
	if !resetAt.After(now) {
		return accountQuotaBudgetScore{score: 1, known: true}
	}

	windowHours := window.Hours()
	if windowHours <= 0 {
		windowHours = 1
	}
	hoursToReset := resetAt.Sub(now).Hours()
	timeRatio := clampFloat(hoursToReset/windowHours, quotaBudgetMinTimeRatio, 1)
	spendability := clampFloat(remaining/timeRatio, 0, 1)
	score := remaining*quotaBudgetRemainingWeight + spendability*quotaBudgetSpendWeight
	return accountQuotaBudgetScore{score: clampFloat(score, 0, 1), known: true}
}

func filterByBestQuotaBudget(accounts []accountWithLoad, requestedModel string, now time.Time) []accountWithLoad {
	if len(accounts) <= 1 {
		return accounts
	}

	best := -1.0
	knownCount := 0
	scores := make([]float64, len(accounts))
	for i, item := range accounts {
		score := accountQuotaBudgetForScheduling(item.account, requestedModel, now)
		scores[i] = score.score
		if score.known {
			knownCount++
		}
		if score.score > best {
			best = score.score
		}
	}
	if knownCount == 0 {
		return accounts
	}

	out := make([]accountWithLoad, 0, len(accounts))
	for i, item := range accounts {
		if scores[i] >= best-quotaBudgetWindowGap {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return accounts
	}
	return out
}

func filterByTopQuotaBudget(accounts []accountWithLoad, requestedModel string, now time.Time) []accountWithLoad {
	if len(accounts) <= 1 {
		return accounts
	}

	best := -1.0
	scores := make([]float64, len(accounts))
	for i, item := range accounts {
		score := accountQuotaBudgetForScheduling(item.account, requestedModel, now)
		if !score.known {
			return accounts
		}
		scores[i] = score.score
		if score.score > best {
			best = score.score
		}
	}

	out := make([]accountWithLoad, 0, len(accounts))
	for i, item := range accounts {
		if scores[i] >= best-quotaBudgetScoreEpsilon {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return accounts
	}
	return out
}

func filterWithinPriorityByQuotaBudget(accounts []accountWithLoad, requestedModel string, now time.Time) []accountWithLoad {
	if len(accounts) <= 1 {
		return accounts
	}
	groups := make(map[int][]accountWithLoad)
	priorities := make([]int, 0, len(accounts))
	seen := make(map[int]struct{})
	for _, item := range accounts {
		if item.account == nil {
			continue
		}
		priority := item.account.Priority
		if _, ok := seen[priority]; !ok {
			seen[priority] = struct{}{}
			priorities = append(priorities, priority)
		}
		groups[priority] = append(groups[priority], item)
	}
	out := make([]accountWithLoad, 0, len(accounts))
	for _, priority := range priorities {
		out = append(out, filterByBestQuotaBudget(groups[priority], requestedModel, now)...)
	}
	return out
}

func filterWithinPriorityAndLoadByTopQuotaBudget(accounts []accountWithLoad, requestedModel string, now time.Time) []accountWithLoad {
	if len(accounts) <= 1 {
		return accounts
	}
	type groupKey struct {
		priority int
		loadRate int
	}

	groups := make(map[groupKey][]accountWithLoad)
	keys := make([]groupKey, 0, len(accounts))
	seen := make(map[groupKey]struct{})
	for _, item := range accounts {
		if item.account == nil {
			continue
		}
		loadRate := 0
		if item.loadInfo != nil {
			loadRate = item.loadInfo.LoadRate
		}
		key := groupKey{priority: item.account.Priority, loadRate: loadRate}
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], item)
	}

	out := make([]accountWithLoad, 0, len(accounts))
	for _, key := range keys {
		out = append(out, filterByTopQuotaBudget(groups[key], requestedModel, now)...)
	}
	return out
}

func extraResetTime(extra map[string]any, resetAtKey, resetAfterKey, updatedAtKey string, now time.Time) *time.Time {
	if resetAtKey != "" {
		if t, ok := extraTime(extra, resetAtKey); ok {
			return &t
		}
	}
	if resetAfterKey == "" {
		return nil
	}
	seconds, ok := extraNumber(extra, resetAfterKey)
	if !ok {
		return nil
	}
	base := now
	if updatedAtKey != "" {
		if t, ok := extraTime(extra, updatedAtKey); ok {
			base = t
		}
	}
	if seconds < 0 {
		seconds = 0
	}
	resetAt := base.Add(time.Duration(seconds) * time.Second)
	return &resetAt
}

func extraNumberOrNaN(extra map[string]any, key string) float64 {
	if v, ok := extraNumber(extra, key); ok {
		return v
	}
	return math.NaN()
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

func isClaudeSonnetModel(model string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(model)), "sonnet")
}

func clampFloat(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
