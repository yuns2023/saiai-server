package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/imroc/req/v3"
)

const (
	loginOAuthCookieMaxAgeSec  = 10 * 60
	loginOAuthDefaultRedirect  = "/dashboard"
	loginOAuthFrontendCallback = "/auth/oauth/callback"
	loginOAuthMaxRedirectLen   = 2048
	loginOAuthMaxFragmentLen   = 512
	loginOAuthProviderContext  = "login_oauth_provider"
)

type loginOAuthProvider struct {
	Name         string
	AuthorizeURL string
	TokenURL     string
	UserURL      string
	EmailURL     string
	Scope        string
	Config       config.LoginOAuthProviderConfig
}

type loginOAuthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

type oauthTokenExchangeError struct {
	StatusCode          int
	ProviderError       string
	ProviderDescription string
}

func (e *oauthTokenExchangeError) Error() string {
	parts := []string{fmt.Sprintf("token exchange status=%d", e.StatusCode)}
	if e.ProviderError != "" {
		parts = append(parts, "error="+singleLine(e.ProviderError))
	}
	if e.ProviderDescription != "" {
		parts = append(parts, "description="+singleLine(e.ProviderDescription))
	}
	return strings.Join(parts, " ")
}

// LoginOAuthStart starts a fixed GitHub or Google authorization-code flow.
// Both providers always use state and PKCE S256.
func (h *AuthHandler) LoginOAuthStart(c *gin.Context) {
	provider, err := h.getLoginOAuthProvider(c.GetString(loginOAuthProviderContext))
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
	redirectTo := sanitizeFrontendRedirectPath(c.Query("redirect"))
	if redirectTo == "" {
		redirectTo = loginOAuthDefaultRedirect
	}
	secure := isRequestHTTPS(c)
	setLoginOAuthCookie(c, provider.Name, "state", state, loginOAuthCookieMaxAgeSec, secure)
	setLoginOAuthCookie(c, provider.Name, "verifier", verifier, loginOAuthCookieMaxAgeSec, secure)
	setLoginOAuthCookie(c, provider.Name, "redirect", redirectTo, loginOAuthCookieMaxAgeSec, secure)
	setLoginOAuthCookie(c, provider.Name, "intent", "login", loginOAuthCookieMaxAgeSec, secure)
	setLoginOAuthCookie(c, provider.Name, "link", "", -1, secure)

	authorizeURL, err := buildLoginOAuthAuthorizeURL(provider, state, oauth.GenerateCodeChallenge(verifier))
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_BUILD_URL_FAILED", "failed to build oauth authorization url").WithCause(err))
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Redirect(http.StatusFound, authorizeURL)
}

type linkLoginOAuthRequest struct {
	Password string `json:"password"`
	TotpCode string `json:"totp_code"`
}

func (h *AuthHandler) GetLoginOAuthConnections(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	providers, err := h.authService.ListExternalOAuthProviders(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"providers": providers})
}

