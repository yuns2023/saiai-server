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
	// ReserveAttempt atomically takes one invitation-check attempt from the pending entry's
	// budget before the code is looked up. It returns the attempt number and ok=false when the
	// entry is gone or the budget is spent (the entry is then deleted). The counter is not
	// reset on exhaustion, so in-flight requests cannot start a fresh budget.
	ReserveAttempt(ctx context.Context, token string, maxAttempts int, ttl time.Duration) (attempt int, ok bool, err error)
}

// socialOAuthMaxInvitationFailures is the wrong-invitation-code budget per pending sign-up.
const socialOAuthMaxInvitationFailures = 5

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
	handoff  SocialOAuthHandoffStore
	// runInTx runs fn inside a database transaction; replaced in tests.
	runInTx func(ctx context.Context, fn func(ctx context.Context) error) error
}

// NewSocialOAuthService creates the Google / GitHub sign-in service.
func NewSocialOAuthService(auth *AuthService, settings *SettingService, totp *TotpService, pending SocialOAuthPendingStore, handoff SocialOAuthHandoffStore) *SocialOAuthService {
	s := &SocialOAuthService{auth: auth, settings: settings, totp: totp, pending: pending, handoff: handoff}
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

	user, err = s.createUser(ctx, identity, policy, nil)
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
		if strings.TrimSpace(invitationCode) == "" {
			return nil, ErrInvitationCodeRequired
		}
		// At most socialOAuthMaxInvitationFailures code checks per pending sign-up, enforced
		// atomically before the lookup so concurrent guesses share the same budget.
		attempt, ok, rerr := s.pending.ReserveAttempt(ctx, token, socialOAuthMaxInvitationFailures, SocialOAuthPendingTTL)
		if rerr != nil {
			logger.LegacyPrintf("service.social_oauth", "[SocialOAuth] reserve attempt failed: %v", rerr)
			return nil, ErrServiceUnavailable
		}
		if !ok {
			return nil, ErrSocialOAuthPendingInvalid
		}
		invitation, err = s.lookupInvitation(ctx, invitationCode)
		if errors.Is(err, ErrInvitationCodeInvalid) && attempt >= socialOAuthMaxInvitationFailures {
			// Last attempt spent on a wrong code: invalidate now.
			_, _ = s.pending.Consume(ctx, token)
			return nil, ErrSocialOAuthPendingInvalid
		}
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
		// The session is already consumed, so this is not a retryable code error.
		return nil, ErrSocialOAuthPendingInvalid
	}

	email := SocialOAuthSyntheticEmail(identity.Provider, identity.Subject)
	if _, err := s.auth.userRepo.GetByEmail(ctx, email); err == nil {
		return nil, ErrSocialOAuthAccountExists
	} else if !errors.Is(err, ErrUserNotFound) {
		return nil, ErrServiceUnavailable
	}

	user, err := s.createUser(ctx, identity, recheck, invitation)
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
	return s.auth.lookupInvitationCode(ctx, code)
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
func (s *SocialOAuthService) createUser(ctx context.Context, identity SocialOAuthIdentity, policy SocialSignupPolicy, invitation *RedeemCode) (*User, error) {
	randomPassword, err := randomHexString(32)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	hashedPassword, err := s.auth.HashPassword(randomPassword)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	defaultBalance := policy.DefaultBalance
	defaultConcurrency := policy.DefaultConcurrency
	user := &User{
		Email:        SocialOAuthSyntheticEmail(identity.Provider, identity.Subject),
		Username:     identity.Username,
		PasswordHash: hashedPassword,
		Role:         RoleUser,
		Balance:      defaultBalance,
		Concurrency:  defaultConcurrency,
		Status:       StatusActive,
	}
	if invitation != nil {
		// Written by Create inside the same transaction that consumes the code.
		user.InvitedByUserID = invitation.OwnerUserID
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
					// Claimed by someone else after our check; the pending session is already
					// consumed, so report a non-retryable session error (the tx rolls back).
					return ErrSocialOAuthPendingInvalid
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
	// Same post-commit, log-only pattern as the other registration paths, but with the
	// subscription list from the strictly read policy.
	s.assignDefaultSubscriptions(ctx, user.ID, policy.DefaultSubscriptions)
	return user, nil
}

func (s *SocialOAuthService) assignDefaultSubscriptions(ctx context.Context, userID int64, items []DefaultSubscriptionSetting) {
	if s.auth == nil || s.auth.defaultSubAssigner == nil || userID <= 0 {
		return
	}
	for _, item := range items {
		if _, _, err := s.auth.defaultSubAssigner.AssignOrExtendSubscription(ctx, &AssignSubscriptionInput{
			UserID:       userID,
			GroupID:      item.GroupID,
			ValidityDays: item.ValidityDays,
			Notes:        "auto assigned by default user subscriptions setting",
		}); err != nil {
			logger.LegacyPrintf("service.social_oauth", "[SocialOAuth] assign default subscription failed: user_id=%d group_id=%d err=%v", userID, item.GroupID, err)
		}
	}
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

// SocialOAuthHandoffTTL bounds how long the frontend has to redeem a callback handoff code.
const SocialOAuthHandoffTTL = 90 * time.Second

// Handoff outcome kinds.
const (
	SocialOAuthHandoffTokens     = "tokens"
	SocialOAuthHandoffTwoFactor  = "two_factor"
	SocialOAuthHandoffInvitation = "invitation"
)

// SocialOAuthHandoff is the callback outcome kept server-side until the browser that started
// the flow redeems it. Only the frontend gets a random code in the URL fragment; redeeming it
// also needs the HttpOnly binder cookie whose SHA-256 is BinderHash.
type SocialOAuthHandoff struct {
	Provider     SocialOAuthProvider `json:"provider"`
	BinderHash   string              `json:"binder_hash"`
	Kind         string              `json:"kind"`
	Redirect     string              `json:"redirect"`
	AccessToken  string              `json:"access_token,omitempty"`
	RefreshToken string              `json:"refresh_token,omitempty"`
	ExpiresIn    int                 `json:"expires_in,omitempty"`
	TempToken    string              `json:"temp_token,omitempty"`
	PendingToken string              `json:"pending_token,omitempty"`
}

// SocialOAuthHandoffStore keeps callback handoffs (Redis in production). Consume must be an
// atomic get-and-delete and return (nil, nil) for unknown or expired codes.
type SocialOAuthHandoffStore interface {
	Save(ctx context.Context, code string, handoff *SocialOAuthHandoff, ttl time.Duration) error
	Consume(ctx context.Context, code string) (*SocialOAuthHandoff, error)
}

// SaveHandoff stores a callback outcome under a one-time code.
func (s *SocialOAuthService) SaveHandoff(ctx context.Context, code string, handoff *SocialOAuthHandoff) error {
	if s == nil || s.handoff == nil {
		return ErrServiceUnavailable
	}
	return s.handoff.Save(ctx, code, handoff, SocialOAuthHandoffTTL)
}

// ConsumeHandoff atomically takes a callback outcome; (nil, nil) when unknown or expired.
func (s *SocialOAuthService) ConsumeHandoff(ctx context.Context, code string) (*SocialOAuthHandoff, error) {
	if s == nil || s.handoff == nil {
		return nil, ErrServiceUnavailable
	}
	return s.handoff.Consume(ctx, code)
}
