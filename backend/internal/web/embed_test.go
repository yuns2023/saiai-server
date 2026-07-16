//go:build embed

package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func writeCLIWrapperFixtures(t *testing.T, directory string) {
	t.Helper()
	fixtures := map[string]string{
		"setup.sh":  `DEFAULT_DOWNLOAD_BASE="https://api.saiai.top/saiai-cli"`,
		"setup.ps1": `$downloadBase = "https://api.saiai.top/saiai-cli"` + "\n" + `$manifestUrl = "$downloadBase/manifest.json"`,
		"setup.cmd": `set "DOWNLOAD_BASE=https://api.saiai.top/saiai-cli"`,
	}
	for name, contents := range fixtures {
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o555))
	}
}

func TestInjectSiteTitle(t *testing.T) {
	t.Run("replaces_title_with_site_name", func(t *testing.T) {
		html := []byte(`<html><head><title>SAiAi - AI API Gateway</title></head><body></body></html>`)
		settingsJSON := []byte(`{"site_name":"MyCustomSite"}`)

		result := injectSiteTitle(html, settingsJSON)

		assert.Contains(t, string(result), "<title>MyCustomSite - AI API Gateway</title>")
		assert.NotContains(t, string(result), "<title>SAiAi - AI API Gateway</title>")
	})

	t.Run("returns_unchanged_when_site_name_empty", func(t *testing.T) {
		html := []byte(`<html><head><title>SAiAi - AI API Gateway</title></head><body></body></html>`)
		settingsJSON := []byte(`{"site_name":""}`)

		result := injectSiteTitle(html, settingsJSON)

		assert.Equal(t, string(html), string(result))
	})

	t.Run("returns_unchanged_when_site_name_missing", func(t *testing.T) {
		html := []byte(`<html><head><title>SAiAi - AI API Gateway</title></head><body></body></html>`)
		settingsJSON := []byte(`{"other_field":"value"}`)

		result := injectSiteTitle(html, settingsJSON)

		assert.Equal(t, string(html), string(result))
	})

	t.Run("returns_unchanged_when_invalid_json", func(t *testing.T) {
		html := []byte(`<html><head><title>SAiAi - AI API Gateway</title></head><body></body></html>`)
		settingsJSON := []byte(`{invalid json}`)

		result := injectSiteTitle(html, settingsJSON)

		assert.Equal(t, string(html), string(result))
	})

	t.Run("returns_unchanged_when_no_title_tag", func(t *testing.T) {
		html := []byte(`<html><head></head><body></body></html>`)
		settingsJSON := []byte(`{"site_name":"MyCustomSite"}`)

		result := injectSiteTitle(html, settingsJSON)

		assert.Equal(t, string(html), string(result))
	})

	t.Run("returns_unchanged_when_title_has_attributes", func(t *testing.T) {
		// The function looks for "<title>" literally, so attributes are not supported
		// This is acceptable since index.html uses plain <title> without attributes
		html := []byte(`<html><head><title lang="en">SAiAi</title></head><body></body></html>`)
		settingsJSON := []byte(`{"site_name":"NewSite"}`)

		result := injectSiteTitle(html, settingsJSON)

		// Should return unchanged since <title> with attributes is not matched
		assert.Equal(t, string(html), string(result))
	})

	t.Run("preserves_rest_of_html", func(t *testing.T) {
		html := []byte(`<html><head><meta charset="UTF-8"><title>SAiAi</title><script src="app.js"></script></head><body><div id="app"></div></body></html>`)
		settingsJSON := []byte(`{"site_name":"TestSite"}`)

		result := injectSiteTitle(html, settingsJSON)

		assert.Contains(t, string(result), `<meta charset="UTF-8">`)
		assert.Contains(t, string(result), `<script src="app.js"></script>`)
		assert.Contains(t, string(result), `<div id="app"></div>`)
		assert.Contains(t, string(result), "<title>TestSite - AI API Gateway</title>")
	})
}

