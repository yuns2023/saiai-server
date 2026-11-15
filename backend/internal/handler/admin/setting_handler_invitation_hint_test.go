package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpdateSettings_InvitationCodeHint(t *testing.T) {
	h, _, repo, router := modelPriceHandlerFixture(t)
	router.PUT("/settings", h.UpdateSettings)
	router.GET("/settings", h.GetSettings)

	w := priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{"invitation_code_hint": "  加群获取：https://t.me/x  "})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "加群获取：https://t.me/x", repo.values[service.SettingKeyInvitationCodeHint])

	// Admin clients that predate the field omit it: the stored text survives.
	w = priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{"site_name": "CCYou.dev"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "加群获取：https://t.me/x", repo.values[service.SettingKeyInvitationCodeHint])

	// GET serializes it for the admin form.
	w = priceHandlerRequest(router, http.MethodGet, "/settings", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got struct {
		Data struct {
			InvitationCodeHint string `json:"invitation_code_hint"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "加群获取：https://t.me/x", got.Data.InvitationCodeHint)

	// Too long (counted in characters, not bytes).
	w = priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{"invitation_code_hint": strings.Repeat("邀", MaxInvitationCodeHintRunes+1)})
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	w = priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{"invitation_code_hint": strings.Repeat("邀", MaxInvitationCodeHintRunes)})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// An explicit empty string clears it (no default text is substituted).
	w = priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{"invitation_code_hint": ""})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "", repo.values[service.SettingKeyInvitationCodeHint])
}
