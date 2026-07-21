package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountFromServiceShallowIncludesCarpoolUnlimitedDevices(t *testing.T) {
	account := &service.Account{
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeOAuth,
		Extra: map[string]any{
			"claude_oauth_mode":                      service.ClaudeOAuthModeCarpool,
			"claude_oauth_carpool_unlimited_devices": true,
		},
	}

	out := AccountFromServiceShallow(account)
	require.NotNil(t, out.ClaudeOAuthCarpoolUnlimitedDevices)
	require.True(t, *out.ClaudeOAuthCarpoolUnlimitedDevices)
	require.NotNil(t, out.ClaudeOAuthCarpoolDeviceLimit)
	require.Equal(t, service.DefaultClaudeOAuthCarpoolDeviceLimit, *out.ClaudeOAuthCarpoolDeviceLimit)
}