func TestReplaceNoncePlaceholder(t *testing.T) {
	t.Run("replaces_single_placeholder", func(t *testing.T) {
		html := []byte(`<script nonce="__CSP_NONCE_VALUE__">console.log('test');</script>`)
		nonce := "abc123xyz"

		result := replaceNoncePlaceholder(html, nonce)

		expected := `<script nonce="abc123xyz">console.log('test');</script>`
		assert.Equal(t, expected, string(result))
	})

	t.Run("replaces_multiple_placeholders", func(t *testing.T) {
		html := []byte(`<script nonce="__CSP_NONCE_VALUE__">a</script><script nonce="__CSP_NONCE_VALUE__">b</script>`)
		nonce := "nonce123"

		result := replaceNoncePlaceholder(html, nonce)

		assert.Equal(t, 2, strings.Count(string(result), `nonce="nonce123"`))
		assert.NotContains(t, string(result), NonceHTMLPlaceholder)
	})

	t.Run("handles_empty_nonce", func(t *testing.T) {
		html := []byte(`<script nonce="__CSP_NONCE_VALUE__">test</script>`)
		nonce := ""

		result := replaceNoncePlaceholder(html, nonce)

		assert.Equal(t, `<script nonce="">test</script>`, string(result))
	})

	t.Run("no_placeholder_returns_unchanged", func(t *testing.T) {
		html := []byte(`<script>console.log('test');</script>`)
		nonce := "abc123"

		result := replaceNoncePlaceholder(html, nonce)

		assert.Equal(t, string(html), string(result))
	})

	t.Run("handles_empty_html", func(t *testing.T) {
		html := []byte(``)
		nonce := "abc123"

		result := replaceNoncePlaceholder(html, nonce)

		assert.Empty(t, result)
	})
}

func TestNonceHTMLPlaceholder(t *testing.T) {
	t.Run("constant_value", func(t *testing.T) {
		assert.Equal(t, "__CSP_NONCE_VALUE__", NonceHTMLPlaceholder)
	})
}

// mockSettingsProvider implements PublicSettingsProvider for testing
type mockSettingsProvider struct {
	settings any
	err      error
	called   int
}

func (m *mockSettingsProvider) GetPublicSettingsForInjection(ctx context.Context) (any, error) {
	m.called++
	return m.settings, m.err
}

func TestFrontendServer_InjectSettings(t *testing.T) {
	t.Run("injects_settings_with_nonce_placeholder", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"key": "value"},
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		settingsJSON := []byte(`{"test":"data"}`)
		result := server.injectSettings(settingsJSON)

		// Should contain the script with nonce placeholder
		assert.Contains(t, string(result), `<script nonce="__CSP_NONCE_VALUE__">`)
		assert.Contains(t, string(result), `window.__APP_CONFIG__={"test":"data"};`)
		assert.Contains(t, string(result), `</script></head>`)
	})

	t.Run("injects_before_head_close", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"key": "value"},
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		settingsJSON := []byte(`{}`)
		result := server.injectSettings(settingsJSON)

		// Script should be injected before </head>
		headCloseIndex := bytes.Index(result, []byte("</head>"))
		scriptIndex := bytes.Index(result, []byte(`<script nonce="`))

		assert.True(t, scriptIndex < headCloseIndex, "script should be before </head>")
	})

	t.Run("handles_complex_settings", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]any{
				"nested": map[string]any{
					"array": []int{1, 2, 3},
				},
			},
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		settingsJSON := []byte(`{"nested":{"array":[1,2,3]},"special":"<>&"}`)
		result := server.injectSettings(settingsJSON)

		assert.Contains(t, string(result), `window.__APP_CONFIG__={"nested":{"array":[1,2,3]},"special":"<>&"};`)
	})
}

