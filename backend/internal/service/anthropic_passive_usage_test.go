package service

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// Header shapes captured from production responses on 2026-11-01.
func claudeUltraSuccessHeaders(sevenDayReset time.Time) http.Header {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(sevenDayReset.Unix(), 10))
	h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed_warning")
	h.Set("Anthropic-Ratelimit-Unified-7d-Surpassed-Threshold", "0.75")
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.86")
	h.Set("Anthropic-Ratelimit-Unified-Fallback-Percentage", "0.5")
	h.Set("Anthropic-Ratelimit-Unified-Overage-Disabled-Reason", "org_level_disabled")
	h.Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
	h.Set("Anthropic-Ratelimit-Unified-Representative-Claim", "seven_day")
	h.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(sevenDayReset.Unix(), 10))
	h.Set("Anthropic-Ratelimit-Unified-Status", "allowed_warning")
	h.Set("Anthropic-Ratelimit-Unified-Upgrade-Paths", "overage")
	return h
}

func claudeMaxSuccessHeaders(fiveHourReset, sevenDayReset time.Time) http.Header {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(fiveHourReset.Unix(), 10))
	h.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.04")
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(sevenDayReset.Unix(), 10))
	h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.66")
	h.Set("Anthropic-Ratelimit-Unified-Fallback-Percentage", "0.5")
	h.Set("Anthropic-Ratelimit-Unified-Overage-Disabled-Reason", "org_level_disabled")
	h.Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
	h.Set("Anthropic-Ratelimit-Unified-Representative-Claim", "five_hour")
	h.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(fiveHourReset.Unix(), 10))
	h.Set("Anthropic-Ratelimit-Unified-Status", "allowed")
	return h
}

func newClaudeSetupTokenAccount(id int64, extra map[string]any) *Account {
	return &Account{ID: id, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive, Extra: extra}
}

func TestUpdateSessionWindow_ClaudeUltraSamplesWeeklyWithoutFiveHour(t *testing.T) {
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)
	sevenDayReset := time.Now().Add(30 * time.Hour).Truncate(time.Second)
	staleEnd := time.Now().Add(20 * time.Hour)
	account := newClaudeSetupTokenAccount(198, map[string]any{
		"session_window_utilization":   0.95,
		"passive_usage_7d_utilization": 0.78,
	})
	account.SessionWindowEnd = &staleEnd

	svc.UpdateSessionWindow(context.Background(), account, claudeUltraSuccessHeaders(sevenDayReset))

	if len(repo.sessionWindowCalls) != 0 {
		t.Fatalf("no 5h header must not touch the session window, got %+v", repo.sessionWindowCalls)
	}
	if len(repo.updateExtraCalls) != 1 {
		t.Fatalf("expected 1 UpdateExtra call, got %d", len(repo.updateExtraCalls))
	}
	updates := repo.updateExtraCalls[0].Updates
	if got, ok := updates["passive_usage_7d_utilization"].(float64); !ok || got != 0.86 {
		t.Fatalf("7d utilization = %v, want 0.86", updates["passive_usage_7d_utilization"])
	}
	if got, ok := updates["passive_usage_7d_reset"].(int64); !ok || got != sevenDayReset.Unix() {
		t.Fatalf("7d reset = %v, want %d", updates["passive_usage_7d_reset"], sevenDayReset.Unix())
	}
	if got, ok := updates[anthropicFiveHourAbsentExtraKey].(bool); !ok || !got {
		t.Fatalf("expected 5h absent marked, got %v", updates[anthropicFiveHourAbsentExtraKey])
	}
	if value, ok := updates["session_window_utilization"]; !ok || value != nil {
		t.Fatalf("expected stale 5h utilization cleared, got %v (present=%v)", value, ok)
	}
	if _, ok := updates["passive_usage_sampled_at"]; !ok {
		t.Fatal("expected passive_usage_sampled_at")
	}
	if len(repo.clearRateLimitIDs) != 0 || len(repo.rateLimitCalls) != 0 {
		t.Fatalf("weekly sampling must not change rate limits: clear=%v set=%v", repo.clearRateLimitIDs, repo.rateLimitCalls)
	}
}

