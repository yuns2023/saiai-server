//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/stretchr/testify/require"
)

const hostedTestAccessToken = "sk-ant-oat01-" + "HOSTEDTESTSECRET"

// ---- fakes ----

type hostedFakeSettings struct {
	SettingRepository
	values map[string]string
}

func (f *hostedFakeSettings) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := f.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

type hostedFakeProxies struct {
	ProxyRepository
	proxies map[int64]*Proxy
}

func (f *hostedFakeProxies) GetByID(_ context.Context, id int64) (*Proxy, error) {
	if p, ok := f.proxies[id]; ok {
		return p, nil
	}
	return nil, ErrProxyNotFound
}

// hostedFakeStore mirrors the Redis store semantics (one-time, owner +
// purpose + state + expiry checked; mismatches keep the session).
type hostedFakeStore struct {
	mu       sync.Mutex
	sessions map[string]*HostedOnboardSession
	authURL  int64
	submit   int64
	consumed int
}

func newHostedFakeStore() *hostedFakeStore {
	return &hostedFakeStore{sessions: map[string]*HostedOnboardSession{}}
}

func (s *hostedFakeStore) SaveSession(_ context.Context, id string, sess *HostedOnboardSession, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *sess
	s.sessions[id] = &cp
	return nil
}

func (s *hostedFakeStore) ConsumeSession(_ context.Context, id string, userID int64, purpose, state string, now time.Time) (*HostedOnboardSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok || sess.UserID != userID || sess.Purpose != purpose || sess.State != state {
		return nil, nil
	}
	delete(s.sessions, id)
	if !now.Before(sess.ExpiresAt) {
		return nil, nil
	}
	s.consumed++
	return sess, nil
}

func (s *hostedFakeStore) CountAuthURL(context.Context, int64, time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authURL++
	return s.authURL, nil
}

func (s *hostedFakeStore) CountSubmit(context.Context, int64, time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.submit++
	return s.submit, nil
}

type hostedFakeRepo struct {
	pending   int
	created   []*HostedApplicationCreate
	createErr error
	cancelled []int64
	rejected  []int64
	approves  []HostedApproveParams
	states    []string
	getApp    *HostedApplication
}

func (r *hostedFakeRepo) CountPending(context.Context, int64) (int, error) { return r.pending, nil }
func (r *hostedFakeRepo) CreatePending(_ context.Context, in *HostedApplicationCreate) (*HostedApplication, error) {
	if r.createErr != nil {
		return nil, r.createErr
	}
	r.created = append(r.created, in)
	app := &HostedApplication{ID: int64(len(r.created)), UserID: in.UserID, Status: HostedApplicationStatusPending,
		DisplayName: in.DisplayName, UpstreamAccountUUID: in.UpstreamAccountUUID, UpstreamEmail: in.UpstreamEmail}
	if in.Proxy != nil {
		app.ProxyProtocol, app.ProxyHost, app.ProxyPort = in.Proxy.Protocol, in.Proxy.Host, in.Proxy.Port
		app.HasProxyAuth = in.Proxy.Username != ""
	}
	return app, nil
}
func (r *hostedFakeRepo) ListByUser(context.Context, int64, int) ([]HostedApplication, error) {
	return []HostedApplication{}, nil
}
func (r *hostedFakeRepo) Get(context.Context, int64) (*HostedApplication, error) {
	if r.getApp == nil {
		return nil, ErrSelfOnboardNotFound
	}
	return r.getApp, nil
}
func (r *hostedFakeRepo) ListAdmin(context.Context, string, int, int) ([]HostedApplication, int64, error) {
	return []HostedApplication{}, 0, nil
}
func (r *hostedFakeRepo) Cancel(_ context.Context, _, id int64) (*HostedApplication, error) {
	r.cancelled = append(r.cancelled, id)
	return &HostedApplication{ID: id, Status: HostedApplicationStatusCancelled}, nil
}
func (r *hostedFakeRepo) Reject(_ context.Context, _, id int64, reason string) (*HostedApplication, error) {
	r.rejected = append(r.rejected, id)
	return &HostedApplication{ID: id, Status: HostedApplicationStatusRejected, RejectReason: reason}, nil
}
func (r *hostedFakeRepo) Approve(_ context.Context, p HostedApproveParams) (*HostedApproveResult, error) {
	r.approves = append(r.approves, p)
	return &HostedApproveResult{Application: &HostedApplication{ID: p.ApplicationID, Status: HostedApplicationStatusApproved}, AccountID: 1, GroupID: 2}, nil
}
func (r *hostedFakeRepo) ExpirePending(context.Context, time.Time, int) (int64, error) { return 0, nil }
func (r *hostedFakeRepo) SetOwnerAccountState(_ context.Context, _, id int64, action string) (*HostedOwnedAccountState, error) {
	r.states = append(r.states, action)
	return &HostedOwnedAccountState{AccountID: id, OwnerPaused: action == HostedOwnerActionPause}, nil
}