func TestFrontendServer_ServeIndexHTML(t *testing.T) {
	t.Run("serves_html_with_nonce", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		// Create a gin context with nonce
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		// Set nonce in context (simulating SecurityHeaders middleware)
		testNonce := "test-nonce-12345"
		c.Set(middleware.CSPNonceKey, testNonce)

		server.serveIndexHTML(c)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Header().Get("Content-Type"), "text/html")

		body := w.Body.String()
		// Nonce placeholder should be replaced
		assert.NotContains(t, body, NonceHTMLPlaceholder)
		assert.Contains(t, body, `nonce="`+testNonce+`"`)
	})

	t.Run("caches_html_content", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		// First request
		w1 := httptest.NewRecorder()
		c1, _ := gin.CreateTestContext(w1)
		c1.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c1.Set(middleware.CSPNonceKey, "nonce1")

		server.serveIndexHTML(c1)
		assert.Equal(t, 1, provider.called)

		// Second request - should use cache
		w2 := httptest.NewRecorder()
		c2, _ := gin.CreateTestContext(w2)
		c2.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c2.Set(middleware.CSPNonceKey, "nonce2")

		server.serveIndexHTML(c2)
		// Settings provider should not be called again
		assert.Equal(t, 1, provider.called)

		// But nonce should be different
		assert.Contains(t, w2.Body.String(), `nonce="nonce2"`)
	})

	t.Run("sets_etag_header", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Set(middleware.CSPNonceKey, "nonce123")

		server.serveIndexHTML(c)

		etag := w.Header().Get("ETag")
		assert.NotEmpty(t, etag)
		assert.True(t, strings.HasPrefix(etag, `"`))
		assert.True(t, strings.HasSuffix(etag, `"`))
	})

	t.Run("returns_304_for_matching_etag", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		// Use a real router for proper 304 handling
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(middleware.CSPNonceKey, "test-nonce")
			c.Next()
		})
		router.Use(server.Middleware())

		// First request to populate cache and get ETag
		w1 := httptest.NewRecorder()
		req1 := httptest.NewRequest(http.MethodGet, "/", nil)
		router.ServeHTTP(w1, req1)
		etag := w1.Header().Get("ETag")
		require.NotEmpty(t, etag)

		// Second request with If-None-Match
		w2 := httptest.NewRecorder()
		req2 := httptest.NewRequest(http.MethodGet, "/", nil)
		req2.Header.Set("If-None-Match", etag)
		router.ServeHTTP(w2, req2)

		assert.Equal(t, http.StatusNotModified, w2.Code)
		assert.Empty(t, w2.Body.String())
	})

	t.Run("sets_cache_control_header", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Set(middleware.CSPNonceKey, "nonce123")

		server.serveIndexHTML(c)

		assert.Equal(t, "no-cache", w.Header().Get("Cache-Control"))
	})

	t.Run("fallback_on_settings_error", func(t *testing.T) {
		provider := &mockSettingsProvider{
			err: context.DeadlineExceeded,
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		// Invalidate cache to force settings fetch
		server.InvalidateCache()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Set(middleware.CSPNonceKey, "nonce123")

		server.serveIndexHTML(c)

		// Should still return 200 with base HTML
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
	})
}

func TestFrontendServer_InvalidateCache(t *testing.T) {
	t.Run("invalidates_cache", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		// First request to populate cache
		w1 := httptest.NewRecorder()
		c1, _ := gin.CreateTestContext(w1)
		c1.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c1.Set(middleware.CSPNonceKey, "nonce1")

		server.serveIndexHTML(c1)
		assert.Equal(t, 1, provider.called)

		// Invalidate cache
		server.InvalidateCache()

		// Update settings
		provider.settings = map[string]string{"test": "new_value"}

		// Second request should fetch new settings
		w2 := httptest.NewRecorder()
		c2, _ := gin.CreateTestContext(w2)
		c2.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c2.Set(middleware.CSPNonceKey, "nonce2")

		server.serveIndexHTML(c2)
		assert.Equal(t, 2, provider.called)
	})

	t.Run("handles_nil_server", func(t *testing.T) {
		var server *FrontendServer
		// Should not panic
		assert.NotPanics(t, func() {
			server.InvalidateCache()
		})
	})

	t.Run("handles_nil_cache", func(t *testing.T) {
		server := &FrontendServer{}
		// Should not panic
		assert.NotPanics(t, func() {
			server.InvalidateCache()
		})
	})
}

