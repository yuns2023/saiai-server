package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/authidentity"
	"github.com/Wei-Shaw/sub2api/ent/oauthregistrationsession"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const oauthRegistrationSessionTTL = 10 * time.Minute

var (
	ErrOAuthAccountLinkRequired = infraerrors.Conflict(
		"OAUTH_ACCOUNT_LINK_REQUIRED",
		"an account with this email already exists; sign in with the existing method",
	)
	ErrOAuthIdentityConflict = infraerrors.Conflict(
		"OAUTH_IDENTITY_CONFLICT",
		"this external identity is already linked",
	)
	ErrOAuthRegistrationSessionInvalid = infraerrors.Unauthorized(
		"OAUTH_REGISTRATION_SESSION_INVALID",
		"invalid or expired oauth registration session",
	)
)

type ExternalOAuthIdentity struct {
	Provider      string
	Subject       string
	VerifiedEmail string
	Username      string
}

type ExternalOAuthLoginResult struct {
	User              *User
	RegistrationToken string
}

func (r *ExternalOAuthLoginResult) RequiresInvitation() bool {
	return r != nil && r.RegistrationToken != ""
}

// ResolveExternalOAuth logs in a previously linked identity or creates a new
// account. Matching an unlinked local account by email is deliberately
// rejected: verified email is not authorization to take over an existing user.
func (s *AuthService) ResolveExternalOAuth(ctx context.Context, identity ExternalOAuthIdentity) (*ExternalOAuthLoginResult, error) {
	identity, err := normalizeExternalOAuthIdentity(identity)
	if err != nil {
		return nil, err
	}
	if s.entClient == nil || s.userRepo == nil || s.refreshTokenCache == nil || s.cfg == nil {
		return nil, ErrServiceUnavailable
	}

	linked, err := s.entClient.AuthIdentity.Query().
		Where(
			authidentity.ProviderEQ(identity.Provider),
			authidentity.SubjectEQ(identity.Subject),
		).
		Only(ctx)
	if err == nil {
		user, err := s.userRepo.GetByID(ctx, linked.UserID)
		if err != nil {
			return nil, ErrServiceUnavailable
		}
		return s.externalOAuthUserResult(user)
	}
	if !dbent.IsNotFound(err) {
		return nil, ErrServiceUnavailable
	}

	existing, err := s.entClient.User.Query().Where(dbuser.EmailEqualFold(identity.VerifiedEmail)).Exist(ctx)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	if existing {
		return nil, ErrOAuthAccountLinkRequired
	}
	if s.settingService == nil || !s.settingService.IsRegistrationEnabled(ctx) {
		return nil, ErrRegDisabled
	}
	if err := s.validateRegistrationEmailPolicy(ctx, identity.VerifiedEmail); err != nil {
		return nil, err
	}

	if s.settingService.IsInvitationCodeEnabled(ctx) {
		token, err := s.createOAuthRegistrationSession(ctx, identity)
		if err != nil {
			return nil, err
		}
		return &ExternalOAuthLoginResult{RegistrationToken: token}, nil
	}

	user, err := s.createExternalOAuthUser(ctx, identity)
	if err != nil {
		return nil, err
	}
	return s.externalOAuthUserResult(user)
}