type hostedFakeOAuth struct {
	ClaudeOAuthClient
	resp     *oauth.TokenResponse
	err      error
	calls    int
	proxyURL string
	code     string
}

func (f *hostedFakeOAuth) ExchangeCodeForToken(_ context.Context, code, _, _, proxyURL string, isSetupToken bool) (*oauth.TokenResponse, error) {
	f.calls++
	f.proxyURL = proxyURL
	f.code = code
	if !isSetupToken {
		return nil, errors.New("expected setup token exchange")
	}
	return f.resp, f.err
}

type hostedFakeResolver struct {
	addrs map[string][]net.IPAddr
}

func (r *hostedFakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if a, ok := r.addrs[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host proxy.internal.example")
}

func hostedGoodToken() *oauth.TokenResponse {
	return &oauth.TokenResponse{
		AccessToken:  hostedTestAccessToken,
		TokenType:    "Bearer",
		ExpiresIn:    31536000,
		Scope:        "user:inference",
		Organization: &oauth.OrgInfo{UUID: "org-1"},
		Account:      &oauth.AccountInfo{UUID: "0B6B8F86-0D9B-4F4E-9F55-6A1F5D7C0A11", EmailAddress: "owner@example.com"},
	}
}

type hostedHarness struct {
	svc      *HostedSelfOnboardService
	store    *hostedFakeStore
	repo     *hostedFakeRepo
	oauth    *hostedFakeOAuth
	settings *hostedFakeSettings
	now      time.Time
}

func newHostedHarness(t *testing.T) *hostedHarness {
	t.Helper()
	h := &hostedHarness{
		store:    newHostedFakeStore(),
		repo:     &hostedFakeRepo{},
		oauth:    &hostedFakeOAuth{resp: hostedGoodToken()},
		settings: &hostedFakeSettings{values: map[string]string{SettingKeyHostedSelfOnboardEnabled: "true", SettingKeyHostedSelfOnboardExchangeProxyID: "7"}},
		now:      time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC),
	}
	proxies := &hostedFakeProxies{proxies: map[int64]*Proxy{
		7: {ID: 7, Protocol: "socks5h", Host: "203.0.113.9", Port: 1080, Status: StatusActive},
		8: {ID: 8, Protocol: "http", Host: "198.51.100.8", Port: 3128, Status: "disabled"},
	}}
	h.svc = NewHostedSelfOnboardService(h.repo, h.store, h.settings, proxies, h.oauth, nil, nil)
	h.svc.now = func() time.Time { return h.now }
	h.svc.resolver = &hostedFakeResolver{addrs: map[string][]net.IPAddr{
		"proxy.example.com":  {{IP: net.ParseIP("93.184.216.34")}},
		"mixed.example.com":  {{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("10.0.0.5")}},
		"mapped.example.com": {{IP: net.ParseIP("::ffff:127.0.0.1")}},
		"zoned.example.com":  {{IP: net.ParseIP("fe80::1"), Zone: "eth0"}},
	}}
	seq := 0
	h.svc.newSessionID = func() (string, error) {
		seq++
		return "sess" + strings.Repeat("0", 27) + string(rune('a'+seq)), nil
	}
	return h
}

// authorize returns a session id and the matching pasted code.
func (h *hostedHarness) authorize(t *testing.T, userID int64) (string, string) {
	t.Helper()
	out, err := h.svc.CreateAuthURL(context.Background(), userID)
	require.NoError(t, err)
	sess := h.store.sessions[out.SessionID]
	require.NotNil(t, sess)
	require.Contains(t, out.AuthURL, "state="+sess.State)
	require.Equal(t, h.now.Add(10*time.Minute), out.ExpiresAt)
	return out.SessionID, "authcode123#" + sess.State
}

func hostedReason(err error) string {
	return infraerrors.FromError(err).Reason
}

// ---- tests ----

func TestHostedSubmitHappyPathUsesPlatformProxyAndNeverReturnsToken(t *testing.T) {
	h := newHostedHarness(t)
	sid, code := h.authorize(t, 5)
	app, err := h.svc.Submit(context.Background(), 5, HostedSubmitInput{SessionID: sid, Code: code, DisplayName: "  my claude  "})
	require.NoError(t, err)
	require.Equal(t, "my claude", app.DisplayName)
	require.Equal(t, "socks5h://203.0.113.9:1080", h.oauth.proxyURL, "no user proxy → platform exchange proxy")
	require.Equal(t, code, h.oauth.code)
	require.Len(t, h.repo.created, 1)
	created := h.repo.created[0]
	require.Equal(t, "0b6b8f86-0d9b-4f4e-9f55-6a1f5d7c0a11", created.UpstreamAccountUUID, "identity normalized")
	require.Equal(t, hostedTestAccessToken, created.Credentials["access_token"])
	require.Equal(t, h.now.Unix()+31536000, created.Credentials["expires_at"])
	require.Equal(t, HostedSelfOnboardMaxPending, created.MaxPending)

	// The session is single-use.
	_, err = h.svc.Submit(context.Background(), 5, HostedSubmitInput{SessionID: sid, Code: code, DisplayName: "x"})
	require.Equal(t, "SELF_ONBOARD_SESSION_INVALID", hostedReason(err))
	require.Equal(t, 1, h.oauth.calls)
}

