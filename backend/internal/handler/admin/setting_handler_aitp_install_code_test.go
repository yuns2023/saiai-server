package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpdateSettings_AITPInstallCodeEnabled(t *testing.T) {
	h, _, repo, router := modelPriceHandlerFixture(t)
	router.PUT("/settings", h.UpdateSettings)
	router.GET("/settings", h.GetSettings)

	readFlag := func(body []byte) bool {
		t.Helper()
		var got struct {
			Data struct {
				Enabled *bool `json:"aitp_install_code_enabled"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(body, &got))
		require.NotNil(t, got.Data.Enabled, "aitp_install_code_enabled must be serialized")
		return *got.Data.Enabled
	}

	// Default off.
	w := priceHandlerRequest(router, http.MethodGet, "/settings", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.False(t, readFlag(w.Body.Bytes()))

	w = priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{"aitp_install_code_enabled": true})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "true", repo.values[service.SettingKeyAITPInstallCodeEnabled])
	require.True(t, readFlag(w.Body.Bytes()))

	// "Not sent" keeps the stored value (old admin pages, the Ops settings script).
	w = priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{"site_name": "CCYou.dev"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "true", repo.values[service.SettingKeyAITPInstallCodeEnabled])

	// An explicit false turns it off.
	w = priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{"aitp_install_code_enabled": false})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "false", repo.values[service.SettingKeyAITPInstallCodeEnabled])

	w = priceHandlerRequest(router, http.MethodGet, "/settings", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.False(t, readFlag(w.Body.Bytes()))
}

func TestDiffSettings_AITPInstallCodeEnabled(t *testing.T) {
	before := &service.SystemSettings{}
	after := &service.SystemSettings{AITPInstallCodeEnabled: true}
	require.Contains(t, diffSettings(before, after, UpdateSettingsRequest{}), "aitp_install_code_enabled")
	require.NotContains(t, diffSettings(after, after, UpdateSettingsRequest{}), "aitp_install_code_enabled")
}
