//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	for _, value := range []string{"205", " 205 ", now.Add(205 * time.Second).Format(http.TimeFormat)} {
		t.Run(value, func(t *testing.T) {
			got := parseOpenAIRetryAfter(value, now)
			require.NotNil(t, got)
			require.Equal(t, now.Add(205*time.Second), *got)
		})
	}
	for _, value := range []string{"86400", now.Add(365 * 24 * time.Hour).Format(http.TimeFormat)} {
		t.Run("capped_"+value, func(t *testing.T) {
			got := parseOpenAIRetryAfter(value, now)
			require.NotNil(t, got)
			require.Equal(t, now.Add(openAINoReset429EscalatedFor), *got)
		})
	}
	for _, value := range []string{"", "0", "-1", "+205", "1.5", "NaN", "205, 206", "9223372036854775807", "99999999999999999999", now.Add(-time.Second).Format(http.TimeFormat)} {
		t.Run("invalid_"+value, func(t *testing.T) {
			require.Nil(t, parseOpenAIRetryAfter(value, now))
		})
	}
}

func TestHandle429OpenAIRetryAfterCapacity(t *testing.T) {
	for _, dateHeader := range []bool{false, true} {
		t.Run(map[bool]string{false: "seconds", true: "date"}[dateHeader], func(t *testing.T) {
			repo := &openAI429SnapshotRepo{}
			svc := NewRateLimitService(repo, nil, nil, nil, nil)
			account := &Account{ID: 123, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			// Reading a response body must not restart the upstream wait timer.
			observedAt := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Second)
			headers := http.Header{}
			headers.Set("Retry-After", "205")
			if dateHeader {
				headers.Set("Retry-After", observedAt.Add(205*time.Second).Format(http.TimeFormat))
			}
			svc.handle429(withCodexObservedAt(context.Background(), observedAt), account, headers,
				[]byte(`{"error":{"type":"rate_limit_error","code":"turn_rate_limit_exceeded","message":"Codex is at capacity, please try again later"}}`))
			require.Equal(t, 1, repo.rateLimitCalls)
			require.Equal(t, observedAt.Add(205*time.Second), repo.rateLimitedAt)
			require.Empty(t, svc.openAINoReset429)
		})
	}
}

func TestHandle429OpenAIInvalidRetryAfterFallsBack(t *testing.T) {
	repo := &openAI429SnapshotRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 123, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	observedAt := time.Now()
	headers := http.Header{}
	headers.Set("Retry-After", "invalid")
	svc.handle429(withCodexObservedAt(context.Background(), observedAt), account, headers, nil)
	require.Equal(t, observedAt.Add(openAINoReset429Cooldown), repo.rateLimitedAt)
}

func TestHandle429OpenAIRetryAfterPreservesQuotaReset(t *testing.T) {
	repo := &openAI429SnapshotRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 123, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	headers := http.Header{}
	headers.Set("Retry-After", "205")
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "3600")
	headers.Set("x-codex-primary-window-minutes", "300")
	before := time.Now()
	svc.handle429(context.Background(), account, headers, nil)
	require.WithinDuration(t, before.Add(time.Hour), repo.rateLimitedAt, time.Second)
	require.NotEmpty(t, repo.updatedExtra)
}