func TestHostedSubmitUserProxyResolvedAndUsed(t *testing.T) {
	h := newHostedHarness(t)
	sid, code := h.authorize(t, 5)
	_, err := h.svc.Submit(context.Background(), 5, HostedSubmitInput{SessionID: sid, Code: code, DisplayName: "n",
		Proxy: &HostedProxyInput{Protocol: "SOCKS5H", Host: "Proxy.Example.com.", Port: 1080, Username: "u", Password: "p@ss"}})
	require.NoError(t, err)
	require.Equal(t, "socks5h://u:p%40ss@proxy.example.com:1080", h.oauth.proxyURL)
	require.Equal(t, "proxy.example.com", h.repo.created[0].Proxy.Host)
}

func TestHostedSubmitSessionChecks(t *testing.T) {
	h := newHostedHarness(t)
	sid, code := h.authorize(t, 5)
	ctx := context.Background()

	// Another user cannot use (or burn) the session.
	_, err := h.svc.Submit(ctx, 6, HostedSubmitInput{SessionID: sid, Code: code, DisplayName: "n"})
	require.Equal(t, "SELF_ONBOARD_SESSION_INVALID", hostedReason(err))
	// Wrong state: refused, session kept.
	_, err = h.svc.Submit(ctx, 5, HostedSubmitInput{SessionID: sid, Code: "authcode123#otherstate", DisplayName: "n"})
	require.Equal(t, "SELF_ONBOARD_SESSION_INVALID", hostedReason(err))
	require.Zero(t, h.oauth.calls)
	// Expired.
	h.now = h.now.Add(10*time.Minute + time.Second)
	_, err = h.svc.Submit(ctx, 5, HostedSubmitInput{SessionID: sid, Code: code, DisplayName: "n"})
	require.Equal(t, "SELF_ONBOARD_SESSION_INVALID", hostedReason(err))
	require.Zero(t, h.oauth.calls)

	// Malformed pasted codes never reach the store.
	counted := h.store.submit
	for _, bad := range []string{"", "nostate", "#state", "code#", "co de#state", "code#st\nate", "a#b#c"} {
		_, err = h.svc.Submit(ctx, 5, HostedSubmitInput{SessionID: "abc", Code: bad, DisplayName: "n"})
		require.Equal(t, "SELF_ONBOARD_SESSION_INVALID", hostedReason(err), bad)
	}
	require.Equal(t, counted, h.store.submit, "format errors are not counted")
}

func TestHostedSubmitChecksBeforeConsumingSession(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		setup  func(h *hostedHarness, in *HostedSubmitInput)
		reason string
	}{
		{"pending limit", func(h *hostedHarness, _ *HostedSubmitInput) { h.repo.pending = 3 }, "SELF_ONBOARD_PENDING_LIMIT"},
		{"rate limited", func(h *hostedHarness, _ *HostedSubmitInput) { h.store.submit = 10 }, "SELF_ONBOARD_RATE_LIMITED"},
		{"no proxy anywhere", func(h *hostedHarness, _ *HostedSubmitInput) {
			delete(h.settings.values, SettingKeyHostedSelfOnboardExchangeProxyID)
		}, "SELF_ONBOARD_PROXY_REQUIRED"},
		{"platform proxy disabled", func(h *hostedHarness, _ *HostedSubmitInput) {
			h.settings.values[SettingKeyHostedSelfOnboardExchangeProxyID] = "8"
		}, "SELF_ONBOARD_PROXY_REQUIRED"},
		{"platform proxy missing", func(h *hostedHarness, _ *HostedSubmitInput) {
			h.settings.values[SettingKeyHostedSelfOnboardExchangeProxyID] = "99"
		}, "SELF_ONBOARD_PROXY_REQUIRED"},
		{"user proxy private", func(_ *hostedHarness, in *HostedSubmitInput) {
			in.Proxy = &HostedProxyInput{Protocol: "http", Host: "mixed.example.com", Port: 8080}
		}, "SELF_ONBOARD_PROXY_UNAVAILABLE"},
		{"user proxy unresolvable", func(_ *hostedHarness, in *HostedSubmitInput) {
			in.Proxy = &HostedProxyInput{Protocol: "http", Host: "nx.example.com", Port: 8080}
		}, "SELF_ONBOARD_PROXY_UNAVAILABLE"},
		{"user proxy invalid", func(_ *hostedHarness, in *HostedSubmitInput) {
			in.Proxy = &HostedProxyInput{Protocol: "ftp", Host: "proxy.example.com", Port: 8080}
		}, "SELF_ONBOARD_PROXY_INVALID"},
		{"display name", func(_ *hostedHarness, in *HostedSubmitInput) { in.DisplayName = strings.Repeat("名", 65) }, "SELF_ONBOARD_INVALID_INPUT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHostedHarness(t)
			sid, code := h.authorize(t, 5)
			in := HostedSubmitInput{SessionID: sid, Code: code, DisplayName: "n"}
			tc.setup(h, &in)
			_, err := h.svc.Submit(ctx, 5, in)
			require.Equal(t, tc.reason, hostedReason(err))
			require.Zero(t, h.store.consumed, "session not consumed")
			require.NotNil(t, h.store.sessions[sid])
			require.Zero(t, h.oauth.calls)
		})
	}
}

