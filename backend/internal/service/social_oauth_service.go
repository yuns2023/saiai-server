package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// SocialOAuthPendingTTL bounds how long a pending (invitation-required) OAuth sign-up stays usable.
const SocialOAuthPendingTTL = 10 * time.Minute

// socialOAuthMaxSubjectLen keeps "<provider>-<subject>" within the 64-octet email local part.
const socialOAuthMaxSubjectLen = 64 - len("google-")

// socialOAuthPendingTokenBytes is the entropy of a pending-registration handle (hex encoded => 64 chars).
const socialOAuthPendingTokenBytes = 32

var (
	ErrSocialOAuthPendingInvalid   = infraerrors.BadRequest("OAUTH_PENDING_INVALID", "oauth registration session is invalid or expired, please sign in again")
	ErrSocialOAuthEmailNotVerified = infraerrors.Forbidden("OAUTH_EMAIL_NOT_VERIFIED", "the provider account has no verified email address")
	ErrSocialOAuthIdentityInvalid  = infraerrors.BadRequest("OAUTH_IDENTITY_INVALID", "the provider returned an invalid account id")
	ErrSocialOAuthAccountExists    = infraerrors.Conflict("OAUTH_ACCOUNT_EXISTS", "this account is already registered, please sign in again")
)

// SocialOAuthIdentity is what a provider callback proved about the signed-in user.
// VerifiedEmail is informational (registration email-suffix policy only); it is never used
// to find, merge or create a local account.
type SocialOAuthIdentity struct {
	Provider      SocialOAuthProvider
	Subject       string
	VerifiedEmail string
	Username      string
}

// SocialOAuthPendingSignup is the server-side state of an invitation-required first sign-in.
type SocialOAuthPendingSignup struct {
	Provider      SocialOAuthProvider `json:"provider"`
	Subject       string              `json:"subject"`
	Username      string              `json:"username"`
	VerifiedEmail string              `json:"verified_email"`
}

// SocialOAuthPendingStore keeps one-time pending sign-ups (Redis in production).
// Peek and Consume return (nil, nil) when the token is unknown or expired.
// Consume must be atomic: at most one caller may ever receive a given entry.
type SocialOAuthPendingStore interface {
	Save(ctx context.Context, token string, pending *SocialOAuthPendingSignup, ttl time.Duration) error
	Peek(ctx context.Context, token string) (*SocialOAuthPendingSignup, error)
	Consume(ctx context.Context, token string) (*SocialOAuthPendingSignup, error)
}

// SocialOAuthResult is the outcome of a sign-in. Exactly one of TokenPair, Requires2FA or
// PendingToken is set.
type SocialOAuthResult struct {
	User         *User
	TokenPair    *TokenPair
	Requires2FA  bool
	TempToken    string
	PendingToken string
}

// SocialOAuthService implements Google / GitHub sign-in on top of the existing user model:
// the local account is the one whose email is the provider's synthetic address
// (e.g. google-<sub>@google-connect.invalid). No schema change is involved.
type SocialOAuthService struct {
	auth     *AuthService
	settings *SettingService
	totp     *TotpService
	pending  SocialOAuthPendingStore
	// runInTx runs fn inside a database transaction; replaced in tests.
	runInTx func(ctx context.Context, fn func(ctx context.Context) error) error
}

// NewSocialOAuthService creates the Google / GitHub sign-in service.
func NewSocialOAuthService(auth *AuthService, settings *SettingService, totp *TotpService, pending SocialOAuthPendingStore) *SocialOAuthService {
	s := &SocialOAuthService{auth: auth, settings: settings, totp: totp, pending: pending}
	s.runInTx = s.entTx
	return s
}

// Config returns the effective provider configuration (error if disabled or incomplete).
func (s *SocialOAuthService) Config(ctx context.Context, provider SocialOAuthProvider) (SocialOAuthConfig, error) {
	if s == nil || s.settings == nil {
		return SocialOAuthConfig{}, ErrServiceUnavailable
	}
	return s.settings.GetSocialOAuthConfig(ctx, provider)
}

// SocialOAuthSyntheticEmail returns the local account email bound to provider+subject.
func SocialOAuthSyntheticEmail(provider SocialOAuthProvider, subject string) string {
	domain := provider.SyntheticEmailDomain()
	subject = strings.TrimSpace(subject)
	if domain == "" || subject == "" {
		return ""
	}
	return string(provider) + "-" + subject + domain
}