// CompleteExternalOAuthRegistration consumes a one-time, server-side handoff.
// The raw token is never stored and the session contains no target user ID.
func (s *AuthService) CompleteExternalOAuthRegistration(ctx context.Context, provider, rawToken, invitationCode string) (*ExternalOAuthLoginResult, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !isSupportedLoginOAuthProvider(provider) || len(rawToken) != 64 || s.entClient == nil || s.userRepo == nil || s.refreshTokenCache == nil || s.cfg == nil {
		return nil, ErrOAuthRegistrationSessionInvalid
	}
	if s.settingService == nil || !s.settingService.IsRegistrationEnabled(ctx) || !s.settingService.IsInvitationCodeEnabled(ctx) {
		return nil, ErrOAuthRegistrationSessionInvalid
	}

	tokenHash := hashOAuthRegistrationToken(rawToken)
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)

	session, err := tx.OAuthRegistrationSession.Query().
		Where(
			oauthregistrationsession.TokenHashEQ(tokenHash),
			oauthregistrationsession.ProviderEQ(provider),
		).
		ForUpdate().
		Only(txCtx)
	if err != nil || !session.ExpiresAt.After(time.Now()) {
		return nil, ErrOAuthRegistrationSessionInvalid
	}

	identity := ExternalOAuthIdentity{
		Provider:      session.Provider,
		Subject:       session.Subject,
		VerifiedEmail: session.VerifiedEmail,
		Username:      session.Username,
	}
	if err := s.validateRegistrationEmailPolicy(ctx, identity.VerifiedEmail); err != nil {
		return nil, err
	}

	linked, err := tx.AuthIdentity.Query().Where(
		authidentity.ProviderEQ(identity.Provider),
		authidentity.SubjectEQ(identity.Subject),
	).Exist(txCtx)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	if linked {
		return nil, ErrOAuthIdentityConflict
	}
	existing, err := tx.User.Query().Where(dbuser.EmailEqualFold(identity.VerifiedEmail)).Exist(txCtx)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	if existing {
		return nil, ErrOAuthAccountLinkRequired
	}

	if s.redeemRepo == nil || strings.TrimSpace(invitationCode) == "" {
		return nil, ErrInvitationCodeInvalid
	}
	redeemCode, err := s.redeemRepo.GetByCode(txCtx, strings.TrimSpace(invitationCode))
	if err != nil || redeemCode.Type != RedeemTypeInvitation || redeemCode.Status != StatusUnused {
		return nil, ErrInvitationCodeInvalid
	}

	user, err := s.buildExternalOAuthUser(txCtx, identity)
	if err != nil {
		return nil, err
	}
	if err := s.userRepo.Create(txCtx, user); err != nil {
		if errors.Is(err, ErrEmailExists) {
			return nil, ErrOAuthAccountLinkRequired
		}
		return nil, ErrServiceUnavailable
	}
	if _, err := tx.AuthIdentity.Create().
		SetUserID(user.ID).
		SetProvider(identity.Provider).
		SetSubject(identity.Subject).
		SetVerifiedEmail(identity.VerifiedEmail).
		SetVerifiedAt(time.Now()).
		Save(txCtx); err != nil {
		return nil, ErrOAuthIdentityConflict
	}
	if err := s.redeemRepo.Use(txCtx, redeemCode.ID, user.ID); err != nil {
		return nil, ErrInvitationCodeInvalid
	}
	if err := tx.OAuthRegistrationSession.DeleteOne(session).Exec(txCtx); err != nil {
		return nil, ErrServiceUnavailable
	}
	if err := tx.Commit(); err != nil {
		return nil, ErrServiceUnavailable
	}

	s.assignDefaultSubscriptions(ctx, user.ID)
	return s.externalOAuthUserResult(user)
}

func (s *AuthService) createOAuthRegistrationSession(ctx context.Context, identity ExternalOAuthIdentity) (string, error) {
	rawToken, err := randomHexString(32)
	if err != nil {
		return "", ErrServiceUnavailable
	}
	now := time.Now()
	// Cleanup is opportunistic and failure is harmless; the queried token still
	// has an explicit expiry check during consumption.
	_, _ = s.entClient.OAuthRegistrationSession.Delete().
		Where(oauthregistrationsession.ExpiresAtLT(now)).
		Exec(ctx)
	_, err = s.entClient.OAuthRegistrationSession.Create().
		SetTokenHash(hashOAuthRegistrationToken(rawToken)).
		SetProvider(identity.Provider).
		SetSubject(identity.Subject).
		SetVerifiedEmail(identity.VerifiedEmail).
		SetUsername(identity.Username).
		SetExpiresAt(now.Add(oauthRegistrationSessionTTL)).
		Save(ctx)
	if err != nil {
		return "", ErrServiceUnavailable
	}
	return rawToken, nil
}

func (s *AuthService) createExternalOAuthUser(ctx context.Context, identity ExternalOAuthIdentity) (*User, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)

	user, err := s.buildExternalOAuthUser(txCtx, identity)
	if err != nil {
		return nil, err
	}
	if err := s.userRepo.Create(txCtx, user); err != nil {
		if errors.Is(err, ErrEmailExists) {
			return nil, ErrOAuthAccountLinkRequired
		}
		return nil, ErrServiceUnavailable
	}
	if _, err := tx.AuthIdentity.Create().
		SetUserID(user.ID).
		SetProvider(identity.Provider).
		SetSubject(identity.Subject).
		SetVerifiedEmail(identity.VerifiedEmail).
		SetVerifiedAt(time.Now()).
		Save(txCtx); err != nil {
		return nil, ErrOAuthIdentityConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, ErrServiceUnavailable
	}
	s.assignDefaultSubscriptions(ctx, user.ID)
	return user, nil
}

