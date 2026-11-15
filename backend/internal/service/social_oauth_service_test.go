//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// ---- stubs ----------------------------------------------------------------

type socialSettingRepo struct {
	SettingRepository
	mu     sync.Mutex
	values map[string]string
	err    error
}

func (r *socialSettingRepo) set(k, v string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[k] = v
}

func (r *socialSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return "", r.err
	}
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}

func (r *socialSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]string{}
	for k, v := range r.values {
		out[k] = v
	}
	return out, nil
}

func (r *socialSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range values {
		r.values[k] = v
	}
	return nil
}

func (r *socialSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := r.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

// socialUserRepo is an in-memory user store with a minimal transaction model:
// creations inside runInTx are staged and dropped on rollback.
type socialUserRepo struct {
	UserRepository
	nextID int64
	users  map[string]*User
	staged []*User
	inTx   bool
}

func newSocialUserRepo() *socialUserRepo {
	return &socialUserRepo{nextID: 100, users: map[string]*User{}}
}

func (r *socialUserRepo) Create(_ context.Context, u *User) error {
	if _, ok := r.users[strings.ToLower(u.Email)]; ok {
		return ErrEmailExists
	}
	for _, s := range r.staged {
		if strings.EqualFold(s.Email, u.Email) {
			return ErrEmailExists
		}
	}
	r.nextID++
	u.ID = r.nextID
	if r.inTx {
		r.staged = append(r.staged, u)
		return nil
	}
	r.users[strings.ToLower(u.Email)] = u
	return nil
}

func (r *socialUserRepo) GetByEmail(_ context.Context, email string) (*User, error) {
	if u, ok := r.users[strings.ToLower(email)]; ok {
		return u, nil
	}
	return nil, ErrUserNotFound
}

func (r *socialUserRepo) GetByID(_ context.Context, id int64) (*User, error) {
	for _, u := range r.users {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, ErrUserNotFound
}

func (r *socialUserRepo) Update(_ context.Context, u *User) error {
	r.users[strings.ToLower(u.Email)] = u
	return nil
}

func (r *socialUserRepo) ExistsByEmail(_ context.Context, email string) (bool, error) {
	_, ok := r.users[strings.ToLower(email)]
	return ok, nil
}

func (r *socialUserRepo) runInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	r.inTx = true
	err := fn(ctx)
	r.inTx = false
	if err != nil {
		r.staged = nil // rollback
		return err
	}
	for _, u := range r.staged {
		r.users[strings.ToLower(u.Email)] = u
	}
	r.staged = nil
	return nil
}

type socialRedeemRepo struct {
	RedeemCodeRepository
	codes  map[string]*RedeemCode
	useErr error
	used   map[int64]int64
}

func (r *socialRedeemRepo) GetByCode(_ context.Context, code string) (*RedeemCode, error) {
	if c, ok := r.codes[code]; ok {
		cp := *c
		return &cp, nil
	}
	return nil, ErrRedeemCodeNotFound
}

func (r *socialRedeemRepo) Use(_ context.Context, id, userID int64) error {
	if r.useErr != nil {
		return r.useErr
	}
	for _, c := range r.codes {
		if c.ID == id {
			if c.Status != StatusUnused {
				return ErrRedeemCodeUsed
			}
			c.Status = StatusUsed
			r.used[id] = userID
			return nil
		}
	}
	return ErrRedeemCodeNotFound
}

type socialRefreshCache struct{ RefreshTokenCache }

func (socialRefreshCache) StoreRefreshToken(context.Context, string, *RefreshTokenData, time.Duration) error {
	return nil
}
func (socialRefreshCache) AddToUserTokenSet(context.Context, int64, string, time.Duration) error {
	return nil
}
func (socialRefreshCache) AddToFamilyTokenSet(context.Context, string, string, time.Duration) error {
	return nil
}

type socialTotpCache struct {
	TotpCache
	sessions map[string]*TotpLoginSession
}

func (c *socialTotpCache) SetLoginSession(_ context.Context, token string, s *TotpLoginSession, _ time.Duration) error {
	c.sessions[token] = s
	return nil
}

type memPendingStore struct {
	mu        sync.Mutex
	now       time.Time
	entries   map[string]memPendingEntry
	failures  map[string]int
	onConsume func()
}

func (m *memPendingStore) RecordFailure(_ context.Context, token string, maxFailures int, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failures == nil {
		m.failures = map[string]int{}
	}
	m.failures[token]++
	if m.failures[token] >= maxFailures {
		delete(m.entries, token)
		delete(m.failures, token)
		return true, nil
	}
	return false, nil
}

type memHandoffStore struct {
	mu      sync.Mutex
	entries map[string]SocialOAuthHandoff
}

func (m *memHandoffStore) Save(_ context.Context, code string, h *SocialOAuthHandoff, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[code] = *h
	return nil
}

func (m *memHandoffStore) Consume(_ context.Context, code string) (*SocialOAuthHandoff, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.entries[code]
	if !ok {
		return nil, nil
	}
	delete(m.entries, code)
	return &h, nil
}

type memPendingEntry struct {
	value   SocialOAuthPendingSignup
	expires time.Time
}

func newMemPendingStore() *memPendingStore {
	return &memPendingStore{now: time.Now(), entries: map[string]memPendingEntry{}}
}

func (m *memPendingStore) Save(_ context.Context, token string, p *SocialOAuthPendingSignup, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[token] = memPendingEntry{value: *p, expires: m.now.Add(ttl)}
	return nil
}

func (m *memPendingStore) get(token string, del bool) *SocialOAuthPendingSignup {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[token]
	if !ok || !m.now.Before(e.expires) {
		delete(m.entries, token)
		return nil
	}
	if del {
		delete(m.entries, token)
	}
	v := e.value
	return &v
}

func (m *memPendingStore) Peek(_ context.Context, token string) (*SocialOAuthPendingSignup, error) {
	return m.get(token, false), nil
}

func (m *memPendingStore) Consume(_ context.Context, token string) (*SocialOAuthPendingSignup, error) {
	v := m.get(token, true)
	if m.onConsume != nil {
		m.onConsume()
	}
	return v, nil
}

// ---- fixture --------------------------------------------------------------

type socialFixture struct {
	svc      *SocialOAuthService
	settings *socialSettingRepo
	users    *socialUserRepo
	redeem   *socialRedeemRepo
	pending  *memPendingStore
	totp     *socialTotpCache
	subs     *defaultSubscriptionAssignerStub
}

func newSocialFixture(t *testing.T, values map[string]string) *socialFixture {
	t.Helper()
	base := map[string]string{
		SettingKeyGoogleOAuthEnabled:      "true",
		SettingKeyGoogleOAuthClientID:     "gid",
		SettingKeyGoogleOAuthClientSecret: "gsecret",
		SettingKeyGitHubOAuthEnabled:      "true",
		SettingKeyGitHubOAuthClientID:     "hid",
		SettingKeyGitHubOAuthClientSecret: "hsecret",
		SettingKeyFrontendURL:             "https://aog.example.com",
		SettingKeyRegistrationEnabled:     "true",
	}
	for k, v := range values {
		base[k] = v
	}
	cfg := &config.Config{
		JWT:     config.JWTConfig{Secret: "social-test-secret", ExpireHour: 1, RefreshTokenExpireDays: 7},
		Default: config.DefaultConfig{UserBalance: 1, UserConcurrency: 2},
	}
	settingRepo := &socialSettingRepo{values: base}
	settingSvc := NewSettingService(settingRepo, cfg)
	users := newSocialUserRepo()
	redeem := &socialRedeemRepo{codes: map[string]*RedeemCode{
		"INVITE-OK": {ID: 7, Code: "INVITE-OK", Type: RedeemTypeInvitation, Status: StatusUnused},
	}, used: map[int64]int64{}}
	subs := &defaultSubscriptionAssignerStub{}
	auth := NewAuthService(nil, users, redeem, socialRefreshCache{}, cfg, settingSvc, nil, nil, nil, nil, subs)
	totpCache := &socialTotpCache{sessions: map[string]*TotpLoginSession{}}
	totp := NewTotpService(users, nil, totpCache, settingSvc, nil, nil)
	pending := newMemPendingStore()
	svc := NewSocialOAuthService(auth, settingSvc, totp, pending, &memHandoffStore{entries: map[string]SocialOAuthHandoff{}})
	svc.runInTx = users.runInTx
	return &socialFixture{svc: svc, settings: settingRepo, users: users, redeem: redeem, pending: pending, totp: totpCache, subs: subs}
}

func googleIdentity() SocialOAuthIdentity {
	return SocialOAuthIdentity{Provider: SocialOAuthProviderGoogle, Subject: "109876543210987654321", VerifiedEmail: "alice@gmail.com", Username: "Alice"}
}

func githubIdentity() SocialOAuthIdentity {
	return SocialOAuthIdentity{Provider: SocialOAuthProviderGitHub, Subject: "583231", VerifiedEmail: "octo@users.example.com", Username: "octocat"}
}

// ---- tests ----------------------------------------------------------------

func TestSocialOAuth_SignIn_HappyPathBothProviders(t *testing.T) {
	for _, id := range []SocialOAuthIdentity{googleIdentity(), githubIdentity()} {
		t.Run(string(id.Provider), func(t *testing.T) {
			f := newSocialFixture(t, nil)
			res, err := f.svc.SignIn(context.Background(), id)
			require.NoError(t, err)
			require.NotNil(t, res.TokenPair)
			require.NotEmpty(t, res.TokenPair.AccessToken)
			require.NotEmpty(t, res.TokenPair.RefreshToken)
			want := string(id.Provider) + "-" + id.Subject + id.Provider.SyntheticEmailDomain()
			require.Equal(t, want, res.User.Email)
			require.Equal(t, id.Username, res.User.Username)
			require.True(t, IsReservedOAuthEmail(res.User.Email))

			// Second sign-in logs into the same account.
			again, err := f.svc.SignIn(context.Background(), id)
			require.NoError(t, err)
			require.Equal(t, res.User.ID, again.User.ID)
			require.Len(t, f.users.users, 1)
		})
	}
}

func TestSocialOAuth_SignIn_NeverMatchesRealEmail(t *testing.T) {
	f := newSocialFixture(t, nil)
	local := &User{Email: "alice@gmail.com", Status: StatusActive, Role: RoleUser}
	require.NoError(t, f.users.Create(context.Background(), local))

	res, err := f.svc.SignIn(context.Background(), googleIdentity())
	require.NoError(t, err)
	require.NotEqual(t, local.ID, res.User.ID, "provider email must not log into the existing local account")
	require.Equal(t, "google-109876543210987654321@google-connect.invalid", res.User.Email)
}

func TestSocialOAuth_SignIn_InvalidSubjectOrEmail(t *testing.T) {
	f := newSocialFixture(t, nil)
	bad := []SocialOAuthIdentity{
		{Provider: SocialOAuthProviderGoogle, Subject: "", VerifiedEmail: "a@b.co"},
		{Provider: SocialOAuthProviderGoogle, Subject: "abc@def", VerifiedEmail: "a@b.co"},
		{Provider: SocialOAuthProviderGoogle, Subject: strings.Repeat("1", 58), VerifiedEmail: "a@b.co"},
		{Provider: SocialOAuthProviderGitHub, Subject: "octocat", VerifiedEmail: "a@b.co"},
		{Provider: SocialOAuthProviderGitHub, Subject: "0123", VerifiedEmail: "a@b.co"},
		{Provider: SocialOAuthProviderGitHub, Subject: "-5", VerifiedEmail: "a@b.co"},
		{Provider: "linuxdo", Subject: "123", VerifiedEmail: "a@b.co"},
	}
	for _, id := range bad {
		_, err := f.svc.SignIn(context.Background(), id)
		require.ErrorIs(t, err, ErrSocialOAuthIdentityInvalid, "subject %q", id.Subject)
	}
	for _, email := range []string{"", "not-an-email", "Alice <a@b.co>", "x@linuxdo-connect.invalid", "x@google-connect.invalid"} {
		id := googleIdentity()
		id.VerifiedEmail = email
		_, err := f.svc.SignIn(context.Background(), id)
		require.ErrorIs(t, err, ErrSocialOAuthEmailNotVerified, "email %q", email)
	}
	require.Empty(t, f.users.users)
}

func TestSocialOAuth_SubjectValidation(t *testing.T) {
	require.True(t, IsSafeSocialOAuthSubject(SocialOAuthProviderGoogle, "109876543210987654321"))
	require.True(t, IsSafeSocialOAuthSubject(SocialOAuthProviderGoogle, "abc_DEF-123"))
	require.False(t, IsSafeSocialOAuthSubject(SocialOAuthProviderGoogle, " 123"))
	require.False(t, IsSafeSocialOAuthSubject(SocialOAuthProviderGoogle, "a.b"))
	require.True(t, IsSafeSocialOAuthSubject(SocialOAuthProviderGitHub, "9223372036854775807"))
	require.False(t, IsSafeSocialOAuthSubject(SocialOAuthProviderGitHub, "92233720368547758070"))
	require.False(t, IsSafeSocialOAuthSubject(SocialOAuthProviderGitHub, "1e5"))
}

func TestSocialOAuth_RegistrationClosed_NewVsExisting(t *testing.T) {
	f := newSocialFixture(t, nil)
	existing, err := f.svc.SignIn(context.Background(), googleIdentity())
	require.NoError(t, err)

	f.settings.set(SettingKeyRegistrationEnabled, "false")

	// Existing account still signs in (same rule as LinuxDo).
	res, err := f.svc.SignIn(context.Background(), googleIdentity())
	require.NoError(t, err)
	require.Equal(t, existing.User.ID, res.User.ID)

	// New identity is refused.
	_, err = f.svc.SignIn(context.Background(), githubIdentity())
	require.ErrorIs(t, err, ErrRegDisabled)
	require.Len(t, f.users.users, 1)
}

func TestSocialOAuth_SettingsReadFailure_FailsClosed(t *testing.T) {
	f := newSocialFixture(t, nil)
	f.settings.err = errors.New("db down")
	_, err := f.svc.SignIn(context.Background(), googleIdentity())
	require.Error(t, err)
	require.Empty(t, f.users.users)

	// Config readable but signup policy not: still refused.
	f2 := newSocialFixture(t, nil)
	f2.svc.settings = NewSettingService(&policyFailRepo{socialSettingRepo: f2.settings}, f2.svc.auth.cfg)
	_, err = f2.svc.SignIn(context.Background(), googleIdentity())
	require.ErrorIs(t, err, ErrServiceUnavailable)
	require.Empty(t, f2.users.users)
}

// policyFailRepo answers provider-config reads but fails the signup-policy read.
type policyFailRepo struct{ *socialSettingRepo }

func (r *policyFailRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	for _, k := range keys {
		if k == SettingKeyRegistrationEnabled {
			return nil, errors.New("policy read failed")
		}
	}
	return r.socialSettingRepo.GetMultiple(ctx, keys)
}

func TestSocialOAuth_EmailSuffixWhitelistUsesVerifiedEmail(t *testing.T) {
	f := newSocialFixture(t, map[string]string{SettingKeyRegistrationEmailSuffixWhitelist: `["@corp.example"]`})
	_, err := f.svc.SignIn(context.Background(), googleIdentity())
	require.Equal(t, "EMAIL_SUFFIX_NOT_ALLOWED", infraerrors.Reason(err))

	id := googleIdentity()
	id.VerifiedEmail = "alice@corp.example"
	res, err := f.svc.SignIn(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, res.TokenPair)
}

func TestSocialOAuth_Invitation_PendingFlow(t *testing.T) {
	f := newSocialFixture(t, map[string]string{SettingKeyInvitationCodeEnabled: "true"})
	ctx := context.Background()

	res, err := f.svc.SignIn(ctx, githubIdentity())
	require.NoError(t, err)
	require.Len(t, res.PendingToken, 64)
	require.Nil(t, res.TokenPair)
	require.Empty(t, f.users.users)

	// Missing / wrong code: rejected, session kept so the user can retry.
	_, err = f.svc.CompleteRegistration(ctx, SocialOAuthProviderGitHub, res.PendingToken, "")
	require.ErrorIs(t, err, ErrInvitationCodeRequired)
	_, err = f.svc.CompleteRegistration(ctx, SocialOAuthProviderGitHub, res.PendingToken, "WRONG")
	require.ErrorIs(t, err, ErrInvitationCodeInvalid)
	require.Empty(t, f.users.users)

	// Cross-provider use of the handle is refused.
	_, err = f.svc.CompleteRegistration(ctx, SocialOAuthProviderGoogle, res.PendingToken, "INVITE-OK")
	require.ErrorIs(t, err, ErrSocialOAuthPendingInvalid)

	done, err := f.svc.CompleteRegistration(ctx, SocialOAuthProviderGitHub, res.PendingToken, "INVITE-OK")
	require.NoError(t, err)
	require.NotNil(t, done.TokenPair)
	require.Equal(t, "github-583231@github-connect.invalid", done.User.Email)
	require.Equal(t, done.User.ID, f.redeem.used[7])

	// Single use.
	_, err = f.svc.CompleteRegistration(ctx, SocialOAuthProviderGitHub, res.PendingToken, "INVITE-OK")
	require.ErrorIs(t, err, ErrSocialOAuthPendingInvalid)
	require.Len(t, f.users.users, 1)
}

func TestSocialOAuth_Invitation_Expired(t *testing.T) {
	f := newSocialFixture(t, map[string]string{SettingKeyInvitationCodeEnabled: "true"})
	res, err := f.svc.SignIn(context.Background(), googleIdentity())
	require.NoError(t, err)
	f.pending.now = f.pending.now.Add(SocialOAuthPendingTTL + time.Second)
	_, err = f.svc.CompleteRegistration(context.Background(), SocialOAuthProviderGoogle, res.PendingToken, "INVITE-OK")
	require.ErrorIs(t, err, ErrSocialOAuthPendingInvalid)
	require.Empty(t, f.users.users)
}

func TestSocialOAuth_Invitation_ProviderDisabledBeforeComplete(t *testing.T) {
	f := newSocialFixture(t, map[string]string{SettingKeyInvitationCodeEnabled: "true"})
	res, err := f.svc.SignIn(context.Background(), googleIdentity())
	require.NoError(t, err)
	f.settings.set(SettingKeyGoogleOAuthEnabled, "false")
	_, err = f.svc.CompleteRegistration(context.Background(), SocialOAuthProviderGoogle, res.PendingToken, "INVITE-OK")
	require.ErrorIs(t, err, ErrSocialOAuthDisabled)
	require.Empty(t, f.users.users)
	require.Empty(t, f.redeem.used)
}

func TestSocialOAuth_Invitation_RegistrationClosedBeforeComplete(t *testing.T) {
	f := newSocialFixture(t, map[string]string{SettingKeyInvitationCodeEnabled: "true"})
	res, err := f.svc.SignIn(context.Background(), googleIdentity())
	require.NoError(t, err)
	f.settings.set(SettingKeyRegistrationEnabled, "false")
	_, err = f.svc.CompleteRegistration(context.Background(), SocialOAuthProviderGoogle, res.PendingToken, "INVITE-OK")
	require.ErrorIs(t, err, ErrRegDisabled)
	require.Empty(t, f.users.users)
}

func TestSocialOAuth_Invitation_PolicyChangeDuringComplete(t *testing.T) {
	for name, change := range map[string][2]string{
		"provider disabled":   {SettingKeyGoogleOAuthEnabled, "false"},
		"registration closed": {SettingKeyRegistrationEnabled, "false"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSocialFixture(t, map[string]string{SettingKeyInvitationCodeEnabled: "true"})
			res, err := f.svc.SignIn(context.Background(), googleIdentity())
			require.NoError(t, err)
			// The change lands after the pre-consume checks passed.
			f.pending.onConsume = func() { f.settings.set(change[0], change[1]) }
			_, err = f.svc.CompleteRegistration(context.Background(), SocialOAuthProviderGoogle, res.PendingToken, "INVITE-OK")
			require.Error(t, err)
			require.Empty(t, f.users.users)
			require.Empty(t, f.redeem.used)
		})
	}

	// Invitation switched on while a no-invitation completion is in flight.
	f := newSocialFixture(t, map[string]string{SettingKeyInvitationCodeEnabled: "true"})
	res, err := f.svc.SignIn(context.Background(), googleIdentity())
	require.NoError(t, err)
	f.settings.set(SettingKeyInvitationCodeEnabled, "false")
	f.pending.onConsume = func() { f.settings.set(SettingKeyInvitationCodeEnabled, "true") }
	_, err = f.svc.CompleteRegistration(context.Background(), SocialOAuthProviderGoogle, res.PendingToken, "")
	require.ErrorIs(t, err, ErrSocialOAuthPendingInvalid, "consumed session: not a retryable code error")
	require.Empty(t, f.users.users)

	// Replaying the consumed handle after the settings settle still fails, nothing claimed.
	f.pending.onConsume = nil
	_, err = f.svc.CompleteRegistration(context.Background(), SocialOAuthProviderGoogle, res.PendingToken, "INVITE-OK")
	require.ErrorIs(t, err, ErrSocialOAuthPendingInvalid)
	require.Empty(t, f.redeem.used)
}

