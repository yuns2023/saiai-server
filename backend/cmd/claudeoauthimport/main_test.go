package main

import (
	"testing"
)

func TestNormalizeExpiresAt(t *testing.T) {
	if got := normalizeExpiresAt(1774221722133); got != 1774221722 {
		t.Fatalf("normalize ms expires_at mismatch: got=%d", got)
	}
	if got := normalizeExpiresAt(1774221722); got != 1774221722 {
		t.Fatalf("normalize sec expires_at mismatch: got=%d", got)
	}
}

func TestBuildCreateAccountInput_Carpool(t *testing.T) {
	fileData := &claudeAiOauthCredentials{
		AccessToken:      "at",
		RefreshToken:     "rt",
		ExpiresAt:        1774221722133,
		Scopes:           []string{"user:inference", "user:profile"},
		SubscriptionType: "pro",
		RateLimitTier:    "default_claude_ai",
	}
	opts := &cliOptions{
		Name:               "claude-pro",
		AccountUUID:        "550e8400-e29b-41d4-a716-446655440000",
		OrgUUID:            "org-123",
		Email:              "user@example.com",
		ProxyURL:           "socks5h://proxy.example.com:1080",
		ProxyName:          "import-proxy",
		ProxyID:            99,
		Mode:               "carpool",
		CarpoolDeviceLimit: 7,
		Concurrency:        3,
		Priority:           1,
		GroupIDs:           []int64{11, 22},
	}

	input, summary, err := buildCreateAccountInput(fileData, opts)
	if err != nil {
		t.Fatalf("buildCreateAccountInput returned error: %v", err)
	}

	if got := input.Credentials["access_token"]; got != "at" {
		t.Fatalf("access_token mismatch: got=%v", got)
	}
	if got := input.Credentials["refresh_token"]; got != "rt" {
		t.Fatalf("refresh_token mismatch: got=%v", got)
	}
	if got := input.Credentials["expires_at"]; got != int64(1774221722) {
		t.Fatalf("expires_at mismatch: got=%v", got)
	}
	if got := input.Credentials["scope"]; got != "user:inference user:profile" {
		t.Fatalf("scope mismatch: got=%v", got)
	}
	if got := input.Extra["account_uuid"]; got != opts.AccountUUID {
		t.Fatalf("account_uuid mismatch: got=%v", got)
	}
	if got := input.Extra["claude_oauth_mode"]; got != "carpool" {
		t.Fatalf("mode mismatch: got=%v", got)
	}
	if got := input.Extra["claude_oauth_carpool_device_limit"]; got != 7 {
		t.Fatalf("carpool limit mismatch: got=%v", got)
	}
	if _, ok := input.Extra["claude_oauth_shared_bucket_count"]; ok {
		t.Fatal("shared bucket count should not be set in carpool mode")
	}
	if summary.ClaudeOAuthCarpoolLimit == nil || *summary.ClaudeOAuthCarpoolLimit != 7 {
		t.Fatalf("summary carpool limit mismatch: got=%v", summary.ClaudeOAuthCarpoolLimit)
	}
	if summary.ClaudeOAuthSharedBuckets != nil {
		t.Fatalf("summary shared buckets should be nil: got=%v", *summary.ClaudeOAuthSharedBuckets)
	}
	if summary.ProxyID == nil || *summary.ProxyID != 99 {
		t.Fatalf("summary proxy id mismatch: got=%v", summary.ProxyID)
	}
	if summary.ProxyURL != "socks5h://proxy.example.com:1080" {
		t.Fatalf("summary proxy url mismatch: got=%q", summary.ProxyURL)
	}
	if summary.ProxyName != "import-proxy" {
		t.Fatalf("summary proxy name mismatch: got=%q", summary.ProxyName)
	}
}

