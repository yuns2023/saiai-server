package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountQuotaBudget_OpenAIConsidersResetTime(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	soon := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"codex_7d_used_percent": 80.0,
			"codex_7d_reset_at":     now.Add(10 * time.Minute).Format(time.RFC3339),
		},
	}
	far := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"codex_7d_used_percent": 60.0,
			"codex_7d_reset_at":     now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
		},
	}

	soonScore := accountQuotaBudgetForScheduling(soon, "gpt-5.2-codex", now)
	farScore := accountQuotaBudgetForScheduling(far, "gpt-5.2-codex", now)
	require.True(t, soonScore.known)
	require.True(t, farScore.known)
	require.Greater(t, soonScore.score, farScore.score)
}

func TestAccountQuotaBudget_OpenAIProtectsTinyRemainingEvenNearReset(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	tinyRemaining := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"codex_7d_used_percent": 99.0,
			"codex_7d_reset_at":     now.Add(5 * time.Minute).Format(time.RFC3339),
		},
	}
	healthy := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"codex_7d_used_percent": 60.0,
			"codex_7d_reset_at":     now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
		},
	}

	tinyScore := accountQuotaBudgetForScheduling(tinyRemaining, "gpt-5.2-codex", now)
	healthyScore := accountQuotaBudgetForScheduling(healthy, "gpt-5.2-codex", now)
	require.True(t, tinyScore.known)
	require.True(t, healthyScore.known)
	require.Less(t, tinyScore.score, healthyScore.score)
}

func TestAccountQuotaBudget_ExpiredResetTreatsWindowAsRecovered(t *testing.T) {
	now := time.Date(2026, 6, 10, 15, 30, 0, 0, time.UTC)
	staleReset := now.Add(-10 * time.Minute)
	activeReset := now.Add(3 * time.Hour)
	staleFullWindow := &Account{
		Platform:         PlatformAnthropic,
		Type:             AccountTypeSetupToken,
		SessionWindowEnd: &staleReset,
		Extra: map[string]any{
			"session_window_utilization": 1.0,
		},
	}
	activeHalfWindow := &Account{
		Platform:         PlatformAnthropic,
		Type:             AccountTypeSetupToken,
		SessionWindowEnd: &activeReset,
		Extra: map[string]any{
			"session_window_utilization": 0.5,
		},
	}

	staleScore := accountQuotaBudgetForScheduling(staleFullWindow, "claude-opus-4-8", now)
	activeScore := accountQuotaBudgetForScheduling(activeHalfWindow, "claude-opus-4-8", now)
	require.True(t, staleScore.known)
	require.True(t, activeScore.known)
	require.Greater(t, staleScore.score, activeScore.score)
}

func TestFilterByBestQuotaBudget_StaysWithinPriority(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	accounts := []accountWithLoad{
		{
			account: &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Priority: 1, Extra: map[string]any{
				"passive_usage_7d_utilization": 0.80,
				"passive_usage_7d_reset":       now.Add(10 * time.Minute).Unix(),
			}},
			loadInfo: &AccountLoadInfo{},
		},
		{
			account: &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Priority: 1, Extra: map[string]any{
				"passive_usage_7d_utilization": 0.60,
				"passive_usage_7d_reset":       now.Add(7 * 24 * time.Hour).Unix(),
			}},
			loadInfo: &AccountLoadInfo{},
		},
		{
			account: &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Priority: 2, Extra: map[string]any{
				"passive_usage_7d_utilization": 0.01,
				"passive_usage_7d_reset":       now.Add(7 * 24 * time.Hour).Unix(),
			}},
			loadInfo: &AccountLoadInfo{},
		},
	}

	filtered := filterWithinPriorityByQuotaBudget(accounts, "claude-sonnet-4-6", now)
	require.Len(t, filtered, 2)
	require.Equal(t, int64(1), filtered[0].account.ID)
	require.Equal(t, int64(3), filtered[1].account.ID, "lower-priority groups are filtered independently, not promoted")
}

func TestFilterByBestQuotaBudget_PrefersExpiredResetWindow(t *testing.T) {
	now := time.Date(2026, 6, 10, 15, 30, 0, 0, time.UTC)
	reset5hExpired := now.Add(-10 * time.Minute)
	reset5hSoon := now.Add(150 * time.Minute)
	reset5hLater := now.Add(4*time.Hour + 40*time.Minute)
	oldLastUsed := now.Add(-5 * time.Hour)
	recentLastUsed := now.Add(-10 * time.Minute)
	accounts := []accountWithLoad{
		{
			account: &Account{
				ID:               179,
				Platform:         PlatformAnthropic,
				Type:             AccountTypeSetupToken,
				Priority:         1,
				LastUsedAt:       &oldLastUsed,
				SessionWindowEnd: &reset5hExpired,
				Extra: map[string]any{
					"session_window_utilization":   1.0,
					"passive_usage_7d_utilization": 0.12,
					"passive_usage_7d_reset":       now.Add(33 * time.Hour).Unix(),
				},
			},
			loadInfo: &AccountLoadInfo{},
		},
		{
			account: &Account{
				ID:               198,
				Platform:         PlatformAnthropic,
				Type:             AccountTypeSetupToken,
				Priority:         1,
				LastUsedAt:       &recentLastUsed,
				SessionWindowEnd: &reset5hSoon,
				Extra: map[string]any{
					"session_window_utilization":   0.50,
					"passive_usage_7d_utilization": 0.20,
					"passive_usage_7d_reset":       now.Add(5*24*time.Hour + 9*time.Hour).Unix(),
				},
			},
			loadInfo: &AccountLoadInfo{},
		},
		{
			account: &Account{
				ID:               200,
				Platform:         PlatformAnthropic,
				Type:             AccountTypeSetupToken,
				Priority:         1,
				LastUsedAt:       &recentLastUsed,
				SessionWindowEnd: &reset5hLater,
				Extra: map[string]any{
					"session_window_utilization":   0.03,
					"passive_usage_7d_utilization": 0.21,
					"passive_usage_7d_reset":       now.Add(4*24*time.Hour + 22*time.Hour).Unix(),
				},
			},
			loadInfo: &AccountLoadInfo{},
		},
	}

	filtered := filterByBestQuotaBudget(accounts, "claude-opus-4-8", now)
	require.Len(t, filtered, 2)
	require.Contains(t, accountWithLoadIDs(filtered), int64(179))
	require.NotContains(t, accountWithLoadIDs(filtered), int64(198))

	selected := selectByLRU(filtered, false)
	require.NotNil(t, selected)
	require.Equal(t, int64(179), selected.account.ID)
}