func TestUpdateSessionWindow_ClaudeMaxKeepsFiveHourAndRestoresAbsence(t *testing.T) {
	fiveHourReset := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	sevenDayReset := time.Now().Add(50 * time.Hour).Truncate(time.Second)

	t.Run("not previously absent", func(t *testing.T) {
		repo := &sessionWindowMockRepo{}
		svc := newRateLimitServiceForTest(repo)
		svc.UpdateSessionWindow(context.Background(), newClaudeSetupTokenAccount(289, map[string]any{}), claudeMaxSuccessHeaders(fiveHourReset, sevenDayReset))

		if len(repo.sessionWindowCalls) != 1 || repo.sessionWindowCalls[0].End == nil || !repo.sessionWindowCalls[0].End.Equal(fiveHourReset) {
			t.Fatalf("expected 5h window from header, got %+v", repo.sessionWindowCalls)
		}
		updates := repo.updateExtraCalls[len(repo.updateExtraCalls)-1].Updates
		if got, ok := updates["session_window_utilization"].(float64); !ok || got != 0.04 {
			t.Fatalf("5h utilization = %v, want 0.04", updates["session_window_utilization"])
		}
		if got, ok := updates["passive_usage_7d_utilization"].(float64); !ok || got != 0.66 {
			t.Fatalf("7d utilization = %v, want 0.66", updates["passive_usage_7d_utilization"])
		}
		if _, ok := updates[anthropicFiveHourAbsentExtraKey]; ok {
			t.Fatalf("absence flag must not be written for an account that was never absent: %v", updates)
		}
	})

	t.Run("previously absent", func(t *testing.T) {
		repo := &sessionWindowMockRepo{}
		svc := newRateLimitServiceForTest(repo)
		account := newClaudeSetupTokenAccount(289, map[string]any{anthropicFiveHourAbsentExtraKey: true})
		svc.UpdateSessionWindow(context.Background(), account, claudeMaxSuccessHeaders(fiveHourReset, sevenDayReset))

		updates := repo.updateExtraCalls[len(repo.updateExtraCalls)-1].Updates
		if got, ok := updates[anthropicFiveHourAbsentExtraKey].(bool); !ok || got {
			t.Fatalf("expected absence flag reset to false, got %v", updates[anthropicFiveHourAbsentExtraKey])
		}
	})
}

func TestUpdateSessionWindow_DoesNotMarkAbsentWithoutSharedWeeklyWindow(t *testing.T) {
	future := strconv.FormatInt(time.Now().Add(48*time.Hour).Unix(), 10)
	cases := map[string]http.Header{
		"aggregate only": {
			"Anthropic-Ratelimit-Unified-Status": {"allowed"},
			"Anthropic-Ratelimit-Unified-Reset":  {future},
		},
		"fable only": {
			"Anthropic-Ratelimit-Unified-7d_oi-Utilization": {"0.2"},
			"Anthropic-Ratelimit-Unified-7d_oi-Reset":       {future},
		},
		"invalid 7d utilization": {
			"Anthropic-Ratelimit-Unified-7d-Utilization": {"NaN"},
			"Anthropic-Ratelimit-Unified-7d-Reset":       {future},
		},
		"malformed 5h header present": {
			"Anthropic-Ratelimit-Unified-5h-Utilization": {"oops"},
			"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.5"},
		},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &sessionWindowMockRepo{}
			svc := newRateLimitServiceForTest(repo)
			svc.UpdateSessionWindow(context.Background(), newClaudeSetupTokenAccount(1, map[string]any{}), headers)
			for _, call := range repo.updateExtraCalls {
				if _, ok := call.Updates[anthropicFiveHourAbsentExtraKey]; ok {
					t.Fatalf("absence must not be decided from %s: %v", name, call.Updates)
				}
				if _, ok := call.Updates["session_window_utilization"]; ok {
					t.Fatalf("5h utilization must not change from %s: %v", name, call.Updates)
				}
			}
		})
	}
}

func TestUpdateSessionWindow_APIKeyAccountNeverMarkedAbsent(t *testing.T) {
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)
	account := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{}}
	svc.UpdateSessionWindow(context.Background(), account, claudeUltraSuccessHeaders(time.Now().Add(24*time.Hour)))
	for _, call := range repo.updateExtraCalls {
		if _, ok := call.Updates[anthropicFiveHourAbsentExtraKey]; ok {
			t.Fatalf("api key account must not get the absence flag: %v", call.Updates)
		}
	}
}

