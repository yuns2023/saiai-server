package service

import "context"

type trustedUserContextKey struct{}
type accountUserIDContextKey struct{}

func WithAccountUserID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, accountUserIDContextKey{}, id)
}

// WithTrustedUser carries the authenticated SAIAI Key owner's administrator-managed
// trust flag. Never populate this value from provider metadata or request headers.
func WithTrustedUser(ctx context.Context, trusted bool) context.Context {
	return context.WithValue(ctx, trustedUserContextKey{}, trusted)
}

// IsTrustedUser defaults to false for missing identity and older auth snapshots.
func IsTrustedUser(ctx context.Context) bool {
	trusted, _ := ctx.Value(trustedUserContextKey{}).(bool)
	return trusted
}

// IsTrustedUserOnly applies only to Anthropic OAuth/setup-token carpool accounts.
// It is orthogonal to device admission, quotas, priority, and request rewriting.
func (a *Account) IsTrustedUserOnly() bool {
	if a == nil || !a.IsAnthropicOAuthOrSetupToken() || a.GetClaudeOAuthMode() != ClaudeOAuthModeCarpool {
		return false
	}
	enabled, _ := a.Extra["claude_oauth_carpool_trusted_only"].(bool)
	return enabled
}

func accountAllowsUser(ctx context.Context, account *Account) bool {
	if account == nil || (account.IsTrustedUserOnly() && !IsTrustedUser(ctx)) {
		return false
	}
	if !account.IsAnthropicOAuthOrSetupToken() || account.GetClaudeOAuthMode() != ClaudeOAuthModeCarpool {
		return true
	}
	enabled, _ := account.Extra["claude_oauth_carpool_users_only"].(bool)
	if !enabled {
		return true
	}
	id, _ := ctx.Value(accountUserIDContextKey{}).(int64)
	if id <= 0 {
		return false
	}
	for _, allowed := range account.CarpoolUserIDs {
		if id == allowed {
			return true
		}
	}
	return false
}
