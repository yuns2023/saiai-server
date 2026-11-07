package service

import (
	"context"
	"errors"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type trustedUserContextKey struct{}
type accountUserIDContextKey struct{}
type accountAPIKeyIDContextKey struct{}

// maxCarpoolAPIKeys bounds the per-account key allowlist; each entry is
// validated with one lookup on save.
const maxCarpoolAPIKeys = 200

// CarpoolAPIKeyRef is one entry of a carpool account's key allowlist. Only ID
// is used for admission; UserID and Name are read back from the key row for
// display and hosted cost disclosure.
type CarpoolAPIKeyRef struct {
	ID     int64  `json:"id"`
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
}

func WithAccountUserID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, accountUserIDContextKey{}, id)
}

// WithAccountAPIKeyID carries the authenticated SAIAI Key ID for carpool key
// allowlists. Never populate it from request headers.
func WithAccountAPIKeyID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, accountAPIKeyIDContextKey{}, id)
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
	keyID, _ := ctx.Value(accountAPIKeyIDContextKey{}).(int64)
	if keyID <= 0 {
		return false
	}
	for _, allowed := range account.CarpoolAPIKeys {
		if keyID == allowed.ID {
			return true
		}
	}
	return false
}

// resolveCarpoolAPIKeys deduplicates the requested key IDs and rejects missing
// or deleted keys, so a bad entry fails the whole save instead of being dropped.
func (s *adminServiceImpl) resolveCarpoolAPIKeys(ctx context.Context, ids []int64) ([]CarpoolAPIKeyRef, error) {
	if len(ids) > maxCarpoolAPIKeys {
		return nil, infraerrors.BadRequest("INVALID_CARPOOL_API_KEYS", "carpool_api_key_ids must contain at most 200 positive key IDs")
	}
	refs := make([]CarpoolAPIKeyRef, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if id <= 0 {
			return nil, infraerrors.BadRequest("INVALID_CARPOOL_API_KEYS", "carpool_api_key_ids must contain at most 200 positive key IDs")
		}
		key, err := s.apiKeyRepo.GetByID(ctx, id)
		if errors.Is(err, ErrAPIKeyNotFound) {
			return nil, infraerrors.BadRequest("INVALID_CARPOOL_API_KEYS", "api key not found")
		}
		if err != nil {
			return nil, err
		}
		refs = append(refs, CarpoolAPIKeyRef{ID: key.ID, UserID: key.UserID, Name: key.Name})
	}
	return refs, nil
}
