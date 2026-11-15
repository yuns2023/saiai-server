package admin

import (
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
