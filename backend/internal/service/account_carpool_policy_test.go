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

func TestCarpoolMaintenancePolicyBounds(t *testing.T) {
	a := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"claude_oauth_carpool_device_limit": 10}}
	if a.GetCarpoolMaintenanceIntervalHours() != 24 || a.GetCarpoolMaintenanceFreeSlots() != 1 {
		t.Fatal("unexpected defaults")
	}
	a.Extra["claude_oauth_carpool_maintenance_interval_hours"] = 999
	a.Extra["claude_oauth_carpool_maintenance_free_slots"] = 99
	if a.GetCarpoolMaintenanceIntervalHours() != 720 || a.GetCarpoolMaintenanceFreeSlots() != 10 {
		t.Fatal("unexpected bounds")
	}
}

func TestSingleDeviceAdmissionAndFixedHeaderDefaults(t *testing.T) {
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{
		"claude_oauth_mode":               ClaudeOAuthModeSingleDevice,
		"claude_oauth_fixed_headers_text": "X-Stainless-Lang: js",
	}}
	if account.IsClaudeOAuthFixedHeadersEnabled() {
		t.Fatal("new accounts without an explicit flag must keep fixed headers disabled")
	}
	account.Extra["claude_oauth_fixed_headers_enabled"] = true // migration result for existing accounts
	if !account.IsClaudeOAuthFixedHeadersEnabled() {
		t.Fatal("migrated legacy accounts must remain enabled")
	}
	if account.IsClaudeOAuthSingleDeviceAdmissionEnabled() {
		t.Fatal("existing accounts must remain unbounded")
	}
	account.Extra["claude_oauth_fixed_headers_enabled"] = false
	if account.IsClaudeOAuthFixedHeadersEnabled() {
		t.Fatal("explicit false must disable fixed headers")
	}
	account.Extra["claude_oauth_single_device_admission_enabled"] = true
	account.Extra["claude_oauth_single_device_admission_auto_expand_enabled"] = true
	if !account.IsClaudeOAuthSingleDeviceAdmissionAutoExpandEnabled() {
		t.Fatal("enabled admission should permit daily maintenance")
	}
	account.Extra["claude_oauth_single_device_admission_enabled"] = false
	if account.IsClaudeOAuthSingleDeviceAdmissionAutoExpandEnabled() {
		t.Fatal("disabled admission must disable daily maintenance")
	}
}
