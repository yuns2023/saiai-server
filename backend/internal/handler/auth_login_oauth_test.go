package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBuildLoginOAuthAuthorizeURLAlwaysUsesStateAndPKCE(t *testing.T) {
	provider := loginOAuthProvider{
		Name:         "github",
		AuthorizeURL: "https://github.com/login/oauth/authorize",
		Scope:        "read:user user:email",
		Config: config.LoginOAuthProviderConfig{
			ClientID:    "client-id",
			RedirectURL: "https://example.com/api/v1/auth/oauth/github/callback",
		},
	}
	raw, err := buildLoginOAuthAuthorizeURL(provider, "state-value", "challenge-value")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	require.Equal(t, "github.com", u.Host)
	require.Equal(t, "state-value", u.Query().Get("state"))
	require.Equal(t, "challenge-value", u.Query().Get("code_challenge"))
	require.Equal(t, "S256", u.Query().Get("code_challenge_method"))
	require.Equal(t, provider.Config.RedirectURL, u.Query().Get("redirect_uri"))
}

func TestSanitizeFrontendRedirectPathRejectsOpenRedirects(t *testing.T) {
	require.Equal(t, "/dashboard", sanitizeFrontendRedirectPath(" /dashboard "))
	for _, candidate := range []string{"dashboard", "//evil.example", "https://evil.example", "/ok\nLocation: evil"} {
		require.Empty(t, sanitizeFrontendRedirectPath(candidate), candidate)
	}
}

func TestFetchGoogleIdentityRequiresVerifiedEmail(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verified bool
		wantErr  bool
	}{
		{name: "verified", verified: true},
		{name: "unverified", verified: false, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer provider-token", r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"sub":"google-subject","email":"alice@example.com","email_verified":` + map[bool]string{true: "true", false: "false"}[tc.verified] + `,"name":"Alice"}`))
			}))
			defer server.Close()

			identity, err := fetchLoginOAuthIdentity(context.Background(), loginOAuthProvider{Name: "google", UserURL: server.URL}, &loginOAuthTokenResponse{AccessToken: "provider-token", TokenType: "Bearer"})
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "google", identity.Provider)
			require.Equal(t, "google-subject", identity.Subject)
			require.Equal(t, "alice@example.com", identity.VerifiedEmail)
		})
	}
}

func TestFetchGitHubIdentityRequiresPrimaryVerifiedEmail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":12345,"login":"alice"}`))
	})
	mux.HandleFunc("/emails", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"email":"other@example.com","primary":false,"verified":true},{"email":"alice@example.com","primary":true,"verified":true}]`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	identity, err := fetchLoginOAuthIdentity(context.Background(), loginOAuthProvider{Name: "github", UserURL: server.URL + "/user", EmailURL: server.URL + "/emails"}, &loginOAuthTokenResponse{AccessToken: "provider-token"})
	require.NoError(t, err)
	require.Equal(t, "12345", identity.Subject)
	require.Equal(t, "alice@example.com", identity.VerifiedEmail)
}

func TestParseLoginOAuthTokenResponseSupportsGitHubFormResponse(t *testing.T) {
	token, ok := parseLoginOAuthTokenResponse("access_token=provider-token&token_type=bearer")
	require.True(t, ok)
	require.Equal(t, "provider-token", token.AccessToken)
	require.Equal(t, "bearer", token.TokenType)
}

func TestLoginOAuthLinkContextIsBoundToProviderStateAndUser(t *testing.T) {
	h := &AuthHandler{cfg: &config.Config{JWT: config.JWTConfig{Secret: "test-secret"}}}
	signed := h.signLoginOAuthLink("github", "state-one", 42)

	userID, err := h.verifyLoginOAuthLink("github", "state-one", signed)
	require.NoError(t, err)
	require.EqualValues(t, 42, userID)

	_, err = h.verifyLoginOAuthLink("google", "state-one", signed)
	require.Error(t, err)
	_, err = h.verifyLoginOAuthLink("github", "state-two", signed)
	require.Error(t, err)
	_, err = h.verifyLoginOAuthLink("github", "state-one", "43"+signed[2:])
	require.Error(t, err)
}