func TestHostedSubmitExchangeFailureIsGeneric(t *testing.T) {
	h := newHostedHarness(t)
	h.oauth.err = errors.New(`token exchange failed: status 400, body: {"error":"invalid_grant","secret":"` + hostedTestAccessToken + `"}`)
	sid, code := h.authorize(t, 5)
	_, err := h.svc.Submit(context.Background(), 5, HostedSubmitInput{SessionID: sid, Code: code, DisplayName: "n"})
	appErr := infraerrors.FromError(err)
	require.Equal(t, "SELF_ONBOARD_EXCHANGE_FAILED", appErr.Reason)
	require.NotEmpty(t, appErr.Metadata["correlation_id"])
	raw, jerr := json.Marshal(appErr.Status)
	require.NoError(t, jerr)
	require.NotContains(t, string(raw), "invalid_grant")
	require.NotContains(t, string(raw), hostedTestAccessToken)
	require.Empty(t, h.repo.created)
	// The session was consumed: a retry needs a new link.
	_, err = h.svc.Submit(context.Background(), 5, HostedSubmitInput{SessionID: sid, Code: code, DisplayName: "n"})
	require.Equal(t, "SELF_ONBOARD_SESSION_INVALID", hostedReason(err))
}

func TestHostedTokenValidation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	mutate := func(f func(r *oauth.TokenResponse)) *oauth.TokenResponse {
		r := hostedGoodToken()
		f(r)
		return r
	}
	cases := []struct {
		name string
		resp *oauth.TokenResponse
		want error
	}{
		{"nil", nil, errHostedTokenMissingAccess},
		{"empty token", mutate(func(r *oauth.TokenResponse) { r.AccessToken = " " }), errHostedTokenMissingAccess},
		{"zero expiry", mutate(func(r *oauth.TokenResponse) { r.ExpiresIn = 0 }), errHostedTokenExpiry},
		{"negative expiry", mutate(func(r *oauth.TokenResponse) { r.ExpiresIn = -5 }), errHostedTokenExpiry},
		{"too short", mutate(func(r *oauth.TokenResponse) { r.ExpiresIn = 600 }), errHostedTokenExpiry},
		{"absurd", mutate(func(r *oauth.TokenResponse) { r.ExpiresIn = 10 * 365 * 24 * 3600 }), errHostedTokenExpiry},
		{"no scope", mutate(func(r *oauth.TokenResponse) { r.Scope = "" }), errHostedTokenScope},
		{"other scope", mutate(func(r *oauth.TokenResponse) { r.Scope = "user:profile user:inferencex" }), errHostedTokenScope},
		{"broader scope", mutate(func(r *oauth.TokenResponse) { r.Scope = "user:inference org:create_api_key" }), errHostedTokenScope},
		{"sessions scope", mutate(func(r *oauth.TokenResponse) { r.Scope = "user:profile user:inference user:sessions:claude_code" }), errHostedTokenScope},
		{"full oauth scope", mutate(func(r *oauth.TokenResponse) { r.Scope = oauth.ScopeOAuth }), errHostedTokenScope},
		{"profile only", mutate(func(r *oauth.TokenResponse) { r.Scope = "user:profile" }), errHostedTokenScope},
		{"duplicate", mutate(func(r *oauth.TokenResponse) { r.Scope = "user:inference user:inference" }), errHostedTokenScope},
		{"no account", mutate(func(r *oauth.TokenResponse) { r.Account = nil }), ErrSelfOnboardIdentityMissing},
		{"empty uuid", mutate(func(r *oauth.TokenResponse) { r.Account.UUID = "" }), ErrSelfOnboardIdentityMissing},
		{"bad uuid", mutate(func(r *oauth.TokenResponse) { r.Account.UUID = "not-a-uuid" }), ErrSelfOnboardIdentityMissing},
		{"nil uuid", mutate(func(r *oauth.TokenResponse) { r.Account.UUID = "00000000-0000-0000-0000-000000000000" }), ErrSelfOnboardIdentityMissing},
		{"braced uuid", mutate(func(r *oauth.TokenResponse) { r.Account.UUID = "{0b6b8f86-0d9b-4f4e-9f55-6a1f5d7c0a11}" }), ErrSelfOnboardIdentityMissing},
	}
	for _, tc := range cases {
		_, err := validateHostedSetupToken(tc.resp, now)
		require.ErrorIs(t, err, tc.want, tc.name)
	}
	tok, err := validateHostedSetupToken(mutate(func(r *oauth.TokenResponse) { r.Scope = "user:profile user:inference" }), now)
	require.NoError(t, err)
	_, err = validateHostedSetupToken(mutate(func(r *oauth.TokenResponse) { r.Scope = " user:inference  user:profile " }), now)
	require.NoError(t, err, "order and spacing do not matter")
	require.Equal(t, now.Unix()+31536000, tok.Credentials["expires_at"], "expiry from expires_in, never invented")
	require.NotContains(t, tok.Credentials, "refresh_token")
	withRefresh := mutate(func(r *oauth.TokenResponse) { r.RefreshToken = "rt-secret" })
	tok, err = validateHostedSetupToken(withRefresh, now)
	require.NoError(t, err)
	require.NotContains(t, tok.Credentials, "refresh_token", "never stored: no second upstream path")
}

