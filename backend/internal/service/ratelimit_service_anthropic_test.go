package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestCalculateAnthropic429ResetTime_Only5hExceeded(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.02")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1770998400")
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.32")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1771549200")

	result := calculateAnthropic429ResetTime(headers)
	assertAnthropicResult(t, result, 1770998400)

	if result.fiveHourReset == nil || !result.fiveHourReset.Equal(time.Unix(1770998400, 0)) {
		t.Errorf("expected fiveHourReset=1770998400, got %v", result.fiveHourReset)
	}
}

func TestCalculateAnthropic429ResetTime_Only7dExceeded(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.50")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1770998400")
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "1.05")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1771549200")

	result := calculateAnthropic429ResetTime(headers)
	assertAnthropicResult(t, result, 1771549200)

	// fiveHourReset should still be populated for session window calculation
	if result.fiveHourReset == nil || !result.fiveHourReset.Equal(time.Unix(1770998400, 0)) {
		t.Errorf("expected fiveHourReset=1770998400, got %v", result.fiveHourReset)
	}
}

func TestCalculateAnthropic429ResetTime_BothExceeded(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.10")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1770998400")
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "1.02")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1771549200")

	result := calculateAnthropic429ResetTime(headers)
	assertAnthropicResult(t, result, 1771549200)
}

func TestCalculateAnthropic429ResetTime_NoPerWindowHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-reset", "1771549200")

	result := calculateAnthropic429ResetTime(headers)
	if result != nil {
		t.Errorf("expected nil result when no per-window headers, got resetAt=%v", result.resetAt)
	}
}

func TestCalculateAnthropic429ResetTime_NoHeaders(t *testing.T) {
	result := calculateAnthropic429ResetTime(http.Header{})
	if result != nil {
		t.Errorf("expected nil result for empty headers, got resetAt=%v", result.resetAt)
	}
}

func TestCalculateAnthropic429ResetTime_SurpassedThreshold(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-surpassed-threshold", "true")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1770998400")
	headers.Set("anthropic-ratelimit-unified-7d-surpassed-threshold", "false")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1771549200")

	result := calculateAnthropic429ResetTime(headers)
	assertAnthropicResult(t, result, 1770998400)
}

func TestCalculateAnthropic429ResetTime_UtilizationExactlyOne(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.0")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1770998400")
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.5")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1771549200")

	result := calculateAnthropic429ResetTime(headers)
	assertAnthropicResult(t, result, 1770998400)
}

func TestCalculateAnthropic429ResetTime_NeitherExceeded_UsesShorter(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.95")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1770998400") // sooner
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.80")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1771549200") // later

	result := calculateAnthropic429ResetTime(headers)
	assertAnthropicResult(t, result, 1770998400)
	if !result.noWindowExceeded || result.anyWindowExceeded() {
		t.Fatalf("expected no exceeded window markers, got 5h=%v 7d=%v noWindow=%v", result.is5hExceeded, result.is7dExceeded, result.noWindowExceeded)
	}
}

func TestCalculateAnthropic429ResetTime_Only5hResetHeader(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.05")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1770998400")

	result := calculateAnthropic429ResetTime(headers)
	assertAnthropicResult(t, result, 1770998400)
}

func TestCalculateAnthropic429ResetTime_Only7dResetHeader(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "1.03")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1771549200")

	result := calculateAnthropic429ResetTime(headers)
	assertAnthropicResult(t, result, 1771549200)

	if result.fiveHourReset != nil {
		t.Errorf("expected fiveHourReset=nil when no 5h headers, got %v", result.fiveHourReset)
	}
}