func TestSocialOAuth_Invitation_ClaimFailureRollsBackUser(t *testing.T) {
	f := newSocialFixture(t, map[string]string{SettingKeyInvitationCodeEnabled: "true"})
	res, err := f.svc.SignIn(context.Background(), googleIdentity())
	require.NoError(t, err)
	f.redeem.useErr = ErrRedeemCodeUsed // claimed concurrently by someone else
	_, err = f.svc.CompleteRegistration(context.Background(), SocialOAuthProviderGoogle, res.PendingToken, "INVITE-OK")
	require.ErrorIs(t, err, ErrSocialOAuthPendingInvalid, "session already consumed: not retryable")
	require.Empty(t, f.users.users, "user creation must not stick when the invitation cannot be claimed")
}

func TestSocialOAuth_Invitation_NoTxClientFailsClosed(t *testing.T) {
	f := newSocialFixture(t, map[string]string{SettingKeyInvitationCodeEnabled: "true"})
	f.svc.runInTx = f.svc.entTx // production path with a nil ent client
	res, err := f.svc.SignIn(context.Background(), googleIdentity())
	require.NoError(t, err)
	_, err = f.svc.CompleteRegistration(context.Background(), SocialOAuthProviderGoogle, res.PendingToken, "INVITE-OK")
	require.ErrorIs(t, err, ErrServiceUnavailable)
	require.Empty(t, f.users.users)
}

