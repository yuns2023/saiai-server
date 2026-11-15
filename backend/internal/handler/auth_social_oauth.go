package handler

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/imroc/req/v3"
	"github.com/tidwall/gjson"
)

// Google / GitHub sign-in. Accounts are bound by a per-provider synthetic email
// (google-<sub>@google-connect.invalid, github-<id>@github-connect.invalid); see
// service.SocialOAuthService. PKCE S256 is always used.

const (
	socialOAuthCookieMaxAgeSec   = 10 * 60
	socialOAuthDefaultRedirectTo = "/dashboard"
	socialOAuthMaxRedirectLen    = 2048
	socialOAuthHTTPTimeout       = 15 * time.Second
)

// socialOAuthBackend is the service surface the handler needs (an interface so the HTTP
// flow can be tested against fake providers without a database).
type socialOAuthBackend interface {
	Config(ctx context.Context, provider service.SocialOAuthProvider) (service.SocialOAuthConfig, error)
	SignIn(ctx context.Context, identity service.SocialOAuthIdentity) (*service.SocialOAuthResult, error)
	CompleteRegistration(ctx context.Context, provider service.SocialOAuthProvider, token, invitationCode string) (*service.SocialOAuthResult, error)
}

type socialOAuthEndpoints struct {
	AuthorizeURL string
	TokenURL     string
	UserInfoURL  string
	EmailsURL    string // GitHub only
	Scope        string
}

var defaultSocialOAuthEndpoints = map[service.SocialOAuthProvider]socialOAuthEndpoints{
	service.SocialOAuthProviderGoogle: {
		AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		UserInfoURL:  "https://openidconnect.googleapis.com/v1/userinfo",
		Scope:        "openid email profile",
	},
	service.SocialOAuthProviderGitHub: {
		AuthorizeURL: "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		UserInfoURL:  "https://api.github.com/user",
		EmailsURL:    "https://api.github.com/user/emails",
		Scope:        "read:user user:email",
	},
}

func (h *AuthHandler) socialEndpointsFor(provider service.SocialOAuthProvider) socialOAuthEndpoints {
	if h.socialEndpoints != nil {
		if ep, ok := h.socialEndpoints[provider]; ok {
			return ep
		}
	}
	return defaultSocialOAuthEndpoints[provider]
}

func (h *AuthHandler) socialOAuthConfig(ctx context.Context, provider service.SocialOAuthProvider) (service.SocialOAuthConfig, error) {
	if h == nil || h.socialOAuth == nil {
		return service.SocialOAuthConfig{}, service.ErrSocialOAuthDisabled
	}
	return h.socialOAuth.Config(ctx, provider)
}

func socialOAuthCookiePath(provider service.SocialOAuthProvider) string {
	return "/api/v1/auth/oauth/" + string(provider)
}

func socialOAuthCookieName(provider service.SocialOAuthProvider, purpose string) string {
	return string(provider) + "_oauth_" + purpose
}

// SocialOAuthStart starts sign-in. GET /api/v1/auth/oauth/{google,github}/start?redirect=/dashboard
func (h *AuthHandler) SocialOAuthStart(provider service.SocialOAuthProvider) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg, err := h.socialOAuthConfig(c.Request.Context(), provider)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		state, err := oauth.GenerateState()
		if err != nil {
			response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_STATE_GEN_FAILED", "failed to generate oauth state").WithCause(err))
			return
		}
		verifier, err := oauth.GenerateCodeVerifier()
		if err != nil {
			response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_PKCE_GEN_FAILED", "failed to generate pkce verifier").WithCause(err))
			return
		}
		redirectTo := sanitizeSocialRedirectPath(c.Query("redirect"))
		if redirectTo == "" {
			redirectTo = socialOAuthDefaultRedirectTo
		}

		authURL, err := buildSocialAuthorizeURL(h.socialEndpointsFor(provider), cfg, state, oauth.GenerateCodeChallenge(verifier))
		if err != nil {
			response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_BUILD_URL_FAILED", "failed to build oauth authorization url").WithCause(err))
			return
		}

		secure := isRequestHTTPS(c)
		path := socialOAuthCookiePath(provider)
		// The state cookie carries the provider so a callback for another provider is refused
		// even if the browser sent the cookie.
		setPathCookie(c, path, socialOAuthCookieName(provider, "state"), encodeCookieValue(string(provider)+":"+state), socialOAuthCookieMaxAgeSec, secure)
		setPathCookie(c, path, socialOAuthCookieName(provider, "verifier"), encodeCookieValue(verifier), socialOAuthCookieMaxAgeSec, secure)
		setPathCookie(c, path, socialOAuthCookieName(provider, "redirect"), encodeCookieValue(redirectTo), socialOAuthCookieMaxAgeSec, secure)
		c.Header("Cache-Control", "no-store")
		c.Redirect(http.StatusFound, authURL)
	}
}