func TestBuildCreateAccountInput_CarpoolUnlimitedDevices(t *testing.T) {
	fileData := &claudeAiOauthCredentials{
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresAt:    1774221722,
	}
	opts := &cliOptions{
		AccountUUID:        "550e8400-e29b-41d4-a716-446655440000",
		ProxyURL:           "http://proxy.example.com:8080",
		Mode:               "carpool",
		CarpoolDeviceLimit: 7,
		CarpoolUnlimited:   true,
		Concurrency:        2,
	}

	input, summary, err := buildCreateAccountInput(fileData, opts)
	if err != nil {
		t.Fatalf("buildCreateAccountInput returned error: %v", err)
	}

	if got := input.Extra["claude_oauth_carpool_unlimited_devices"]; got != true {
		t.Fatalf("carpool unlimited flag mismatch: got=%v", got)
	}
	if got := input.Extra["claude_oauth_carpool_device_limit"]; got != 7 {
		t.Fatalf("carpool limit mismatch: got=%v", got)
	}
	if !summary.CarpoolUnlimited {
		t.Fatal("summary should report unlimited carpool devices")
	}
	if summary.ClaudeOAuthCarpoolLimit == nil || *summary.ClaudeOAuthCarpoolLimit != 7 {
		t.Fatalf("summary carpool limit mismatch: got=%v", summary.ClaudeOAuthCarpoolLimit)
	}
}

func TestBuildCreateAccountInput_Shared(t *testing.T) {
	fileData := &claudeAiOauthCredentials{
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresAt:    1774221722,
	}
	opts := &cliOptions{
		AccountUUID:       "550e8400-e29b-41d4-a716-446655440000",
		ProxyURL:          "http://proxy.example.com:8080",
		Mode:              "shared",
		SharedBucketCount: 5,
		Concurrency:       2,
	}

	input, summary, err := buildCreateAccountInput(fileData, opts)
	if err != nil {
		t.Fatalf("buildCreateAccountInput returned error: %v", err)
	}

	if got := input.Extra["claude_oauth_mode"]; got != "shared" {
		t.Fatalf("mode mismatch: got=%v", got)
	}
	if got := input.Extra["claude_oauth_shared_bucket_count"]; got != 5 {
		t.Fatalf("shared bucket count mismatch: got=%v", got)
	}
	if _, ok := input.Extra["claude_oauth_carpool_device_limit"]; ok {
		t.Fatal("carpool limit should not be set in shared mode")
	}
	if summary.ClaudeOAuthSharedBuckets == nil || *summary.ClaudeOAuthSharedBuckets != 5 {
		t.Fatalf("summary shared buckets mismatch: got=%v", summary.ClaudeOAuthSharedBuckets)
	}
}

func TestBuildCreateAccountInput_PinnedOmitsLegacyLimitFields(t *testing.T) {
	fileData := &claudeAiOauthCredentials{
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresAt:    1774221722,
	}
	opts := &cliOptions{
		AccountUUID: "550e8400-e29b-41d4-a716-446655440000",
		ProxyURL:    "http://proxy.example.com:8080",
		Mode:        "pinned",
		Concurrency: 2,
	}

	input, summary, err := buildCreateAccountInput(fileData, opts)
	if err != nil {
		t.Fatalf("buildCreateAccountInput returned error: %v", err)
	}

	if got := input.Extra["claude_oauth_mode"]; got != "pinned" {
		t.Fatalf("mode mismatch: got=%v", got)
	}
	if _, ok := input.Extra["claude_oauth_shared_bucket_count"]; ok {
		t.Fatal("shared bucket count should not be set in pinned mode")
	}
	if _, ok := input.Extra["claude_oauth_carpool_device_limit"]; ok {
		t.Fatal("carpool limit should not be set in pinned mode")
	}
	if summary.ClaudeOAuthSharedBuckets != nil {
		t.Fatalf("summary shared buckets should be nil: got=%v", *summary.ClaudeOAuthSharedBuckets)
	}
	if summary.ClaudeOAuthCarpoolLimit != nil {
		t.Fatalf("summary carpool limit should be nil: got=%v", *summary.ClaudeOAuthCarpoolLimit)
	}
}

func TestDefaultImportedAccountName(t *testing.T) {
	fileData := &claudeAiOauthCredentials{SubscriptionType: "pro"}
	opts := &cliOptions{AccountUUID: "550e8400-e29b-41d4-a716-446655440000"}
	got := defaultImportedAccountName(fileData, opts)
	want := "claude-oauth-pro-550e8400"
	if got != want {
		t.Fatalf("defaultImportedAccountName mismatch: got=%q want=%q", got, want)
	}
}