func TestSampleAnthropicPassiveUsageHeaders_RejectsBadValues(t *testing.T) {
	now := time.Now()
	future := now.Add(48 * time.Hour).Truncate(time.Second)
	for _, raw := range []string{"NaN", "+Inf", "-0.1", "2.5", "abc", ""} {
		h := http.Header{}
		h.Set("anthropic-ratelimit-unified-7d-utilization", raw)
		h.Set("anthropic-ratelimit-unified-5h-utilization", raw)
		sample := sampleAnthropicPassiveUsageHeaders(h, now)
		if _, ok := sample.updates["passive_usage_7d_utilization"]; ok {
			t.Fatalf("7d utilization %q must be rejected", raw)
		}
		if _, ok := sample.updates["session_window_utilization"]; ok {
			t.Fatalf("5h utilization %q must be rejected", raw)
		}
	}

	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(future.UnixMilli(), 10))
	h.Set("anthropic-ratelimit-unified-7d-sonnet-reset", strconv.FormatInt(now.Add(30*24*time.Hour).Unix(), 10))
	sample := sampleAnthropicPassiveUsageHeaders(h, now)
	if got, ok := sample.updates["passive_usage_7d_reset"].(int64); !ok || got != future.Unix() {
		t.Fatalf("millisecond 7d reset = %v, want %d", sample.updates["passive_usage_7d_reset"], future.Unix())
	}
	if _, ok := sample.updates["passive_usage_7d_sonnet_reset"]; ok {
		t.Fatal("reset beyond the weekly horizon must be rejected")
	}
}

func TestAnthropicRateLimitWindowName(t *testing.T) {
	cases := map[string]string{
		"anthropic-ratelimit-unified-5h-utilization":                "5h",
		"anthropic-ratelimit-unified-7d-surpassed-threshold":        "7d",
		"anthropic-ratelimit-unified-7d-sonnet-reset":               "7d-sonnet",
		"anthropic-ratelimit-unified-7d_oi-status":                  "7d_oi",
		"anthropic-ratelimit-unified-30d_team-utilization":          "30d_team",
		"anthropic-ratelimit-unified-status":                        "",
		"anthropic-ratelimit-unified-reset":                         "",
		"anthropic-ratelimit-unified-overage-status":                "",
		"anthropic-ratelimit-unified-overage-disabled-reason":       "",
		"anthropic-ratelimit-unified-representative-claim":          "",
		"anthropic-ratelimit-unified-upgrade-paths":                 "",
		"anthropic-ratelimit-unified-fallback-percentage":           "",
		"anthropic-ratelimit-unified--utilization":                  "",
		"anthropic-ratelimit-unified-7d-sonnet-surpassed-threshold": "7d-sonnet",
	}
	for header, want := range cases {
		got, ok := anthropicRateLimitWindowName(header)
		if want == "" {
			if ok {
				t.Errorf("%s: expected no window, got %q", header, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%s: window = %q (ok=%v), want %q", header, got, ok, want)
		}
	}

	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-30d_team-utilization", "0.1")
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.1")
	sample := sampleAnthropicPassiveUsageHeaders(h, time.Now())
	if len(sample.unknownWindows) != 1 || sample.unknownWindows[0] != "30d_team" {
		t.Fatalf("unknown windows = %v, want [30d_team]", sample.unknownWindows)
	}
	for key := range sample.updates {
		if key != "passive_usage_7d_utilization" {
			t.Fatalf("unknown windows must not be stored yet, got key %q", key)
		}
	}
}

func TestHandle429_ClaudeUltraWeeklyLimitDoesNotFabricateFiveHourWindow(t *testing.T) {
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)
	sevenDayReset := time.Now().Add(26 * time.Hour).Truncate(time.Second)
	headers := claudeUltraSuccessHeaders(sevenDayReset)
	headers.Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "1.0")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Surpassed-Threshold", "1.0")
	headers.Set("Anthropic-Ratelimit-Unified-Status", "rejected")

	account := newClaudeSetupTokenAccount(200, map[string]any{})
	svc.handle429ForModel(context.Background(), account, headers, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."}}`), "claude-opus-5")

	if len(repo.rateLimitCalls) != 1 || !repo.rateLimitCalls[0].ResetAt.Equal(sevenDayReset) {
		t.Fatalf("expected account rate limited until 7d reset %v, got %+v", sevenDayReset, repo.rateLimitCalls)
	}
	if len(repo.sessionWindowCalls) != 0 {
		t.Fatalf("no 5h reset header must not fabricate a 5h window, got %+v", repo.sessionWindowCalls)
	}
	if len(repo.updateExtraCalls) != 1 {
		t.Fatalf("expected 429 usage sample, got %d UpdateExtra calls", len(repo.updateExtraCalls))
	}
	updates := repo.updateExtraCalls[0].Updates
	if got, ok := updates["passive_usage_7d_utilization"].(float64); !ok || got != 1.0 {
		t.Fatalf("429 7d utilization = %v, want 1.0", updates["passive_usage_7d_utilization"])
	}
	if _, ok := updates[anthropicFiveHourAbsentExtraKey]; ok {
		t.Fatalf("429 responses must not decide 5h absence: %v", updates)
	}
}

func TestHandle429_ClaudeMaxFiveHourLimitStillSetsSessionWindow(t *testing.T) {
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)
	fiveHourReset := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	headers := claudeMaxSuccessHeaders(fiveHourReset, time.Now().Add(80*time.Hour))
	headers.Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "1.0")

	svc.handle429ForModel(context.Background(), newClaudeSetupTokenAccount(289, map[string]any{}), headers, nil, "claude-opus-5")

	if len(repo.sessionWindowCalls) != 1 || !repo.sessionWindowCalls[0].End.Equal(fiveHourReset) || repo.sessionWindowCalls[0].Status != "rejected" {
		t.Fatalf("expected rejected 5h window ending %v, got %+v", fiveHourReset, repo.sessionWindowCalls)
	}
	if len(repo.rateLimitCalls) != 1 || !repo.rateLimitCalls[0].ResetAt.Equal(fiveHourReset) {
		t.Fatalf("expected rate limit until 5h reset, got %+v", repo.rateLimitCalls)
	}
}