// SocialOAuthCallback handles the provider redirect and hands the result to the frontend
// callback page via the URL fragment. GET /api/v1/auth/oauth/{google,github}/callback
func (h *AuthHandler) SocialOAuthCallback(provider service.SocialOAuthProvider) gin.HandlerFunc {
	return func(c *gin.Context) {
		frontendCallback := provider.FrontendCallbackPath()
		// Read the one-time cookies, then clear them before anything is written: a deferred
		// Set-Cookie after c.Redirect would be dropped because the headers are already sent.
		stateCookie, _ := readCookieDecoded(c, socialOAuthCookieName(provider, "state"))
		verifier, _ := readCookieDecoded(c, socialOAuthCookieName(provider, "verifier"))
		redirectTo, _ := readCookieDecoded(c, socialOAuthCookieName(provider, "redirect"))
		secure := isRequestHTTPS(c)
		path := socialOAuthCookiePath(provider)
		for _, purpose := range []string{"state", "verifier", "redirect"} {
			clearPathCookie(c, path, socialOAuthCookieName(provider, purpose), secure)
		}

		cfg, err := h.socialOAuthConfig(c.Request.Context(), provider)
		if err != nil {
			redirectOAuthError(c, frontendCallback, "provider_unavailable", infraerrors.Reason(err), "")
			return
		}
		if providerErr := strings.TrimSpace(c.Query("error")); providerErr != "" {
			redirectOAuthError(c, frontendCallback, "provider_error", providerErr, singleLine(c.Query("error_description")))
			return
		}
		code := strings.TrimSpace(c.Query("code"))
		state := strings.TrimSpace(c.Query("state"))
		if code == "" || state == "" {
			redirectOAuthError(c, frontendCallback, "missing_params", "missing code/state", "")
			return
		}
		if !socialOAuthStateMatches(stateCookie, provider, state) {
			redirectOAuthError(c, frontendCallback, "invalid_state", "invalid oauth state", "")
			return
		}
		if verifier == "" {
			redirectOAuthError(c, frontendCallback, "missing_verifier", "missing pkce verifier", "")
			return
		}
		redirectTo = sanitizeSocialRedirectPath(redirectTo)
		if redirectTo == "" {
			redirectTo = socialOAuthDefaultRedirectTo
		}

		endpoints := h.socialEndpointsFor(provider)
		accessToken, err := socialOAuthExchangeCode(c.Request.Context(), endpoints, cfg, code, verifier)
		if err != nil {
			log.Printf("[SocialOAuth] %s token exchange failed: %v", provider, err)
			redirectOAuthError(c, frontendCallback, "token_exchange_failed", "failed to exchange oauth code", "")
			return
		}
		identity, err := socialOAuthFetchIdentity(c.Request.Context(), provider, endpoints, accessToken)
		if err != nil {
			if errors.Is(err, service.ErrSocialOAuthEmailNotVerified) {
				redirectOAuthError(c, frontendCallback, "email_not_verified", infraerrors.Reason(err), "")
				return
			}
			log.Printf("[SocialOAuth] %s userinfo failed: %v", provider, err)
			redirectOAuthError(c, frontendCallback, "userinfo_failed", "failed to fetch user info", "")
			return
		}

		result, err := h.socialOAuth.SignIn(c.Request.Context(), identity)
		if err != nil {
			redirectOAuthError(c, frontendCallback, "login_failed", infraerrors.Reason(err), infraerrors.Message(err))
			return
		}

		fragment := url.Values{}
		fragment.Set("provider", string(provider))
		fragment.Set("redirect", redirectTo)
		switch {
		case result.PendingToken != "":
			fragment.Set("error", "invitation_required")
			fragment.Set("pending_oauth_token", result.PendingToken)
		case result.Requires2FA:
			fragment.Set("requires_2fa", "1")
			fragment.Set("temp_token", result.TempToken)
		case result.TokenPair != nil:
			fragment.Set("access_token", result.TokenPair.AccessToken)
			fragment.Set("refresh_token", result.TokenPair.RefreshToken)
			fragment.Set("expires_in", strconv.Itoa(result.TokenPair.ExpiresIn))
			fragment.Set("token_type", "Bearer")
		default:
			redirectOAuthError(c, frontendCallback, "login_failed", "SERVICE_UNAVAILABLE", "")
			return
		}
		redirectWithFragment(c, frontendCallback, fragment)
	}
}