func TestFrontendServer_Middleware(t *testing.T) {
	t.Run("skips_api_routes", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		apiPaths := []string{
			"/api/v1/users",
			"/v1/models",
			"/v1beta/chat",
			"/sora/v1/models",
			"/antigravity/test",
			"/setup/init",
			"/health",
			"/responses",
			"/responses/compact",
		}

		for _, path := range apiPaths {
			t.Run(path, func(t *testing.T) {
				router := gin.New()
				router.Use(server.Middleware())
				nextCalled := false
				router.GET(path, func(c *gin.Context) {
					nextCalled = true
					c.String(http.StatusOK, "ok")
				})

				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				router.ServeHTTP(w, req)

				assert.True(t, nextCalled, "next handler should be called for API route")
			})
		}
	})

	t.Run("skips_responses_compact_post_routes", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())
		nextCalled := false
		router.POST("/responses/compact", func(c *gin.Context) {
			nextCalled = true
			c.String(http.StatusOK, `{"ok":true}`)
		})

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/responses/compact", strings.NewReader(`{"model":"gpt-5"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.True(t, nextCalled, "next handler should be called for compact API route")
		assert.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, `{"ok":true}`, w.Body.String())
	})

	t.Run("serves_index_for_spa_routes", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(middleware.CSPNonceKey, "test-nonce")
			c.Next()
		})
		router.Use(server.Middleware())

		spaPaths := []string{
			"/",
			"/dashboard",
			"/users/123",
			"/settings/profile",
		}

		for _, path := range spaPaths {
			t.Run(path, func(t *testing.T) {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				router.ServeHTTP(w, req)

				assert.Equal(t, http.StatusOK, w.Code)
				assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
			})
		}
	})

	t.Run("serves_static_files", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())

		// Request for existing static file
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/logo.png", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Header().Get("Content-Type"), "image/png")
	})

	t.Run("serves_cli_manifest_from_external_asset_dir", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}
		cliDir := t.TempDir()
		require.NoError(t, os.WriteFile(
			filepath.Join(cliDir, "manifest.json"),
			[]byte(`{"manifest_schema":1,"client_mode":"global-config","configuration_schema_version":1,"version":"1.0.0","assets":{"saiai-linux-x86_64":{"sha256":"abc","size":3}}}`),
			0o644,
		))

		server, err := NewFrontendServer(provider, cliDir)
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/saiai-cli/manifest.json", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.JSONEq(t, `{"manifest_schema":1,"client_mode":"global-config","configuration_schema_version":1,"version":"1.0.0","assets":{"saiai-linux-x86_64":{"sha256":"abc","size":3}}}`, w.Body.String())
	})

	t.Run("serves_cli_binary_without_caching", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}
		cliDir := t.TempDir()
		require.NoError(t, os.WriteFile(
			filepath.Join(cliDir, "saiai-linux-x86_64"),
			[]byte("binary fixture"),
			0o555,
		))

		server, err := NewFrontendServer(provider, cliDir)
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodHead, "/saiai-cli/saiai-linux-x86_64", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	})

	t.Run("does_not_cache_unavailable_cli_assets", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}
		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/saiai-cli/manifest.json", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	})

	t.Run("does_not_expose_configured_cli_path_when_bundle_is_missing", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}
		cliDir := filepath.Join(t.TempDir(), "operator-only", "missing-bundle")
		server, err := NewFrontendServer(provider, cliDir)
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/saiai-cli/manifest.json", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.NotContains(t, w.Body.String(), cliDir)
		assert.Equal(t, "saiai client bundle unavailable: configured client bundle is missing; run sync-saiai-cli.sh", strings.TrimSpace(w.Body.String()))
	})

	t.Run("returns_503_instead_of_falling_back_when_wrapper_is_missing", func(t *testing.T) {
		provider := &mockSettingsProvider{settings: map[string]string{"test": "value"}}
		cliDir := t.TempDir()
		server, err := NewFrontendServer(provider, cliDir)
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://gateway.example/saiai-cli/setup.sh", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.Contains(t, w.Body.String(), "configured bundle is incomplete")
	})

	t.Run("serves_cli_wrapper_with_current_origin_download_base", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}
		cliDir := t.TempDir()
		writeCLIWrapperFixtures(t, cliDir)

		server, err := NewFrontendServer(provider, cliDir)
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://192.168.50.10:18025/saiai-cli/setup.sh", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.Contains(t, w.Body.String(), `DEFAULT_DOWNLOAD_BASE="http://192.168.50.10:18025/saiai-cli"`)
		assert.NotContains(t, w.Body.String(), `DEFAULT_DOWNLOAD_BASE="https://api.saiai.top/saiai-cli"`)
	})

	t.Run("serves_cli_wrappers_from_the_active_external_bundle", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}
		cliDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(cliDir, "setup.sh"), []byte("external https://api.saiai.top/saiai-cli wrapper"), 0o555))

		server, err := NewFrontendServer(provider, cliDir)
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://gateway.example/saiai-cli/setup.sh", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.Equal(t, "external https://gateway.example/saiai-cli wrapper", w.Body.String())
	})

	t.Run("serves_cli_wrapper_with_forwarded_origin_download_base", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}
		cliDir := t.TempDir()
		writeCLIWrapperFixtures(t, cliDir)

		server, err := NewFrontendServer(provider, cliDir, "127.0.0.1/32")
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://api.example.com/saiai-cli/setup.ps1", nil)
		req.RemoteAddr = "127.0.0.1:43100"
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", "attacker.example")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.Contains(t, w.Body.String(), `"https://api.example.com/saiai-cli"`)
		assert.Contains(t, w.Body.String(), `$manifestUrl = "$downloadBase/manifest.json"`)
		assert.NotContains(t, w.Body.String(), `"https://api.saiai.top/saiai-cli"`)
	})

	t.Run("rejects_an_invalid_direct_host_instead_of_rendering_executable_text", func(t *testing.T) {
		provider := &mockSettingsProvider{settings: map[string]string{"test": "value"}}
		cliDir := t.TempDir()
		writeCLIWrapperFixtures(t, cliDir)
		server, err := NewFrontendServer(provider, cliDir)
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://gateway.example/saiai-cli/setup.sh", nil)
		req.Host = "gateway.example$(touch-pwned)"
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.NotContains(t, w.Body.String(), "touch-pwned")
	})

	t.Run("never_injects_forwarded_script_metacharacters_into_any_wrapper", func(t *testing.T) {
		provider := &mockSettingsProvider{settings: map[string]string{"test": "value"}}
		cliDir := t.TempDir()
		writeCLIWrapperFixtures(t, cliDir)
		server, err := NewFrontendServer(provider, cliDir, "127.0.0.1/32")
		require.NoError(t, err)

		tests := []struct {
			path      string
			header    string
			malicious string
		}{
			{path: "/saiai-cli/setup.sh", header: "X-Forwarded-Host", malicious: "api.example$(touch-pwned)"},
			{path: "/saiai-cli/setup.ps1", header: "X-Forwarded-Host", malicious: "api.example;Start-Process-calc"},
			{path: "/saiai-cli/setup.cmd", header: "Forwarded", malicious: `for=10.0.0.1;host="api.example%COMSPEC%"`},
		}
		for _, test := range tests {
			t.Run(test.path, func(t *testing.T) {
				router := gin.New()
				router.Use(server.Middleware())

				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "http://safe.example"+test.path, nil)
				req.RemoteAddr = "127.0.0.1:43100"
				req.Header.Set("X-Forwarded-Proto", "https")
				req.Header.Set(test.header, test.malicious)
				router.ServeHTTP(w, req)

				assert.Equal(t, http.StatusOK, w.Code)
				assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
				assert.Contains(t, w.Body.String(), "https://safe.example/saiai-cli")
				assert.NotContains(t, w.Body.String(), test.malicious)
				assert.NotContains(t, w.Body.String(), "touch-pwned")
				assert.NotContains(t, w.Body.String(), "Start-Process-calc")
				assert.NotContains(t, w.Body.String(), "%COMSPEC%")
			})
		}
	})
}

