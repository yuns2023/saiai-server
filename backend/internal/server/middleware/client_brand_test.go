//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Independent baseline copied from the text before client brand settings.
const baselineInvalidCodexAPIKeyMessage = "API key 无效或已删除，请更新 SAIAI Key 后重新初始化 Codex。"

func TestInvalidCodexAPIKeyMessageFollowsBrand(t *testing.T) {
	for _, values := range [][2]string{{"", ""}, {"  ", "\t"}} {
		brand, err := service.NewClientBrand(values[0], values[1])
		require.NoError(t, err)
		require.Equal(t, baselineInvalidCodexAPIKeyMessage, invalidCodexAPIKeyMessageFor(brand))
	}
	require.Equal(t, baselineInvalidCodexAPIKeyMessage, invalidCodexAPIKeyMessageFor(service.ClientBrand{}))
	for _, tc := range []struct{ brand, command, want string }{
		{"CCYou", "", "API key 无效或已删除，请更新 CCYou Key 后重新初始化 Codex。"},
		{"", "aitp init", "API key 无效或已删除，请更新 API Key 后重新运行 aitp init。"},
		{"CCYou", "aitp init", "API key 无效或已删除，请更新 CCYou Key 后重新运行 aitp init。"},
	} {
		brand, err := service.NewClientBrand(tc.brand, tc.command)
		require.NoError(t, err)
		got := invalidCodexAPIKeyMessageFor(brand)
		require.Equal(t, tc.want, got)
		require.NotContains(t, strings.ToLower(got), "saiai")
	}
}

// The site's brand flows from the settings table through the ClientBrand
// middleware into the API-key middleware's rejection; only the message changes.
func TestClientBrandMiddlewareBrandsInvalidKeyRejection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name     string
		settings map[string]string
		want     string
	}{
		{"missing settings", map[string]string{}, baselineInvalidCodexAPIKeyMessage},
		{"empty settings", map[string]string{service.SettingKeyClientBrandName: "", service.SettingKeyClientSetupCommand: ""}, baselineInvalidCodexAPIKeyMessage},
		{"branded", map[string]string{service.SettingKeyClientBrandName: "CCYou", service.SettingKeyClientSetupCommand: "aitp init"}, "API key 无效或已删除，请更新 CCYou Key 后重新运行 aitp init。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := service.NewSettingService(&maintenanceSettingRepo{values: tc.settings}, &config.Config{})
			repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
				return nil, service.ErrAPIKeyNotFound
			}}
			cfg := &config.Config{RunMode: config.RunModeStandard}
			apiKeys := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
			router := gin.New()
			router.Use(ClientBrand(settings))
			router.POST("/v1/responses", gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeys, nil, nil, cfg)), func(c *gin.Context) {
				t.Error("invalid Key reached the handler")
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			req.Header.Set("Authorization", "Bearer TEST_ONLY_DELETED")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusUnauthorized, w.Code)
			var body struct {
				ErrorResponse
				Error struct {
					Code    string `json:"code"`
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, "INVALID_API_KEY", body.Code)
			require.Equal(t, "INVALID_API_KEY", body.Error.Code)
			require.Equal(t, "authentication_error", body.Error.Type)
			require.Equal(t, tc.want, body.Message)
			require.Equal(t, tc.want, body.Error.Message)
		})
	}
}

func TestClientBrandMiddlewareAttachesOneSnapshotOnlyWhenCustomized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	run := func(settings *service.SettingService) (service.ClientBrand, bool) {
		var got service.ClientBrand
		var sameRequest bool
		router := gin.New()
		var original *http.Request
		router.Use(func(c *gin.Context) { original = c.Request; c.Next() })
		router.Use(ClientBrand(settings))
		router.GET("/x", func(c *gin.Context) {
			got = service.ClientBrandFromGin(c)
			sameRequest = c.Request == original
		})
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
		return got, sameRequest
	}

	got, same := run(service.NewSettingService(&maintenanceSettingRepo{values: map[string]string{}}, &config.Config{}))
	require.Equal(t, service.ClientBrand{}, got)
	require.True(t, same, "unbranded sites keep the request untouched")

	got, same = run(nil)
	require.Equal(t, service.ClientBrand{}, got)
	require.True(t, same)

	got, same = run(service.NewSettingService(&maintenanceSettingRepo{values: map[string]string{service.SettingKeyClientBrandName: "CCYou"}}, &config.Config{}))
	require.Equal(t, "CCYou", got.Name())
	require.False(t, same)
}