// IsSafeSocialOAuthSubject validates the provider's stable account id.
// Google: OIDC "sub" ([A-Za-z0-9_-], bounded length). GitHub: positive decimal numeric id.
func IsSafeSocialOAuthSubject(provider SocialOAuthProvider, subject string) bool {
	if subject == "" || subject != strings.TrimSpace(subject) || len(subject) > socialOAuthMaxSubjectLen {
		return false
	}
	switch provider {
	case SocialOAuthProviderGoogle:
		for _, r := range subject {
			switch {
			case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_', r == '-':
			default:
				return false
			}
		}
		return true
	case SocialOAuthProviderGitHub:
		if len(subject) > 19 || subject[0] == '0' {
			return false
		}
		for _, r := range subject {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func normalizeSocialOAuthIdentity(identity SocialOAuthIdentity) (SocialOAuthIdentity, error) {
	if _, ok := ParseSocialOAuthProvider(string(identity.Provider)); !ok {
		return SocialOAuthIdentity{}, ErrSocialOAuthIdentityInvalid
	}
	if !IsSafeSocialOAuthSubject(identity.Provider, identity.Subject) {
		return SocialOAuthIdentity{}, ErrSocialOAuthIdentityInvalid
	}
	email := strings.TrimSpace(identity.VerifiedEmail)
	if email == "" || len(email) > 255 || isReservedEmail(email) {
		return SocialOAuthIdentity{}, ErrSocialOAuthEmailNotVerified
	}
	if parsed, err := mail.ParseAddress(email); err != nil || parsed.Address != email {
		return SocialOAuthIdentity{}, ErrSocialOAuthEmailNotVerified
	}
	identity.VerifiedEmail = email
	identity.Username = strings.TrimSpace(identity.Username)
	if len([]rune(identity.Username)) > 100 {
		identity.Username = string([]rune(identity.Username)[:100])
	}
	return identity, nil
}

// SignIn logs in the account bound to the identity, or starts/creates a registration.
func (s *SocialOAuthService) SignIn(ctx context.Context, identity SocialOAuthIdentity) (*SocialOAuthResult, error) {
	identity, err := normalizeSocialOAuthIdentity(identity)
	if err != nil {
		return nil, err
	}
	if _, err := s.Config(ctx, identity.Provider); err != nil {
		return nil, err
	}
	if s.auth == nil || s.auth.userRepo == nil {
		return nil, ErrServiceUnavailable
	}

	email := SocialOAuthSyntheticEmail(identity.Provider, identity.Subject)
	user, err := s.auth.userRepo.GetByEmail(ctx, email)
	if err == nil {
		return s.loginExisting(ctx, user)
	}
	if !errors.Is(err, ErrUserNotFound) {
		logger.LegacyPrintf("service.social_oauth", "[SocialOAuth] lookup user failed: provider=%s err=%v", identity.Provider, err)
		return nil, ErrServiceUnavailable
	}

	// First sign-in = registration.
	policy, err := s.signupPolicy(ctx, identity)
	if err != nil {
		return nil, err
	}
	if policy.InvitationRequired {
		token, err := s.savePending(ctx, identity)
		if err != nil {
			return nil, err
		}
		return &SocialOAuthResult{PendingToken: token}, nil
	}

	user, err = s.createUser(ctx, identity, nil)
	if errors.Is(err, ErrEmailExists) {
		// Concurrent first sign-in for the same subject: treat as an existing account.
		existing, getErr := s.auth.userRepo.GetByEmail(ctx, email)
		if getErr != nil {
			return nil, ErrServiceUnavailable
		}
		return s.loginExisting(ctx, existing)
	}
	if err != nil {
		return nil, err
	}
	return s.issue(ctx, user)
}

// CompleteRegistration finishes an invitation-required first sign-in. The pending session is
// single use: it is consumed atomically before the account is created, so a replay fails.
// A wrong or missing invitation code is rejected before consumption so the user can retry.
func (s *SocialOAuthService) CompleteRegistration(ctx context.Context, provider SocialOAuthProvider, token, invitationCode string) (*SocialOAuthResult, error) {
	if !isWellFormedPendingToken(token) || s.pending == nil {
		return nil, ErrSocialOAuthPendingInvalid
	}
	peeked, err := s.pending.Peek(ctx, token)
	if err != nil {
		logger.LegacyPrintf("service.social_oauth", "[SocialOAuth] peek pending failed: %v", err)
		return nil, ErrServiceUnavailable
	}
	if peeked == nil || peeked.Provider != provider {
		return nil, ErrSocialOAuthPendingInvalid
	}
	if _, err := s.Config(ctx, provider); err != nil {
		return nil, err
	}
	identity, err := normalizeSocialOAuthIdentity(SocialOAuthIdentity{
		Provider:      peeked.Provider,
		Subject:       peeked.Subject,
		VerifiedEmail: peeked.VerifiedEmail,
		Username:      peeked.Username,
	})
	if err != nil {
		return nil, ErrSocialOAuthPendingInvalid
	}
	if s.auth == nil || s.auth.userRepo == nil {
		return nil, ErrServiceUnavailable
	}

	policy, err := s.signupPolicy(ctx, identity)
	if err != nil {
		return nil, err
	}
	var invitation *RedeemCode
	if policy.InvitationRequired {
		invitation, err = s.lookupInvitation(ctx, invitationCode)
		if err != nil {
			return nil, err
		}
	}

	consumed, err := s.pending.Consume(ctx, token)
	if err != nil {
		logger.LegacyPrintf("service.social_oauth", "[SocialOAuth] consume pending failed: %v", err)
		return nil, ErrServiceUnavailable
	}
	if consumed == nil || consumed.Provider != peeked.Provider || consumed.Subject != peeked.Subject {
		return nil, ErrSocialOAuthPendingInvalid
	}

	// Re-check after consumption so a provider/registration change that landed while this
	// request was in flight is honoured (the session is gone either way; the user restarts).
	if _, err := s.Config(ctx, provider); err != nil {
		return nil, err
	}
	recheck, err := s.signupPolicy(ctx, identity)
	if err != nil {
		return nil, err
	}
	if recheck.InvitationRequired && invitation == nil {
		return nil, ErrInvitationCodeInvalid
	}

	email := SocialOAuthSyntheticEmail(identity.Provider, identity.Subject)
	if _, err := s.auth.userRepo.GetByEmail(ctx, email); err == nil {
		return nil, ErrSocialOAuthAccountExists
	} else if !errors.Is(err, ErrUserNotFound) {
		return nil, ErrServiceUnavailable
	}

	user, err := s.createUser(ctx, identity, invitation)
	if errors.Is(err, ErrEmailExists) {
		return nil, ErrSocialOAuthAccountExists
	}
	if err != nil {
		return nil, err
	}
	return s.issue(ctx, user)
}

func (s *SocialOAuthService) loginExisting(ctx context.Context, user *User) (*SocialOAuthResult, error) {
	if user == nil || !user.IsActive() {
		return nil, ErrUserNotActive
	}
	if user.TotpEnabled {
		// Same rule as password login (TOTP applies while the site-wide switch is on), but a
		// settings read failure refuses the login instead of skipping the second factor.
		on, err := s.settings.IsTotpEnabledStrict(ctx)
		if err != nil {
			logger.LegacyPrintf("service.social_oauth", "[SocialOAuth] read totp setting failed: %v", err)
			return nil, ErrServiceUnavailable
		}
		if on {
			if s.totp == nil {
				return nil, ErrServiceUnavailable
			}
			tempToken, err := s.totp.CreateLoginSession(ctx, user.ID, user.Email)
			if err != nil {
				logger.LegacyPrintf("service.social_oauth", "[SocialOAuth] create 2fa session failed: user_id=%d err=%v", user.ID, err)
				return nil, ErrServiceUnavailable
			}
			return &SocialOAuthResult{User: user, Requires2FA: true, TempToken: tempToken}, nil
		}
	}
	return s.issue(ctx, user)
}

func (s *SocialOAuthService) issue(ctx context.Context, user *User) (*SocialOAuthResult, error) {
	pair, err := s.auth.GenerateTokenPair(ctx, user, "")
	if err != nil {
		logger.LegacyPrintf("service.social_oauth", "[SocialOAuth] generate token pair failed: user_id=%d err=%v", user.ID, err)
		return nil, ErrServiceUnavailable
	}
	return &SocialOAuthResult{User: user, TokenPair: pair}, nil
}

func (s *SocialOAuthService) signupPolicy(ctx context.Context, identity SocialOAuthIdentity) (SocialSignupPolicy, error) {
	if s.settings == nil {
		return SocialSignupPolicy{}, ErrRegDisabled
	}
	policy, err := s.settings.GetSocialSignupPolicy(ctx)
	if err != nil {
		logger.LegacyPrintf("service.social_oauth", "[SocialOAuth] read signup policy failed: %v", err)
		return SocialSignupPolicy{}, ErrServiceUnavailable
	}
	if !policy.RegistrationEnabled {
		return SocialSignupPolicy{}, ErrRegDisabled
	}
	// The suffix whitelist is checked against the provider-verified address; otherwise
	// OAuth sign-up would bypass it.
	if !IsRegistrationEmailSuffixAllowed(identity.VerifiedEmail, policy.EmailSuffixes) {
		return SocialSignupPolicy{}, buildEmailSuffixNotAllowedError(policy.EmailSuffixes)
	}
	return policy, nil
}

func (s *SocialOAuthService) lookupInvitation(ctx context.Context, code string) (*RedeemCode, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, ErrInvitationCodeRequired
	}
	if s.auth.redeemRepo == nil {
		return nil, ErrServiceUnavailable
	}
	redeemCode, err := s.auth.redeemRepo.GetByCode(ctx, code)
	if err != nil || redeemCode == nil {
		return nil, ErrInvitationCodeInvalid
	}
	if redeemCode.Type != RedeemTypeInvitation || redeemCode.Status != StatusUnused {
		return nil, ErrInvitationCodeInvalid
	}
	return redeemCode, nil
}

func (s *SocialOAuthService) savePending(ctx context.Context, identity SocialOAuthIdentity) (string, error) {
	if s.pending == nil {
		return "", ErrServiceUnavailable
	}
	token, err := randomHexString(socialOAuthPendingTokenBytes)
	if err != nil {
		return "", ErrServiceUnavailable
	}
	err = s.pending.Save(ctx, token, &SocialOAuthPendingSignup{
		Provider:      identity.Provider,
		Subject:       identity.Subject,
		Username:      identity.Username,
		VerifiedEmail: identity.VerifiedEmail,
	}, SocialOAuthPendingTTL)
	if err != nil {
		logger.LegacyPrintf("service.social_oauth", "[SocialOAuth] save pending failed: %v", err)
		return "", ErrServiceUnavailable
	}
	return token, nil
}

// createUser creates the synthetic-email account. With an invitation, account creation and
// invitation consumption share one transaction: if the code cannot be claimed, no user remains.
func (s *SocialOAuthService) createUser(ctx context.Context, identity SocialOAuthIdentity, invitation *RedeemCode) (*User, error) {
	randomPassword, err := randomHexString(32)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	hashedPassword, err := s.auth.HashPassword(randomPassword)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	defaultBalance := s.auth.cfg.Default.UserBalance
	defaultConcurrency := s.auth.cfg.Default.UserConcurrency
	if s.settings != nil {
		defaultBalance = s.settings.GetDefaultBalance(ctx)
		defaultConcurrency = s.settings.GetDefaultConcurrency(ctx)
	}
	user := &User{
		Email:        SocialOAuthSyntheticEmail(identity.Provider, identity.Subject),
		Username:     identity.Username,
		PasswordHash: hashedPassword,
		Role:         RoleUser,
		Balance:      defaultBalance,
		Concurrency:  defaultConcurrency,
		Status:       StatusActive,
	}

	create := func(ctx context.Context) error {
		if err := s.auth.userRepo.Create(ctx, user); err != nil {
			if errors.Is(err, ErrEmailExists) {
				return ErrEmailExists
			}
			logger.LegacyPrintf("service.social_oauth", "[SocialOAuth] create user failed: %v", err)
			return ErrServiceUnavailable
		}
		return nil
	}

	if invitation == nil {
		if err := create(ctx); err != nil {
			return nil, err
		}
	} else {
		if s.auth.redeemRepo == nil {
			return nil, ErrServiceUnavailable
		}
		err := s.runInTx(ctx, func(txCtx context.Context) error {
			if err := create(txCtx); err != nil {
				return err
			}
			if err := s.auth.redeemRepo.Use(txCtx, invitation.ID, user.ID); err != nil {
				if errors.Is(err, ErrRedeemCodeUsed) || errors.Is(err, ErrRedeemCodeNotFound) {
					return ErrInvitationCodeInvalid
				}
				logger.LegacyPrintf("service.social_oauth", "[SocialOAuth] claim invitation failed: user_id=%d err=%v", user.ID, err)
				return ErrServiceUnavailable
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	s.auth.assignDefaultSubscriptions(ctx, user.ID)
	return user, nil
}

func (s *SocialOAuthService) entTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.auth == nil || s.auth.entClient == nil {
		logger.LegacyPrintf("service.social_oauth", "%s", "[SocialOAuth] invitation sign-up requires a database transaction")
		return ErrServiceUnavailable
	}
	tx, err := s.auth.entClient.Tx(ctx)
	if err != nil {
		logger.LegacyPrintf("service.social_oauth", "[SocialOAuth] begin tx failed: %v", err)
		return ErrServiceUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(dbent.NewTxContext(ctx, tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		logger.LegacyPrintf("service.social_oauth", "[SocialOAuth] commit tx failed: %v", err)
		return ErrServiceUnavailable
	}
	return nil
}

func isWellFormedPendingToken(token string) bool {
	if len(token) != socialOAuthPendingTokenBytes*2 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil && strings.ToLower(token) == token
}