func TestIsAnthropicWindowExceeded(t *testing.T) {
	tests := []struct {
		name     string
		headers  http.Header
		window   string
		expected bool
	}{
		{
			name:     "utilization above 1.0",
			headers:  makeHeader("anthropic-ratelimit-unified-5h-utilization", "1.02"),
			window:   "5h",
			expected: true,
		},
		{
			name:     "utilization exactly 1.0",
			headers:  makeHeader("anthropic-ratelimit-unified-5h-utilization", "1.0"),
			window:   "5h",
			expected: true,
		},
		{
			name:     "utilization below 1.0",
			headers:  makeHeader("anthropic-ratelimit-unified-5h-utilization", "0.99"),
			window:   "5h",
			expected: false,
		},
		{
			name:     "surpassed-threshold true",
			headers:  makeHeader("anthropic-ratelimit-unified-7d-surpassed-threshold", "true"),
			window:   "7d",
			expected: true,
		},
		{
			name:     "surpassed-threshold True (case insensitive)",
			headers:  makeHeader("anthropic-ratelimit-unified-7d-surpassed-threshold", "True"),
			window:   "7d",
			expected: true,
		},
		{
			name:     "surpassed-threshold false",
			headers:  makeHeader("anthropic-ratelimit-unified-7d-surpassed-threshold", "false"),
			window:   "7d",
			expected: false,
		},
		{
			name:     "no headers",
			headers:  http.Header{},
			window:   "5h",
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isAnthropicWindowExceeded(tc.headers, tc.window)
			if got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestHandle429_AnthropicCarpool5hQuotaUsesSessionWindowEnd(t *testing.T) {
	now := time.Now().UTC()
	windowStart := now.Add(-2 * time.Hour).Truncate(time.Second)
	windowEnd := now.Add(90 * time.Minute).Truncate(time.Second)
	account := &Account{
		ID:                 42,
		Platform:           PlatformAnthropic,
		Type:               AccountTypeSetupToken,
		SessionWindowStart: anthropicTimePtr(windowStart),
		SessionWindowEnd:   anthropicTimePtr(windowEnd),
	}
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)

	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"carpool 5h quota exhausted"},"request_id":"req_test"}`)
	svc.handle429(context.Background(), account, http.Header{}, body)

	if len(repo.rateLimitCalls) != 1 {
		t.Fatalf("expected one rate-limit call, got %d", len(repo.rateLimitCalls))
	}
	if got := repo.rateLimitCalls[0].ResetAt.UTC(); !got.Equal(windowEnd) {
		t.Fatalf("resetAt = %v, want %v", got, windowEnd)
	}
	if len(repo.sessionWindowCalls) != 1 {
		t.Fatalf("expected one session-window update, got %d", len(repo.sessionWindowCalls))
	}
	call := repo.sessionWindowCalls[0]
	if call.Status != "rejected" {
		t.Fatalf("session window status = %q, want rejected", call.Status)
	}
	if call.Start == nil || !call.Start.UTC().Equal(windowStart) {
		t.Fatalf("session window start = %v, want %v", call.Start, windowStart)
	}
	if call.End == nil || !call.End.UTC().Equal(windowEnd) {
		t.Fatalf("session window end = %v, want %v", call.End, windowEnd)
	}
}

func TestHandle429_AnthropicFableNoWindowExceededSetsModelRateLimit(t *testing.T) {
	reset5h := time.Now().Add(4 * time.Hour).Truncate(time.Second)
	reset7d := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Second)
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.15")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", reset5h.Unix()))
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.50")
	headers.Set("anthropic-ratelimit-unified-7d-reset", fmt.Sprintf("%d", reset7d.Unix()))

	account := &Account{ID: 208, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."}}`)

	svc.handle429ForModel(context.Background(), account, headers, body, "claude-fable-5")

	if len(repo.rateLimitCalls) != 0 {
		t.Fatalf("expected no account-level rate-limit calls, got %d", len(repo.rateLimitCalls))
	}
	if len(repo.sessionWindowCalls) != 0 {
		t.Fatalf("expected no rejected session-window update, got %d", len(repo.sessionWindowCalls))
	}
	if len(repo.modelRateLimitCalls) != 1 {
		t.Fatalf("expected one model rate-limit call, got %d", len(repo.modelRateLimitCalls))
	}
	call := repo.modelRateLimitCalls[0]
	if call.ID != 208 {
		t.Fatalf("model rate-limit account id = %d, want 208", call.ID)
	}
	if call.Scope != "claude-fable-5" {
		t.Fatalf("model rate-limit scope = %q, want claude-fable-5", call.Scope)
	}
	if !call.ResetAt.Equal(reset5h.UTC()) {
		t.Fatalf("model resetAt = %v, want %v", call.ResetAt, reset5h.UTC())
	}
}