func TestFilterByTopQuotaBudget_PrefersBetterBudgetBeforeLRU(t *testing.T) {
	now := time.Date(2026, 6, 11, 0, 10, 24, 0, time.UTC)
	account179WindowEnd := now.Add(80 * time.Minute)
	account200WindowEnd := now.Add(-10 * time.Minute)
	account179LastUsed := now.Add(-9 * time.Minute)
	account200LastUsed := now.Add(-7*time.Hour - 32*time.Minute)
	accounts := []accountWithLoad{
		{
			account: &Account{
				ID:               179,
				Platform:         PlatformAnthropic,
				Type:             AccountTypeSetupToken,
				Priority:         1,
				LastUsedAt:       &account179LastUsed,
				SessionWindowEnd: &account179WindowEnd,
				Extra: map[string]any{
					"session_window_utilization":   0.0,
					"passive_usage_7d_utilization": 0.17,
					"passive_usage_7d_reset":       now.Add(24*time.Hour + 50*time.Minute).Unix(),
				},
			},
			loadInfo: &AccountLoadInfo{LoadRate: 0},
		},
		{
			account: &Account{
				ID:               200,
				Platform:         PlatformAnthropic,
				Type:             AccountTypeSetupToken,
				Priority:         1,
				LastUsedAt:       &account200LastUsed,
				SessionWindowEnd: &account200WindowEnd,
				Extra: map[string]any{
					"session_window_utilization":   0.02,
					"passive_usage_7d_utilization": 0.23,
					"passive_usage_7d_reset":       now.Add(4*24*time.Hour + 13*time.Hour + 50*time.Minute).Unix(),
				},
			},
			loadInfo: &AccountLoadInfo{LoadRate: 0},
		},
	}

	oldFiltered := filterByBestQuotaBudget(accounts, "claude-opus-4-8", now)
	require.Len(t, oldFiltered, 2, "old tolerance would hand the choice to LRU")

	filtered := filterByTopQuotaBudget(accounts, "claude-opus-4-8", now)
	require.Len(t, filtered, 1)
	require.Equal(t, int64(179), filtered[0].account.ID)
}

func TestFilterWithinPriorityAndLoadByTopQuotaBudget_DoesNotCrossLoadLayer(t *testing.T) {
	now := time.Date(2026, 6, 11, 0, 10, 24, 0, time.UTC)
	windowEnd := now.Add(3 * time.Hour)
	accounts := []accountWithLoad{
		{
			account: &Account{
				ID:               1,
				Platform:         PlatformAnthropic,
				Type:             AccountTypeSetupToken,
				Priority:         1,
				SessionWindowEnd: &windowEnd,
				Extra: map[string]any{
					"session_window_utilization":   0.0,
					"passive_usage_7d_utilization": 0.10,
					"passive_usage_7d_reset":       now.Add(4 * 24 * time.Hour).Unix(),
				},
			},
			loadInfo: &AccountLoadInfo{LoadRate: 50},
		},
		{
			account: &Account{
				ID:               2,
				Platform:         PlatformAnthropic,
				Type:             AccountTypeSetupToken,
				Priority:         1,
				SessionWindowEnd: &windowEnd,
				Extra: map[string]any{
					"session_window_utilization":   0.20,
					"passive_usage_7d_utilization": 0.30,
					"passive_usage_7d_reset":       now.Add(4 * 24 * time.Hour).Unix(),
				},
			},
			loadInfo: &AccountLoadInfo{LoadRate: 0},
		},
	}

	filtered := filterWithinPriorityAndLoadByTopQuotaBudget(accounts, "claude-opus-4-8", now)
	require.Len(t, filtered, 2)
	require.ElementsMatch(t, []int64{1, 2}, accountWithLoadIDs(filtered))
}

func accountWithLoadIDs(accounts []accountWithLoad) []int64 {
	ids := make([]int64, 0, len(accounts))
	for _, item := range accounts {
		if item.account != nil {
			ids = append(ids, item.account.ID)
		}
	}
	return ids
}