// LinkLoginOAuthStart starts an explicit account-link flow. A valid local
// session plus fresh password/TOTP verification is required; provider email is
// never used as proof that the caller owns the local account.
func (h *AuthHandler) LinkLoginOAuthStart(c *gin.Context) {
	provider, err := h.getLoginOAuthProvider(c.GetString(loginOAuthProviderContext))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	var reqBody linkLoginOAuthRequest
	if err := c.ShouldBindJSON(&reqBody); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	user, err := h.userService.GetByID(c.Request.Context(), subject.UserID)
	if err != nil || user == nil || !user.IsActive() {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	requireTotp := h.totpService != nil && h.settingSvc != nil && h.settingSvc.IsTotpEnabled(c.Request.Context()) && user.TotpEnabled
	if requireTotp {
		if err := h.totpService.VerifyCode(c.Request.Context(), user.ID, strings.TrimSpace(reqBody.TotpCode)); err != nil {
			response.ErrorFrom(c, err)
			return
		}
	} else if !user.CheckPassword(reqBody.Password) {
		response.ErrorFrom(c, service.ErrPasswordIncorrect)
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
	redirectTo := sanitizeFrontendRedirectPath(c.Query("redirect"))
	if redirectTo == "" {
		redirectTo = "/profile"
	}
	secure := isRequestHTTPS(c)
	setLoginOAuthCookie(c, provider.Name, "state", state, loginOAuthCookieMaxAgeSec, secure)
	setLoginOAuthCookie(c, provider.Name, "verifier", verifier, loginOAuthCookieMaxAgeSec, secure)
	setLoginOAuthCookie(c, provider.Name, "redirect", redirectTo, loginOAuthCookieMaxAgeSec, secure)
	setLoginOAuthCookie(c, provider.Name, "intent", "link", loginOAuthCookieMaxAgeSec, secure)
	setLoginOAuthCookie(c, provider.Name, "link", h.signLoginOAuthLink(provider.Name, state, user.ID), loginOAuthCookieMaxAgeSec, secure)

	authorizeURL, err := buildLoginOAuthAuthorizeURL(provider, state, oauth.GenerateCodeChallenge(verifier))
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_BUILD_URL_FAILED", "failed to build oauth authorization url").WithCause(err))
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, gin.H{"authorization_url": authorizeURL})
}

// LoginOAuthCallback verifies the provider identity and resolves only a
// canonical (provider, subject) binding. Email matches never bind implicitly.
func (h *AuthHandler) LoginOAuthCallback(c *gin.Context) {
	provider, err := h.getLoginOAuthProvider(c.GetString(loginOAuthProviderContext))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	frontendCallback := h.loginOAuthFrontendCallback()
	secure := isRequestHTTPS(c)
	defer clearLoginOAuthCookies(c, provider.Name, secure)
	if providerError := strings.TrimSpace(c.Query("error")); providerError != "" {
		redirectOAuthError(c, frontendCallback, "provider_error", providerError, c.Query("error_description"))
		return
	}
	code := strings.TrimSpace(c.Query("code"))
	state := strings.TrimSpace(c.Query("state"))
	if code == "" || state == "" {
		redirectOAuthError(c, frontendCallback, "missing_params", "missing code/state", "")
		return
	}

	expectedState, stateErr := readLoginOAuthCookie(c, provider.Name, "state")
	if stateErr != nil || expectedState == "" || state != expectedState {
		redirectOAuthError(c, frontendCallback, "invalid_state", "invalid oauth state", "")
		return
	}
	verifier, verifierErr := readLoginOAuthCookie(c, provider.Name, "verifier")
	if verifierErr != nil || verifier == "" {
		redirectOAuthError(c, frontendCallback, "missing_verifier", "missing pkce verifier", "")
		return
	}
	redirectTo, _ := readLoginOAuthCookie(c, provider.Name, "redirect")
	redirectTo = sanitizeFrontendRedirectPath(redirectTo)
	if redirectTo == "" {
		redirectTo = loginOAuthDefaultRedirect
	}
	intent, _ := readLoginOAuthCookie(c, provider.Name, "intent")
	linkUserID := int64(0)
	if intent == "link" {
		linkContext, linkErr := readLoginOAuthCookie(c, provider.Name, "link")
		linkUserID, err = h.verifyLoginOAuthLink(provider.Name, state, linkContext)
		if linkErr != nil || err != nil {
			redirectOAuthError(c, frontendCallback, "invalid_link_session", "invalid or expired oauth link session", "")
			return
		}
	}

	token, err := exchangeLoginOAuthCode(c.Request.Context(), provider, code, verifier)
	if err != nil {
		slog.Warn("login oauth token exchange failed", "provider", provider.Name, "error", err)
		redirectOAuthError(c, frontendCallback, "token_exchange_failed", "failed to exchange oauth code", "")
		return
	}
	identity, err := fetchLoginOAuthIdentity(c.Request.Context(), provider, token)
	if err != nil {
		slog.Warn("login oauth identity verification failed", "provider", provider.Name, "error", err)
		redirectOAuthError(c, frontendCallback, "userinfo_failed", "provider did not return a usable verified identity", "")
		return
	}
	if intent == "link" {
		if err := h.authService.LinkExternalOAuthIdentity(c.Request.Context(), linkUserID, identity); err != nil {
			redirectOAuthError(c, frontendCallback, infraerrors.Reason(err), infraerrors.Message(err), "")
			return
		}
		slog.Info("login oauth identity linked", "provider", provider.Name, "user_id", linkUserID, "client_ip", ip.GetClientIP(c))
		fragment := url.Values{}
		fragment.Set("provider", provider.Name)
		fragment.Set("linked", "true")
		fragment.Set("redirect", redirectTo)
		redirectWithFragment(c, frontendCallback, fragment)
		return
	}
	result, err := h.authService.ResolveExternalOAuth(c.Request.Context(), identity)
	if err != nil {
		redirectOAuthError(c, frontendCallback, infraerrors.Reason(err), infraerrors.Message(err), "")
		return
	}
	fragment := url.Values{}
	fragment.Set("provider", provider.Name)
	fragment.Set("redirect", redirectTo)
	if result.RequiresInvitation() {
		fragment.Set("error", "invitation_required")
		fragment.Set("oauth_registration_token", result.RegistrationToken)
		redirectWithFragment(c, frontendCallback, fragment)
		return
	}
	if h.settingSvc != nil && h.settingSvc.IsBackendModeEnabled(c.Request.Context()) && !result.User.IsAdmin() {
		redirectOAuthError(c, frontendCallback, "FORBIDDEN", "Backend mode is active. Only admin login is allowed.", "")
		return
	}
	if h.totpService != nil && h.settingSvc != nil && h.settingSvc.IsTotpEnabled(c.Request.Context()) && result.User.TotpEnabled {
		tempToken, err := h.totpService.CreateLoginSession(c.Request.Context(), result.User.ID, result.User.Email)
		if err != nil {
			redirectOAuthError(c, frontendCallback, "OAUTH_2FA_SESSION_FAILED", "failed to create 2FA session", "")
			return
		}
		fragment.Set("requires_2fa", "true")
		fragment.Set("temp_token", tempToken)
		fragment.Set("user_email_masked", service.MaskEmail(result.User.Email))
		redirectWithFragment(c, frontendCallback, fragment)
		return
	}
	tokenPair, err := h.authService.GenerateTokenPair(c.Request.Context(), result.User, "")
	if err != nil {
		redirectOAuthError(c, frontendCallback, "SERVICE_UNAVAILABLE", "failed to create login session", "")
		return
	}
	setTokenPairFragment(fragment, tokenPair)
	redirectWithFragment(c, frontendCallback, fragment)
}

type completeLoginOAuthRegistrationRequest struct {
	RegistrationToken string `json:"oauth_registration_token" binding:"required"`
	InvitationCode    string `json:"invitation_code"          binding:"required"`
}

func (h *AuthHandler) CompleteLoginOAuthRegistration(c *gin.Context) {
	provider, err := h.getLoginOAuthProvider(c.GetString(loginOAuthProviderContext))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	var reqBody completeLoginOAuthRegistrationRequest
	if err := c.ShouldBindJSON(&reqBody); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	result, err := h.authService.CompleteExternalOAuthRegistration(
		c.Request.Context(), provider.Name, reqBody.RegistrationToken, reqBody.InvitationCode,
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	tokenPair, err := h.authService.GenerateTokenPair(c.Request.Context(), result.User, "")
	if err != nil {
		response.ErrorFrom(c, service.ErrServiceUnavailable)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"access_token":  tokenPair.AccessToken,
		"refresh_token": tokenPair.RefreshToken,
		"expires_in":    tokenPair.ExpiresIn,
		"token_type":    "Bearer",
	})
}

func (h *AuthHandler) signLoginOAuthLink(provider, state string, userID int64) string {
	userIDText := strconv.FormatInt(userID, 10)
	mac := hmac.New(sha256.New, []byte(h.cfg.JWT.Secret))
	_, _ = mac.Write([]byte(provider + "\n" + state + "\n" + userIDText))
	return userIDText + "." + hex.EncodeToString(mac.Sum(nil))
}

func (h *AuthHandler) verifyLoginOAuthLink(provider, state, signed string) (int64, error) {
	userIDText, signature, ok := strings.Cut(signed, ".")
	if !ok || userIDText == "" || len(signature) != sha256.Size*2 {
		return 0, errors.New("invalid oauth link context")
	}
	userID, err := strconv.ParseInt(userIDText, 10, 64)
	if err != nil || userID <= 0 {
		return 0, errors.New("invalid oauth link user")
	}
	provided, err := hex.DecodeString(signature)
	if err != nil {
		return 0, errors.New("invalid oauth link signature")
	}
	mac := hmac.New(sha256.New, []byte(h.cfg.JWT.Secret))
	_, _ = mac.Write([]byte(provider + "\n" + state + "\n" + userIDText))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return 0, errors.New("invalid oauth link signature")
	}
	return userID, nil
}

