//go:build embed

package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	classicIndexFixture = `<!doctype html><html><head><title>classic</title></head><body>CLASSIC</body></html>`
	aogIndexFixture     = `<!doctype html><html><head><title>aog</title></head><body>AOG</body></html>`
)

type aogTestSettings struct {
	err   error
	calls int
}

func (p *aogTestSettings) GetPublicSettingsForInjection(context.Context) (any, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return map[string]string{"site_name": "AOGCode"}, nil
}

func aogTestFS() (fstest.MapFS, fstest.MapFS) {
	classic := fstest.MapFS{
		"index.html":          {Data: []byte(classicIndexFixture)},
		"assets/index-abc.js": {Data: []byte("classic-js")},
		"logo.png":            {Data: []byte("classic-logo")},
	}
	aog := fstest.MapFS{
		"index.html":       {Data: []byte(aogIndexFixture)},
		"_aog/app-123.js":  {Data: []byte("aog-js")},
		"_aog/favicon.svg": {Data: []byte("<svg/>")},
	}
	return classic, aog
}

func newAOGTestRouter(t *testing.T, provider PublicSettingsProvider) (*gin.Engine, *FrontendServer) {
	t.Helper()
	classic, aog := aogTestFS()
	server, err := newFrontendServer(provider, classic, aog, FrontendOptions{UserUI: UserUIAOG})
	require.NoError(t, err)
	router := gin.New()
	router.Use(server.Middleware())
	router.NoRoute(func(c *gin.Context) { c.String(http.StatusTeapot, "api") })
	return router, server
}

func doGet(router http.Handler, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

func TestFrontendServer_AOGMode_Routing(t *testing.T) {
	router, _ := newAOGTestRouter(t, &aogTestSettings{})

	for _, p := range []string{"/", "/dashboard", "/login?redirect=%2Fadmin%2Fusers", "/purchase?out_trade_no=1", "/no/such"} {
		w := doGet(router, p)
		assert.Equal(t, http.StatusOK, w.Code, p)
		assert.Contains(t, w.Body.String(), "AOG", p)
		assert.Contains(t, w.Body.String(), `window.__SAIAI_USER_UI__="aog";`, p)
		assert.Contains(t, w.Body.String(), `"site_name":"AOGCode"`, p)
	}
	for _, p := range []string{"/admin", "/admin/", "/admin/users?page=2", "/setup"} {
		w := doGet(router, p)
		assert.Equal(t, http.StatusOK, w.Code, p)
		assert.Contains(t, w.Body.String(), "CLASSIC", p)
		assert.Contains(t, w.Body.String(), `window.__SAIAI_USER_UI__="aog";`, p)
	}

	assert.Equal(t, "aog-js", doGet(router, "/_aog/app-123.js").Body.String())
	assert.Equal(t, "classic-js", doGet(router, "/assets/index-abc.js").Body.String())
	assert.Equal(t, "classic-logo", doGet(router, "/logo.png").Body.String())
	for _, p := range []string{"/_aog/missing.js", "/_aog/", "/assets/missing.js", "/assets/"} {
		assert.Equal(t, http.StatusNotFound, doGet(router, p).Code, p)
	}

	// API and setup API paths still bypass the frontend.
	for _, p := range []string{"/api/v1/settings/public", "/v1/messages", "/setup/status", "/health"} {
		assert.Equal(t, http.StatusTeapot, doGet(router, p).Code, p)
	}
}

func TestFrontendServer_AOGMode_SeparateCachesInvalidateTogether(t *testing.T) {
	provider := &aogTestSettings{}
	router, server := newAOGTestRouter(t, provider)

	aogFirst := doGet(router, "/dashboard")
	classicFirst := doGet(router, "/admin")
	require.NotEqual(t, aogFirst.Header().Get("ETag"), classicFirst.Header().Get("ETag"))
	assert.Equal(t, 2, provider.calls)

	// Cached: no further settings reads, each UI keeps its own HTML.
	assert.Contains(t, doGet(router, "/keys").Body.String(), "AOG")
	assert.Contains(t, doGet(router, "/admin/users").Body.String(), "CLASSIC")
	assert.Equal(t, 2, provider.calls)

	server.InvalidateCache()
	doGet(router, "/keys")
	doGet(router, "/admin/users")
	assert.Equal(t, 4, provider.calls)
}

func TestFrontendServer_AOGMode_NeverRevalidatesNonceHTML(t *testing.T) {
	router, _ := newAOGTestRouter(t, &aogTestSettings{})

	for _, p := range []string{"/dashboard", "/admin"} {
		first := doGet(router, p)
		etag := first.Header().Get("ETag")
		require.NotEmpty(t, etag, p)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Header.Set("If-None-Match", etag)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, p)
		assert.Contains(t, w.Body.String(), `window.__SAIAI_USER_UI__="aog";`, p)
	}
}

func TestFrontendServer_AOGMode_SettingsFailureKeepsMarker(t *testing.T) {
	router, _ := newAOGTestRouter(t, &aogTestSettings{err: errors.New("db down")})

	admin := doGet(router, "/admin")
	assert.Equal(t, http.StatusOK, admin.Code)
	assert.Contains(t, admin.Body.String(), "CLASSIC")
	assert.Contains(t, admin.Body.String(), `window.__SAIAI_USER_UI__="aog";`)
	assert.NotContains(t, admin.Body.String(), NonceHTMLPlaceholder)
}

func TestFrontendServer_AOGMode_RequiresAOGBuild(t *testing.T) {
	classic, _ := aogTestFS()
	_, err := newFrontendServer(&aogTestSettings{}, classic, nil, FrontendOptions{UserUI: UserUIAOG})
	assert.Error(t, err)

	_, err = newFrontendServer(&aogTestSettings{}, classic, fstest.MapFS{}, FrontendOptions{UserUI: UserUIAOG})
	assert.Error(t, err)

	_, err = newFrontendServer(&aogTestSettings{}, classic, nil, FrontendOptions{UserUI: "bogus"})
	assert.Error(t, err)
}

func TestFrontendServer_ClassicMode_NoUserUIMarker(t *testing.T) {
	classic, aog := aogTestFS()
	server, err := newFrontendServer(&aogTestSettings{}, classic, aog, FrontendOptions{})
	require.NoError(t, err)
	router := gin.New()
	router.Use(server.Middleware())

	for _, p := range []string{"/", "/dashboard", "/admin"} {
		body := doGet(router, p).Body.String()
		assert.Contains(t, body, "CLASSIC", p)
		assert.False(t, strings.Contains(body, "__SAIAI_USER_UI__"), p)
	}
	// Classic mode never serves the AOG namespace.
	assert.Contains(t, doGet(router, "/_aog/app-123.js").Body.String(), "CLASSIC")
}