func TestSocialOAuth_CompleteRejectsMalformedToken(t *testing.T) {
	f := newSocialFixture(t, map[string]string{SettingKeyInvitationCodeEnabled: "true"})
	for _, tok := range []string{"", "abc", strings.Repeat("Z", 64), strings.Repeat("A", 64)} {
		_, err := f.svc.CompleteRegistration(context.Background(), SocialOAuthProviderGoogle, tok, "INVITE-OK")
		require.ErrorIs(t, err, ErrSocialOAuthPendingInvalid)
	}
}

func TestSocialOAuth_TOTPUserGoesTo2FA(t *testing.T) {
	f := newSocialFixture(t, map[string]string{SettingKeyTotpEnabled: "true"})
	first, err := f.svc.SignIn(context.Background(), googleIdentity())
	require.NoError(t, err)
	first.User.TotpEnabled = true

	res, err := f.svc.SignIn(context.Background(), googleIdentity())
	require.NoError(t, err)
	require.True(t, res.Requires2FA)
	require.NotEmpty(t, res.TempToken)
	require.Nil(t, res.TokenPair, "no tokens before the second factor")
	require.Equal(t, first.User.ID, f.totp.sessions[res.TempToken].UserID)

	// Site-wide switch off: same as password login, no second factor.
	f.settings.set(SettingKeyTotpEnabled, "false")
	res, err = f.svc.SignIn(context.Background(), googleIdentity())
	require.NoError(t, err)
	require.False(t, res.Requires2FA)
	require.NotNil(t, res.TokenPair)
}

