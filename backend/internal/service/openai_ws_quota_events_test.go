package service

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseOpenAIWSCodexRateLimitEvent(testContext *testing.T) {
	observedAt := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	resetAt := observedAt.Add(time.Hour).Unix()
	scenarios := []struct {
		name         string
		payload      string
		valid        bool
		weekly       bool
		wantReset    int
		missingReset bool
	}{
		{name: "five_hour", payload: fmt.Sprintf(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":100,"window_minutes":300,"reset_at":%d}}}`, resetAt), valid: true, wantReset: 3600},
		{name: "weekly_primary", payload: fmt.Sprintf(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":100,"window_minutes":10080,"reset_at":%d}}}`, resetAt), valid: true, weekly: true, wantReset: 3600},
		{name: "weekly_secondary", payload: fmt.Sprintf(`{"type":"codex.rate_limits","rate_limits":{"secondary":{"used_percent":100,"window_minutes":10080,"reset_at":%d}}}`, resetAt), valid: true, weekly: true, wantReset: 3600},
		{name: "expired_reset", payload: fmt.Sprintf(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":100,"window_minutes":300,"reset_at":%d}}}`, observedAt.Add(-time.Hour).Unix()), valid: true},
		{name: "missing_reset", payload: `{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":100,"window_minutes":300}}}`, valid: true, missingReset: true},
		{name: "negative_reset", payload: `{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":100,"window_minutes":300,"reset_at":-1}}}`, valid: true, missingReset: true},
		{name: "named_model_is_not_account_quota", payload: `{"type":"codex.rate_limits","metered_limit_name":"model-specific","rate_limits":{"primary":{"used_percent":100,"window_minutes":300}}}`},
		{name: "unknown_limit_name", payload: `{"type":"codex.rate_limits","limit_name":"other","rate_limits":{"primary":{"used_percent":100,"window_minutes":300}}}`},
		{name: "unknown_window", payload: `{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":100,"window_minutes":60}}}`},
		{name: "missing_window", payload: `{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":100}}}`},
		{name: "missing_percent", payload: `{"type":"codex.rate_limits","rate_limits":{"primary":{"window_minutes":300}}}`},
		{name: "negative_percent", payload: `{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":-1,"window_minutes":300}}}`},
		{name: "string_percent", payload: `{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":"100","window_minutes":300}}}`},
		{name: "unrelated_event", payload: `{"type":"response.created","rate_limits":{"primary":{"used_percent":100,"window_minutes":300}}}`},
		{name: "invalid_json", payload: `{"type":"codex.rate_limits"`},
	}
	for _, scenario := range scenarios {
		testContext.Run(scenario.name, func(testContext *testing.T) {
			snapshot := parseOpenAIWSCodexRateLimitEvent([]byte(scenario.payload), observedAt)
			if !scenario.valid {
				require.Nil(testContext, snapshot)
				return
			}
			require.NotNil(testContext, snapshot)
			normalized := snapshot.Normalize()
			usedPercent, resetSeconds := normalized.Used5hPercent, normalized.Reset5hSeconds
			if scenario.weekly {
				usedPercent, resetSeconds = normalized.Used7dPercent, normalized.Reset7dSeconds
			}
			require.NotNil(testContext, usedPercent)
			require.Equal(testContext, 100.0, *usedPercent)
			if scenario.missingReset {
				require.Nil(testContext, resetSeconds)
			} else {
				require.NotNil(testContext, resetSeconds)
				require.Equal(testContext, scenario.wantReset, *resetSeconds)
			}
			if scenario.wantReset == 0 || scenario.missingReset {
				reset := codexRateLimitResetAtFromSnapshot(snapshot, observedAt)
				require.True(testContext, reset == nil || !reset.After(observedAt))
			}
		})
	}
}

func TestOpenAIWSQuotaErrorEnvelopeAndHeaders(testContext *testing.T) {
	for _, payload := range []string{
		`{"type":"error","error":{"type":"usage_limit_reached","resets_at":123}}`,
		`{"type":"response.failed","response":{"error":{"type":"usage_limit_reached","resets_at":123}}}`,
		`{"type":"response.done","response":{"status":"failed","error":{"type":"usage_limit_reached","resets_at":123}}}`,
	} {
		body := openAIWSQuotaErrorPayload([]byte(payload))
		reset := parseOpenAIRateLimitResetTime(body)
		require.NotNil(testContext, reset)
		require.Equal(testContext, int64(123), *reset)
	}
	require.Nil(testContext, openAIWSQuotaErrorPayload([]byte(`{"type":"response.done","response":{"status":"completed","error":{"type":"usage_limit_reached"}}}`)))
	require.Nil(testContext, openAIWSQuotaErrorPayload([]byte(`{"type":"response.failed","response":{"error":null}}`)))
	require.Nil(testContext, openAIWSQuotaErrorPayload([]byte(`{"type":"error","error":{"type":"usage_limit_reached"}`)))
	require.False(testContext, isOpenAIWSQuotaReplaySafe([]byte(`{"response":{"output":[{"type":"function_call","name":"tool"}]}}`)))
	require.False(testContext, isOpenAIWSQuotaReplaySafe([]byte(`{"response":{"usage":{"output_tokens":1}}}`)))
	handshake := http.Header{"X-Codex-Primary-Used-Percent": {"5"}}
	headers := openAIWSQuotaErrorHeaders(handshake, []byte(`{"headers":{"x-codex-primary-used-percent":"100","retry-after":"12","authorization":"TEST_ONLY_SHOULD_NOT_COPY","x-codex-secondary-used-percent":100}}`))
	require.Equal(testContext, "100", headers.Get("x-codex-primary-used-percent"))
	require.Equal(testContext, "12", headers.Get("retry-after"))
	require.Empty(testContext, headers.Get("authorization"))
	require.Empty(testContext, headers.Get("x-codex-secondary-used-percent"))
	require.Equal(testContext, "5", handshake.Get("x-codex-primary-used-percent"))
}

func TestOpenAIWSDualQuotaResetUsesLatestActiveWindow(testContext *testing.T) {
	observedAt := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for _, weeklyReset := range []int{0, 1200, 7200} {
		testContext.Run(fmt.Sprint(weeklyReset), func(testContext *testing.T) {
			payload := fmt.Sprintf(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":100,"window_minutes":300,"reset_at":%d},"secondary":{"used_percent":100,"window_minutes":10080,"reset_at":%d}}}`, observedAt.Add(time.Hour).Unix(), observedAt.Add(time.Duration(weeklyReset)*time.Second).Unix())
			snapshot := parseOpenAIWSCodexRateLimitEvent([]byte(payload), observedAt)
			expectedReset := observedAt.Add(time.Duration(max(3600, weeklyReset)) * time.Second)
			require.Equal(testContext, &expectedReset, codexRateLimitResetAtFromSnapshot(snapshot, observedAt))
			require.Equal(testContext, &expectedReset, codexRateLimitResetAtFromExtra(buildCodexUsageExtraUpdates(snapshot, observedAt), observedAt))
			headers := http.Header{
				"X-Codex-Primary-Used-Percent":          {"100"},
				"X-Codex-Primary-Window-Minutes":        {"300"},
				"X-Codex-Primary-Reset-After-Seconds":   {"3600"},
				"X-Codex-Secondary-Used-Percent":        {"100"},
				"X-Codex-Secondary-Window-Minutes":      {"10080"},
				"X-Codex-Secondary-Reset-After-Seconds": {fmt.Sprint(weeklyReset)},
			}
			before := time.Now()
			reset := (&RateLimitService{}).calculateOpenAI429ResetTime(headers)
			require.NotNil(testContext, reset)
			require.WithinDuration(testContext, before.Add(time.Duration(max(3600, weeklyReset))*time.Second), *reset, time.Second)
		})
	}
}
