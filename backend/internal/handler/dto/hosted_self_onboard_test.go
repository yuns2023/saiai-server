//go:build unit

package dto

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func hostedJSONKeys(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Explicit allowlists: the user and admin application DTOs never carry the
// upstream token, proxy credentials or the upstream identity.
func TestHostedApplicationDTOAllowlists(t *testing.T) {
	accountID := int64(9)
	reviewed := time.Now()
	app := &service.HostedApplication{ID: 1, UserID: 2, UserEmail: "u@example.com", Status: "approved", DisplayName: "n",
		UpstreamAccountUUID: "0b6b8f86-0d9b-4f4e-9f55-6a1f5d7c0a11", UpstreamOrgUUID: "org", UpstreamEmail: "o@example.com",
		ProxyProtocol: "socks5h", ProxyHost: "proxy.example.com", ProxyPort: 1080, HasProxyAuth: true,
		AccountID: &accountID, ReviewedAt: &reviewed, CreatedAt: reviewed}

	user := HostedApplicationFromService(app)
	require.Equal(t, []string{"account_id", "created_at", "display_name", "id", "proxy_display", "reject_reason", "reviewed_at", "status", "upstream_email"},
		hostedJSONKeys(t, user))
	require.Equal(t, "socks5h://proxy.example.com:1080", user.ProxyDisplay)

	admin := AdminHostedApplicationFromService(app)
	require.Equal(t, []string{"account_id", "created_at", "display_name", "has_proxy_auth", "id", "proxy_display", "proxy_host", "proxy_port",
		"proxy_protocol", "reject_reason", "reviewed_at", "status", "upstream_email", "user_email", "user_id"}, hostedJSONKeys(t, admin))

	for _, v := range []any{user, admin, HostedApplicationListFromService([]service.HostedApplication{*app})} {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "access_token")
		require.NotContains(t, string(raw), "password")
		require.NotContains(t, string(raw), app.UpstreamAccountUUID)
	}
	// Empty list is [] (not null) for the frontend.
	raw, err := json.Marshal(HostedApplicationListFromService(nil))
	require.NoError(t, err)
	require.JSONEq(t, `{"items":[]}`, string(raw))
}
