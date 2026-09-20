package service

import "testing"

func TestAccountIsClaudeOAuthCarpoolUnlimitedDevices(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		want    bool
	}{
		{
			name: "explicitly enabled for carpool oauth",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeOAuth,
				Extra: map[string]any{
					"claude_oauth_mode":                      ClaudeOAuthModeCarpool,
					"claude_oauth_carpool_unlimited_devices": true,
				},
			},
			want: true,
		},
		{
			name: "missing flag keeps bounded default",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeSetupToken,
				Extra:    map[string]any{"claude_oauth_mode": ClaudeOAuthModeCarpool},
			},
		},
		{
			name: "string true is not accepted",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeOAuth,
				Extra: map[string]any{
					"claude_oauth_mode":                      ClaudeOAuthModeCarpool,
					"claude_oauth_carpool_unlimited_devices": "true",
				},
			},
		},
		{
			name: "flag is ignored outside carpool",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeOAuth,
				Extra: map[string]any{
					"claude_oauth_mode":                      ClaudeOAuthModePinned,
					"claude_oauth_carpool_unlimited_devices": true,
				},
			},
		},
		{
			name: "flag is ignored outside anthropic oauth",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{"claude_oauth_carpool_unlimited_devices": true},
			},
		},
		{name: "nil account"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.account.IsClaudeOAuthCarpoolUnlimitedDevices(); got != tt.want {
				t.Fatalf("IsClaudeOAuthCarpoolUnlimitedDevices() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAccountIsClaudeOAuthCarpoolAutoExpandEnabled(t *testing.T) {
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"claude_oauth_mode":                        ClaudeOAuthModeCarpool,
			"claude_oauth_carpool_auto_expand_enabled": true,
		},
	}
	if !account.IsClaudeOAuthCarpoolAutoExpandEnabled() {
		t.Fatal("expected bounded carpool account to enable automatic expansion")
	}

	account.Extra["claude_oauth_carpool_unlimited_devices"] = true
	if account.IsClaudeOAuthCarpoolAutoExpandEnabled() {
		t.Fatal("unlimited carpool account must not run bounded maintenance")
	}

	delete(account.Extra, "claude_oauth_carpool_unlimited_devices")
	account.Extra["claude_oauth_mode"] = ClaudeOAuthModeShared
	if account.IsClaudeOAuthCarpoolAutoExpandEnabled() {
		t.Fatal("automatic expansion must be ignored outside carpool mode")
	}
}

func TestAccountGetClaudeOAuthCarpoolAutoMaintenanceTarget(t *testing.T) {
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"claude_oauth_mode": ClaudeOAuthModeCarpool},
	}

	if got := account.GetClaudeOAuthCarpoolAutoMaintenanceTarget(); got != DefaultClaudeOAuthCarpoolAutoMaintenanceTarget {
		t.Fatalf("default target = %d, want %d", got, DefaultClaudeOAuthCarpoolAutoMaintenanceTarget)
	}

	account.Extra["claude_oauth_carpool_auto_maintenance_target"] = 24
	if got := account.GetClaudeOAuthCarpoolAutoMaintenanceTarget(); got != 24 {
		t.Fatalf("configured target = %d, want 24", got)
	}

	account.Extra["claude_oauth_carpool_auto_maintenance_target"] = 99
	if got := account.GetClaudeOAuthCarpoolAutoMaintenanceTarget(); got != maxClaudeOAuthCarpoolDeviceLimit {
		t.Fatalf("clamped target = %d, want %d", got, maxClaudeOAuthCarpoolDeviceLimit)
	}

	account.Extra["claude_oauth_carpool_auto_maintenance_target"] = 0
	if got := account.GetClaudeOAuthCarpoolAutoMaintenanceTarget(); got != DefaultClaudeOAuthCarpoolAutoMaintenanceTarget {
		t.Fatalf("invalid target fallback = %d, want %d", got, DefaultClaudeOAuthCarpoolAutoMaintenanceTarget)
	}
}
