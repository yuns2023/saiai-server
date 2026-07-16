package service

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestShouldRejectNewSessionForHighFiveHourUsage(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	future := now.Add(2 * time.Hour)
	expired := now.Add(-time.Second)

	tests := []struct {
		name    string
		account *Account
		want    bool
	}{
		{
			name: "anthropic oauth above threshold",
			account: &Account{
				Platform:         PlatformAnthropic,
				Type:             AccountTypeOAuth,
				SessionWindowEnd: &future,
				Extra:            map[string]any{"session_window_utilization": 0.800001},
			},
			want: true,
		},
		{
			name: "anthropic setup token above threshold",
			account: &Account{
				Platform:         PlatformAnthropic,
				Type:             AccountTypeSetupToken,
				SessionWindowEnd: &future,
				Extra:            map[string]any{"session_window_utilization": json.Number("0.81")},
			},
			want: true,
		},
		{
			name: "anthropic exactly eighty percent is accepted",
			account: &Account{
				Platform:         PlatformAnthropic,
				Type:             AccountTypeOAuth,
				SessionWindowEnd: &future,
				Extra:            map[string]any{"session_window_utilization": 0.80},
			},
		},
		{
			name: "anthropic expired window is recovered",
			account: &Account{
				Platform:         PlatformAnthropic,
				Type:             AccountTypeOAuth,
				SessionWindowEnd: &expired,
				Extra:            map[string]any{"session_window_utilization": 0.99},
			},
		},
		{
			name: "anthropic missing reset fails open",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{"session_window_utilization": 0.99},
			},
		},
		{
			name: "anthropic missing utilization fails open",
			account: &Account{
				Platform:         PlatformAnthropic,
				Type:             AccountTypeOAuth,
				SessionWindowEnd: &future,
			},
		},
		{
			name: "anthropic out of range utilization fails open",
			account: &Account{
				Platform:         PlatformAnthropic,
				Type:             AccountTypeOAuth,
				SessionWindowEnd: &future,
				Extra:            map[string]any{"session_window_utilization": 81.0},
			},
		},
		{
			name: "anthropic non finite utilization fails open",
			account: &Account{
				Platform:         PlatformAnthropic,
				Type:             AccountTypeOAuth,
				SessionWindowEnd: &future,
				Extra:            map[string]any{"session_window_utilization": math.NaN()},
			},
		},
		{
			name: "openai oauth above threshold with absolute reset",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra: map[string]any{
					"codex_5h_used_percent": 80.0001,
					"codex_5h_reset_at":     future.Format(time.RFC3339),
				},
			},
			want: true,
		},
		{
			name: "openai exactly eighty percent is accepted",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra: map[string]any{
					"codex_5h_used_percent": "80",
					"codex_5h_reset_at":     future.Unix(),
				},
			},
		},
		{
			name: "openai expired absolute reset is recovered",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra: map[string]any{
					"codex_5h_used_percent": 95.0,
					"codex_5h_reset_at":     expired.Format(time.RFC3339),
				},
			},
		},
		{
			name: "openai missing utilization fails open",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{"codex_5h_reset_at": future.Format(time.RFC3339)},
			},
		},
		{
			name: "openai relative reset is anchored to usage update",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra: map[string]any{
					"codex_5h_used_percent":        95.0,
					"codex_5h_reset_after_seconds": 3600,
					"codex_usage_updated_at":       now.Add(-30 * time.Minute).Format(time.RFC3339),
				},
			},
			want: true,
		},
		{
			name: "openai expired relative reset is recovered",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra: map[string]any{
					"codex_5h_used_percent":        95.0,
					"codex_5h_reset_after_seconds": 60,
					"codex_usage_updated_at":       now.Add(-2 * time.Hour),
				},
			},
		},
		{
			name: "openai missing update for relative reset fails open",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra: map[string]any{
					"codex_5h_used_percent":        95.0,
					"codex_5h_reset_after_seconds": 3600,
				},
			},
		},
		{
			name: "openai malformed reset fails open",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra: map[string]any{
					"codex_5h_used_percent": 95.0,
					"codex_5h_reset_at":     "not-a-time",
				},
			},
		},
		{
			name: "openai out of range utilization fails open",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra: map[string]any{
					"codex_5h_used_percent": 100.01,
					"codex_5h_reset_at":     future.Format(time.RFC3339),
				},
			},
		},
		{
			name: "openai api key is not gated",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Extra: map[string]any{
					"codex_5h_used_percent": 99.0,
					"codex_5h_reset_at":     future.Format(time.RFC3339),
				},
			},
		},
		{
			name: "nil account is accepted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldRejectNewSessionForHighFiveHourUsage(tt.account, now))
		})
	}
}