func TestSocialOAuth_TOTPSettingReadFailure_Refuses(t *testing.T) {
	f := newSocialFixture(t, map[string]string{SettingKeyTotpEnabled: "true"})
	first, err := f.svc.SignIn(context.Background(), googleIdentity())
	require.NoError(t, err)
	first.User.TotpEnabled = true
	f.svc.settings = NewSettingService(&totpFailRepo{socialSettingRepo: f.settings}, f.svc.auth.cfg)
	_, err = f.svc.SignIn(context.Background(), googleIdentity())
	require.ErrorIs(t, err, ErrServiceUnavailable)
}

type totpFailRepo struct{ *socialSettingRepo }

func (r *totpFailRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if len(keys) == 1 && keys[0] == SettingKeyTotpEnabled {
		return nil, errors.New("totp read failed")
	}
	return r.socialSettingRepo.GetMultiple(ctx, keys)
}

func TestSocialOAuth_DisabledUserRefused(t *testing.T) {
	f := newSocialFixture(t, nil)
	first, err := f.svc.SignIn(context.Background(), githubIdentity())
	require.NoError(t, err)
	first.User.Status = StatusDisabled
	_, err = f.svc.SignIn(context.Background(), githubIdentity())
	require.ErrorIs(t, err, ErrUserNotActive)
}

