package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpdateSettings_UserInvite(t *testing.T) {
	h, _, repo, router := modelPriceHandlerFixture(t)
	router.PUT("/settings", h.UpdateSettings)
	router.GET("/settings", h.GetSettings)

	w := priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{
		"user_invite_enabled": true, "user_invite_max_unused": 3, "user_invite_max_total": 50,
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "true", repo.values[service.SettingKeyUserInviteEnabled])
	require.Equal(t, "3", repo.values[service.SettingKeyUserInviteMaxUnused])
	require.Equal(t, "50", repo.values[service.SettingKeyUserInviteMaxTotal])

	// Omitted fields keep their stored values.
	w = priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{"site_name": "x"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "true", repo.values[service.SettingKeyUserInviteEnabled])
	require.Equal(t, "3", repo.values[service.SettingKeyUserInviteMaxUnused])

	for _, bad := range []map[string]any{
		{"user_invite_max_unused": 0},
		{"user_invite_max_unused": service.MaxUserInviteMaxUnused + 1},
		{"user_invite_max_total": -1},
		{"user_invite_max_total": service.MaxUserInviteMaxTotal + 1},
	} {
		w = priceHandlerRequest(router, http.MethodPut, "/settings", bad)
		require.Equal(t, http.StatusBadRequest, w.Code, bad)
	}

	w = priceHandlerRequest(router, http.MethodGet, "/settings", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		Data struct {
			Enabled   bool `json:"user_invite_enabled"`
			MaxUnused int  `json:"user_invite_max_unused"`
			MaxTotal  int  `json:"user_invite_max_total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.True(t, got.Data.Enabled)
	require.Equal(t, 3, got.Data.MaxUnused)
	require.Equal(t, 50, got.Data.MaxTotal)
}