func TestHostedSubmitIdentityMissingCreatesNothing(t *testing.T) {
	h := newHostedHarness(t)
	h.oauth.resp.Account = nil
	sid, code := h.authorize(t, 5)
	_, err := h.svc.Submit(context.Background(), 5, HostedSubmitInput{SessionID: sid, Code: code, DisplayName: "n"})
	require.Equal(t, "SELF_ONBOARD_IDENTITY_MISSING", hostedReason(err))
	require.Empty(t, h.repo.created)
}

func TestHostedProxyValidationTable(t *testing.T) {
	ok := func(p HostedProxyInput) {
		t.Helper()
		_, err := NormalizeHostedProxyInput(&p)
		require.NoError(t, err, "%+v", p)
	}
	bad := func(p HostedProxyInput) {
		t.Helper()
		_, err := NormalizeHostedProxyInput(&p)
		require.Equal(t, "SELF_ONBOARD_PROXY_INVALID", hostedReason(err), "%+v", p)
	}
	ok(HostedProxyInput{Protocol: "http", Host: "proxy.example.com", Port: 1})
	ok(HostedProxyInput{Protocol: "https", Host: "proxy.example.com", Port: 65535})
	ok(HostedProxyInput{Protocol: "socks5", Host: "[2606:4700::1]", Port: 1080})
	ok(HostedProxyInput{Protocol: "socks5h", Host: "93.184.216.34", Port: 1080, Username: "u", Password: "p"})
	ok(HostedProxyInput{Protocol: "http", Host: "p.example.com", Port: 80, Username: strings.Repeat("u", 100), Password: "p"})
	bad(HostedProxyInput{Protocol: "ftp", Host: "proxy.example.com", Port: 21})
	bad(HostedProxyInput{Protocol: "http", Host: "proxy.example.com", Port: 0})
	bad(HostedProxyInput{Protocol: "http", Host: "proxy.example.com", Port: 65536})
	bad(HostedProxyInput{Protocol: "http", Host: "", Port: 80})
	bad(HostedProxyInput{Protocol: "http", Host: "fe80::1%eth0", Port: 80})
	bad(HostedProxyInput{Protocol: "http", Host: "[fe80::1%25eth0]", Port: 80})
	bad(HostedProxyInput{Protocol: "http", Host: "user@proxy.example.com", Port: 80})
	bad(HostedProxyInput{Protocol: "http", Host: "proxy.example.com/path", Port: 80})
	bad(HostedProxyInput{Protocol: "http", Host: "localhost", Port: 80})
	bad(HostedProxyInput{Protocol: "http", Host: "[proxy.example.com]", Port: 80})
	bad(HostedProxyInput{Protocol: "http", Host: "[93.184.216.34]", Port: 80})
	bad(HostedProxyInput{Protocol: "http", Host: "proxy.example.com", Port: 80, Username: "u"})
	bad(HostedProxyInput{Protocol: "http", Host: "proxy.example.com", Port: 80, Password: "p"})
	bad(HostedProxyInput{Protocol: "http", Host: "proxy.example.com", Port: 80, Username: "u", Password: "p\r\nX-Injected: 1"})
	bad(HostedProxyInput{Protocol: "http", Host: "proxy.example.com", Port: 80, Username: "u\x00", Password: "p"})
	bad(HostedProxyInput{Protocol: "http", Host: "proxy.example.com", Port: 80, Username: strings.Repeat("u", 256), Password: "p"})
	bad(HostedProxyInput{Protocol: "http", Host: "proxy.example.com", Port: 80, Username: "u", Password: strings.Repeat("p", 101)})
}