type completeSocialOAuthRequest struct {
	PendingOAuthToken string `json:"pending_oauth_token" binding:"required"`
	InvitationCode    string `json:"invitation_code"`
}

// CompleteSocialOAuthRegistration finishes an invitation-required first sign-in.
// POST /api/v1/auth/oauth/{google,github}/complete-registration
func (h *AuthHandler) CompleteSocialOAuthRegistration(provider service.SocialOAuthProvider) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req completeSocialOAuthRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			response.BadRequest(c, "Invalid request: "+err.Error())
			return
		}
		if h.socialOAuth == nil {
			response.ErrorFrom(c, service.ErrSocialOAuthDisabled)
			return
		}
		result, err := h.socialOAuth.CompleteRegistration(c.Request.Context(), provider, strings.TrimSpace(req.PendingOAuthToken), req.InvitationCode)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		if result == nil || result.TokenPair == nil || result.User == nil {
			response.ErrorFrom(c, service.ErrServiceUnavailable)
			return
		}
		c.Header("Cache-Control", "no-store")
		response.Success(c, AuthResponse{
			AccessToken:  result.TokenPair.AccessToken,
			RefreshToken: result.TokenPair.RefreshToken,
			ExpiresIn:    result.TokenPair.ExpiresIn,
			TokenType:    "Bearer",
			User:         dto.UserFromService(result.User),
		})
	}
}

// socialOAuthStateMatches checks the decoded state cookie ("<provider>:<state>") against the
// callback's state in constant time and refuses a cookie minted for another provider.
func socialOAuthStateMatches(raw string, provider service.SocialOAuthProvider, state string) bool {
	if raw == "" {
		return false
	}
	cookieProvider, expected, ok := strings.Cut(raw, ":")
	if !ok || cookieProvider != string(provider) || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(state)) == 1
}

