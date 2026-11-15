package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// SocialOAuthProvider identifies a first-party "sign in with" provider (not upstream AI account OAuth).
type SocialOAuthProvider string

const (
	SocialOAuthProviderGoogle SocialOAuthProvider = "google"
	SocialOAuthProviderGitHub SocialOAuthProvider = "github"
)

// ParseSocialOAuthProvider returns the provider for a route/path value, or false if unsupported.
func ParseSocialOAuthProvider(raw string) (SocialOAuthProvider, bool) {
	switch SocialOAuthProvider(strings.ToLower(strings.TrimSpace(raw))) {
	case SocialOAuthProviderGoogle:
		return SocialOAuthProviderGoogle, true
	case SocialOAuthProviderGitHub:
		return SocialOAuthProviderGitHub, true
	default:
		return "", false
	}
}

// SyntheticEmailDomain is the reserved domain that binds local accounts to this provider.
func (p SocialOAuthProvider) SyntheticEmailDomain() string {
	switch p {
	case SocialOAuthProviderGoogle:
		return GoogleOAuthSyntheticEmailDomain
	case SocialOAuthProviderGitHub:
		return GitHubOAuthSyntheticEmailDomain
	default:
		return ""
	}
}

// CallbackPath is the backend callback route registered with the provider.
func (p SocialOAuthProvider) CallbackPath() string {
	return "/api/v1/auth/oauth/" + string(p) + "/callback"
}

// FrontendCallbackPath is the same-origin frontend page that finishes the flow.
func (p SocialOAuthProvider) FrontendCallbackPath() string {
	return "/auth/oauth/" + string(p) + "/callback"
}

func (p SocialOAuthProvider) settingKeys() (enabled, clientID, clientSecret string) {
	switch p {
	case SocialOAuthProviderGoogle:
		return SettingKeyGoogleOAuthEnabled, SettingKeyGoogleOAuthClientID, SettingKeyGoogleOAuthClientSecret
	case SocialOAuthProviderGitHub:
		return SettingKeyGitHubOAuthEnabled, SettingKeyGitHubOAuthClientID, SettingKeyGitHubOAuthClientSecret
	default:
		return "", "", ""
	}
}

// SocialOAuthConfig is the effective, validated configuration for one provider.
type SocialOAuthConfig struct {
	Provider     SocialOAuthProvider
	ClientID     string
	ClientSecret string
	// RedirectURL is <frontend_url>/api/v1/auth/oauth/<provider>/callback.
	RedirectURL string
	// FrontendCallbackURL is a same-origin relative path.
	FrontendCallbackURL string
}

var (
	ErrSocialOAuthDisabled      = infraerrors.NotFound("OAUTH_DISABLED", "oauth login is disabled")
	ErrSocialOAuthConfigInvalid = infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth provider is not fully configured")
)

// SocialOAuthRedirectURL derives the provider callback URL from the site's frontend URL.
// Returns "" when the frontend URL is missing or not an absolute http(s) URL without query/userinfo.
func SocialOAuthRedirectURL(frontendURL string, provider SocialOAuthProvider) string {
	base := strings.TrimSpace(frontendURL)
	if base == "" || config.ValidateAbsoluteHTTPURL(base) != nil {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return ""
	}
	return strings.TrimRight(base, "/") + provider.CallbackPath()
}

// GetSocialOAuthConfig returns the effective configuration for a provider.
// Any settings read failure is returned as an error (fail closed); a disabled or incomplete provider is refused.
func (s *SettingService) GetSocialOAuthConfig(ctx context.Context, provider SocialOAuthProvider) (SocialOAuthConfig, error) {
	if s == nil || s.settingRepo == nil {
		return SocialOAuthConfig{}, infraerrors.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
	}
	enabledKey, idKey, secretKey := provider.settingKeys()
	if enabledKey == "" {
		return SocialOAuthConfig{}, ErrSocialOAuthDisabled
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{enabledKey, idKey, secretKey, SettingKeyFrontendURL})
	if err != nil {
		return SocialOAuthConfig{}, infraerrors.ServiceUnavailable("OAUTH_CONFIG_UNAVAILABLE", "oauth settings unavailable").WithCause(fmt.Errorf("get %s oauth settings: %w", provider, err))
	}
	if values[enabledKey] != "true" {
		return SocialOAuthConfig{}, ErrSocialOAuthDisabled
	}
	frontendURL := strings.TrimSpace(values[SettingKeyFrontendURL])
	if frontendURL == "" && s.cfg != nil {
		frontendURL = strings.TrimSpace(s.cfg.Server.FrontendURL)
	}
	cfg := SocialOAuthConfig{
		Provider:            provider,
		ClientID:            strings.TrimSpace(values[idKey]),
		ClientSecret:        strings.TrimSpace(values[secretKey]),
		RedirectURL:         SocialOAuthRedirectURL(frontendURL, provider),
		FrontendCallbackURL: provider.FrontendCallbackPath(),
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURL == "" {
		return SocialOAuthConfig{}, ErrSocialOAuthConfigInvalid
	}
	return cfg, nil
}

// SocialSignupPolicy is the registration policy that applies to a first OAuth sign-in.
type SocialSignupPolicy struct {
	RegistrationEnabled bool
	InvitationRequired  bool
	EmailSuffixes       []string
}

// GetSocialSignupPolicy reads the registration policy in one query. Unlike IsRegistrationEnabled /
// IsInvitationCodeEnabled (which default to "off" on read errors, and "off" means "no invitation
// needed"), this returns the error so that new sign-ups fail closed.
func (s *SettingService) GetSocialSignupPolicy(ctx context.Context) (SocialSignupPolicy, error) {
	if s == nil || s.settingRepo == nil {
		return SocialSignupPolicy{}, ErrServiceUnavailable
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyRegistrationEnabled,
		SettingKeyInvitationCodeEnabled,
		SettingKeyRegistrationEmailSuffixWhitelist,
	})
	if err != nil {
		return SocialSignupPolicy{}, fmt.Errorf("get signup policy: %w", err)
	}
	return SocialSignupPolicy{
		RegistrationEnabled: values[SettingKeyRegistrationEnabled] == "true",
		InvitationRequired:  values[SettingKeyInvitationCodeEnabled] == "true",
		EmailSuffixes:       ParseRegistrationEmailSuffixWhitelist(values[SettingKeyRegistrationEmailSuffixWhitelist]),
	}, nil
}

// IsTotpEnabledStrict is IsTotpEnabled that reports read failures instead of treating them as "off".
func (s *SettingService) IsTotpEnabledStrict(ctx context.Context) (bool, error) {
	if s == nil || s.settingRepo == nil {
		return false, ErrServiceUnavailable
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyTotpEnabled})
	if err != nil {
		return false, fmt.Errorf("get totp setting: %w", err)
	}
	return values[SettingKeyTotpEnabled] == "true", nil
}

// EffectiveFrontendURL returns the stored frontend URL, or the config-file fallback when it is empty.
func (s *SettingService) EffectiveFrontendURL(stored string) string {
	if v := strings.TrimSpace(stored); v != "" {
		return v
	}
	if s != nil && s.cfg != nil {
		return strings.TrimSpace(s.cfg.Server.FrontendURL)
	}
	return ""
}