func TestHostedProxyPublicAddressCheck(t *testing.T) {
	public := []string{"93.184.216.34", "2606:4700::1", "8.8.8.8"}
	private := []string{
		"127.0.0.1", "::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1",
		"100.64.0.1", "100.127.255.254", "169.254.169.254", "fe80::1", "fc00::1", "fd00::1", "0.0.0.0", "::",
		"224.0.0.1", "ff02::1", "255.255.255.255", "240.0.0.1", "198.18.0.1", "192.0.2.1",
		"64:ff9b::a00:1", "2002:a00:1::1", "::a00:1", "fec0::1", "3fff::1",
	}
	for _, ip := range public {
		require.True(t, IsPublicHostedProxyIP(net.ParseIP(ip)), ip)
	}
	for _, ip := range private {
		require.False(t, IsPublicHostedProxyIP(net.ParseIP(ip)), ip)
	}

	h := newHostedHarness(t)
	ctx := context.Background()
	require.NoError(t, h.svc.checkProxyResolvesPublic(ctx, "proxy.example.com"))
	require.NoError(t, h.svc.checkProxyResolvesPublic(ctx, "93.184.216.34"))
	for _, host := range []string{"mixed.example.com", "mapped.example.com", "zoned.example.com", "nx.example.com", "169.254.169.254", "::ffff:10.0.0.1"} {
		err := h.svc.checkProxyResolvesPublic(ctx, host)
		require.Equal(t, "SELF_ONBOARD_PROXY_UNAVAILABLE", hostedReason(err), host)
		require.NotContains(t, infraerrors.FromError(err).Message, "internal", "never echo resolver text")
	}
}

func TestHostedSwitchOffMatrix(t *testing.T) {
	h := newHostedHarness(t)
	h.settings.values[SettingKeyHostedSelfOnboardEnabled] = "false"
	ctx := context.Background()

	_, err := h.svc.CreateAuthURL(ctx, 5)
	require.Equal(t, "SELF_ONBOARD_DISABLED", hostedReason(err))
	_, err = h.svc.Submit(ctx, 5, HostedSubmitInput{SessionID: "abc", Code: "a#b", DisplayName: "n"})
	require.Equal(t, "SELF_ONBOARD_DISABLED", hostedReason(err))
	_, err = h.svc.Approve(ctx, 1, 3, HostedApproveRequest{ProxyMode: HostedApproveProxyExisting, ProxyID: 7})
	require.Equal(t, "SELF_ONBOARD_DISABLED", hostedReason(err))
	require.Empty(t, h.repo.approves)

	cfg, err := h.svc.Config(ctx)
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	require.False(t, cfg.ExchangeProxyConfigured)
	require.Equal(t, 3, cfg.MaxPending)

	// Still available with the switch off.
	_, err = h.svc.ListMine(ctx, 5)
	require.NoError(t, err)
	_, err = h.svc.Cancel(ctx, 5, 3)
	require.NoError(t, err)
	_, err = h.svc.Reject(ctx, 1, 3, "duplicate")
	require.NoError(t, err)
	for _, action := range []string{HostedOwnerActionPause, HostedOwnerActionResume, HostedOwnerActionWithdraw} {
		_, err = h.svc.SetOwnedAccountState(ctx, 5, 9, action)
		require.NoError(t, err)
	}
	_, _, err = h.svc.ListAdmin(ctx, HostedApplicationStatusPending, 1, 20)
	require.NoError(t, err)
}

func TestHostedConfigReportsExchangeProxy(t *testing.T) {
	h := newHostedHarness(t)
	cfg, err := h.svc.Config(context.Background())
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.True(t, cfg.ExchangeProxyConfigured)
	h.settings.values[SettingKeyHostedSelfOnboardExchangeProxyID] = "8"
	cfg, err = h.svc.Config(context.Background())
	require.NoError(t, err)
	require.False(t, cfg.ExchangeProxyConfigured, "disabled proxy is not configured")
}

func TestHostedAuthURLRateLimit(t *testing.T) {
	h := newHostedHarness(t)
	for i := 0; i < 10; i++ {
		_, err := h.svc.CreateAuthURL(context.Background(), 5)
		require.NoError(t, err)
	}
	_, err := h.svc.CreateAuthURL(context.Background(), 5)
	require.Equal(t, "SELF_ONBOARD_RATE_LIMITED", hostedReason(err))
}

