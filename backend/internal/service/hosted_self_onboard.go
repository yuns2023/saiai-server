package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
)

// Hosted self-onboarding (P2c): a logged-in user submits their own Claude
// setup-token account (inference scope only) for hosting; an admin approves
// it into an account in the user's own exclusive hosted group.
//
// Invariants:
//   - The upstream token never leaves the server: user/admin DTOs carry no
//     credentials, logs record only stage, status and a correlation id.
//   - Never direct egress: the code exchange uses the user's proxy or the
//     configured platform exchange proxy; an approved account always has a
//     proxy and is never sent without one (Account.EgressProxyURL).
//   - Credentials exist only while an application is pending (DB CHECK).

var (
	ErrSelfOnboardDisabled         = infraerrors.Forbidden("SELF_ONBOARD_DISABLED", "hosting applications are not open")
	ErrSelfOnboardProxyRequired    = infraerrors.BadRequest("SELF_ONBOARD_PROXY_REQUIRED", "a proxy is required: enter your own proxy or contact the administrator")
	ErrSelfOnboardProxyInvalid     = infraerrors.BadRequest("SELF_ONBOARD_PROXY_INVALID", "invalid proxy settings")
	ErrSelfOnboardProxyUnavailable = infraerrors.BadRequest("SELF_ONBOARD_PROXY_UNAVAILABLE", "proxy unavailable")
	ErrSelfOnboardSessionInvalid   = infraerrors.BadRequest("SELF_ONBOARD_SESSION_INVALID", "the authorization session is invalid or expired; generate a new link")
	ErrSelfOnboardExchangeFailed   = infraerrors.BadRequest("SELF_ONBOARD_EXCHANGE_FAILED", "authorization failed; generate a new link and try again")
	ErrSelfOnboardIdentityMissing  = infraerrors.BadRequest("SELF_ONBOARD_IDENTITY_MISSING", "the authorization did not identify the Claude account; contact the administrator")
	ErrSelfOnboardAlreadyHosted    = infraerrors.Conflict("SELF_ONBOARD_ALREADY_HOSTED", "this Claude account is already hosted or under review; contact the administrator")
	ErrSelfOnboardPendingLimit     = infraerrors.Conflict("SELF_ONBOARD_PENDING_LIMIT", "too many applications are waiting for review")
	ErrSelfOnboardRateLimited      = infraerrors.TooManyRequests("SELF_ONBOARD_RATE_LIMITED", "too many attempts; try again later")
	ErrSelfOnboardNotFound         = infraerrors.NotFound("SELF_ONBOARD_NOT_FOUND", "not found")
	ErrSelfOnboardInvalidState     = infraerrors.Conflict("SELF_ONBOARD_INVALID_STATE", "the application can no longer be changed")
	ErrSelfOnboardInvalidInput     = infraerrors.BadRequest("SELF_ONBOARD_INVALID_INPUT", "invalid input")
	ErrSelfOnboardUnavailable      = infraerrors.ServiceUnavailable("SELF_ONBOARD_UNAVAILABLE", "temporarily unavailable; try again later")

	// ErrSelfOnboardedAccountNeedsProxy: self-onboarded accounts are never
	// sent directly from the server.
	ErrSelfOnboardedAccountNeedsProxy = infraerrors.BadRequest("SELF_ONBOARDED_ACCOUNT_REQUIRES_PROXY", "a self-onboarded hosted account must keep a proxy")

	// ErrSelfOnboardedAccountTypeLocked: the platform and type of a
	// self-onboarded account cannot change.
	ErrSelfOnboardedAccountTypeLocked = infraerrors.BadRequest("SELF_ONBOARDED_ACCOUNT_TYPE_LOCKED", "the type of a self-onboarded hosted account cannot change")

	// ErrUpstreamAccountAlreadyExists: a live account of the platform already
	// has this upstream identity (unique index from migration 133).
	ErrUpstreamAccountAlreadyExists = infraerrors.Conflict("UPSTREAM_ACCOUNT_ALREADY_EXISTS", "another live account already uses this upstream account (same account_uuid)")
)

const (
	HostedApplicationStatusPending   = "pending"
	HostedApplicationStatusApproved  = "approved"
	HostedApplicationStatusRejected  = "rejected"
	HostedApplicationStatusCancelled = "cancelled"
	HostedApplicationStatusExpired   = "expired"

	HostedSelfOnboardMaxPending       = 3
	HostedApplicationListMaxPageSize  = 100
	HostedApplicationListMaxPage      = 100000
	hostedSelfOnboardSessionTTL       = 10 * time.Minute
	hostedSelfOnboardSessionPurpose   = "setup_token"
	hostedSelfOnboardAuthURLLimit     = 10
	hostedSelfOnboardAuthURLWindow    = time.Hour
	hostedSelfOnboardSubmitLimit      = 10
	hostedSelfOnboardSubmitWindow     = 24 * time.Hour
	hostedSelfOnboardExchangeDeadline = 20 * time.Second
	hostedSelfOnboardResolveTimeout   = 5 * time.Second
	HostedApplicationPendingMaxAge    = 30 * 24 * time.Hour
	hostedApplicationListLimit        = 100
	hostedDisplayNameMaxRunes         = 64
	hostedRejectReasonMaxRunes        = 500
	hostedProxyCredentialMaxBytes     = 255
	hostedProxyCredentialMaxRunes     = 100
	hostedProxyHostMaxLen             = 253
	hostedTokenMinLifetime            = time.Hour
	hostedTokenMaxLifetime            = 2 * 366 * 24 * time.Hour
	// hostedApprovalMinTokenValidity: approving a token about to expire
	// would create an account that dies immediately.
	hostedApprovalMinTokenValidity = 24 * time.Hour
)