func TestIsAnthropicModelScoped429Model(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "claude-fable-5", want: true},
		{model: "claude-fable-5-20260610", want: true},
		{model: "claude-sonnet-5", want: false},
		{model: "claude-mythos-5", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := isAnthropicModelScoped429Model(tt.model); got != tt.want {
				t.Fatalf("isAnthropicModelScoped429Model(%q)=%v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestHandle429_AnthropicFableNoWindowExceededMissing5hResetSkipsPersistence(t *testing.T) {
	reset7d := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Second)
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.50")
	headers.Set("anthropic-ratelimit-unified-7d-reset", fmt.Sprintf("%d", reset7d.Unix()))

	account := &Account{ID: 208, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)
	body := []byte(`{"error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit."}}`)

	svc.handle429ForModel(context.Background(), account, headers, body, "claude-fable-5")

	if len(repo.rateLimitCalls) != 0 {
		t.Fatalf("expected no account-level rate-limit calls, got %d", len(repo.rateLimitCalls))
	}
	if len(repo.modelRateLimitCalls) != 0 {
		t.Fatalf("expected no model rate-limit call without 5h reset, got %d", len(repo.modelRateLimitCalls))
	}
}

func TestHandle429_AnthropicNoWindowExceededNonScopedModelDoesNotSetAccountLimit(t *testing.T) {
	reset5h := time.Now().Add(4 * time.Hour).Truncate(time.Second)
	reset7d := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Second)
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.15")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", reset5h.Unix()))
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.50")
	headers.Set("anthropic-ratelimit-unified-7d-reset", fmt.Sprintf("%d", reset7d.Unix()))

	account := &Account{ID: 208, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)
	body := []byte(`{"error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit."}}`)

	svc.handle429ForModel(context.Background(), account, headers, body, "claude-sonnet-5")

	if len(repo.rateLimitCalls) != 0 {
		t.Fatalf("expected no account-level rate-limit calls, got %d", len(repo.rateLimitCalls))
	}
	if len(repo.modelRateLimitCalls) != 0 {
		t.Fatalf("expected no model rate-limit call for non-scoped model, got %d", len(repo.modelRateLimitCalls))
	}
}

func TestHandle429_Anthropic5hExceededStillSetsAccountLimit(t *testing.T) {
	reset5h := time.Now().Add(4 * time.Hour).Truncate(time.Second)
	reset7d := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Second)
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.01")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", reset5h.Unix()))
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.50")
	headers.Set("anthropic-ratelimit-unified-7d-reset", fmt.Sprintf("%d", reset7d.Unix()))

	account := &Account{ID: 208, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)
	body := []byte(`{"error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit."}}`)

	svc.handle429ForModel(context.Background(), account, headers, body, "claude-fable-5")

	if len(repo.modelRateLimitCalls) != 0 {
		t.Fatalf("expected no model rate-limit calls for exceeded 5h window, got %d", len(repo.modelRateLimitCalls))
	}
	if len(repo.rateLimitCalls) != 1 {
		t.Fatalf("expected one account-level rate-limit call, got %d", len(repo.rateLimitCalls))
	}
	if !repo.rateLimitCalls[0].ResetAt.Equal(reset5h) {
		t.Fatalf("account resetAt = %v, want %v", repo.rateLimitCalls[0].ResetAt, reset5h)
	}
}

func TestHandle429_AnthropicCarpool5hQuotaFallsBackWhenWindowMissing(t *testing.T) {
	account := &Account{ID: 43, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)
	body := []byte(`{"error":{"message":"carpool 5h quota exhausted","type":"rate_limit_error"}}`)

	before := time.Now().Add(anthropicCarpool5hQuotaFallbackCooldown)
	svc.handle429(context.Background(), account, http.Header{}, body)
	after := time.Now().Add(anthropicCarpool5hQuotaFallbackCooldown)

	if len(repo.rateLimitCalls) != 1 {
		t.Fatalf("expected one rate-limit call, got %d", len(repo.rateLimitCalls))
	}
	got := repo.rateLimitCalls[0].ResetAt
	if got.Before(before) || got.After(after) {
		t.Fatalf("resetAt = %v, want in [%v, %v]", got, before, after)
	}
	if len(repo.sessionWindowCalls) != 0 {
		t.Fatalf("expected no session-window update for fallback cooldown, got %d", len(repo.sessionWindowCalls))
	}
}

func TestHandle429_AnthropicNoResetOtherMessageStillSkipped(t *testing.T) {
	account := &Account{ID: 44, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)
	body := []byte(`{"error":{"message":"Usage credits are required for long context requests.","type":"rate_limit_error"}}`)

	svc.handle429(context.Background(), account, http.Header{}, body)

	if len(repo.rateLimitCalls) != 0 {
		t.Fatalf("expected no rate-limit call, got %d", len(repo.rateLimitCalls))
	}
}

// assertAnthropicResult is a test helper that verifies the result is non-nil and
// has the expected resetAt unix timestamp.
func assertAnthropicResult(t *testing.T, result *anthropic429Result, wantUnix int64) {
	t.Helper()
	if result == nil {
		t.Fatal("expected non-nil result")
		return // unreachable, but satisfies staticcheck SA5011
	}
	want := time.Unix(wantUnix, 0)
	if !result.resetAt.Equal(want) {
		t.Errorf("expected resetAt=%v, got %v", want, result.resetAt)
	}
}

func makeHeader(key, value string) http.Header {
	h := http.Header{}
	h.Set(key, value)
	return h
}

func anthropicTimePtr(t time.Time) *time.Time {
	return &t
}