func TestBuildCreateAccountInput_RequiresAccountUUID(t *testing.T) {
	fileData := &claudeAiOauthCredentials{
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresAt:    1774221722,
	}
	opts := &cliOptions{
		ProxyURL:    "socks5h://proxy.example.com:1080",
		Mode:        "carpool",
		Concurrency: 1,
	}
	if _, _, err := buildCreateAccountInput(fileData, opts); err == nil {
		t.Fatal("expected missing account_uuid error")
	}
}

func TestParseImportedProxy_UpgradesSOCKS5(t *testing.T) {
	spec, err := parseImportedProxy("socks5://user:pass@192.0.2.44:5987", "")
	if err != nil {
		t.Fatalf("parseImportedProxy returned error: %v", err)
	}
	if spec.Protocol != "socks5h" {
		t.Fatalf("protocol mismatch: got=%q", spec.Protocol)
	}
	if spec.RawURL != "socks5h://user:pass@192.0.2.44:5987" {
		t.Fatalf("raw url mismatch: got=%q", spec.RawURL)
	}
	if spec.Username != "user" || spec.Password != "pass" {
		t.Fatalf("credentials mismatch: got=%q/%q", spec.Username, spec.Password)
	}
}

func TestApplyDetectedProfile_FillsMissingFields(t *testing.T) {
	fileData := &claudeAiOauthCredentials{}
	opts := &cliOptions{}
	profile := &claudeOAuthProfileResponse{
		Account: &claudeOAuthProfileAccount{
			UUID:  "550e8400-e29b-41d4-a716-446655440000",
			Email: "user@example.com",
		},
		Organization: &claudeOAuthProfileOrganization{
			UUID:             "org-123",
			OrganizationType: "claude_pro",
			RateLimitTier:    "default_claude_ai",
		},
	}

	applyDetectedProfile(fileData, opts, profile)

	if opts.AccountUUID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("account uuid mismatch: got=%q", opts.AccountUUID)
	}
	if opts.Email != "user@example.com" {
		t.Fatalf("email mismatch: got=%q", opts.Email)
	}
	if opts.OrgUUID != "org-123" {
		t.Fatalf("org uuid mismatch: got=%q", opts.OrgUUID)
	}
	if fileData.SubscriptionType != "pro" {
		t.Fatalf("subscription type mismatch: got=%q", fileData.SubscriptionType)
	}
	if fileData.RateLimitTier != "default_claude_ai" {
		t.Fatalf("rate limit tier mismatch: got=%q", fileData.RateLimitTier)
	}
}

func TestApplyDetectedProfile_PreservesExplicitOverrides(t *testing.T) {
	fileData := &claudeAiOauthCredentials{
		SubscriptionType: "max",
		RateLimitTier:    "custom",
	}
	opts := &cliOptions{
		AccountUUID: "11111111-1111-1111-1111-111111111111",
		OrgUUID:     "org-explicit",
		Email:       "explicit@example.com",
	}
	profile := &claudeOAuthProfileResponse{
		Account: &claudeOAuthProfileAccount{
			UUID:  "550e8400-e29b-41d4-a716-446655440000",
			Email: "user@example.com",
		},
		Organization: &claudeOAuthProfileOrganization{
			UUID:             "org-123",
			OrganizationType: "claude_pro",
			RateLimitTier:    "default_claude_ai",
		},
	}

	applyDetectedProfile(fileData, opts, profile)

	if opts.AccountUUID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("account uuid override lost: got=%q", opts.AccountUUID)
	}
	if opts.OrgUUID != "org-explicit" {
		t.Fatalf("org uuid override lost: got=%q", opts.OrgUUID)
	}
	if opts.Email != "explicit@example.com" {
		t.Fatalf("email override lost: got=%q", opts.Email)
	}
	if fileData.SubscriptionType != "max" {
		t.Fatalf("subscription type override lost: got=%q", fileData.SubscriptionType)
	}
	if fileData.RateLimitTier != "custom" {
		t.Fatalf("rate limit tier override lost: got=%q", fileData.RateLimitTier)
	}
}