func TestPublicRequestOrigin(t *testing.T) {
	t.Run("uses_request_host_for_direct_http", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://192.168.50.10:18025/saiai-cli/setup.sh", nil)

		assert.Equal(t, "http://192.168.50.10:18025", publicRequestOrigin(req, nil))
	})

	t.Run("uses_trusted_forwarded_proto_but_never_forwarded_host", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://api.saiai.top/saiai-cli/setup.sh", nil)
		req.RemoteAddr = "127.0.0.1:43100"
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", "attacker.example")

		assert.Equal(t, "https://api.saiai.top", publicRequestOrigin(req, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}))
	})

	t.Run("allows_a_trusted_plaintext_tcp_relay_without_proto_for_a_private_ip_literal", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://192.168.50.10:18080/saiai-cli/setup.sh", nil)
		req.RemoteAddr = "127.0.0.1:43100"

		assert.Equal(t, "http://192.168.50.10:18080", publicRequestOrigin(req, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}))
	})

	t.Run("rejects_a_trusted_plaintext_peer_without_proto_for_non_local_authorities", func(t *testing.T) {
		for _, authority := range []string{"api.saiai.top", "8.8.8.8:18080", "localhost:18080"} {
			req := httptest.NewRequest(http.MethodGet, "http://"+authority+"/saiai-cli/setup.sh", nil)
			req.RemoteAddr = "127.0.0.1:43100"

			assert.Empty(t, publicRequestOrigin(req, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}), authority)
		}
	})

	t.Run("does_not_apply_the_tcp_relay_exception_to_present_invalid_proto_headers", func(t *testing.T) {
		for _, values := range [][]string{{""}, {"http, https"}, {"http", "https"}, {"ftp"}} {
			req := httptest.NewRequest(http.MethodGet, "http://192.168.50.10:18080/saiai-cli/setup.sh", nil)
			req.RemoteAddr = "127.0.0.1:43100"
			for _, value := range values {
				req.Header.Add("X-Forwarded-Proto", value)
			}

			assert.Empty(t, publicRequestOrigin(req, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}), values)
		}
	})

	t.Run("rejects_forwarded_proto_without_the_canonical_proxy_header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://saiai.example.com/saiai-cli/setup.sh", nil)
		req.RemoteAddr = "127.0.0.1:43100"
		req.Header.Set("Forwarded", `for=10.0.0.1;proto=https;host="attacker.example"`)

		assert.Empty(t, publicRequestOrigin(req, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}))
	})

	t.Run("rejects_ambiguous_forwarded_proto_values", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://saiai.example.com/saiai-cli/setup.sh", nil)
		req.RemoteAddr = "127.0.0.1:43100"
		req.Header.Set("X-Forwarded-Proto", "http, https")

		assert.Empty(t, publicRequestOrigin(req, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}))
	})

	t.Run("rejects_a_forwarded_downgrade_of_a_real_tls_request", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://saiai.example.com/saiai-cli/setup.sh", nil)
		req.RemoteAddr = "127.0.0.1:43100"
		req.Header.Set("X-Forwarded-Proto", "http")

		assert.Empty(t, publicRequestOrigin(req, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}))
	})

	t.Run("ignores_forwarded_origin_from_an_untrusted_peer", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://safe.example:8080/saiai-cli/setup.sh", nil)
		req.RemoteAddr = "192.0.2.44:43100"
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", "attacker.example")

		assert.Equal(t, "http://safe.example:8080", publicRequestOrigin(req, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}))
	})
}