func (h *AuthHandler) getLoginOAuthProvider(name string) (loginOAuthProvider, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if h == nil || h.cfg == nil {
		return loginOAuthProvider{}, infraerrors.ServiceUnavailable("CONFIG_NOT_READY", "config not loaded")
	}
	var provider loginOAuthProvider
	switch name {
	case "github":
		provider = loginOAuthProvider{
			Name:         "github",
			AuthorizeURL: "https://github.com/login/oauth/authorize",
			TokenURL:     "https://github.com/login/oauth/access_token",
			UserURL:      "https://api.github.com/user",
			EmailURL:     "https://api.github.com/user/emails",
			Scope:        "read:user user:email",
			Config:       h.cfg.GitHubOAuth,
		}
	case "google":
		provider = loginOAuthProvider{
			Name:         "google",
			AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			UserURL:      "https://openidconnect.googleapis.com/v1/userinfo",
			Scope:        "openid email profile",
			Config:       h.cfg.GoogleOAuth,
		}
	default:
		return loginOAuthProvider{}, infraerrors.NotFound("OAUTH_PROVIDER_NOT_FOUND", "oauth provider not found")
	}
	if !provider.Config.Enabled {
		return loginOAuthProvider{}, infraerrors.NotFound("OAUTH_DISABLED", "oauth login is disabled")
	}
	if provider.Config.ClientID == "" || provider.Config.ClientSecret == "" || provider.Config.RedirectURL == "" {
		return loginOAuthProvider{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "oauth provider is not fully configured")
	}
	return provider, nil
}

