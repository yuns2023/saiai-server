package service

import (
	"testing"
	"time"
)

func TestApplyClaudeOAuthSingleDeviceDefaults_AppliesDefaults(t *testing.T) {
	extra := map[string]any{
		"claude_oauth_mode": "single_device",
	}

	applyClaudeOAuthSingleDeviceDefaults(PlatformAnthropic, AccountTypeOAuth, extra)

	if got, ok := extra["claude_oauth_quota_disable_threshold_percent"]; ok {
		t.Fatalf("expected legacy quota disable threshold to remain unset by default, got %v", got)
	}
	if got := extra["claude_oauth_disable_token_refresh"]; got != true {
		t.Fatalf("expected token refresh disabled by default, got %v", got)
	}
	if got := extra["claude_oauth_token_disable_before_expiry_minutes"]; got != DefaultClaudeOAuthTokenDisableBeforeExpiryMinutes {
		t.Fatalf("expected expiry disable minutes %v, got %v", DefaultClaudeOAuthTokenDisableBeforeExpiryMinutes, got)
	}
}

func TestApplyClaudeOAuthSingleDeviceDefaults_PreservesOverrides(t *testing.T) {
	extra := map[string]any{
		"claude_oauth_mode":                                "single_device",
		"claude_oauth_quota_disable_threshold_percent":     92.0,
		"claude_oauth_disable_token_refresh":               false,
		"claude_oauth_token_disable_before_expiry_minutes": 7,
	}

	applyClaudeOAuthSingleDeviceDefaults(PlatformAnthropic, AccountTypeOAuth, extra)

	if got := extra["claude_oauth_quota_disable_threshold_percent"]; got != 92.0 {
		t.Fatalf("expected custom quota disable threshold preserved, got %v", got)
	}
	if got := extra["claude_oauth_disable_token_refresh"]; got != false {
		t.Fatalf("expected custom token refresh switch preserved, got %v", got)
	}
	if got := extra["claude_oauth_token_disable_before_expiry_minutes"]; got != 7 {
		t.Fatalf("expected custom expiry disable minutes preserved, got %v", got)
	}
}

func TestAccountIsSchedulable_SingleDeviceOAuthNearExpiryDisabled(t *testing.T) {
	expiresAt := time.Now().Add(2 * time.Minute)
	account := &Account{
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token": "token",
			"expires_at":   expiresAt.Unix(),
		},
		Extra: map[string]any{
			"claude_oauth_mode": "single_device",
		},
	}

	if account.IsSchedulable() {
		t.Fatalf("expected single_device oauth account near expiry to be unschedulable")
	}
}

func TestAccountShouldRateLimitForClaudeOAuth5hUsage_ConfiguredThreshold(t *testing.T) {
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"claude_oauth_mode":                            "single_device",
			"claude_oauth_5h_rate_limit_threshold_percent": 85.0,
		},
	}

	if !account.ShouldRateLimitForClaudeOAuth5hUsage(86) {
		t.Fatalf("expected oauth account above configured 5h threshold to rate-limit")
	}
	if account.ShouldRateLimitForClaudeOAuth5hUsage(84.9) {
		t.Fatalf("expected oauth account below configured 5h threshold to remain available")
	}
}
