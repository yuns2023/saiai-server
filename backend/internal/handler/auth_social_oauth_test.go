package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ---- fakes -----------------------------------------------------------------

type fakeSocialBackend struct {
	mu         sync.Mutex
	configErr  error
	result     *service.SocialOAuthResult
	signInErr  error
	identities []service.SocialOAuthIdentity

	completeResult *service.SocialOAuthResult
	completeErr    error
	completeCalls  int

	handoffs map[string]service.SocialOAuthHandoff
}

func (f *fakeSocialBackend) SaveHandoff(_ context.Context, code string, h *service.SocialOAuthHandoff) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.handoffs == nil {
		f.handoffs = map[string]service.SocialOAuthHandoff{}
	}
	f.handoffs[code] = *h
	return nil
}

func (f *fakeSocialBackend) ConsumeHandoff(_ context.Context, code string) (*service.SocialOAuthHandoff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.handoffs[code]
	if !ok {
		return nil, nil
	}
	delete(f.handoffs, code)
	return &h, nil
}

// expireHandoffs simulates the server-side TTL elapsing.
func (f *fakeSocialBackend) expireHandoffs() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handoffs = nil
}

func (f *fakeSocialBackend) Config(_ context.Context, p service.SocialOAuthProvider) (service.SocialOAuthConfig, error) {
	if f.configErr != nil {
		return service.SocialOAuthConfig{}, f.configErr
	}
	return service.SocialOAuthConfig{
		Provider:            p,
		ClientID:            "client-" + string(p),
		ClientSecret:        "secret-" + string(p),
		RedirectURL:         "https://aog.example.com" + p.CallbackPath(),
		FrontendCallbackURL: p.FrontendCallbackPath(),
	}, nil
}

func (f *fakeSocialBackend) SignIn(_ context.Context, id service.SocialOAuthIdentity) (*service.SocialOAuthResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identities = append(f.identities, id)
	if f.signInErr != nil {
		return nil, f.signInErr
	}
	if f.result != nil {
		return f.result, nil
	}
	return &service.SocialOAuthResult{
		User:      &service.User{ID: 1, Email: service.SocialOAuthSyntheticEmail(id.Provider, id.Subject)},
		TokenPair: &service.TokenPair{AccessToken: "at-1", RefreshToken: "rt-1", ExpiresIn: 900},
	}, nil
}

func (f *fakeSocialBackend) CompleteRegistration(_ context.Context, _ service.SocialOAuthProvider, _, _ string) (*service.SocialOAuthResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completeCalls++
	if f.completeErr != nil {
		return nil, f.completeErr
	}
	return f.completeResult, nil
}

