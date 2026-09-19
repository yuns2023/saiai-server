//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeAccountBlockedModelPatternsExtra(t *testing.T) {
	extra := map[string]any{
		accountBlockedModelPatternsExtraKey: []any{" Claude-Fable-* ", "claude-fable-*", "", "GPT-4O*"},
	}

	require.NoError(t, NormalizeAccountBlockedModelPatternsExtra(extra))
	require.Equal(t, []string{"claude-fable-*", "gpt-4o*"}, extra[accountBlockedModelPatternsExtraKey])
}

func TestNormalizeAccountBlockedModelPatternsExtraRejectsInvalidValues(t *testing.T) {
	tests := []map[string]any{
		{accountBlockedModelPatternsExtraKey: "claude-fable-*"},
		{accountBlockedModelPatternsExtraKey: []any{"claude-fable-*", 1}},
		{accountBlockedModelPatternsExtraKey: []any{"bad\npattern"}},
	}
	for _, extra := range tests {
		require.Error(t, NormalizeAccountBlockedModelPatternsExtra(extra))
	}
}

func TestAccountIsModelBlocked(t *testing.T) {
	account := &Account{
		Platform: PlatformAnthropic,
		Extra: map[string]any{
			accountBlockedModelPatternsExtraKey: []any{"claude-fable-*", "claude-sonnet-4-5-20250929"},
		},
	}

	require.True(t, account.IsModelBlocked("claude-fable-5-1"))
	require.True(t, account.IsModelBlocked("claude-sonnet-4-5"), "normalized Claude aliases must not bypass the denylist")
	require.False(t, account.IsModelBlocked("claude-opus-4-6"))
	require.False(t, account.IsModelSupported("claude-fable-5-1"))
	require.True(t, account.IsModelSupported("claude-opus-4-6"))
}

func TestAccountIsModelBlockedChecksMappedTarget(t *testing.T) {
	account := &Account{
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"client-model": "claude-fable-5-1"},
		},
		Extra: map[string]any{
			accountBlockedModelPatternsExtraKey: []string{"claude-fable-*"},
		},
	}

	require.True(t, account.IsModelBlocked("client-model"))
	require.False(t, account.IsModelSupported("client-model"))
}

func TestGatewayModelSupportAppliesAccountDenylist(t *testing.T) {
	svc := &GatewayService{}
	account := &Account{
		ID:       253,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
		Extra: map[string]any{
			accountBlockedModelPatternsExtraKey: []any{"claude-fable-*"},
		},
	}

	require.False(t, svc.isModelSupportedByAccount(account, "claude-fable-5-1"))
	require.True(t, svc.isModelSupportedByAccount(account, "claude-sonnet-5"))
}