func TestNormalizePublicHost(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "dns", raw: "API.Example.COM:443", want: "api.example.com:443"},
		{name: "localhost", raw: "localhost:8080", want: "localhost:8080"},
		{name: "ipv4", raw: "192.168.50.10:18080", want: "192.168.50.10:18080"},
		{name: "ipv6", raw: "[2001:db8::1]:8443", want: "[2001:db8::1]:8443"},
		{name: "shell substitution", raw: "api.example$(id)", want: ""},
		{name: "backticks", raw: "api.example`id`", want: ""},
		{name: "powershell separator", raw: "api.example;calc", want: ""},
		{name: "cmd expansion", raw: "api.example%COMSPEC%", want: ""},
		{name: "quoted host", raw: `"api.example"`, want: ""},
		{name: "bad port", raw: "api.example:443x", want: ""},
		{name: "userinfo", raw: "user@api.example", want: ""},
		{name: "unbracketed ipv6", raw: "2001:db8::1", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, normalizePublicHost(test.raw))
		})
	}
}

func TestNewFrontendServer(t *testing.T) {
	t.Run("rejects_an_invalid_trusted_proxy_boundary", func(t *testing.T) {
		provider := &mockSettingsProvider{settings: map[string]string{"test": "value"}}
		for _, invalid := range []string{"not-a-proxy", "", " 127.0.0.1 "} {
			server, err := NewFrontendServer(provider, "", "127.0.0.1/32", invalid)

			require.Error(t, err)
			assert.Nil(t, server)
			assert.Contains(t, err.Error(), "invalid trusted proxy")
		}
	})

	t.Run("creates_server_successfully", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, "")

		require.NoError(t, err)
		assert.NotNil(t, server)
		assert.NotNil(t, server.distFS)
		assert.NotNil(t, server.fileServer)
		assert.NotNil(t, server.baseHTML)
		assert.NotNil(t, server.cache)
		assert.Equal(t, provider, server.settings)
	})

	t.Run("reads_base_html", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, "")
		require.NoError(t, err)

		assert.NotEmpty(t, server.baseHTML)
		assert.Contains(t, string(server.baseHTML), "<!doctype html>")
	})
}