func (h *AuthHandler) loginOAuthFrontendCallback() string {
	base := strings.TrimRight(strings.TrimSpace(h.cfg.Server.FrontendURL), "/")
	if base == "" {
		return loginOAuthFrontendCallback
	}
	return base + loginOAuthFrontendCallback
}

func buildLoginOAuthAuthorizeURL(provider loginOAuthProvider, state, challenge string) (string, error) {
	u, err := url.Parse(provider.AuthorizeURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", provider.Config.ClientID)
	q.Set("redirect_uri", provider.Config.RedirectURL)
	q.Set("scope", provider.Scope)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if provider.Name == "google" {
		q.Set("access_type", "online")
		q.Set("include_granted_scopes", "true")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func exchangeLoginOAuthCode(ctx context.Context, provider loginOAuthProvider, code, verifier string) (*loginOAuthTokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", provider.Config.ClientID)
	form.Set("client_secret", provider.Config.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", provider.Config.RedirectURL)
	form.Set("code_verifier", verifier)
	resp, err := req.C().SetTimeout(30*time.Second).R().
		SetContext(ctx).
		SetHeader("Accept", "application/json").
		SetFormDataFromValues(form).
		Post(provider.TokenURL)
	if err != nil {
		return nil, fmt.Errorf("request token: %w", err)
	}
	body := strings.TrimSpace(resp.String())
	if !resp.IsSuccessState() {
		providerError, providerDescription := parseOAuthProviderError(body)
		return nil, &oauthTokenExchangeError{resp.StatusCode, providerError, providerDescription}
	}
	token, ok := parseLoginOAuthTokenResponse(body)
	if !ok {
		return nil, &oauthTokenExchangeError{StatusCode: resp.StatusCode}
	}
	return token, nil
}

func fetchLoginOAuthIdentity(ctx context.Context, provider loginOAuthProvider, token *loginOAuthTokenResponse) (service.ExternalOAuthIdentity, error) {
	authorization, err := buildBearerAuthorization(token.TokenType, token.AccessToken)
	if err != nil {
		return service.ExternalOAuthIdentity{}, err
	}
	client := req.C().SetTimeout(30 * time.Second)
	userResp, err := client.R().SetContext(ctx).
		SetHeader("Accept", "application/json").
		SetHeader("Authorization", authorization).
		SetHeader("X-GitHub-Api-Version", "2022-11-28").
		Get(provider.UserURL)
	if err != nil || !userResp.IsSuccessState() {
		return service.ExternalOAuthIdentity{}, errors.New("userinfo request failed")
	}
	if provider.Name == "google" {
		var info struct {
			Subject       string `json:"sub"`
			Email         string `json:"email"`
			EmailVerified bool   `json:"email_verified"`
			Name          string `json:"name"`
		}
		if err := json.Unmarshal(userResp.Bytes(), &info); err != nil || !info.EmailVerified || strings.TrimSpace(info.Subject) == "" {
			return service.ExternalOAuthIdentity{}, errors.New("google userinfo missing verified identity")
		}
		return service.ExternalOAuthIdentity{Provider: "google", Subject: info.Subject, VerifiedEmail: info.Email, Username: info.Name}, nil
	}

	var githubUser struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := json.Unmarshal(userResp.Bytes(), &githubUser); err != nil || githubUser.ID <= 0 {
		return service.ExternalOAuthIdentity{}, errors.New("github userinfo missing subject")
	}
	emailResp, err := client.R().SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("Authorization", authorization).
		SetHeader("X-GitHub-Api-Version", "2022-11-28").
		Get(provider.EmailURL)
	if err != nil || !emailResp.IsSuccessState() {
		return service.ExternalOAuthIdentity{}, errors.New("github email request failed")
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := json.Unmarshal(emailResp.Bytes(), &emails); err != nil {
		return service.ExternalOAuthIdentity{}, errors.New("invalid github email response")
	}
	verifiedEmail := ""
	for _, candidate := range emails {
		if candidate.Primary && candidate.Verified {
			verifiedEmail = candidate.Email
			break
		}
	}
	if strings.TrimSpace(verifiedEmail) == "" {
		return service.ExternalOAuthIdentity{}, errors.New("github primary email is not verified")
	}
	return service.ExternalOAuthIdentity{
		Provider:      "github",
		Subject:       strconv.FormatInt(githubUser.ID, 10),
		VerifiedEmail: verifiedEmail,
		Username:      githubUser.Login,
	}, nil
}

func setTokenPairFragment(fragment url.Values, tokenPair *service.TokenPair) {
	fragment.Set("access_token", tokenPair.AccessToken)
	fragment.Set("refresh_token", tokenPair.RefreshToken)
	fragment.Set("expires_in", strconv.Itoa(tokenPair.ExpiresIn))
	fragment.Set("token_type", "Bearer")
}

func redirectOAuthError(c *gin.Context, frontendCallback, code, message, description string) {
	fragment := url.Values{}
	fragment.Set("error", truncateFragmentValue(code))
	if strings.TrimSpace(message) != "" {
		fragment.Set("error_message", truncateFragmentValue(message))
	}
	if strings.TrimSpace(description) != "" {
		fragment.Set("error_description", truncateFragmentValue(description))
	}
	redirectWithFragment(c, frontendCallback, fragment)
}

func redirectWithFragment(c *gin.Context, frontendCallback string, fragment url.Values) {
	u, err := url.Parse(frontendCallback)
	if err != nil || (u.Scheme != "" && !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		c.Redirect(http.StatusFound, loginOAuthDefaultRedirect)
		return
	}
	u.Fragment = fragment.Encode()
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Redirect(http.StatusFound, u.String())
}

func parseOAuthProviderError(body string) (string, string) {
	var parsed struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Message          string `json:"message"`
	}
	if json.Unmarshal([]byte(body), &parsed) == nil && (parsed.Error != "" || parsed.ErrorDescription != "" || parsed.Message != "") {
		return parsed.Error, firstNonEmpty(parsed.ErrorDescription, parsed.Message)
	}
	values, err := url.ParseQuery(body)
	if err != nil {
		return "", ""
	}
	return firstNonEmpty(values.Get("error"), values.Get("code")), firstNonEmpty(values.Get("error_description"), values.Get("message"))
}

func parseLoginOAuthTokenResponse(body string) (*loginOAuthTokenResponse, bool) {
	var token loginOAuthTokenResponse
	if json.Unmarshal([]byte(body), &token) == nil && strings.TrimSpace(token.AccessToken) != "" {
		return &token, true
	}
	values, err := url.ParseQuery(body)
	if err != nil || strings.TrimSpace(values.Get("access_token")) == "" {
		return nil, false
	}
	return &loginOAuthTokenResponse{AccessToken: values.Get("access_token"), TokenType: values.Get("token_type")}, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func singleLine(value string) string { return strings.Join(strings.Fields(value), " ") }

func sanitizeFrontendRedirectPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || len(path) > loginOAuthMaxRedirectLen || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "://") || strings.ContainsAny(path, "\r\n") {
		return ""
	}
	return path
}

func isRequestHTTPS(c *gin.Context) bool {
	return c.Request.TLS != nil || strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")), "https")
}

func loginOAuthCookieName(provider, purpose string) string {
	return "login_oauth_" + provider + "_" + purpose
}
func loginOAuthCookiePath(provider string) string { return "/api/v1/auth/oauth/" + provider }

func setLoginOAuthCookie(c *gin.Context, provider, purpose, value string, maxAge int, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: loginOAuthCookieName(provider, purpose), Value: base64.RawURLEncoding.EncodeToString([]byte(value)),
		Path: loginOAuthCookiePath(provider), MaxAge: maxAge, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func readLoginOAuthCookie(c *gin.Context, provider, purpose string) (string, error) {
	cookie, err := c.Request.Cookie(loginOAuthCookieName(provider, purpose))
	if err != nil {
		return "", err
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	return string(decoded), err
}

func clearLoginOAuthCookies(c *gin.Context, provider string, secure bool) {
	for _, purpose := range []string{"state", "verifier", "redirect", "intent", "link"} {
		http.SetCookie(c.Writer, &http.Cookie{
			Name: loginOAuthCookieName(provider, purpose), Path: loginOAuthCookiePath(provider), MaxAge: -1,
			HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
		})
	}
}

func truncateFragmentValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > loginOAuthMaxFragmentLen {
		value = value[:loginOAuthMaxFragmentLen]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func buildBearerAuthorization(tokenType, accessToken string) (string, error) {
	if tokenType = strings.TrimSpace(tokenType); tokenType == "" {
		tokenType = "Bearer"
	}
	if !strings.EqualFold(tokenType, "Bearer") {
		return "", errors.New("unsupported token type")
	}
	if accessToken = strings.TrimSpace(accessToken); accessToken == "" || strings.ContainsAny(accessToken, " \t\r\n") {
		return "", errors.New("invalid access token")
	}
	return "Bearer " + accessToken, nil
}