func TestHostedApproveBuildsCarpoolTemplate(t *testing.T) {
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	app := &HostedApplicationSecrets{
		HostedApplication: HostedApplication{ID: 4, UserID: 9, DisplayName: "mine", UpstreamAccountUUID: "0b6b8f86-0d9b-4f4e-9f55-6a1f5d7c0a11",
			UpstreamOrgUUID: "org-1", UpstreamEmail: "o@example.com"},
		Credentials: map[string]any{"access_token": hostedTestAccessToken, "expires_at": float64(now.Add(300 * 24 * time.Hour).Unix())},
	}
	cfg := ParseHostedSelfOnboardSettings(nil)
	in := HostedApproveRequest{ProxyMode: HostedApproveProxyExisting, ProxyID: 1}.Resolve(cfg)
	require.Equal(t, HostedApproveInput{ProxyMode: "existing", ProxyID: 1, Concurrency: 3, Priority: 1, CarpoolDeviceLimit: 5,
		CarpoolAdmissionsPerCycle: 1, CarpoolIntervalHours: 24}, in)
	spec, err := buildHostedApprovedAccount(app, in, cfg, now)
	require.NoError(t, err)
	require.Equal(t, "carpool", spec.Extra["claude_oauth_mode"])
	require.Equal(t, 5, spec.Extra["claude_oauth_carpool_device_limit"])
	require.Equal(t, 1, spec.Extra["claude_oauth_carpool_maintenance_admissions_per_cycle"])
	require.Equal(t, 24, spec.Extra["claude_oauth_carpool_maintenance_interval_hours"])
	require.Equal(t, true, spec.Extra["claude_oauth_carpool_auto_expand_enabled"])
	require.NotEmpty(t, spec.Extra[CarpoolMaintenanceGenerationKey])
	require.Equal(t, app.UpstreamAccountUUID, spec.Extra["account_uuid"])
	require.Equal(t, 0.10, spec.FeeRate)
	require.Equal(t, "托管-9", spec.GroupName)

	// A 64-character Chinese display name still yields a valid account name
	// (accounts.name is limited to 100 bytes).
	long := *app
	long.DisplayName = strings.Repeat("名", 64)
	spec, err = buildHostedApprovedAccount(&long, in, cfg, now)
	require.NoError(t, err)
	require.LessOrEqual(t, len(spec.Name), 100)
	require.True(t, utf8.ValidString(spec.Name))

	// Expiring token / bad template are refused.
	soon := *app
	soon.Credentials = map[string]any{"access_token": "x", "expires_at": float64(now.Add(time.Hour).Unix())}
	_, err = buildHostedApprovedAccount(&soon, in, cfg, now)
	require.Equal(t, "SELF_ONBOARD_INVALID_STATE", hostedReason(err))
	for _, broken := range []HostedApproveInput{
		{ProxyMode: "direct", Concurrency: 1, CarpoolDeviceLimit: 1, CarpoolIntervalHours: 1},
		{ProxyMode: "existing", ProxyID: 0, Concurrency: 1, CarpoolDeviceLimit: 1, CarpoolIntervalHours: 1},
		{ProxyMode: "existing", ProxyID: 1, Concurrency: 0, CarpoolDeviceLimit: 1, CarpoolIntervalHours: 1},
		{ProxyMode: "existing", ProxyID: 1, Concurrency: 1, CarpoolDeviceLimit: 33, CarpoolIntervalHours: 1},
		{ProxyMode: "existing", ProxyID: 1, Concurrency: 1, CarpoolDeviceLimit: 2, CarpoolAdmissionsPerCycle: 3, CarpoolIntervalHours: 1},
		{ProxyMode: "existing", ProxyID: 1, Concurrency: 1, CarpoolDeviceLimit: 2, CarpoolIntervalHours: 721},
	} {
		require.Error(t, ValidateHostedApproveInput(broken), "%+v", broken)
	}
}

func TestHostedApproveSubmittedProxyIsResolvedAgain(t *testing.T) {
	h := newHostedHarness(t)
	h.repo.getApp = &HostedApplication{ID: 3, Status: HostedApplicationStatusPending, ProxyProtocol: "http", ProxyHost: "mixed.example.com", ProxyPort: 8080}
	_, err := h.svc.Approve(context.Background(), 1, 3, HostedApproveRequest{ProxyMode: HostedApproveProxySubmitted})
	require.Equal(t, "SELF_ONBOARD_PROXY_UNAVAILABLE", hostedReason(err))
	require.Empty(t, h.repo.approves)

	h.repo.getApp = &HostedApplication{ID: 3, Status: HostedApplicationStatusPending}
	_, err = h.svc.Approve(context.Background(), 1, 3, HostedApproveRequest{ProxyMode: HostedApproveProxySubmitted})
	require.Equal(t, "SELF_ONBOARD_PROXY_REQUIRED", hostedReason(err), "no submitted proxy, no direct option")

	h.repo.getApp = &HostedApplication{ID: 3, Status: HostedApplicationStatusPending, ProxyProtocol: "http", ProxyHost: "proxy.example.com", ProxyPort: 8080}
	_, err = h.svc.Approve(context.Background(), 1, 3, HostedApproveRequest{ProxyMode: HostedApproveProxySubmitted})
	require.NoError(t, err)
	require.Len(t, h.repo.approves, 1)
	p := h.repo.approves[0]
	require.True(t, p.UseSubmitted)
	// Build refuses a row whose host changed after the check.
	_, err = p.Build(&HostedApplicationSecrets{HostedApplication: HostedApplication{ProxyHost: "other.example.com", ProxyPort: 1, ProxyProtocol: "http"}})
	require.Equal(t, "SELF_ONBOARD_PROXY_REQUIRED", hostedReason(err))
}