func TestHasEmbeddedFrontend(t *testing.T) {
	t.Run("returns_true_when_frontend_embedded", func(t *testing.T) {
		result := HasEmbeddedFrontend()
		assert.True(t, result)
	})
}

// Tests for legacy ServeEmbeddedFrontend function
func TestServeEmbeddedFrontend(t *testing.T) {
	t.Run("serves_static_files", func(t *testing.T) {
		middleware := ServeEmbeddedFrontend()

		router := gin.New()
		router.Use(middleware)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/logo.png", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Header().Get("Content-Type"), "image/png")
	})

	t.Run("serves_index_html_for_root", func(t *testing.T) {
		middleware := ServeEmbeddedFrontend()

		router := gin.New()
		router.Use(middleware)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
		assert.Contains(t, w.Body.String(), "<!doctype html>")
	})

	t.Run("serves_index_html_for_spa_routes", func(t *testing.T) {
		middleware := ServeEmbeddedFrontend()

		router := gin.New()
		router.Use(middleware)

		spaPaths := []string{"/dashboard", "/users/123", "/settings"}

		for _, path := range spaPaths {
			t.Run(path, func(t *testing.T) {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				router.ServeHTTP(w, req)

				assert.Equal(t, http.StatusOK, w.Code)
				assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
			})
		}
	})

	t.Run("skips_api_routes", func(t *testing.T) {
		middleware := ServeEmbeddedFrontend()

		apiPaths := []string{
			"/api/users",
			"/v1/models",
			"/v1beta/chat",
			"/sora/v1/models",
			"/antigravity/test",
			"/setup/init",
			"/health",
			"/responses",
			"/responses/compact",
		}

		for _, path := range apiPaths {
			t.Run(path, func(t *testing.T) {
				nextCalled := false
				router := gin.New()
				router.Use(middleware)
				router.GET(path, func(c *gin.Context) {
					nextCalled = true
					c.String(http.StatusOK, "ok")
				})

				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				router.ServeHTTP(w, req)

				assert.True(t, nextCalled, "next handler should be called for API route")
			})
		}
	})
}