func (s *AuthService) buildExternalOAuthUser(ctx context.Context, identity ExternalOAuthIdentity) (*User, error) {
	randomPassword, err := randomHexString(32)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	hashedPassword, err := s.HashPassword(randomPassword)
	if err != nil {
		return nil, fmt.Errorf("hash oauth password: %w", err)
	}
	defaultBalance := s.cfg.Default.UserBalance
	defaultConcurrency := s.cfg.Default.UserConcurrency
	if s.settingService != nil {
		defaultBalance = s.settingService.GetDefaultBalance(ctx)
		defaultConcurrency = s.settingService.GetDefaultConcurrency(ctx)
	}
	return &User{
		Email:        identity.VerifiedEmail,
		Username:     identity.Username,
		PasswordHash: hashedPassword,
		Role:         RoleUser,
		Balance:      defaultBalance,
		Concurrency:  defaultConcurrency,
		Status:       StatusActive,
	}, nil
}

func (s *AuthService) externalOAuthUserResult(user *User) (*ExternalOAuthLoginResult, error) {
	if user == nil || !user.IsActive() {
		return nil, ErrUserNotActive
	}
	return &ExternalOAuthLoginResult{User: user}, nil
}

// LinkExternalOAuthIdentity explicitly binds a verified provider subject to an
// already authenticated local user. The handler must perform fresh local
// reauthentication before calling this method; email equality alone is never
// sufficient authorization.
func (s *AuthService) LinkExternalOAuthIdentity(ctx context.Context, userID int64, identity ExternalOAuthIdentity) error {
	identity, err := normalizeExternalOAuthIdentity(identity)
	if err != nil {
		return err
	}
	if userID <= 0 || s.entClient == nil || s.userRepo == nil {
		return ErrServiceUnavailable
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil || user == nil {
		return ErrServiceUnavailable
	}
	if !user.IsActive() {
		return ErrUserNotActive
	}

	linked, err := s.entClient.AuthIdentity.Query().Where(
		authidentity.ProviderEQ(identity.Provider),
		authidentity.SubjectEQ(identity.Subject),
	).Only(ctx)
	if err == nil {
		if linked.UserID == userID {
			return nil
		}
		return ErrOAuthIdentityConflict
	}
	if !dbent.IsNotFound(err) {
		return ErrServiceUnavailable
	}

	_, err = s.entClient.AuthIdentity.Create().
		SetUserID(userID).
		SetProvider(identity.Provider).
		SetSubject(identity.Subject).
		SetVerifiedEmail(identity.VerifiedEmail).
		SetVerifiedAt(time.Now()).
		Save(ctx)
	if err != nil {
		if dbent.IsConstraintError(err) {
			return ErrOAuthIdentityConflict
		}
		return ErrServiceUnavailable
	}
	return nil
}

func (s *AuthService) ListExternalOAuthProviders(ctx context.Context, userID int64) ([]string, error) {
	if userID <= 0 || s.entClient == nil {
		return nil, ErrServiceUnavailable
	}
	providers, err := s.entClient.AuthIdentity.Query().
		Where(authidentity.UserIDEQ(userID)).
		Select(authidentity.FieldProvider).
		Strings(ctx)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	return providers, nil
}

func normalizeExternalOAuthIdentity(identity ExternalOAuthIdentity) (ExternalOAuthIdentity, error) {
	identity.Provider = strings.ToLower(strings.TrimSpace(identity.Provider))
	identity.Subject = strings.TrimSpace(identity.Subject)
	identity.VerifiedEmail = strings.ToLower(strings.TrimSpace(identity.VerifiedEmail))
	identity.Username = strings.TrimSpace(identity.Username)
	if !isSupportedLoginOAuthProvider(identity.Provider) || identity.Subject == "" || len(identity.Subject) > 255 {
		return ExternalOAuthIdentity{}, infraerrors.BadRequest("OAUTH_IDENTITY_INVALID", "invalid oauth identity")
	}
	if identity.VerifiedEmail == "" || len(identity.VerifiedEmail) > 255 || isReservedEmail(identity.VerifiedEmail) {
		return ExternalOAuthIdentity{}, infraerrors.BadRequest("OAUTH_EMAIL_INVALID", "oauth provider did not return a valid verified email")
	}
	parsed, err := mail.ParseAddress(identity.VerifiedEmail)
	if err != nil || !strings.EqualFold(parsed.Address, identity.VerifiedEmail) {
		return ExternalOAuthIdentity{}, infraerrors.BadRequest("OAUTH_EMAIL_INVALID", "oauth provider did not return a valid verified email")
	}
	if len([]rune(identity.Username)) > 100 {
		identity.Username = string([]rune(identity.Username)[:100])
	}
	return identity, nil
}

func isSupportedLoginOAuthProvider(provider string) bool {
	return provider == "github" || provider == "google"
}

func hashOAuthRegistrationToken(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}