// fakeProvider is an httptest OAuth provider (token + userinfo + GitHub emails) that
// verifies the PKCE S256 pair.
type fakeProvider struct {
	srv          *httptest.Server
	challenge    string
	googleInfo   string
	githubUser   string
	githubEmails string
	// githubEmailsFn, when set, serves /user/emails per request (pagination tests).
	githubEmailsFn func(r *http.Request) string
	tokenForms     []url.Values
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	p := &fakeProvider{
		googleInfo:   `{"sub":"109876543210987654321","email":"alice@gmail.com","email_verified":true,"name":"Alice"}`,
		githubUser:   `{"id":583231,"login":"octocat","email":"public@example.com"}`,
		githubEmails: `[{"email":"other@example.com","primary":false,"verified":true},{"email":"octo@example.com","primary":true,"verified":true}]`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		p.tokenForms = append(p.tokenForms, r.PostForm)
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge || r.PostForm.Get("code") != "good-code" {
			// GitHub style: 200 with an error body.
			_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"provider-at","token_type":"bearer"}`))
	})
	auth := func(next func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer provider-at" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			next(w, r)
		}
	}
	mux.HandleFunc("/google/userinfo", auth(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(p.googleInfo)) }))
	mux.HandleFunc("/github/user", auth(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(p.githubUser)) }))
	mux.HandleFunc("/github/user/emails", auth(func(w http.ResponseWriter, r *http.Request) {
		if p.githubEmailsFn != nil {
			_, _ = w.Write([]byte(p.githubEmailsFn(r)))
			return
		}
		_, _ = w.Write([]byte(p.githubEmails))
	}))
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeProvider) endpoints() map[service.SocialOAuthProvider]socialOAuthEndpoints {
	return map[service.SocialOAuthProvider]socialOAuthEndpoints{
		service.SocialOAuthProviderGoogle: {
			AuthorizeURL: "https://accounts.google.test/auth",
			TokenURL:     p.srv.URL + "/token",
			UserInfoURL:  p.srv.URL + "/google/userinfo",
			Scope:        "openid email profile",
		},
		service.SocialOAuthProviderGitHub: {
			AuthorizeURL: "https://github.test/login/oauth/authorize",
			TokenURL:     p.srv.URL + "/token",
			UserInfoURL:  p.srv.URL + "/github/user",
			EmailsURL:    p.srv.URL + "/github/user/emails",
			Scope:        "read:user user:email",
		},
	}
}

func newSocialRouter(backend *fakeSocialBackend, provider *fakeProvider) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := &AuthHandler{socialOAuth: backend}
	if provider != nil {
		h.socialEndpoints = provider.endpoints()
	}
	r := gin.New()
	for _, p := range []service.SocialOAuthProvider{service.SocialOAuthProviderGoogle, service.SocialOAuthProviderGitHub} {
		base := "/api/v1/auth/oauth/" + string(p)
		r.GET(base+"/start", h.SocialOAuthStart(p))
		r.GET(base+"/callback", h.SocialOAuthCallback(p))
		r.POST(base+"/complete-registration", h.CompleteSocialOAuthRegistration(p))
		r.POST(base+"/exchange", h.ExchangeSocialOAuthHandoff(p))
	}
	return r
}

type startResult struct {
	authURL *url.URL
	cookies []*http.Cookie
}

func doStart(t *testing.T, r *gin.Engine, provider service.SocialOAuthProvider, query string, fp *fakeProvider) startResult {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/"+string(provider)+"/start"+query, nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusFound, w.Code, w.Body.String())
	u, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	if fp != nil {
		fp.challenge = u.Query().Get("code_challenge")
	}
	return startResult{authURL: u, cookies: w.Result().Cookies()}
}

func doCallback(t *testing.T, r *gin.Engine, provider service.SocialOAuthProvider, query string, cookies []*http.Cookie) (url.Values, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/"+string(provider)+"/callback"+query, nil)
	for _, c := range cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusFound, w.Code, w.Body.String())
	loc, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, provider.FrontendCallbackPath(), loc.Path)
	require.Empty(t, loc.Host, "frontend callback must be same-origin")
	frag, err := url.ParseQuery(loc.Fragment)
	require.NoError(t, err)
	return frag, w
}

func fullFlow(t *testing.T, provider service.SocialOAuthProvider, backend *fakeSocialBackend, fp *fakeProvider) url.Values {
	t.Helper()
	frag, _, _ := callbackFlow(t, provider, backend, fp)
	return frag
}

// callbackFlow runs start + callback and returns the fragment, the binder cookie(s) and the router.
func callbackFlow(t *testing.T, provider service.SocialOAuthProvider, backend *fakeSocialBackend, fp *fakeProvider) (url.Values, []*http.Cookie, *gin.Engine) {
	t.Helper()
	r := newSocialRouter(backend, fp)
	start := doStart(t, r, provider, "?redirect=/keys", fp)
	state := start.authURL.Query().Get("state")
	frag, w := doCallback(t, r, provider, "?code=good-code&state="+url.QueryEscape(state), start.cookies)
	return frag, handoffCookies(w), r
}

func handoffCookies(w *httptest.ResponseRecorder) []*http.Cookie {
	var out []*http.Cookie
	for _, c := range w.Result().Cookies() {
		if strings.HasSuffix(c.Name, "_oauth_handoff") && c.MaxAge > 0 {
			out = append(out, c)
		}
	}
	return out
}

type exchangeResult struct {
	status  int
	data    map[string]any
	reason  string
	cleared bool
}

func doExchange(t *testing.T, r *gin.Engine, provider service.SocialOAuthProvider, code string, cookies []*http.Cookie) exchangeResult {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/"+string(provider)+"/exchange", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var env struct {
		Data   map[string]any `json:"data"`
		Reason string         `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
	res := exchangeResult{status: w.Code, data: env.Data, reason: env.Reason}
	for _, c := range w.Result().Cookies() {
		if c.Name == string(provider)+"_oauth_handoff" && c.MaxAge < 0 {
			res.cleared = true
		}
	}
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	return res
}

// outcomeFlow runs the whole browser flow including the exchange.
func outcomeFlow(t *testing.T, provider service.SocialOAuthProvider, backend *fakeSocialBackend, fp *fakeProvider) (url.Values, map[string]any) {
	t.Helper()
	frag, cookies, r := callbackFlow(t, provider, backend, fp)
	requireOnlyCode(t, frag)
	res := doExchange(t, r, provider, frag.Get("code"), cookies)
	require.Equal(t, http.StatusOK, res.status, res.reason)
	require.True(t, res.cleared, "binder cookie cleared after exchange")
	return frag, res.data
}

// requireOnlyCode asserts that nothing but the one-time code travels in the URL.
func requireOnlyCode(t *testing.T, frag url.Values) {
	t.Helper()
	require.Len(t, frag, 1, "fragment: %v", frag)
	require.True(t, isHexToken(frag.Get("code")))
}

// ---- tests -----------------------------------------------------------------

func TestSocialOAuthStart_PKCEAndCookies(t *testing.T) {
	backend := &fakeSocialBackend{}
	fp := newFakeProvider(t)
	r := newSocialRouter(backend, fp)

	for _, p := range []service.SocialOAuthProvider{service.SocialOAuthProviderGoogle, service.SocialOAuthProviderGitHub} {
		res := doStart(t, r, p, "?redirect=/keys", fp)
		q := res.authURL.Query()
		require.Equal(t, "S256", q.Get("code_challenge_method"))
		require.NotEmpty(t, q.Get("code_challenge"))
		require.NotEmpty(t, q.Get("state"))
		require.Equal(t, "client-"+string(p), q.Get("client_id"))
		require.Equal(t, "https://aog.example.com"+p.CallbackPath(), q.Get("redirect_uri"))
		require.Equal(t, "code", q.Get("response_type"))
		require.Empty(t, q.Get("client_secret"))

		names := map[string]*http.Cookie{}
		for _, c := range res.cookies {
			names[c.Name] = c
		}
		for _, purpose := range []string{"state", "verifier", "redirect"} {
			c := names[string(p)+"_oauth_"+purpose]
			require.NotNil(t, c, purpose)
			require.True(t, c.HttpOnly)
			require.True(t, c.Secure, "secure behind https proxy")
			require.Equal(t, http.SameSiteLaxMode, c.SameSite)
			require.Equal(t, "/api/v1/auth/oauth/"+string(p), c.Path)
			require.Equal(t, socialOAuthCookieMaxAgeSec, c.MaxAge)
		}
		state, err := decodeCookieValue(names[string(p)+"_oauth_state"].Value)
		require.NoError(t, err)
		require.Equal(t, string(p)+":"+q.Get("state"), state)
	}
}

func TestSocialOAuthStart_DisabledProvider(t *testing.T) {
	r := newSocialRouter(&fakeSocialBackend{configErr: service.ErrSocialOAuthDisabled}, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/google/start", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Empty(t, w.Result().Cookies())

	// A handler built without the service (nil) is also disabled.
	h := NewAuthHandler(nil, nil, nil, nil, nil, nil, nil, nil)
	require.Nil(t, h.socialOAuth)
}

func TestSocialOAuthCallback_GoogleHappyPath(t *testing.T) {
	backend := &fakeSocialBackend{}
	fp := newFakeProvider(t)
	_, out := outcomeFlow(t, service.SocialOAuthProviderGoogle, backend, fp)

	require.Equal(t, "tokens", out["kind"])
	require.Equal(t, "at-1", out["access_token"])
	require.Equal(t, "rt-1", out["refresh_token"])
	require.EqualValues(t, 900, out["expires_in"])
	require.Equal(t, "/keys", out["redirect"])
	require.Equal(t, "google", out["provider"])
	require.Len(t, backend.identities, 1)
	id := backend.identities[0]
	require.Equal(t, service.SocialOAuthProviderGoogle, id.Provider)
	require.Equal(t, "109876543210987654321", id.Subject)
	require.Equal(t, "alice@gmail.com", id.VerifiedEmail)

	form := fp.tokenForms[0]
	require.Equal(t, "secret-google", form.Get("client_secret"))
	require.Equal(t, "https://aog.example.com/api/v1/auth/oauth/google/callback", form.Get("redirect_uri"))
	require.Equal(t, "authorization_code", form.Get("grant_type"))
}

func TestSocialOAuthCallback_GoogleEmailNotVerified(t *testing.T) {
	for _, info := range []string{
		`{"sub":"1","email":"a@b.co","email_verified":false}`,
		`{"sub":"1","email":"a@b.co"}`,
		`{"sub":"1","email":"a@b.co","email_verified":"false"}`,
		`{"sub":"1","email":"a@b.co","email_verified":"true"}`,
		`{"sub":"1","email_verified":true}`,
	} {
		backend := &fakeSocialBackend{}
		fp := newFakeProvider(t)
		fp.googleInfo = info
		frag := fullFlow(t, service.SocialOAuthProviderGoogle, backend, fp)
		require.Equal(t, "email_not_verified", frag.Get("error"), info)
		require.Empty(t, frag.Get("access_token"))
		require.Empty(t, backend.identities, "sign-in must not be attempted")
	}
}

func TestSocialOAuthCallback_GoogleMissingSub(t *testing.T) {
	backend := &fakeSocialBackend{}
	fp := newFakeProvider(t)
	fp.googleInfo = `{"sub":123,"email":"a@b.co","email_verified":true}`
	frag := fullFlow(t, service.SocialOAuthProviderGoogle, backend, fp)
	require.Equal(t, "userinfo_failed", frag.Get("error"))
	require.Empty(t, backend.identities)
}

func TestSocialOAuthCallback_GitHubHappyPathUsesNumericID(t *testing.T) {
	backend := &fakeSocialBackend{}
	fp := newFakeProvider(t)
	_, out := outcomeFlow(t, service.SocialOAuthProviderGitHub, backend, fp)
	require.Equal(t, "at-1", out["access_token"])
	require.Len(t, backend.identities, 1)
	id := backend.identities[0]
	require.Equal(t, "583231", id.Subject, "subject is the numeric id, never login/email")
	require.Equal(t, "octo@example.com", id.VerifiedEmail, "primary+verified address, not the public profile email")
	require.Equal(t, "octocat", id.Username)
}

func TestSocialOAuthCallback_GitHubNoPrimaryVerifiedEmail(t *testing.T) {
	for _, emails := range []string{
		`[]`,
		`[{"email":"a@b.co","primary":true,"verified":false},{"email":"c@d.co","primary":false,"verified":true}]`,
		`[{"email":"a@b.co","primary":"true","verified":"true"}]`,
	} {
		backend := &fakeSocialBackend{}
		fp := newFakeProvider(t)
		fp.githubEmails = emails
		frag := fullFlow(t, service.SocialOAuthProviderGitHub, backend, fp)
		require.Equal(t, "email_not_verified", frag.Get("error"), emails)
		require.Empty(t, backend.identities)
	}
}

func TestSocialOAuthCallback_GitHubNonNumericID(t *testing.T) {
	for _, user := range []string{`{"id":"583231","login":"x"}`, `{"login":"x"}`, `{"id":0}`, `{"id":-4}`, `{"id":1.5}`} {
		backend := &fakeSocialBackend{}
		fp := newFakeProvider(t)
		fp.githubUser = user
		frag := fullFlow(t, service.SocialOAuthProviderGitHub, backend, fp)
		require.Equal(t, "userinfo_failed", frag.Get("error"), user)
		require.Empty(t, backend.identities)
	}
}

func TestSocialOAuthExchange_RejectsErrorWithToken(t *testing.T) {
	for _, body := range []string{
		`{"error":"bad_verification_code","access_token":"oops","token_type":"bearer"}`,
		`error=bad_verification_code&access_token=oops&token_type=bearer`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		_, err := socialOAuthExchangeCode(context.Background(), socialOAuthEndpoints{TokenURL: srv.URL}, service.SocialOAuthConfig{ClientID: "c", ClientSecret: "s"}, "code", "verifier")
		srv.Close()
		require.Error(t, err, body)
	}
}

func TestSocialOAuthCallback_StateChecks(t *testing.T) {
	backend := &fakeSocialBackend{}
	fp := newFakeProvider(t)
	r := newSocialRouter(backend, fp)
	google := doStart(t, r, service.SocialOAuthProviderGoogle, "", fp)
	state := google.authURL.Query().Get("state")

	// Wrong state.
	frag, _ := doCallback(t, r, service.SocialOAuthProviderGoogle, "?code=good-code&state=nope", google.cookies)
	require.Equal(t, "invalid_state", frag.Get("error"))

	// Missing cookie.
	frag, _ = doCallback(t, r, service.SocialOAuthProviderGoogle, "?code=good-code&state="+url.QueryEscape(state), nil)
	require.Equal(t, "invalid_state", frag.Get("error"))

	// Cross-provider: Google's cookie values replayed under GitHub's cookie names.
	var crossed []*http.Cookie
	for _, c := range google.cookies {
		crossed = append(crossed, &http.Cookie{Name: strings.Replace(c.Name, "google_", "github_", 1), Value: c.Value})
	}
	frag, _ = doCallback(t, r, service.SocialOAuthProviderGitHub, "?code=good-code&state="+url.QueryEscape(state), crossed)
	require.Equal(t, "invalid_state", frag.Get("error"))

	// Missing verifier.
	var noVerifier []*http.Cookie
	for _, c := range google.cookies {
		if c.Name != "google_oauth_verifier" {
			noVerifier = append(noVerifier, c)
		}
	}
	frag, _ = doCallback(t, r, service.SocialOAuthProviderGoogle, "?code=good-code&state="+url.QueryEscape(state), noVerifier)
	require.Equal(t, "missing_verifier", frag.Get("error"))

	// Missing code.
	frag, _ = doCallback(t, r, service.SocialOAuthProviderGoogle, "?state="+url.QueryEscape(state), google.cookies)
	require.Equal(t, "missing_params", frag.Get("error"))

	// Provider-reported error.
	frag, _ = doCallback(t, r, service.SocialOAuthProviderGoogle, "?error=access_denied", google.cookies)
	require.Equal(t, "provider_error", frag.Get("error"))

	// Wrong PKCE verifier/code => token exchange fails.
	frag, _ = doCallback(t, r, service.SocialOAuthProviderGoogle, "?code=bad-code&state="+url.QueryEscape(state), google.cookies)
	require.Equal(t, "token_exchange_failed", frag.Get("error"))

	require.Empty(t, backend.identities)
}

func TestSocialOAuthCallback_ClearsCookiesOnEveryOutcome(t *testing.T) {
	backend := &fakeSocialBackend{}
	fp := newFakeProvider(t)
	r := newSocialRouter(backend, fp)
	start := doStart(t, r, service.SocialOAuthProviderGitHub, "", fp)
	for _, query := range []string{"?code=x&state=wrong", "?code=good-code&state=" + url.QueryEscape(start.authURL.Query().Get("state"))} {
		_, w := doCallback(t, r, service.SocialOAuthProviderGitHub, query, start.cookies)
		cleared := map[string]bool{}
		for _, c := range w.Result().Cookies() {
			if c.MaxAge < 0 && c.Path == "/api/v1/auth/oauth/github" {
				cleared[c.Name] = true
			}
		}
		require.True(t, cleared["github_oauth_state"], query)
		require.True(t, cleared["github_oauth_verifier"], query)
		require.True(t, cleared["github_oauth_redirect"], query)
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	}
}

func TestSocialOAuthCallback_PendingRegistration(t *testing.T) {
	backend := &fakeSocialBackend{result: &service.SocialOAuthResult{PendingToken: strings.Repeat("a", 64)}}
	_, out := outcomeFlow(t, service.SocialOAuthProviderGoogle, backend, newFakeProvider(t))
	require.Equal(t, "invitation", out["kind"])
	require.Equal(t, strings.Repeat("a", 64), out["pending_oauth_token"])
	require.Equal(t, "/keys", out["redirect"])
	require.NotContains(t, out, "access_token")
}

func TestSocialOAuthCallback_TOTPUserGetsTempTokenOnly(t *testing.T) {
	backend := &fakeSocialBackend{result: &service.SocialOAuthResult{Requires2FA: true, TempToken: "tmp-123", User: &service.User{ID: 9}}}
	_, out := outcomeFlow(t, service.SocialOAuthProviderGitHub, backend, newFakeProvider(t))
	require.Equal(t, "two_factor", out["kind"])
	require.Equal(t, "tmp-123", out["temp_token"])
	require.NotContains(t, out, "access_token")
	require.NotContains(t, out, "refresh_token")
}

func TestSocialOAuthCallback_DisabledUserRefused(t *testing.T) {
	backend := &fakeSocialBackend{signInErr: service.ErrUserNotActive}
	frag := fullFlow(t, service.SocialOAuthProviderGoogle, backend, newFakeProvider(t))
	require.Equal(t, "login_failed", frag.Get("error"))
	require.Equal(t, "USER_NOT_ACTIVE", frag.Get("error_message"))
	require.Empty(t, frag.Get("access_token"))
}

func TestSocialOAuthCallback_ProviderDisabledAfterStart(t *testing.T) {
	backend := &fakeSocialBackend{}
	fp := newFakeProvider(t)
	r := newSocialRouter(backend, fp)
	start := doStart(t, r, service.SocialOAuthProviderGoogle, "", fp)
	backend.configErr = service.ErrSocialOAuthDisabled
	frag, _ := doCallback(t, r, service.SocialOAuthProviderGoogle, "?code=good-code&state="+url.QueryEscape(start.authURL.Query().Get("state")), start.cookies)
	require.Equal(t, "provider_unavailable", frag.Get("error"))
	require.Empty(t, backend.identities)
}

func TestSocialOAuthStart_RedirectIsSanitized(t *testing.T) {
	backend := &fakeSocialBackend{}
	fp := newFakeProvider(t)
	r := newSocialRouter(backend, fp)
	for _, raw := range []string{"//evil.com", "/\\evil.com", "https://evil.com", "/a\r\nSet-Cookie:x=1", "javascript:alert(1)"} {
		start := doStart(t, r, service.SocialOAuthProviderGoogle, "?redirect="+url.QueryEscape(raw), fp)
		frag, w := doCallback(t, r, service.SocialOAuthProviderGoogle, "?code=good-code&state="+url.QueryEscape(start.authURL.Query().Get("state")), start.cookies)
		res := doExchange(t, r, service.SocialOAuthProviderGoogle, frag.Get("code"), handoffCookies(w))
		require.Equal(t, "/dashboard", res.data["redirect"], raw)
	}
}

func TestSanitizeSocialRedirectPath(t *testing.T) {
	require.Equal(t, "/keys?tab=1#x", sanitizeSocialRedirectPath("/keys?tab=1#x"))
	require.Equal(t, "/dashboard", sanitizeSocialRedirectPath(" /dashboard "))
	for _, bad := range []string{
		"", "dashboard", "//evil", "//evil.com/path", "/\\evil", "/\\\\evil", "/a\\b",
		"https://evil", "/x?next=https://evil", "/a\r\nb", "/a\nb", "/a\tb", "/a\x00b", "/a\x7fb",
		"/" + strings.Repeat("a", socialOAuthMaxRedirectLen),
	} {
		require.Equal(t, "", sanitizeSocialRedirectPath(bad), "%q", bad)
	}
}

func TestCompleteSocialOAuthRegistration(t *testing.T) {
	backend := &fakeSocialBackend{completeResult: &service.SocialOAuthResult{
		User:      &service.User{ID: 5, Email: "github-1@github-connect.invalid", Role: service.RoleUser, Status: service.StatusActive},
		TokenPair: &service.TokenPair{AccessToken: "at-5", RefreshToken: "rt-5", ExpiresIn: 900},
	}}
	r := newSocialRouter(backend, nil)
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/github/complete-registration", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}

	w := post(`{"pending_oauth_token":"` + strings.Repeat("b", 64) + `","invitation_code":"X"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var env struct {
		Code int `json:"code"`
		Data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			User         struct {
				ID int64 `json:"id"`
			} `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, 0, env.Code)
	require.Equal(t, "at-5", env.Data.AccessToken)
	require.Equal(t, int64(5), env.Data.User.ID)

	backend.completeErr = service.ErrSocialOAuthPendingInvalid
	w = post(`{"pending_oauth_token":"` + strings.Repeat("b", 64) + `","invitation_code":"X"}`)
	require.Equal(t, http.StatusBadRequest, w.Code, "not 401: the SPA would treat 401 as an expired session")
	require.Contains(t, w.Body.String(), "OAUTH_PENDING_INVALID")

	w = post(`{}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSocialOAuthHandoff_BinderCookieAttributes(t *testing.T) {
	backend := &fakeSocialBackend{}
	fp := newFakeProvider(t)
	r := newSocialRouter(backend, fp)
	start := doStart(t, r, service.SocialOAuthProviderGoogle, "", fp)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/google/callback?code=good-code&state="+url.QueryEscape(start.authURL.Query().Get("state")), nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	for _, c := range start.cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	cookies := handoffCookies(w)
	require.Len(t, cookies, 1)
	c := cookies[0]
	require.Equal(t, "google_oauth_handoff", c.Name)
	require.True(t, c.HttpOnly)
	require.True(t, c.Secure)
	require.Equal(t, http.SameSiteLaxMode, c.SameSite)
	require.Equal(t, "/api/v1/auth/oauth/google", c.Path)
	require.LessOrEqual(t, c.MaxAge, 120)

	// The stored handoff holds only the binder hash, never the binder itself.
	for _, h := range backend.handoffs {
		require.Equal(t, sha256Hex(c.Value), h.BinderHash)
		require.NotEqual(t, c.Value, h.BinderHash)
	}
}

func TestSocialOAuthHandoff_RejectsEverythingButTheRightBrowser(t *testing.T) {
	newFlow := func() (url.Values, []*http.Cookie, *gin.Engine, *fakeSocialBackend) {
		backend := &fakeSocialBackend{}
		frag, cookies, r := callbackFlow(t, service.SocialOAuthProviderGoogle, backend, newFakeProvider(t))
		requireOnlyCode(t, frag)
		return frag, cookies, r, backend
	}
	requireInvalid := func(res exchangeResult, msg string) {
		t.Helper()
		require.Equal(t, http.StatusBadRequest, res.status, msg)
		require.Equal(t, "OAUTH_HANDOFF_INVALID", res.reason, msg)
		require.Nil(t, res.data, msg)
	}

	// Code without the binder cookie (e.g. a link sent to a victim) is useless.
	frag, _, r, _ := newFlow()
	requireInvalid(doExchange(t, r, service.SocialOAuthProviderGoogle, frag.Get("code"), nil), "no cookie")

	// Wrong binder cookie (attacker's own browser).
	frag, _, r, _ = newFlow()
	forged := []*http.Cookie{{Name: "google_oauth_handoff", Value: strings.Repeat("f", 64)}}
	requireInvalid(doExchange(t, r, service.SocialOAuthProviderGoogle, frag.Get("code"), forged), "wrong cookie")

	// Reused code.
	frag, cookies, r, _ := newFlow()
	require.Equal(t, http.StatusOK, doExchange(t, r, service.SocialOAuthProviderGoogle, frag.Get("code"), cookies).status)
	requireInvalid(doExchange(t, r, service.SocialOAuthProviderGoogle, frag.Get("code"), cookies), "reused")

	// Other provider's exchange endpoint, even with the cookie renamed to match.
	frag, cookies, r, _ = newFlow()
	renamed := []*http.Cookie{{Name: "github_oauth_handoff", Value: cookies[0].Value}}
	requireInvalid(doExchange(t, r, service.SocialOAuthProviderGitHub, frag.Get("code"), renamed), "other provider")

	// Expired.
	frag, cookies, r, backend := newFlow()
	backend.expireHandoffs()
	requireInvalid(doExchange(t, r, service.SocialOAuthProviderGoogle, frag.Get("code"), cookies), "expired")

	// Malformed / forged code values.
	_, cookies, r, _ = newFlow()
	for _, code := range []string{"", "abc", strings.Repeat("A", 64), strings.Repeat("0", 64)} {
		requireInvalid(doExchange(t, r, service.SocialOAuthProviderGoogle, code, cookies), "code "+code)
	}
}

func TestSocialOAuthCallback_NoSecretsInURL(t *testing.T) {
	for _, result := range []*service.SocialOAuthResult{
		nil, // default: tokens
		{PendingToken: strings.Repeat("a", 64)},
		{Requires2FA: true, TempToken: "tmp-123", User: &service.User{ID: 9}},
	} {
		backend := &fakeSocialBackend{result: result}
		frag, cookies, _ := callbackFlow(t, service.SocialOAuthProviderGoogle, backend, newFakeProvider(t))
		requireOnlyCode(t, frag)
		require.Len(t, cookies, 1)
	}
}

func TestSocialOAuthCallback_GitHubEmailsPaginated(t *testing.T) {
	backend := &fakeSocialBackend{}
	fp := newFakeProvider(t)
	var page1 []map[string]any
	for i := 0; i < githubEmailsPerPage; i++ {
		page1 = append(page1, map[string]any{"email": "x" + strconv.Itoa(i) + "@example.com", "primary": false, "verified": true})
	}
	b1, _ := json.Marshal(page1)
	pages := map[string]string{"1": string(b1), "2": `[{"email":"octo@example.com","primary":true,"verified":true}]`}
	var requested []string
	fp.githubEmailsFn = func(r *http.Request) string {
		require.Equal(t, "100", r.URL.Query().Get("per_page"))
		requested = append(requested, r.URL.Query().Get("page"))
		return pages[r.URL.Query().Get("page")]
	}
	_, out := outcomeFlow(t, service.SocialOAuthProviderGitHub, backend, fp)
	require.Equal(t, "tokens", out["kind"])
	require.Equal(t, []string{"1", "2"}, requested)
	require.Equal(t, "octo@example.com", backend.identities[0].VerifiedEmail)

	// Bounded: full pages without a match stop after githubEmailsMaxPage.
	backend = &fakeSocialBackend{}
	fp = newFakeProvider(t)
	requested = nil
	fp.githubEmailsFn = func(r *http.Request) string {
		requested = append(requested, r.URL.Query().Get("page"))
		return string(b1)
	}
	frag := fullFlow(t, service.SocialOAuthProviderGitHub, backend, fp)
	require.Equal(t, "email_not_verified", frag.Get("error"))
	require.Len(t, requested, githubEmailsMaxPage)
}

func TestSocialOAuthCallback_OversizedProviderResponse(t *testing.T) {
	backend := &fakeSocialBackend{}
	fp := newFakeProvider(t)
	fp.googleInfo = `{"sub":"1","email":"a@b.co","email_verified":true,"pad":"` + strings.Repeat("x", socialOAuthMaxBodyBytes) + `"}`
	frag := fullFlow(t, service.SocialOAuthProviderGoogle, backend, fp)
	require.Equal(t, "userinfo_failed", frag.Get("error"))
	require.Empty(t, backend.identities)
}
