package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpdateSettings_ClientBrandKeepsPreviousAndValidates(t *testing.T) {
	h, _, repo, router := modelPriceHandlerFixture(t)
	router.PUT("/settings", h.UpdateSettings)
	router.GET("/settings", h.GetSettings)

	w := priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{
		"client_brand_name": "  CCYou ", "client_setup_command": " aitp init ",
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "CCYou", repo.values[service.SettingKeyClientBrandName])
	require.Equal(t, "aitp init", repo.values[service.SettingKeyClientSetupCommand])
	var put struct {
		Data struct {
			ClientBrandName    string `json:"client_brand_name"`
			ClientSetupCommand string `json:"client_setup_command"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &put))
	require.Equal(t, "CCYou", put.Data.ClientBrandName)
	require.Equal(t, "aitp init", put.Data.ClientSetupCommand)
	// The save is visible to the next request without waiting for the cache TTL.
	brand := h.settingService.ClientBrand(context.Background())
	require.Equal(t, "CCYou", brand.Name())
	require.Equal(t, "aitp init", brand.SetupCommand())

	// Old admin pages and the Ops settings script omit the fields: values survive.
	w = priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{"site_name": "CCYou.dev", "maintenance_mode_enabled": false})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "CCYou", repo.values[service.SettingKeyClientBrandName])
	require.Equal(t, "aitp init", repo.values[service.SettingKeyClientSetupCommand])

	w = priceHandlerRequest(router, http.MethodGet, "/settings", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got struct {
		Data struct {
			ClientBrandName    string `json:"client_brand_name"`
			ClientSetupCommand string `json:"client_setup_command"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "CCYou", got.Data.ClientBrandName)
	require.Equal(t, "aitp init", got.Data.ClientSetupCommand)

	for _, body := range []map[string]any{
		{"client_brand_name": strings.Repeat("品", service.MaxClientBrandNameRunes+1)},
		{"client_brand_name": "CC\nYou"},
		{"client_brand_name": "CC\u0000You"},
		{"client_brand_name": "ReClaude"},
		{"client_setup_command": strings.Repeat("x", service.MaxClientSetupCommandRunes+1)},
		{"client_setup_command": "aitp\tinit"},
		{"client_brand_name": "SAiAi"},
		{"client_setup_command": "saiai init"},
	} {
		w = priceHandlerRequest(router, http.MethodPut, "/settings", body)
		require.Equal(t, http.StatusBadRequest, w.Code, "%v: %s", body, w.Body.String())
		require.Equal(t, "CCYou", repo.values[service.SettingKeyClientBrandName], "rejected request writes nothing")
		require.Equal(t, "aitp init", repo.values[service.SettingKeyClientSetupCommand])
	}

	// Explicit empty strings clear them: the site is back to the original texts.
	w = priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{"client_brand_name": "", "client_setup_command": "  "})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "", repo.values[service.SettingKeyClientBrandName])
	require.Equal(t, "", repo.values[service.SettingKeyClientSetupCommand])
	require.False(t, h.settingService.ClientBrand(context.Background()).Customized())
}

func TestDiffSettings_ClientBrandIsAudited(t *testing.T) {
	before := &service.SystemSettings{}
	after := &service.SystemSettings{ClientBrandName: "CCYou", ClientSetupCommand: "aitp init"}
	changed := diffSettings(before, after, UpdateSettingsRequest{})
	require.Contains(t, changed, "client_brand_name")
	require.Contains(t, changed, "client_setup_command")
	require.NotContains(t, diffSettings(after, after, UpdateSettingsRequest{}), "client_brand_name")
}
