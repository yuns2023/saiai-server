package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountFromServiceShallowIncludesCarpoolPolicy(t *testing.T) {
	account := &service.Account{
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeOAuth,
		Extra: map[string]any{
			"claude_oauth_mode":                        service.ClaudeOAuthModeCarpool,
			"claude_oauth_carpool_auto_expand_enabled": true,
		},
	}

	out := AccountFromServiceShallow(account)
	require.NotNil(t, out.ClaudeOAuthCarpoolUnlimitedDevices)
	require.False(t, *out.ClaudeOAuthCarpoolUnlimitedDevices)
	require.NotNil(t, out.ClaudeOAuthCarpoolAutoExpand)
	require.True(t, *out.ClaudeOAuthCarpoolAutoExpand)
	require.NotNil(t, out.ClaudeOAuthCarpoolDeviceLimit)
	require.Equal(t, service.DefaultClaudeOAuthCarpoolDeviceLimit, *out.ClaudeOAuthCarpoolDeviceLimit)
}