// HostedProxyInput is a user-submitted proxy (protocol://host:port with
// optional username+password).
type HostedProxyInput struct {
	Protocol string
	Host     string
	Port     int
	Username string
	Password string
}

// HostedOnboardSession is the per-user authorization session kept in Redis.
type HostedOnboardSession struct {
	UserID       int64
	Purpose      string
	State        string
	CodeVerifier string
	ExpiresAt    time.Time
}

// HostedOnboardStore keeps per-user sessions and rate-limit counters.
type HostedOnboardStore interface {
	SaveSession(ctx context.Context, sessionID string, session *HostedOnboardSession, ttl time.Duration) error
	// ConsumeSession atomically checks owner, purpose, state and expiry and
	// deletes the session. It returns nil (and keeps the session) when
	// anything does not match.
	ConsumeSession(ctx context.Context, sessionID string, userID int64, purpose, state string, now time.Time) (*HostedOnboardSession, error)
	CountAuthURL(ctx context.Context, userID int64, window time.Duration) (int64, error)
	CountSubmit(ctx context.Context, userID int64, window time.Duration) (int64, error)
}

// HostedApplication is an application without secrets.
type HostedApplication struct {
	ID                  int64
	UserID              int64
	UserEmail           string
	Status              string
	DisplayName         string
	Platform            string
	UpstreamAccountUUID string
	UpstreamOrgUUID     string
	UpstreamEmail       string
	ProxyProtocol       string
	ProxyHost           string
	ProxyPort           int
	HasProxyAuth        bool
	AccountID           *int64
	GroupID             *int64
	ReviewedBy          *int64
	ReviewedAt          *time.Time
	RejectReason        string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// HasProxy reports whether the applicant submitted a proxy.
func (a *HostedApplication) HasProxy() bool { return a != nil && a.ProxyHost != "" }

// ProxyDisplay is "protocol://host:port" (never credentials) or "".
func (a *HostedApplication) ProxyDisplay() string {
	if !a.HasProxy() {
		return ""
	}
	return a.ProxyProtocol + "://" + net.JoinHostPort(a.ProxyHost, strconv.Itoa(a.ProxyPort))
}

// HostedApplicationCreate is a validated new pending application.
type HostedApplicationCreate struct {
	UserID              int64
	DisplayName         string
	Platform            string
	UpstreamAccountUUID string
	UpstreamOrgUUID     string
	UpstreamEmail       string
	Credentials         map[string]any
	Proxy               *HostedProxyInput
	MaxPending          int
}

// HostedApplicationSecrets is the pending application as seen inside the
// approval transaction (credentials and proxy password present).
type HostedApplicationSecrets struct {
	HostedApplication
	Credentials   map[string]any
	ProxyUsername string
	ProxyPassword string
}

// HostedApprovedAccountSpec is what the approval transaction creates.
type HostedApprovedAccountSpec struct {
	Name        string
	Credentials map[string]any
	Extra       map[string]any
	Concurrency int
	Priority    int
	// GroupName/GroupDescription/FeeRate apply when the owner group is created.
	GroupName        string
	GroupDescription string
	FeeRate          float64
}

// HostedApproveParams drives HostedApplicationRepository.Approve.
type HostedApproveParams struct {
	ApplicationID int64
	AdminUserID   int64
	// ExistingProxyID > 0 binds an existing active proxy; otherwise the
	// application's submitted proxy is created.
	ExistingProxyID int64
	UseSubmitted    bool
	// Build runs inside the transaction on the locked pending application
	// (no network I/O). Its error aborts the approval.
	Build func(app *HostedApplicationSecrets) (*HostedApprovedAccountSpec, error)
}

// HostedApproveResult is the approval outcome (also returned on replays).
type HostedApproveResult struct {
	Application  *HostedApplication
	AccountID    int64
	GroupID      int64
	CreatedProxy int64 // 0 when an existing proxy was bound or on replay
	Replayed     bool
}

// HostedOwnedAccountState is the owner-facing pause/withdraw state.
type HostedOwnedAccountState struct {
	AccountID         int64 `json:"id"`
	OwnerPaused       bool  `json:"owner_paused"`
	WithdrawRequested bool  `json:"withdraw_requested"`
}

// Owner actions on an owned account.
const (
	HostedOwnerActionPause    = "pause"
	HostedOwnerActionResume   = "resume"
	HostedOwnerActionWithdraw = "withdraw"
)

// HostedApplicationRepository persists applications. Every state change is a
// conditional update on status = 'pending', so approval, cancel, reject and
// expiry cannot both win.
type HostedApplicationRepository interface {
	CountPending(ctx context.Context, userID int64) (int, error)
	// CreatePending locks the user row, re-checks (user active, switch on,
	// pending < MaxPending, no live account / pending application with the
	// same upstream identity) and inserts.
	CreatePending(ctx context.Context, in *HostedApplicationCreate) (*HostedApplication, error)
	ListByUser(ctx context.Context, userID int64, limit int) ([]HostedApplication, error)
	// Get returns one application without secrets.
	Get(ctx context.Context, id int64) (*HostedApplication, error)
	ListAdmin(ctx context.Context, status string, page, pageSize int) ([]HostedApplication, int64, error)
	Cancel(ctx context.Context, userID, id int64) (*HostedApplication, error)
	Reject(ctx context.Context, adminUserID, id int64, reason string) (*HostedApplication, error)
	Approve(ctx context.Context, params HostedApproveParams) (*HostedApproveResult, error)
	ExpirePending(ctx context.Context, createdBefore time.Time, limit int) (int64, error)
	// SetOwnerAccountState applies an owner action to an account the user
	// currently owns (owner_user_id = user AND not deleted).
	SetOwnerAccountState(ctx context.Context, ownerUserID, accountID int64, action string) (*HostedOwnedAccountState, error)
}

// HostedProxyTester probes a proxy after it was created (latency cache).
type HostedProxyTester interface {
	TestProxy(ctx context.Context, id int64) (*ProxyTestResult, error)
}

// HostedNetResolver resolves a proxy host name.
type HostedNetResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// HostedSelfOnboardService implements the user and admin flows.
type HostedSelfOnboardService struct {
	repo         HostedApplicationRepository
	store        HostedOnboardStore
	settingRepo  SettingRepository
	proxyRepo    ProxyRepository
	oauthClient  ClaudeOAuthClient
	proxyTester  HostedProxyTester
	authCache    APIKeyAuthCacheInvalidator
	resolver     HostedNetResolver
	now          func() time.Time
	newSessionID func() (string, error)
}

func NewHostedSelfOnboardService(
	repo HostedApplicationRepository,
	store HostedOnboardStore,
	settingRepo SettingRepository,
	proxyRepo ProxyRepository,
	oauthClient ClaudeOAuthClient,
	proxyTester HostedProxyTester,
	authCache APIKeyAuthCacheInvalidator,
) *HostedSelfOnboardService {
	return &HostedSelfOnboardService{
		repo:         repo,
		store:        store,
		settingRepo:  settingRepo,
		proxyRepo:    proxyRepo,
		oauthClient:  oauthClient,
		proxyTester:  proxyTester,
		authCache:    authCache,
		resolver:     net.DefaultResolver,
		now:          time.Now,
		newSessionID: oauth.GenerateSessionID,
	}
}

func (s *HostedSelfOnboardService) log(ctx context.Context) *zap.Logger {
	return logger.FromContext(ctx).With(zap.String("component", "hosted.self_onboard"))
}

// Settings loads the feature settings. Errors fail closed.
func (s *HostedSelfOnboardService) Settings(ctx context.Context) (HostedSelfOnboardSettings, error) {
	values, err := s.settingRepo.GetMultiple(ctx, HostedSelfOnboardSettingKeys())
	if err != nil {
		return HostedSelfOnboardSettings{}, err
	}
	return ParseHostedSelfOnboardSettings(values), nil
}

// HostedSelfOnboardConfig is GET /hosted/self-onboard/config.
type HostedSelfOnboardConfig struct {
	Enabled                 bool `json:"enabled"`
	ExchangeProxyConfigured bool `json:"exchange_proxy_configured"`
	MaxPending              int  `json:"max_pending"`
}

func (s *HostedSelfOnboardService) Config(ctx context.Context) (*HostedSelfOnboardConfig, error) {
	cfg, err := s.Settings(ctx)
	if err != nil {
		return nil, ErrSelfOnboardUnavailable.WithCause(err)
	}
	out := &HostedSelfOnboardConfig{Enabled: cfg.Enabled, MaxPending: HostedSelfOnboardMaxPending}
	if cfg.Enabled && cfg.ExchangeProxyID > 0 {
		if _, err := s.platformExchangeProxyURL(ctx, cfg.ExchangeProxyID); err == nil {
			out.ExchangeProxyConfigured = true
		}
	}
	return out, nil
}

// HostedAuthURL is POST /hosted/applications/auth-url.
type HostedAuthURL struct {
	SessionID string    `json:"session_id"`
	AuthURL   string    `json:"auth_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateAuthURL starts a setup-token (inference scope) authorization bound
// to the user. At most hostedSelfOnboardAuthURLLimit per hour.
func (s *HostedSelfOnboardService) CreateAuthURL(ctx context.Context, userID int64) (*HostedAuthURL, error) {
	if userID <= 0 {
		return nil, ErrSelfOnboardNotFound
	}
	cfg, err := s.Settings(ctx)
	if err != nil {
		return nil, ErrSelfOnboardUnavailable.WithCause(err)
	}
	if !cfg.Enabled {
		return nil, ErrSelfOnboardDisabled
	}
	n, err := s.store.CountAuthURL(ctx, userID, hostedSelfOnboardAuthURLWindow)
	if err != nil {
		return nil, ErrSelfOnboardUnavailable.WithCause(err)
	}
	if n > hostedSelfOnboardAuthURLLimit {
		return nil, ErrSelfOnboardRateLimited
	}
	state, err := oauth.GenerateState()
	if err != nil {
		return nil, ErrSelfOnboardUnavailable.WithCause(err)
	}
	verifier, err := oauth.GenerateCodeVerifier()
	if err != nil {
		return nil, ErrSelfOnboardUnavailable.WithCause(err)
	}
	sessionID, err := s.newSessionID()
	if err != nil {
		return nil, ErrSelfOnboardUnavailable.WithCause(err)
	}
	expiresAt := s.now().UTC().Add(hostedSelfOnboardSessionTTL).Truncate(time.Second)
	if err := s.store.SaveSession(ctx, sessionID, &HostedOnboardSession{
		UserID:       userID,
		Purpose:      hostedSelfOnboardSessionPurpose,
		State:        state,
		CodeVerifier: verifier,
		ExpiresAt:    expiresAt,
	}, hostedSelfOnboardSessionTTL); err != nil {
		return nil, ErrSelfOnboardUnavailable.WithCause(err)
	}
	return &HostedAuthURL{
		SessionID: sessionID,
		AuthURL:   oauth.BuildAuthorizationURL(state, oauth.GenerateCodeChallenge(verifier), oauth.ScopeInference),
		ExpiresAt: expiresAt,
	}, nil
}

// HostedSubmitInput is POST /hosted/applications.
type HostedSubmitInput struct {
	SessionID   string
	Code        string
	DisplayName string
	Proxy       *HostedProxyInput
}

// Submit validates everything that needs no upstream call, consumes the
// session, exchanges the code through a proxy and stores a pending
// application. The token is never returned.
func (s *HostedSelfOnboardService) Submit(ctx context.Context, userID int64, in HostedSubmitInput) (*HostedApplication, error) {
	if userID <= 0 {
		return nil, ErrSelfOnboardNotFound
	}
	cfg, err := s.Settings(ctx)
	if err != nil {
		return nil, ErrSelfOnboardUnavailable.WithCause(err)
	}
	if !cfg.Enabled {
		return nil, ErrSelfOnboardDisabled
	}

	// 1. Formats (nothing consumed, nothing counted).
	if !validHostedSessionID(in.SessionID) {
		return nil, ErrSelfOnboardSessionInvalid
	}
	authCode, state, ok := parseHostedAuthCode(in.Code)
	if !ok {
		return nil, ErrSelfOnboardSessionInvalid
	}
	displayName, ok := normalizeHostedDisplayName(in.DisplayName)
	if !ok {
		return nil, ErrSelfOnboardInvalidInput
	}
	var proxyInput *HostedProxyInput
	if in.Proxy != nil {
		proxyInput, err = NormalizeHostedProxyInput(in.Proxy)
		if err != nil {
			return nil, err
		}
	}

	// 2. Daily attempts (failed exchanges count too).
	n, err := s.store.CountSubmit(ctx, userID, hostedSelfOnboardSubmitWindow)
	if err != nil {
		return nil, ErrSelfOnboardUnavailable.WithCause(err)
	}
	if n > hostedSelfOnboardSubmitLimit {
		return nil, ErrSelfOnboardRateLimited
	}

	// 3. Quick pending check (re-checked under the user lock on insert).
	pending, err := s.repo.CountPending(ctx, userID)
	if err != nil {
		return nil, ErrSelfOnboardUnavailable.WithCause(err)
	}
	if pending >= HostedSelfOnboardMaxPending {
		return nil, ErrSelfOnboardPendingLimit
	}

	// 4. Exchange proxy: the user's (resolved now, public addresses only),
	// else the platform exchange proxy, else refuse. Never direct.
	var proxyURL string
	if proxyInput != nil {
		if err := s.checkProxyResolvesPublic(ctx, proxyInput.Host); err != nil {
			return nil, err
		}
		proxyURL = hostedProxyURL(proxyInput)
	} else {
		if cfg.ExchangeProxyID <= 0 {
			return nil, ErrSelfOnboardProxyRequired
		}
		proxyURL, err = s.platformExchangeProxyURL(ctx, cfg.ExchangeProxyID)
		if err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(proxyURL) == "" {
		return nil, ErrSelfOnboardProxyRequired
	}

	// 5. One-time session: owner, purpose, state and expiry checked and the
	// session deleted atomically.
	session, err := s.store.ConsumeSession(ctx, in.SessionID, userID, hostedSelfOnboardSessionPurpose, state, s.now())
	if err != nil {
		return nil, ErrSelfOnboardUnavailable.WithCause(err)
	}
	if session == nil {
		return nil, ErrSelfOnboardSessionInvalid
	}

	// 6. Exchange (total deadline 20 s). Errors and responses are never
	// echoed or logged beyond stage + correlation id.
	correlationID := newHostedCorrelationID()
	exchangeCtx, cancel := context.WithTimeout(WithQuietOAuthExchange(ctx, correlationID), hostedSelfOnboardExchangeDeadline)
	tokenResp, err := s.oauthClient.ExchangeCodeForToken(exchangeCtx, authCode+"#"+state, session.CodeVerifier, session.State, proxyURL, true)
	cancel()
	if err != nil {
		s.log(ctx).Warn("hosted self-onboard exchange failed",
			zap.String("stage", "exchange"),
			zap.Int64("user_id", userID),
			zap.Bool("user_proxy", proxyInput != nil),
			zap.Bool("deadline_exceeded", errors.Is(err, context.DeadlineExceeded) || errors.Is(exchangeCtx.Err(), context.DeadlineExceeded)),
			zap.String("correlation_id", correlationID))
		return nil, ErrSelfOnboardExchangeFailed.WithMetadata(map[string]string{"correlation_id": correlationID})
	}
	token, err := validateHostedSetupToken(tokenResp, s.now())
	if err != nil {
		s.log(ctx).Warn("hosted self-onboard token rejected",
			zap.String("stage", "validate_token"),
			zap.Int64("user_id", userID),
			zap.String("reason", hostedTokenRejectReason(err)),
			zap.String("correlation_id", correlationID))
		if errors.Is(err, ErrSelfOnboardIdentityMissing) {
			return nil, ErrSelfOnboardIdentityMissing.WithMetadata(map[string]string{"correlation_id": correlationID})
		}
		return nil, ErrSelfOnboardExchangeFailed.WithMetadata(map[string]string{"correlation_id": correlationID})
	}

	// 7. Insert under the user lock with all limits re-checked.
	app, err := s.repo.CreatePending(ctx, &HostedApplicationCreate{
		UserID:              userID,
		DisplayName:         displayName,
		Platform:            PlatformAnthropic,
		UpstreamAccountUUID: token.AccountUUID,
		UpstreamOrgUUID:     token.OrgUUID,
		UpstreamEmail:       token.Email,
		Credentials:         token.Credentials,
		Proxy:               proxyInput,
		MaxPending:          HostedSelfOnboardMaxPending,
	})
	if err != nil {
		if infraerrors.FromError(err).Reason != infraerrors.UnknownReason {
			return nil, err
		}
		s.log(ctx).Error("hosted self-onboard insert failed",
			zap.String("stage", "insert"), zap.Int64("user_id", userID), zap.String("correlation_id", correlationID))
		return nil, ErrSelfOnboardUnavailable
	}
	s.log(ctx).Info("hosted self-onboard application submitted",
		zap.String("stage", "submitted"), zap.Int64("user_id", userID), zap.Int64("application_id", app.ID),
		zap.String("correlation_id", correlationID))
	return app, nil
}

func (s *HostedSelfOnboardService) ListMine(ctx context.Context, userID int64) ([]HostedApplication, error) {
	if userID <= 0 {
		return []HostedApplication{}, nil
	}
	return s.repo.ListByUser(ctx, userID, hostedApplicationListLimit)
}

func (s *HostedSelfOnboardService) Cancel(ctx context.Context, userID, id int64) (*HostedApplication, error) {
	if userID <= 0 || id <= 0 {
		return nil, ErrSelfOnboardNotFound
	}
	return s.repo.Cancel(ctx, userID, id)
}

// SetOwnedAccountState pauses/resumes/requests withdrawal of an account the
// user currently owns. Works with the switch off.
func (s *HostedSelfOnboardService) SetOwnedAccountState(ctx context.Context, userID, accountID int64, action string) (*HostedOwnedAccountState, error) {
	if userID <= 0 || accountID <= 0 {
		return nil, ErrSelfOnboardNotFound
	}
	switch action {
	case HostedOwnerActionPause, HostedOwnerActionResume, HostedOwnerActionWithdraw:
	default:
		return nil, ErrSelfOnboardInvalidInput
	}
	state, err := s.repo.SetOwnerAccountState(ctx, userID, accountID, action)
	if err != nil {
		return nil, err
	}
	s.log(ctx).Info("hosted owner account action",
		zap.String("action", action), zap.Int64("user_id", userID), zap.Int64("account_id", accountID))
	return state, nil
}

// ListAdmin lists applications for review (no secrets).
func (s *HostedSelfOnboardService) ListAdmin(ctx context.Context, status string, page, pageSize int) ([]HostedApplication, int64, error) {
	switch status {
	case "", HostedApplicationStatusPending, HostedApplicationStatusApproved, HostedApplicationStatusRejected,
		HostedApplicationStatusCancelled, HostedApplicationStatusExpired:
	default:
		return nil, 0, ErrSelfOnboardInvalidInput
	}
	return s.repo.ListAdmin(ctx, status, page, pageSize)
}

func (s *HostedSelfOnboardService) Reject(ctx context.Context, adminUserID, id int64, reason string) (*HostedApplication, error) {
	if id <= 0 {
		return nil, ErrSelfOnboardNotFound
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > hostedRejectReasonMaxRunes || hasControlChars(reason, true) {
		return nil, ErrSelfOnboardInvalidInput
	}
	return s.repo.Reject(ctx, adminUserID, id, reason)
}

// HostedApproveInput is POST /admin/hosted-applications/:id/approve.
type HostedApproveInput struct {
	ProxyMode                 string // "existing" | "submitted"
	ProxyID                   int64
	Concurrency               int
	Priority                  int
	CarpoolDeviceLimit        int
	CarpoolAdmissionsPerCycle int
	CarpoolIntervalHours      int
}

// HostedApproveRequest is the admin body; omitted template values take the
// site defaults (settings hosted_self_onboard_default_*).
type HostedApproveRequest struct {
	ProxyMode                 string
	ProxyID                   int64
	Concurrency               *int
	Priority                  *int
	CarpoolDeviceLimit        *int
	CarpoolAdmissionsPerCycle *int
	CarpoolIntervalHours      *int
}

// Resolve fills omitted values from the settings defaults.
func (r HostedApproveRequest) Resolve(cfg HostedSelfOnboardSettings) HostedApproveInput {
	pick := func(v *int, def int) int {
		if v != nil {
			return *v
		}
		return def
	}
	in := HostedApproveInput{
		ProxyMode:                 r.ProxyMode,
		ProxyID:                   r.ProxyID,
		Concurrency:               pick(r.Concurrency, cfg.DefaultConcurrency),
		Priority:                  pick(r.Priority, cfg.DefaultPriority),
		CarpoolDeviceLimit:        pick(r.CarpoolDeviceLimit, cfg.DefaultDeviceLimit),
		CarpoolIntervalHours:      pick(r.CarpoolIntervalHours, cfg.DefaultIntervalHours),
		CarpoolAdmissionsPerCycle: 0,
	}
	in.CarpoolAdmissionsPerCycle = pick(r.CarpoolAdmissionsPerCycle, minInt(cfg.DefaultAdmissionsPerCycle, in.CarpoolDeviceLimit))
	return in
}

const (
	HostedApproveProxyExisting  = "existing"
	HostedApproveProxySubmitted = "submitted"
)

// ValidateHostedApproveInput checks the admin template.
func ValidateHostedApproveInput(in HostedApproveInput) error {
	switch in.ProxyMode {
	case HostedApproveProxyExisting:
		if in.ProxyID <= 0 {
			return ErrSelfOnboardProxyRequired
		}
	case HostedApproveProxySubmitted:
	default:
		return ErrSelfOnboardProxyRequired
	}
	if in.Concurrency < 1 || in.Concurrency > MaxHostedSelfOnboardConcurrency ||
		in.Priority < 0 || in.Priority > MaxHostedSelfOnboardPriority ||
		in.CarpoolDeviceLimit < 1 || in.CarpoolDeviceLimit > MaxHostedSelfOnboardCarpoolDeviceLimitValue ||
		in.CarpoolAdmissionsPerCycle < 0 || in.CarpoolAdmissionsPerCycle > in.CarpoolDeviceLimit ||
		in.CarpoolIntervalHours < 1 || in.CarpoolIntervalHours > MaxHostedSelfOnboardCarpoolIntervalHours {
		return ErrSelfOnboardInvalidInput
	}
	return nil
}

// Approve creates the hosted account in one transaction (see
// HostedApplicationRepository.Approve). Re-approving an approved application
// returns the stored result.
func (s *HostedSelfOnboardService) Approve(ctx context.Context, adminUserID, id int64, req HostedApproveRequest) (*HostedApproveResult, error) {
	if id <= 0 {
		return nil, ErrSelfOnboardNotFound
	}
	cfg, err := s.Settings(ctx)
	if err != nil {
		return nil, ErrSelfOnboardUnavailable.WithCause(err)
	}
	if !cfg.Enabled {
		return nil, ErrSelfOnboardDisabled
	}
	in := req.Resolve(cfg)
	if err := ValidateHostedApproveInput(in); err != nil {
		return nil, err
	}
	params := HostedApproveParams{ApplicationID: id, AdminUserID: adminUserID}
	if in.ProxyMode == HostedApproveProxyExisting {
		params.ExistingProxyID = in.ProxyID
	} else {
		params.UseSubmitted = true
	}
	// The submitted proxy host is resolved again right before approval (its
	// DNS may have changed since submission). Resolution is network I/O, so it
	// runs before the transaction; Build refuses if the locked row's host is
	// not the one checked here.
	var checkedHost string
	if params.UseSubmitted {
		current, err := s.repo.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if current.Status == HostedApplicationStatusPending {
			if !current.HasProxy() {
				return nil, ErrSelfOnboardProxyRequired
			}
			if err := s.checkProxyResolvesPublic(ctx, current.ProxyHost); err != nil {
				return nil, err
			}
			checkedHost = current.ProxyHost
		}
	}
	now := s.now()
	params.Build = func(app *HostedApplicationSecrets) (*HostedApprovedAccountSpec, error) {
		if params.UseSubmitted && (!app.HasProxy() || app.ProxyHost != checkedHost) {
			return nil, ErrSelfOnboardProxyRequired
		}
		return buildHostedApprovedAccount(app, in, cfg, now)
	}
	result, err := s.repo.Approve(ctx, params)
	if err != nil {
		return nil, err
	}
	if !result.Replayed {
		if s.authCache != nil && result.Application != nil {
			s.authCache.InvalidateAuthCacheByUserID(ctx, result.Application.UserID)
		}
		if result.CreatedProxy > 0 && s.proxyTester != nil {
			proxyID := result.CreatedProxy
			go func() {
				probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				_, _ = s.proxyTester.TestProxy(probeCtx, proxyID)
			}()
		}
		s.log(ctx).Info("hosted application approved",
			zap.Int64("application_id", id), zap.Int64("admin_user_id", adminUserID),
			zap.Int64("account_id", result.AccountID), zap.Int64("group_id", result.GroupID))
	}
	return result, nil
}

// ExpireStale marks pending applications older than 30 days expired (and
// wipes their secrets). Runs regardless of the switch.
func (s *HostedSelfOnboardService) ExpireStale(ctx context.Context) (int64, error) {
	return s.repo.ExpirePending(ctx, s.now().Add(-HostedApplicationPendingMaxAge), 500)
}

func (s *HostedSelfOnboardService) platformExchangeProxyURL(ctx context.Context, proxyID int64) (string, error) {
	proxy, err := s.proxyRepo.GetByID(ctx, proxyID)
	if err != nil || proxy == nil {
		return "", ErrSelfOnboardProxyRequired
	}
	if !proxy.IsActive() || strings.TrimSpace(proxy.Host) == "" || proxy.Port <= 0 {
		return "", ErrSelfOnboardProxyRequired
	}
	return proxy.URL(), nil
}

// checkProxyResolvesPublic resolves the host once; every address must be
// public. Failures are reported only as "proxy unavailable".
func (s *HostedSelfOnboardService) checkProxyResolvesPublic(ctx context.Context, host string) error {
	if addr, err := parseHostedIPLiteral(host); err == nil {
		if !IsPublicHostedProxyIP(addr) {
			return ErrSelfOnboardProxyUnavailable
		}
		return nil
	}
	resolveCtx, cancel := context.WithTimeout(ctx, hostedSelfOnboardResolveTimeout)
	defer cancel()
	addrs, err := s.resolver.LookupIPAddr(resolveCtx, host)
	if err != nil || len(addrs) == 0 {
		return ErrSelfOnboardProxyUnavailable
	}
	for _, a := range addrs {
		if a.Zone != "" || !IsPublicHostedProxyIP(a.IP) {
			return ErrSelfOnboardProxyUnavailable
		}
	}
	return nil
}

func hostedProxyURL(p *HostedProxyInput) string {
	return (&Proxy{Protocol: p.Protocol, Host: p.Host, Port: p.Port, Username: p.Username, Password: p.Password}).URL()
}

func newHostedCorrelationID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func validHostedSessionID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !isHostedIDChar(r) {
			return false
		}
	}
	return true
}

func isHostedIDChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		return true
	default:
		return false
	}
}

// parseHostedAuthCode splits the pasted "code#state".
func parseHostedAuthCode(raw string) (code, state string, ok bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 4096 {
		return "", "", false
	}
	idx := strings.IndexByte(raw, '#')
	if idx <= 0 || idx == len(raw)-1 {
		return "", "", false
	}
	code, state = raw[:idx], raw[idx+1:]
	if len(state) > 512 || !hostedTokenChars(code) || !hostedTokenChars(state) {
		return "", "", false
	}
	return code, state, true
}

func hostedTokenChars(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c <= 0x20 || c >= 0x7f || c == '#' {
			return false
		}
	}
	return true
}

func normalizeHostedDisplayName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > hostedDisplayNameMaxRunes || hasControlChars(name, false) {
		return "", false
	}
	return name, true
}

// hasControlChars reports C0/C1 controls (and DEL); allowNewline keeps \n.
func hasControlChars(v string, allowNewline bool) bool {
	for _, r := range v {
		if allowNewline && r == '\n' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return true
		}
	}
	return false
}

// hostedSetupToken is a validated exchange result.
type hostedSetupToken struct {
	AccountUUID string
	OrgUUID     string
	Email       string
	Credentials map[string]any
}

var (
	errHostedTokenMissingAccess = errors.New("missing access token")
	errHostedTokenExpiry        = errors.New("expires_in out of range")
	errHostedTokenScope         = errors.New("inference scope missing")
)

func hostedTokenRejectReason(err error) string {
	switch {
	case errors.Is(err, errHostedTokenMissingAccess):
		return "missing_access_token"
	case errors.Is(err, errHostedTokenExpiry):
		return "expires_in"
	case errors.Is(err, errHostedTokenScope):
		return "scope"
	case errors.Is(err, ErrSelfOnboardIdentityMissing):
		return "identity_missing"
	default:
		return "invalid"
	}
}

// validateHostedSetupToken requires a non-empty access token, a sane
// expires_in (no invented expiry), the inference scope and a valid
// account.uuid.
func validateHostedSetupToken(resp *oauth.TokenResponse, now time.Time) (*hostedSetupToken, error) {
	if resp == nil || strings.TrimSpace(resp.AccessToken) == "" {
		return nil, errHostedTokenMissingAccess
	}
	// Bounds checked in whole seconds before any Duration multiplication.
	if resp.ExpiresIn < int64(hostedTokenMinLifetime/time.Second) || resp.ExpiresIn > int64(hostedTokenMaxLifetime/time.Second) {
		return nil, errHostedTokenExpiry
	}
	if !isHostedSetupTokenScope(resp.Scope) {
		return nil, errHostedTokenScope
	}
	if resp.Account == nil {
		return nil, ErrSelfOnboardIdentityMissing
	}
	accountUUID, ok := NormalizeUpstreamAccountUUID(resp.Account.UUID)
	if !ok {
		return nil, ErrSelfOnboardIdentityMissing
	}
	out := &hostedSetupToken{AccountUUID: accountUUID}
	if resp.Organization != nil {
		out.OrgUUID = truncateRunes(strings.TrimSpace(resp.Organization.UUID), 100)
	}
	out.Email = truncateRunes(strings.TrimSpace(resp.Account.EmailAddress), 320)
	creds := map[string]any{
		"access_token": resp.AccessToken,
		"token_type":   resp.TokenType,
		"expires_in":   resp.ExpiresIn,
		"expires_at":   now.Unix() + resp.ExpiresIn,
		"scope":        resp.Scope,
	}
	// No refresh_token: a setup-token is long-lived and inference-only; a
	// stored refresh token would only add a second upstream path.
	out.Credentials = creds
	return out, nil
}

// isHostedSetupTokenScope accepts exactly {user:inference} or
// {user:inference, user:profile} (any order, no duplicates): a setup-token
// must not carry broader rights (org:create_api_key, sessions, ...).
func isHostedSetupTokenScope(scopes string) bool {
	seen := map[string]bool{}
	for _, s := range strings.Fields(scopes) {
		if seen[s] {
			return false
		}
		seen[s] = true
		if s != oauth.ScopeInference && s != "user:profile" {
			return false
		}
	}
	return seen[oauth.ScopeInference]
}

// truncateUTF8Bytes cuts v to at most n bytes on a rune boundary.
func truncateUTF8Bytes(v string, n int) string {
	if len(v) <= n {
		return v
	}
	cut := 0
	for i := range v {
		if i > n {
			break
		}
		cut = i
	}
	return v[:cut]
}

func truncateRunes(v string, n int) string {
	if utf8.RuneCountInString(v) <= n {
		return v
	}
	return string([]rune(v)[:n])
}

// NormalizeUpstreamAccountUUID accepts only the canonical 8-4-4-4-12 form
// (any case, surrounding spaces ignored) and returns it lower-case. The nil
// UUID is not an identity. Migration 133 applies the same rule.
func NormalizeUpstreamAccountUUID(raw string) (string, bool) {
	v := strings.Trim(raw, " \t\r\n") // = migration 133 btrim set
	if len(v) != 36 {
		return "", false
	}
	parsed, err := uuid.Parse(v)
	if err != nil || parsed == uuid.Nil {
		return "", false
	}
	return parsed.String(), true
}

// AccountUpstreamUUIDFromExtra returns the canonical extra.account_uuid of an
// Anthropic account.
func AccountUpstreamUUIDFromExtra(a *Account) (string, bool) {
	if a == nil || a.Platform != PlatformAnthropic {
		return "", false
	}
	return NormalizeUpstreamAccountUUID(a.GetExtraString("account_uuid"))
}

// AccountUpstreamUUIDForCreate is the identity a new account is stored with:
// an explicit valid UpstreamAccountUUID, else the Anthropic extra value.
func AccountUpstreamUUIDForCreate(a *Account) *string {
	if a == nil {
		return nil
	}
	if a.UpstreamAccountUUID != nil {
		if v, ok := NormalizeUpstreamAccountUUID(*a.UpstreamAccountUUID); ok {
			return &v
		}
	}
	if v, ok := AccountUpstreamUUIDFromExtra(a); ok {
		return &v
	}
	return nil
}

// AccountIdentityUnknown: an Anthropic OAuth/setup-token account without a
// known upstream identity; duplicates of it cannot be detected.
func AccountIdentityUnknown(a *Account) bool {
	return a != nil && a.IsAnthropicOAuthOrSetupToken() && a.UpstreamAccountUUID == nil
}

// buildHostedApprovedAccount builds the account from the locked application
// and the admin template. No I/O.
func buildHostedApprovedAccount(app *HostedApplicationSecrets, in HostedApproveInput, cfg HostedSelfOnboardSettings, now time.Time) (*HostedApprovedAccountSpec, error) {
	if app == nil || len(app.Credentials) == 0 {
		return nil, ErrSelfOnboardInvalidState
	}
	if token, ok := app.Credentials["access_token"].(string); !ok || strings.TrimSpace(token) == "" {
		return nil, ErrSelfOnboardInvalidState
	}
	expiresAt, ok := hostedCredentialUnix(app.Credentials["expires_at"])
	if !ok || !now.Add(hostedApprovalMinTokenValidity).Before(time.Unix(expiresAt, 0)) {
		return nil, ErrSelfOnboardInvalidState.WithMetadata(map[string]string{"detail": "token_expired"})
	}
	credentials := make(map[string]any, len(app.Credentials))
	for k, v := range app.Credentials {
		credentials[k] = v
	}
	extra := map[string]any{
		"account_uuid":                                          app.UpstreamAccountUUID,
		"enable_tls_fingerprint":                                true,
		"claude_oauth_mode":                                     ClaudeOAuthModeCarpool,
		"claude_oauth_carpool_users_only":                       false,
		"claude_oauth_carpool_device_limit":                     in.CarpoolDeviceLimit,
		"claude_oauth_carpool_maintenance_interval_hours":       in.CarpoolIntervalHours,
		"claude_oauth_carpool_maintenance_admissions_per_cycle": in.CarpoolAdmissionsPerCycle,
		"hosted_application_id":                                 app.ID,
	}
	if in.CarpoolAdmissionsPerCycle > 0 {
		extra["claude_oauth_carpool_auto_expand_enabled"] = true
	}
	if app.UpstreamOrgUUID != "" {
		extra["org_uuid"] = app.UpstreamOrgUUID
	}
	if app.UpstreamEmail != "" {
		extra["email_address"] = app.UpstreamEmail
	}
	// Same normalizers/validators as the admin create path.
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Credentials: credentials, Extra: extra}
	applyClaudeOAuthSingleDeviceDefaults(account.Platform, account.Type, account.Extra)
	normalizeSingleDeviceFixedHeaders(account)
	if err := validateClaudeOAuthSingleDeviceConfig(account); err != nil {
		return nil, ErrSelfOnboardInvalidInput
	}
	if err := ValidateQuotaResetConfig(account.Extra); err != nil {
		return nil, ErrSelfOnboardInvalidInput
	}
	if account.GetClaudeOAuthMode() != ClaudeOAuthModeCarpool ||
		account.GetClaudeOAuthCarpoolDeviceLimit() != in.CarpoolDeviceLimit ||
		account.GetCarpoolMaintenanceIntervalHours() != in.CarpoolIntervalHours ||
		account.GetCarpoolAdmissionsPerCycle() != in.CarpoolAdmissionsPerCycle {
		return nil, ErrSelfOnboardInvalidInput
	}
	NormalizeCarpoolMaintenance(account, nil, now)

	name := fmt.Sprintf("自助托管 #%d %s", app.ID, app.DisplayName)
	return &HostedApprovedAccountSpec{
		Name:             truncateUTF8Bytes(name, 100), // accounts.name: ent MaxLen counts bytes
		Credentials:      account.Credentials,
		Extra:            account.Extra,
		Concurrency:      in.Concurrency,
		Priority:         in.Priority,
		GroupName:        fmt.Sprintf("托管-%d", app.UserID),
		GroupDescription: fmt.Sprintf("自助托管：用户 #%d 的专属托管组", app.UserID),
		FeeRate:          cfg.FeeRate,
	}, nil
}

func hostedCredentialUnix(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	case string:
		parsed, err := strconv.ParseInt(n, 10, 64)
		return parsed, err == nil
	default:
		if num, ok := v.(interface{ Int64() (int64, error) }); ok {
			parsed, err := num.Int64()
			return parsed, err == nil
		}
		return 0, false
	}
}

type quietOAuthExchangeKey struct{}

// WithQuietOAuthExchange marks a Claude code exchange whose client must not
// log request bodies, raw transport errors or response bodies (hosted
// self-onboarding); logs carry the correlation id instead.
func WithQuietOAuthExchange(ctx context.Context, correlationID string) context.Context {
	return context.WithValue(ctx, quietOAuthExchangeKey{}, correlationID)
}

// QuietOAuthExchange reports the marker set by WithQuietOAuthExchange.
func QuietOAuthExchange(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(quietOAuthExchangeKey{}).(string)
	return id, ok
}