func TestSocialOAuth_ProviderDisabled(t *testing.T) {
	f := newSocialFixture(t, map[string]string{SettingKeyGitHubOAuthEnabled: "false"})
	_, err := f.svc.SignIn(context.Background(), githubIdentity())
	require.ErrorIs(t, err, ErrSocialOAuthDisabled)
}

func TestGetSocialOAuthConfig(t *testing.T) {
	f := newSocialFixture(t, map[string]string{SettingKeyFrontendURL: "https://aog.example.com/"})
	cfg, err := f.svc.Config(context.Background(), SocialOAuthProviderGoogle)
	require.NoError(t, err)
	require.Equal(t, "https://aog.example.com/api/v1/auth/oauth/google/callback", cfg.RedirectURL)
	require.Equal(t, "/auth/oauth/google/callback", cfg.FrontendCallbackURL)

	// Off by default: no keys at all.
	empty := NewSettingService(&socialSettingRepo{values: map[string]string{}}, &config.Config{})
	_, err = empty.GetSocialOAuthConfig(context.Background(), SocialOAuthProviderGitHub)
	require.ErrorIs(t, err, ErrSocialOAuthDisabled)

	// Enabled but no frontend URL / secret: refused.
	f.settings.set(SettingKeyFrontendURL, "")
	_, err = f.svc.Config(context.Background(), SocialOAuthProviderGoogle)
	require.ErrorIs(t, err, ErrSocialOAuthConfigInvalid)
	f.settings.set(SettingKeyFrontendURL, "https://aog.example.com?x=1")
	_, err = f.svc.Config(context.Background(), SocialOAuthProviderGoogle)
	require.ErrorIs(t, err, ErrSocialOAuthConfigInvalid)
	f.settings.set(SettingKeyFrontendURL, "https://aog.example.com")
	f.settings.set(SettingKeyGitHubOAuthClientSecret, "")
	_, err = f.svc.Config(context.Background(), SocialOAuthProviderGitHub)
	require.ErrorIs(t, err, ErrSocialOAuthConfigInvalid)

	// Read failure fails closed.
	f.settings.err = errors.New("db down")
	_, err = f.svc.Config(context.Background(), SocialOAuthProviderGoogle)
	require.Error(t, err)
}

