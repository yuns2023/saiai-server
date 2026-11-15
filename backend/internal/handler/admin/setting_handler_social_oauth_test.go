package admin

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestResolveSocialOAuthUpdate(t *testing.T) {
	on, off := true, false
	id := " cid "
	const fe = "https://aog.example.com"
	g := service.SocialOAuthProviderGoogle

	// Old clients that do not send the fields keep the stored values.
	out, msg := resolveSocialOAuthUpdate("Google", nil, nil, "", true, "stored", "s3cret", fe, g)
	require.Empty(t, msg)
	require.True(t, out.enabled)
	require.Equal(t, "stored", out.clientID)
	require.Empty(t, out.clientSecret, "empty secret means keep the stored one")

	// Disabling needs nothing else.
	out, msg = resolveSocialOAuthUpdate("Google", &off, nil, "", true, "", "", "", g)
	require.Empty(t, msg)
	require.False(t, out.enabled)

	// Enabling validates client id, secret and the derived callback URL.
	_, msg = resolveSocialOAuthUpdate("Google", &on, nil, "", false, "", "", fe, g)
	require.Contains(t, msg, "Client ID")
	_, msg = resolveSocialOAuthUpdate("Google", &on, &id, "", false, "", "", fe, g)
	require.Contains(t, msg, "Client Secret")
	_, msg = resolveSocialOAuthUpdate("Google", &on, &id, "new", false, "", "", "", g)
	require.Contains(t, msg, "Frontend URL")
	_, msg = resolveSocialOAuthUpdate("Google", &on, &id, "new", false, "", "", "https://aog.example.com/?x=1", g)
	require.Contains(t, msg, "Frontend URL")

	out, msg = resolveSocialOAuthUpdate("Google", &on, &id, "", false, "", "stored-secret", fe, g)
	require.Empty(t, msg)
	require.Equal(t, "cid", out.clientID)
}

type fakeReservedCounter struct {
	counts map[string]int
	err    error
	asked  []string
}

func (f *fakeReservedCounter) CountUsersWithEmailDomain(_ context.Context, domain string) (int, error) {
	f.asked = append(f.asked, domain)
	return f.counts[domain], f.err
}

func TestSyntheticEmailCollisionCheck(t *testing.T) {
	h := &SettingHandler{}
	require.Contains(t, h.syntheticEmailCollision(context.Background(), "Google", service.SocialOAuthProviderGoogle), "unable to check", "no counter: fail closed")

	counter := &fakeReservedCounter{counts: map[string]int{"@github-connect.invalid": 3}}
	h.SetReservedEmailCounter(counter)
	require.Empty(t, h.syntheticEmailCollision(context.Background(), "Google", service.SocialOAuthProviderGoogle))
	msg := h.syntheticEmailCollision(context.Background(), "GitHub", service.SocialOAuthProviderGitHub)
	require.Contains(t, msg, "3 existing user(s)")
	require.Contains(t, msg, "@github-connect.invalid")
	require.Equal(t, []string{"@google-connect.invalid", "@github-connect.invalid"}, counter.asked)

	counter.err = errors.New("db down")
	require.Contains(t, h.syntheticEmailCollision(context.Background(), "Google", service.SocialOAuthProviderGoogle), "unable to check")
}

func TestResolveSocialOAuthUpdate_FrontendURLMustBeOrigin(t *testing.T) {
	on := true
	id := "cid"
	_, msg := resolveSocialOAuthUpdate("Google", &on, &id, "s", false, "", "", "https://aog.example.com/app", service.SocialOAuthProviderGoogle)
	require.Contains(t, msg, "bare origin")
}

func TestUpdateSettings_FirstEnableRefusedOnSyntheticCollision(t *testing.T) {
	h, _, repo, router := modelPriceHandlerFixture(t)
	router.PUT("/settings", h.UpdateSettings)
	counter := &fakeReservedCounter{counts: map[string]int{"@google-connect.invalid": 2}}
	h.SetReservedEmailCounter(counter)
	enable := map[string]any{
		"frontend_url":               "https://aog.example.com",
		"google_oauth_enabled":       true,
		"google_oauth_client_id":     "gid",
		"google_oauth_client_secret": "gsecret",
	}

	w := priceHandlerRequest(router, http.MethodPut, "/settings", enable)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "2 existing user(s)")
	require.NotEqual(t, "true", repo.values[service.SettingKeyGoogleOAuthEnabled])

	// No collisions: enabled, and the never-cleared marker is recorded.
	counter.counts = map[string]int{}
	w = priceHandlerRequest(router, http.MethodPut, "/settings", enable)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "true", repo.values[service.SettingKeyGoogleOAuthEnabled])
	require.Equal(t, "true", repo.values[service.SettingKeyGoogleOAuthEverEnabled])

	// Disable, then re-enable: our own synthetic accounts now exist, the marker skips the check.
	disable := map[string]any{"frontend_url": "https://aog.example.com", "google_oauth_enabled": false}
	require.Equal(t, http.StatusOK, priceHandlerRequest(router, http.MethodPut, "/settings", disable).Code)
	counter.counts = map[string]int{"@google-connect.invalid": 5}
	counter.asked = nil
	w = priceHandlerRequest(router, http.MethodPut, "/settings", enable)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Empty(t, counter.asked)
}