// Tests for HTMLCache
func TestHTMLCache(t *testing.T) {
	t.Run("new_cache_returns_nil", func(t *testing.T) {
		cache := NewHTMLCache()
		assert.Nil(t, cache.Get())
	})

	t.Run("set_and_get", func(t *testing.T) {
		cache := NewHTMLCache()
		cache.SetBaseHTML([]byte("<html></html>"))

		html := []byte("<html><body>test</body></html>")
		settings := []byte(`{"key":"value"}`)
		cache.Set(html, settings)

		result := cache.Get()
		require.NotNil(t, result)
		assert.Equal(t, html, result.Content)
		assert.NotEmpty(t, result.ETag)
	})

	t.Run("invalidate_clears_cache", func(t *testing.T) {
		cache := NewHTMLCache()
		cache.SetBaseHTML([]byte("<html></html>"))

		html := []byte("<html><body>test</body></html>")
		settings := []byte(`{"key":"value"}`)
		cache.Set(html, settings)

		require.NotNil(t, cache.Get())

		cache.Invalidate()

		assert.Nil(t, cache.Get())
	})

	t.Run("etag_changes_with_settings", func(t *testing.T) {
		cache := NewHTMLCache()
		cache.SetBaseHTML([]byte("<html></html>"))

		html := []byte("<html><body>test</body></html>")

		cache.Set(html, []byte(`{"v":1}`))
		etag1 := cache.Get().ETag

		cache.Invalidate()
		cache.Set(html, []byte(`{"v":2}`))
		etag2 := cache.Get().ETag

		assert.NotEqual(t, etag1, etag2)
	})

	t.Run("etag_format", func(t *testing.T) {
		cache := NewHTMLCache()
		cache.SetBaseHTML([]byte("<html></html>"))

		cache.Set([]byte("<html></html>"), []byte(`{}`))
		result := cache.Get()

		// ETag should be quoted
		assert.True(t, strings.HasPrefix(result.ETag, `"`))
		assert.True(t, strings.HasSuffix(result.ETag, `"`))
		// Should contain dash separator
		assert.Contains(t, result.ETag[1:len(result.ETag)-1], "-")
	})
}

// Benchmark tests
func BenchmarkReplaceNoncePlaceholder(b *testing.B) {
	html := []byte(`<!DOCTYPE html><html><head><script nonce="__CSP_NONCE_VALUE__">window.__APP_CONFIG__={"test":"data"};</script></head><body></body></html>`)
	nonce := "abcdefghijklmnop123456=="

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		replaceNoncePlaceholder(html, nonce)
	}
}

func BenchmarkFrontendServerServeIndexHTML(b *testing.B) {
	provider := &mockSettingsProvider{
		settings: map[string]string{"test": "value"},
	}

	server, _ := NewFrontendServer(provider, "")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Set(middleware.CSPNonceKey, "test-nonce")

		server.serveIndexHTML(c)
	}
}
