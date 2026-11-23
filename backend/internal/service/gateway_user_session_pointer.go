package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// A user session pointer remembers which account last served one Claude
// conversation of one authenticated user, independent of the group. Group
// sticky bindings stay authoritative; the pointer is read only when the current
// group has no binding (e.g. smart switching moved the Key from a subscription
// group to a pay-as-you-go group) so the conversation can stay on an account
// that is also in the new group and keep its upstream prompt cache.
//
// The pointer is only a candidate: every sticky check (group membership,
// platform, model, schedulability, quota, window cost, RPM, market admission)
// still applies, and a rejected pointer never deletes any binding.

const maxUserSessionPointerSessionIDLen = 256

type userSessionPointerContextKey struct{}

type userSessionPointer struct {
	userID int64
	digest string
}

// userSessionPointerCache is optional so cache stubs without it keep working.
type userSessionPointerCache interface {
	GetUserSessionAccountID(ctx context.Context, userID int64, digest string) (int64, error)
	SetUserSessionAccountID(ctx context.Context, userID int64, digest string, accountID int64, ttl time.Duration) error
}

// WithUserSessionPointer enables the pointer for an Anthropic request whose
// metadata carries a session id. userID must come from authentication.
func WithUserSessionPointer(ctx context.Context, userID int64, platform, metadataUserID string) context.Context {
	if ctx == nil || userID <= 0 || platform != PlatformAnthropic {
		return ctx
	}
	parsed := ParseMetadataUserID(strings.TrimSpace(metadataUserID))
	if parsed == nil {
		return ctx
	}
	sessionID := strings.TrimSpace(parsed.SessionID)
	if sessionID == "" || len(sessionID) > maxUserSessionPointerSessionIDLen {
		return ctx
	}
	// The device is part of the identity so another device of the same user
	// never inherits this conversation's account (shared/carpool semantics).
	device := strings.TrimSpace(oauthSlotSource(metadataUserID))
	sum := sha256.Sum256([]byte(platform + "\x00" + device + "\x00" + sessionID))
	return context.WithValue(ctx, userSessionPointerContextKey{}, userSessionPointer{userID: userID, digest: hex.EncodeToString(sum[:])})
}

// isStickyCacheMiss reports whether a sticky lookup error is a plain miss.
func isStickyCacheMiss(err error) bool {
	return err == nil || errors.Is(err, ErrGatewaySessionCacheMiss) || errors.Is(err, redis.Nil)
}

func userSessionPointerFromContext(ctx context.Context) (userSessionPointer, bool) {
	if ctx == nil {
		return userSessionPointer{}, false
	}
	p, ok := ctx.Value(userSessionPointerContextKey{}).(userSessionPointer)
	return p, ok && p.userID > 0 && p.digest != ""
}

// userSessionPointerAccountID returns 0 on a miss, a disabled pointer or any
// cache error: the pointer is advisory and never fails a request.
func (s *GatewayService) userSessionPointerAccountID(ctx context.Context) int64 {
	if s == nil || s.cache == nil {
		return 0
	}
	p, ok := userSessionPointerFromContext(ctx)
	if !ok {
		return 0
	}
	cache, ok := s.cache.(userSessionPointerCache)
	if !ok {
		return 0
	}
	accountID, err := cache.GetUserSessionAccountID(ctx, p.userID, p.digest)
	if err != nil {
		if !errors.Is(err, ErrGatewaySessionCacheMiss) {
			slog.Debug("user_session_pointer_read_failed", "user_id", p.userID, "error", err)
		}
		return 0
	}
	if accountID <= 0 {
		return 0
	}
	return accountID
}

// RecordUserSessionPointer stores the account that just served the request
// successfully. Called after a successful forward only.
func (s *GatewayService) RecordUserSessionPointer(ctx context.Context, account *Account) {
	if s == nil || s.cache == nil || account == nil || account.ID <= 0 {
		return
	}
	p, ok := userSessionPointerFromContext(ctx)
	if !ok {
		return
	}
	cache, ok := s.cache.(userSessionPointerCache)
	if !ok {
		return
	}
	if err := cache.SetUserSessionAccountID(ctx, p.userID, p.digest, account.ID, s.stickySessionTTLForAccount(ctx, account)); err != nil {
		slog.Debug("user_session_pointer_write_failed", "user_id", p.userID, "account_id", account.ID, "error", err)
	}
}

// touchStickyHit keeps an existing group binding alive. A pointer hit has no
// binding in this group yet, so it creates one through the normal setter
// (pending for success-only accounts, nothing during ownership selection where
// ClaimStickyOwnership writes it).
func (s *GatewayService) touchStickyHit(ctx context.Context, groupID *int64, sessionHash string, account *Account, fromPointer bool) {
	if !fromPointer {
		s.refreshStickySessionTTLForAccount(ctx, groupID, sessionHash, account)
		return
	}
	if account == nil {
		return
	}
	slog.Info("user_session_pointer_hit",
		"group_id", derefGroupID(groupID),
		"session", shortSessionHash(sessionHash),
		"account_id", account.ID)
	s.setStickySessionAccount(ctx, groupID, sessionHash, account.ID, 0)
}

// clearRejectedSticky deletes the current group's binding for a rejected
// sticky account, but never for a pointer candidate: the group had no binding
// when the pointer was read, and a binding written since then by another
// request must survive.
func (s *GatewayService) clearRejectedSticky(ctx context.Context, groupID *int64, sessionHash string, fromPointer bool) {
	if fromPointer || stickyOwnershipSelection(ctx) || s.cache == nil {
		return
	}
	_ = s.cache.DeleteSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
}
