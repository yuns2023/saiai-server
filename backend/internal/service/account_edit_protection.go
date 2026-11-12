package service

import (
	"fmt"
	"maps"
	"strings"
)

// The admin edit page submits credentials and extra built from the account as
// it was when the page loaded ({...props.account.credentials}). Saving later
// must not put back values the server changed meanwhile: rotated OAuth tokens
// (an old refresh_token breaks the next refresh) and runtime state such as
// usage samples, quota counters and rate limits. These helpers take such keys
// from the locked current row instead of the submitted snapshot. The page
// never edits them (see EditAccountModal.vue: OAuth/setup-token edits only
// intercept_warmup_requests, temp_unschedulable_* and model_mapping).

// editorProtectedAuthCredentialKeys are the token and identity keys written by
// OAuth / setup-token authorization and refresh (Claude, OpenAI/Sora, Gemini,
// Antigravity builders) plus the refresh version stamp.
var editorProtectedAuthCredentialKeys = map[string]struct{}{
	"access_token":          {},
	"refresh_token":         {},
	"id_token":              {},
	"token_type":            {},
	"expires_in":            {},
	"expires_at":            {},
	"scope":                 {},
	"_token_version":        {},
	"email":                 {},
	"chatgpt_account_id":    {},
	"chatgpt_user_id":       {},
	"organization_id":       {},
	"plan_type":             {},
	"client_id":             {},
	"project_id":            {},
	"tier_id":               {},
	"oauth_type":            {},
	"drive_storage_limit":   {},
	"drive_storage_usage":   {},
	"drive_tier_updated_at": {},
}

// editorRuntimeExtraKeys are extra keys written by the server at runtime
// (billing, usage sampling, rate limiting, maintenance). Exact names: some
// config keys share prefixes (e.g. codex_cli_only, quota_*_limit).
var editorRuntimeExtraKeys = map[string]struct{}{
	"quota_used":                                                {},
	"quota_daily_used":                                          {},
	"quota_daily_start":                                         {},
	"quota_weekly_used":                                         {},
	"quota_weekly_start":                                        {},
	"session_window_utilization":                                {},
	"codex_primary_used_percent":                                {},
	"codex_primary_reset_after_seconds":                         {},
	"codex_primary_window_minutes":                              {},
	"codex_secondary_used_percent":                              {},
	"codex_secondary_reset_after_seconds":                       {},
	"codex_secondary_window_minutes":                            {},
	"codex_primary_over_secondary_percent":                      {},
	"codex_usage_updated_at":                                    {},
	"codex_5h_used_percent":                                     {},
	"codex_5h_reset_after_seconds":                              {},
	"codex_5h_window_minutes":                                   {},
	"codex_5h_reset_at":                                         {},
	"codex_7d_used_percent":                                     {},
	"codex_7d_reset_after_seconds":                              {},
	"codex_7d_window_minutes":                                   {},
	"codex_7d_reset_at":                                         {},
	modelRateLimitsKey:                                          {},
	"privacy_mode":                                              {},
	"antigravity_quota_scopes":                                  {},
	"antigravity_credits_overages":                              {},
	"drive_storage_limit":                                       {},
	"drive_storage_usage":                                       {},
	"drive_tier_updated_at":                                     {},
	"claude_oauth_carpool_last_maintenance_day":                 {},
	"claude_oauth_single_device_admission_last_maintenance_day": {},
}

// All passive_usage_* keys are usage samples written by the gateway.
const editorRuntimeExtraPrefix = "passive_usage_"

func isEditorRuntimeExtraKey(key string) bool {
	if _, ok := editorRuntimeExtraKeys[key]; ok {
		return true
	}
	return strings.HasPrefix(key, editorRuntimeExtraPrefix)
}

func isEditorProtectedAuthCredentialKey(key string) bool {
	_, ok := editorProtectedAuthCredentialKeys[key]
	return ok
}

// keepCurrentKeys replaces every key selected by owned in submitted with the
// current value: keys present now are copied, keys absent now are removed (a
// stale submitted value or JSON null must not bring them back).
func keepCurrentKeys(submitted, current map[string]any, owned func(string) bool) map[string]any {
	out := maps.Clone(submitted)
	if out == nil {
		out = map[string]any{}
	}
	for k := range out {
		if owned(k) {
			delete(out, k)
		}
	}
	for k, v := range current {
		if owned(k) {
			out[k] = cloneExtraValue(v)
		}
	}
	return out
}

// cloneExtraValue copies nested maps (not slices) so later edits (e.g.
// overages cleanup on model_rate_limits) never modify the locked row's map.
func cloneExtraValue(v any) any {
	if m, ok := v.(map[string]any); ok {
		out := make(map[string]any, len(m))
		for k, inner := range m {
			out[k] = cloneExtraValue(inner)
		}
		return out
	}
	return v
}

func cloneExtraMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneExtraValue(v)
	}
	return out
}

// quotaDailyResetSignature / quotaWeeklyResetSignature normalize the config
// that determines each fixed reset time, as ComputeQuotaResetAt reads it.
func quotaDailyResetSignature(extra map[string]any) string {
	if mode, _ := extra["quota_daily_reset_mode"].(string); mode != "fixed" {
		return "rolling"
	}
	hour := int(parseExtraFloat64(extra["quota_daily_reset_hour"]))
	if hour < 0 || hour > 23 {
		hour = 0
	}
	return fmt.Sprintf("fixed|%d|%s", hour, quotaResetTimezoneName(extra))
}

func quotaWeeklyResetSignature(extra map[string]any) string {
	if mode, _ := extra["quota_weekly_reset_mode"].(string); mode != "fixed" {
		return "rolling"
	}
	day := 1
	if d, ok := extra["quota_weekly_reset_day"]; ok {
		day = int(parseExtraFloat64(d))
	}
	if day < 0 || day > 6 {
		day = 1
	}
	hour := int(parseExtraFloat64(extra["quota_weekly_reset_hour"]))
	if hour < 0 || hour > 23 {
		hour = 0
	}
	return fmt.Sprintf("fixed|%d|%d|%s", day, hour, quotaResetTimezoneName(extra))
}

func quotaResetTimezoneName(extra map[string]any) string {
	if tz, _ := extra["quota_reset_timezone"].(string); tz != "" {
		return tz
	}
	return "UTC"
}

// applyQuotaResetAtForEdit recomputes a fixed reset time only for the part
// (daily / weekly) whose reset config changed; otherwise the stored value,
// which billing advances when a window rolls over, is kept (or kept absent).
func applyQuotaResetAtForEdit(next, current map[string]any) {
	ComputeQuotaResetAt(next)
	restore := func(key string) {
		if v, ok := current[key]; ok {
			next[key] = v
		} else {
			delete(next, key)
		}
	}
	if quotaDailyResetSignature(next) == quotaDailyResetSignature(current) {
		restore("quota_daily_reset_at")
	}
	if quotaWeeklyResetSignature(next) == quotaWeeklyResetSignature(current) {
		restore("quota_weekly_reset_at")
	}
}
