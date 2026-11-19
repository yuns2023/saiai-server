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
	if a.GetCarpoolMaintenanceIntervalHours() != 24 || a.GetCarpoolAdmissionsPerCycle() != 0 {
		t.Fatal("unexpected defaults")
	}
	a.Extra["claude_oauth_carpool_maintenance_interval_hours"] = 999
	a.Extra["claude_oauth_carpool_maintenance_admissions_per_cycle"] = 99
	if a.GetCarpoolMaintenanceIntervalHours() != 720 || a.GetCarpoolAdmissionsPerCycle() != 10 {
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

func TestCarpoolAdmissionsExplicitZeroOverridesLegacy(t *testing.T) {
	a := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"claude_oauth_carpool_device_limit": 9, "claude_oauth_carpool_maintenance_free_slots": 2}}
	if a.GetCarpoolAdmissionsPerCycle() != 2 {
		t.Fatal("legacy finite allowance must be preserved")
	}
	a.Extra["claude_oauth_carpool_maintenance_admissions_per_cycle"] = 0
	if a.GetCarpoolAdmissionsPerCycle() != 0 {
		t.Fatal("explicit zero must disable the cycle budget")
	}
	a.Extra["claude_oauth_carpool_maintenance_admissions_per_cycle"] = 1
	if a.GetCarpoolAdmissionsPerCycle() != 1 {
		t.Fatal("explicit finite allowance must be preserved")
	}
}

func TestCarpoolEvictIdleHoursPolicy(t *testing.T) {
	a := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"claude_oauth_carpool_device_limit": 10}}
	cases := []struct {
		name  string
		value any
		set   bool
		want  int
	}{
		{name: "absent defaults to 24", want: DefaultCarpoolEvictIdleHours},
		{name: "null defaults to 24", value: nil, set: true, want: DefaultCarpoolEvictIdleHours},
		{name: "explicit zero lifts the restriction", value: float64(0), set: true, want: 0},
		{name: "positive", value: float64(6), set: true, want: 6},
		{name: "numeric string", value: "12", set: true, want: 12},
		{name: "unparseable string keeps default", value: "abc", set: true, want: DefaultCarpoolEvictIdleHours},
		{name: "negative clamps to zero", value: float64(-3), set: true, want: 0},
		{name: "upper bound", value: float64(9999), set: true, want: 720},
		{name: "huge value clamps without overflow", value: float64(1e300), set: true, want: 720},
		{name: "blank string keeps default", value: "  ", set: true, want: DefaultCarpoolEvictIdleHours},
		{name: "binary prefix keeps default", value: "0b0", set: true, want: DefaultCarpoolEvictIdleHours},
		{name: "hex prefix keeps default", value: "0x10", set: true, want: DefaultCarpoolEvictIdleHours},
		{name: "inf string keeps default", value: "inf", set: true, want: DefaultCarpoolEvictIdleHours},
		{name: "decimal with exponent", value: " 1.2e1 ", set: true, want: 12},
		{name: "boolean keeps default", value: false, set: true, want: DefaultCarpoolEvictIdleHours},
		{name: "object keeps default", value: map[string]any{}, set: true, want: DefaultCarpoolEvictIdleHours},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			delete(a.Extra, CarpoolEvictIdleHoursKey)
			if tc.set {
				a.Extra[CarpoolEvictIdleHoursKey] = tc.value
			}
			if got := a.GetCarpoolEvictIdleHours(); got != tc.want {
				t.Fatalf("GetCarpoolEvictIdleHours() = %d, want %d", got, tc.want)
			}
			if got := a.CarpoolAdmissionPolicy().EvictIdleHours; got != tc.want {
				t.Fatalf("policy EvictIdleHours = %d, want %d", got, tc.want)
			}
		})
	}
}