func TestFiveHourAbsentAccount_SkipsFiveHourGateFallbackAndDisplay(t *testing.T) {
	now := time.Now()
	windowEnd := now.Add(4 * time.Hour)
	extra := map[string]any{"session_window_utilization": 0.95, "passive_usage_7d_utilization": 0.5}
	present := newClaudeSetupTokenAccount(1, extra)
	present.SessionWindowEnd = &windowEnd
	if !shouldRejectNewSessionForHighFiveHourUsage(present, now) {
		t.Fatal("control: a 95% 5h window should trip the new-session guard")
	}

	absentExtra := map[string]any{"session_window_utilization": 0.95, "passive_usage_7d_utilization": 0.5, anthropicFiveHourAbsentExtraKey: true}
	absent := newClaudeSetupTokenAccount(2, absentExtra)
	absent.SessionWindowEnd = &windowEnd
	if shouldRejectNewSessionForHighFiveHourUsage(absent, now) {
		t.Fatal("an account without a 5h window must not be rejected by the 5h guard")
	}
	if _, fromWindow := anthropicCarpool5hQuotaFallbackResetAt(absent, now); fromWindow {
		t.Fatal("carpool 5h fallback must not reuse a stale window on a 5h-absent account")
	}
	if info := (&AccountUsageService{}).estimateSetupTokenUsageAt(absent, now); info.FiveHour != nil {
		t.Fatalf("5h-absent account must not display a 5h window, got %+v", info.FiveHour)
	}
	for _, window := range hostedAccountSnapshot(absent, now).Windows {
		if window.Key == "five_hour" {
			t.Fatalf("hosted snapshot must not show five_hour for a 5h-absent account: %+v", window)
		}
	}

	nullFlag := newClaudeSetupTokenAccount(3, map[string]any{anthropicFiveHourAbsentExtraKey: nil})
	if nullFlag.IsAnthropicFiveHourWindowAbsent() {
		t.Fatal("a JSON null flag must read as not absent")
	}
}