func buildSocialAuthorizeURL(endpoints socialOAuthEndpoints, cfg service.SocialOAuthConfig, state, codeChallenge string) (string, error) {
	u, err := url.Parse(endpoints.AuthorizeURL)
	if err != nil {
		return "", fmt.Errorf("parse authorize url: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", cfg.RedirectURL)
	q.Set("scope", endpoints.Scope)
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	if cfg.Provider == service.SocialOAuthProviderGoogle {
		q.Set("prompt", "select_account")
	}
	if cfg.Provider == service.SocialOAuthProviderGitHub {
		q.Set("allow_signup", "true")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func socialOAuthExchangeCode(ctx context.Context, endpoints socialOAuthEndpoints, cfg service.SocialOAuthConfig, code, verifier string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", cfg.RedirectURL)
	form.Set("code_verifier", verifier)
	resp, err := req.C().SetTimeout(socialOAuthHTTPTimeout).R().
		SetContext(ctx).
		SetHeader("Accept", "application/json").
		SetFormDataFromValues(form).
		Post(endpoints.TokenURL)
	if err != nil {
		return "", fmt.Errorf("request token: %w", err)
	}
	body := strings.TrimSpace(resp.String())
	if !resp.IsSuccessState() {
		providerErr, providerDesc := parseOAuthProviderError(body)
		return "", fmt.Errorf("token status=%d error=%q description=%q", resp.StatusCode, providerErr, truncateLogValue(providerDesc, 256))
	}
	// GitHub reports errors with HTTP 200 and no access_token.
	token, ok := parseLinuxDoTokenResponse(body)
	if !ok || strings.TrimSpace(token.AccessToken) == "" {
		providerErr, _ := parseOAuthProviderError(body)
		return "", fmt.Errorf("token response without access_token (error=%q)", providerErr)
	}
	authorization, err := buildBearerAuthorization(token.TokenType, token.AccessToken)
	if err != nil {
		return "", err
	}
	return authorization, nil
}

// socialOAuthFetchIdentity returns the provider's stable account id plus a provider-verified
// email. Google: OIDC userinfo with email_verified=true. GitHub: numeric user id and a
// primary+verified address from /user/emails.
func socialOAuthFetchIdentity(ctx context.Context, provider service.SocialOAuthProvider, endpoints socialOAuthEndpoints, authorization string) (service.SocialOAuthIdentity, error) {
	client := req.C().SetTimeout(socialOAuthHTTPTimeout)
	get := func(target, accept string) (string, error) {
		r := client.R().SetContext(ctx).
			SetHeader("Accept", accept).
			SetHeader("Authorization", authorization)
		if provider == service.SocialOAuthProviderGitHub {
			r.SetHeader("X-GitHub-Api-Version", "2022-11-28")
		}
		resp, err := r.Get(target)
		if err != nil {
			return "", fmt.Errorf("request %s: %w", target, err)
		}
		if !resp.IsSuccessState() {
			return "", fmt.Errorf("%s status=%d", target, resp.StatusCode)
		}
		return resp.String(), nil
	}

	switch provider {
	case service.SocialOAuthProviderGoogle:
		body, err := get(endpoints.UserInfoURL, "application/json")
		if err != nil {
			return service.SocialOAuthIdentity{}, err
		}
		return parseGoogleUserInfo(body)
	case service.SocialOAuthProviderGitHub:
		userBody, err := get(endpoints.UserInfoURL, "application/vnd.github+json")
		if err != nil {
			return service.SocialOAuthIdentity{}, err
		}
		subject, login, err := parseGitHubUser(userBody)
		if err != nil {
			return service.SocialOAuthIdentity{}, err
		}
		emailsBody, err := get(endpoints.EmailsURL, "application/vnd.github+json")
		if err != nil {
			return service.SocialOAuthIdentity{}, err
		}
		email, err := parseGitHubPrimaryVerifiedEmail(emailsBody)
		if err != nil {
			return service.SocialOAuthIdentity{}, err
		}
		return service.SocialOAuthIdentity{Provider: provider, Subject: subject, VerifiedEmail: email, Username: login}, nil
	default:
		return service.SocialOAuthIdentity{}, errors.New("unsupported provider")
	}
}

func parseGoogleUserInfo(body string) (service.SocialOAuthIdentity, error) {
	if !gjson.Valid(body) {
		return service.SocialOAuthIdentity{}, errors.New("invalid google userinfo json")
	}
	sub := gjson.Get(body, "sub")
	if sub.Type != gjson.String || strings.TrimSpace(sub.String()) == "" {
		return service.SocialOAuthIdentity{}, errors.New("google userinfo missing sub")
	}
	verified := gjson.Get(body, "email_verified")
	isVerified := verified.Type == gjson.True || (verified.Type == gjson.String && verified.String() == "true")
	email := strings.TrimSpace(gjson.Get(body, "email").String())
	if !isVerified || email == "" {
		return service.SocialOAuthIdentity{}, service.ErrSocialOAuthEmailNotVerified
	}
	return service.SocialOAuthIdentity{
		Provider:      service.SocialOAuthProviderGoogle,
		Subject:       sub.String(),
		VerifiedEmail: email,
		Username:      strings.TrimSpace(gjson.Get(body, "name").String()),
	}, nil
}

// parseGitHubUser returns the numeric id (never login or email) as the subject.
func parseGitHubUser(body string) (subject string, login string, err error) {
	if !gjson.Valid(body) {
		return "", "", errors.New("invalid github user json")
	}
	id := gjson.Get(body, "id")
	if id.Type != gjson.Number {
		return "", "", errors.New("github user missing numeric id")
	}
	n, err := strconv.ParseInt(id.Raw, 10, 64)
	if err != nil || n <= 0 {
		return "", "", errors.New("github user id invalid")
	}
	return strconv.FormatInt(n, 10), strings.TrimSpace(gjson.Get(body, "login").String()), nil
}

func parseGitHubPrimaryVerifiedEmail(body string) (string, error) {
	if !gjson.Valid(body) || !gjson.Parse(body).IsArray() {
		return "", errors.New("invalid github emails json")
	}
	for _, item := range gjson.Parse(body).Array() {
		if item.Get("primary").Type == gjson.True && item.Get("verified").Type == gjson.True {
			if email := strings.TrimSpace(item.Get("email").String()); email != "" {
				return email, nil
			}
		}
	}
	return "", service.ErrSocialOAuthEmailNotVerified
}

// sanitizeSocialRedirectPath accepts only same-origin, path-absolute targets.
// Stricter than the LinuxDo sanitizer: also rejects backslashes and every control character.
func sanitizeSocialRedirectPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || len(path) > socialOAuthMaxRedirectLen {
		return ""
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return ""
	}
	if strings.Contains(path, "\\") || strings.Contains(path, "://") {
		return ""
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return path
}

func setPathCookie(c *gin.Context, path, name, value string, maxAgeSec int, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   maxAgeSec,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearPathCookie(c *gin.Context, path, name string, secure bool) {
	setPathCookie(c, path, name, "", -1, secure)
}