func TestSelfOnboardedAccountNeverDirect(t *testing.T) {
	proxyID := int64(3)
	acc := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive, Schedulable: true, SelfOnboarded: true}
	_, err := acc.EgressProxyURL()
	require.ErrorIs(t, err, ErrAccountProxyRequired)
	require.False(t, acc.IsSchedulable())

	acc.ProxyID = &proxyID
	_, err = acc.EgressProxyURL()
	require.ErrorIs(t, err, ErrAccountProxyRequired, "proxy id without a loaded (live) proxy")
	acc.Proxy = &Proxy{ID: 3, Protocol: "socks5h", Host: "203.0.113.1", Port: 1080, Status: "disabled"}
	_, err = acc.EgressProxyURL()
	require.ErrorIs(t, err, ErrAccountProxyRequired)
	require.False(t, acc.IsSchedulable())
	acc.Proxy.Status = StatusActive
	u, err := acc.EgressProxyURL()
	require.NoError(t, err)
	require.Equal(t, "socks5h://203.0.113.1:1080", u)
	require.True(t, acc.IsSchedulable())

	// Existing direct accounts are unaffected.
	direct := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive, Schedulable: true}
	u, err = direct.EgressProxyURL()
	require.NoError(t, err)
	require.Empty(t, u)
	require.True(t, direct.IsSchedulable())
}

func TestOwnerPauseBlocksScheduling(t *testing.T) {
	now := time.Now()
	acc := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive, Schedulable: true}
	require.True(t, acc.IsSchedulable())
	require.False(t, shouldClearStickySession(acc, ""))
	acc.OwnerPausedAt = &now
	require.False(t, acc.IsSchedulable())
	require.True(t, shouldClearStickySession(acc, ""))
	require.Equal(t, "paused", hostedAccountStatus(acc, now))
	acc.OwnerPausedAt = nil
	acc.OwnerWithdrawRequestedAt = &now
	require.False(t, acc.IsSchedulable())
	require.True(t, shouldClearStickySession(acc, ""))
}

func TestHostedApplicationDTOFreeOfSecrets(t *testing.T) {
	h := newHostedHarness(t)
	sid, code := h.authorize(t, 5)
	app, err := h.svc.Submit(context.Background(), 5, HostedSubmitInput{SessionID: sid, Code: code, DisplayName: "n",
		Proxy: &HostedProxyInput{Protocol: "http", Host: "proxy.example.com", Port: 8080, Username: "u", Password: "proxy-secret-pw"}})
	require.NoError(t, err)
	raw, err := json.Marshal(app)
	require.NoError(t, err)
	require.NotContains(t, string(raw), hostedTestAccessToken)
	require.NotContains(t, string(raw), "proxy-secret-pw")
	require.NotContains(t, string(raw), "access_token")
	require.Equal(t, "http://proxy.example.com:8080", app.ProxyDisplay())
}

func TestSelfOnboardedRefreshNeverDirect(t *testing.T) {
	proxies := &hostedFakeProxies{proxies: map[int64]*Proxy{
		8: {ID: 8, Protocol: "http", Host: "198.51.100.8", Port: 3128, Status: "disabled"},
	}}
	oauthClient := &hostedFakeOAuth{}
	svc := NewOAuthService(proxies, oauthClient)
	defer svc.Stop()
	acc := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, SelfOnboarded: true,
		Credentials: map[string]any{"refresh_token": "rt"}}
	_, err := svc.RefreshAccountToken(context.Background(), acc)
	require.ErrorIs(t, err, ErrAccountProxyRequired)
	disabled := int64(8)
	acc.ProxyID = &disabled
	_, err = svc.RefreshAccountToken(context.Background(), acc)
	require.ErrorIs(t, err, ErrAccountProxyRequired)
	missing := int64(99)
	acc.ProxyID = &missing
	_, err = svc.RefreshAccountToken(context.Background(), acc)
	require.ErrorIs(t, err, ErrAccountProxyRequired)
}