func TestPublicSettings_SocialFlagsOnlyNoSecrets(t *testing.T) {
	f := newSocialFixture(t, map[string]string{SettingKeyGitHubOAuthEnabled: "false"})
	pub, err := f.svc.settings.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.True(t, pub.GoogleOAuthEnabled)
	require.False(t, pub.GitHubOAuthEnabled)

	injected, err := f.svc.settings.GetPublicSettingsForInjection(context.Background())
	require.NoError(t, err)
	raw, err := json.Marshal(injected)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"google_oauth_enabled":true`)
	require.Contains(t, string(raw), `"github_oauth_enabled":false`)
	for _, leak := range []string{"gsecret", "hsecret", "client_secret", "client_id", `"gid"`} {
		require.NotContains(t, string(raw), leak)
	}

	empty := NewSettingService(&socialSettingRepo{values: map[string]string{}}, &config.Config{})
	pub, err = empty.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.False(t, pub.GoogleOAuthEnabled, "off by default")
	require.False(t, pub.GitHubOAuthEnabled, "off by default")
}

// ---- reserved synthetic domains -------------------------------------------

func TestReservedOAuthDomains_RejectedEverywhere(t *testing.T) {
	reserved := []string{
		"linuxdo-1@linuxdo-connect.invalid",
		"google-123@google-connect.invalid",
		"GitHub-1@GITHUB-CONNECT.INVALID",
		"anything@github-connect.invalid",
	}
	ctx := context.Background()
	for _, email := range reserved {
		require.True(t, isReservedEmail(email), email)

		// Normal registration.
		repo := &userRepoStub{}
		svc := newAuthService(repo, map[string]string{SettingKeyRegistrationEnabled: "true"}, nil)
		_, _, err := svc.Register(ctx, email, "password")
		require.ErrorIs(t, err, ErrEmailReserved, email)

		// Verification code request.
		_, err = svc.SendVerifyCodeAsync(ctx, email)
		require.ErrorIs(t, err, ErrEmailReserved, email)

		// Self-service email change to a synthetic address.
		users := newSocialUserRepo()
		u := &User{Email: "real@example.com", Status: StatusActive}
		require.NoError(t, users.Create(ctx, u))
		us := NewUserService(users, nil, nil)
		e := email
		_, err = us.UpdateProfile(ctx, u.ID, UpdateProfileRequest{Email: &e})
		require.ErrorIs(t, err, ErrEmailReserved, email)

		// Admin create / edit to a synthetic address.
		admin := &adminServiceImpl{userRepo: users}
		_, err = admin.CreateUser(ctx, &CreateUserInput{Email: email, Password: "password123"})
		require.ErrorIs(t, err, ErrEmailReserved, email)
		_, err = admin.UpdateUser(ctx, u.ID, &UpdateUserInput{Email: email})
		require.ErrorIs(t, err, ErrEmailReserved, email)
		require.Equal(t, "real@example.com", u.Email)
	}
	require.False(t, isReservedEmail("someone@google.com"))
	require.False(t, isReservedEmail("someone@github.com"))
}

func TestReservedOAuthDomains_SelfServiceCannotLeaveSyntheticEmail(t *testing.T) {
	ctx := context.Background()
	users := newSocialUserRepo()
	u := &User{Email: "google-123@google-connect.invalid", Status: StatusActive}
	require.NoError(t, users.Create(ctx, u))
	us := NewUserService(users, nil, nil)
	newEmail := "victim@example.com"
	_, err := us.UpdateProfile(ctx, u.ID, UpdateProfileRequest{Email: &newEmail})
	require.ErrorIs(t, err, ErrEmailReserved)

	// Unchanged email (e.g. username-only edit that echoes the email) still works.
	same := u.Email
	name := "new name"
	_, err = us.UpdateProfile(ctx, u.ID, UpdateProfileRequest{Email: &same, Username: &name})
	require.NoError(t, err)
}

func TestSocialOAuth_Invitation_WrongCodeBudget(t *testing.T) {
	f := newSocialFixture(t, map[string]string{SettingKeyInvitationCodeEnabled: "true"})
	ctx := context.Background()
	res, err := f.svc.SignIn(ctx, googleIdentity())
	require.NoError(t, err)
	for i := 1; i < socialOAuthMaxInvitationFailures; i++ {
		_, err = f.svc.CompleteRegistration(ctx, SocialOAuthProviderGoogle, res.PendingToken, "WRONG")
		require.ErrorIs(t, err, ErrInvitationCodeInvalid, "attempt %d", i)
	}
	_, err = f.svc.CompleteRegistration(ctx, SocialOAuthProviderGoogle, res.PendingToken, "WRONG")
	require.ErrorIs(t, err, ErrSocialOAuthPendingInvalid, "budget spent: session invalidated")
	_, err = f.svc.CompleteRegistration(ctx, SocialOAuthProviderGoogle, res.PendingToken, "INVITE-OK")
	require.ErrorIs(t, err, ErrSocialOAuthPendingInvalid)
	require.Empty(t, f.users.users)
	require.Empty(t, f.redeem.used)
}

func TestSocialOAuth_NewUserDefaultsReadStrictly(t *testing.T) {
	f := newSocialFixture(t, map[string]string{
		SettingKeyDefaultBalance:       "7.5",
		SettingKeyDefaultConcurrency:   "4",
		SettingKeyDefaultSubscriptions: `[{"group_id":3,"validity_days":30}]`,
	})
	res, err := f.svc.SignIn(context.Background(), githubIdentity())
	require.NoError(t, err)
	require.Equal(t, 7.5, res.User.Balance)
	require.Equal(t, 4, res.User.Concurrency)
	require.Len(t, f.subs.calls, 1)
	require.Equal(t, int64(3), f.subs.calls[0].GroupID)

	// A failure reading those keys refuses the sign-up rather than granting config defaults.
	f2 := newSocialFixture(t, nil)
	f2.svc.settings = NewSettingService(&keyFailRepo{socialSettingRepo: f2.settings, key: SettingKeyDefaultBalance}, f2.svc.auth.cfg)
	_, err = f2.svc.SignIn(context.Background(), githubIdentity())
	require.ErrorIs(t, err, ErrServiceUnavailable)
	require.Empty(t, f2.users.users)
}

// keyFailRepo fails any multi-read that includes key.
type keyFailRepo struct {
	*socialSettingRepo
	key string
}

func (r *keyFailRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	for _, k := range keys {
		if k == r.key {
			return nil, errors.New("read failed")
		}
	}
	return r.socialSettingRepo.GetMultiple(ctx, keys)
}

func TestSocialOAuthRedirectURL_OriginOnly(t *testing.T) {
	require.Equal(t, "https://a.example/api/v1/auth/oauth/github/callback", SocialOAuthRedirectURL("https://a.example", SocialOAuthProviderGitHub))
	require.Equal(t, "https://a.example/api/v1/auth/oauth/github/callback", SocialOAuthRedirectURL("https://a.example/", SocialOAuthProviderGitHub))
	require.Equal(t, "http://a.example:8080/api/v1/auth/oauth/google/callback", SocialOAuthRedirectURL("http://a.example:8080", SocialOAuthProviderGoogle))
	for _, bad := range []string{"", "a.example", "ftp://a.example", "https://a.example/app", "https://a.example/app/", "https://a.example?x=1", "https://a.example#f", "https://u:p@a.example"} {
		require.Empty(t, SocialOAuthRedirectURL(bad, SocialOAuthProviderGoogle), bad)
	}
}

func TestUpdateSettings_RecordsSocialOAuthEverEnabled(t *testing.T) {
	f := newSocialFixture(t, nil)
	settings := &SystemSettings{GoogleOAuthEnabled: true, GoogleOAuthClientID: "gid", FrontendURL: "https://aog.example.com"}
	require.NoError(t, f.svc.settings.UpdateSettings(context.Background(), settings))
	require.Equal(t, "true", f.settings.values[SettingKeyGoogleOAuthEverEnabled])
	_, wrote := f.settings.values[SettingKeyGitHubOAuthEverEnabled]
	require.False(t, wrote, "only set when the provider is enabled")

	// Disabling never clears the marker.
	settings.GoogleOAuthEnabled = false
	require.NoError(t, f.svc.settings.UpdateSettings(context.Background(), settings))
	require.Equal(t, "true", f.settings.values[SettingKeyGoogleOAuthEverEnabled])
	all, err := f.svc.settings.GetAllSettings(context.Background())
	require.NoError(t, err)
	require.True(t, all.GoogleOAuthEverEnabled)
}
