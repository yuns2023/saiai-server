//go:build embed

package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// saiaiCLIExternalFiles 是被分流到 SAIAI_CLIENT_DIR 的文件白名单。
// 不在此白名单内的 /saiai-cli/* 请求（例如 setup.sh）继续由 embed.FS 提供。
var saiaiCLIExternalFiles = map[string]struct{}{
	"saiai-linux-x86_64":        {},
	"saiai-linux-aarch64":       {},
	"saiai-macos-x86_64":        {},
	"saiai-macos-aarch64":       {},
	"saiai-windows-x86_64.exe":  {},
	"saiai-windows-aarch64.exe": {},
	"manifest.json":             {},
}

const (
	// NonceHTMLPlaceholder is the placeholder for nonce in HTML script tags
	NonceHTMLPlaceholder = "__CSP_NONCE_VALUE__"
)

//go:embed all:dist
var frontendFS embed.FS

// PublicSettingsProvider is an interface to fetch public settings
type PublicSettingsProvider interface {
	GetPublicSettingsForInjection(ctx context.Context) (any, error)
}

// FrontendServer serves the embedded frontend with settings injection
type FrontendServer struct {
	distFS     fs.FS
	fileServer http.Handler
	baseHTML   []byte
	cache      *HTMLCache
	settings   PublicSettingsProvider
	cliDir     string
}

// NewFrontendServer creates a new frontend server with settings injection.
// cliDir 指定外部 saiai binary 所在目录（例如 /var/lib/saiai-server/client-runtime/saiai-cli/）。
// 为空时 /saiai-cli/saiai-* 请求会返回 503 提示运维跑 sync-saiai-cli.sh。
func NewFrontendServer(settingsProvider PublicSettingsProvider, cliDir string) (*FrontendServer, error) {
	distFS, err := fs.Sub(frontendFS, "dist")
	if err != nil {
		return nil, err
	}

	// Read base HTML once
	file, err := distFS.Open("index.html")
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	baseHTML, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	cache := NewHTMLCache()
	cache.SetBaseHTML(baseHTML)

	return &FrontendServer{
		distFS:     distFS,
		fileServer: http.FileServer(http.FS(distFS)),
		baseHTML:   baseHTML,
		cache:      cache,
		settings:   settingsProvider,
		cliDir:     strings.TrimSpace(cliDir),
	}, nil
}

// InvalidateCache invalidates the HTML cache (call when settings change)
func (s *FrontendServer) InvalidateCache() {
	if s != nil && s.cache != nil {
		s.cache.Invalidate()
	}
}

// Middleware returns the Gin middleware handler
func (s *FrontendServer) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqPath := c.Request.URL.Path

		// Skip API routes
		if shouldBypassEmbeddedFrontend(reqPath) {
			c.Next()
			return
		}

		// saiai binary 从 SAIAI_CLIENT_DIR 读 filesystem；非白名单的 /saiai-cli/*
		// （例如 setup.sh）回退到下面的 embed.FS 服务。
		if s.tryServeExternalCLI(c) {
			return
		}

		// setup.sh / setup.ps1 / setup.cmd are embedded wrapper scripts. Render
		// their default binary download base from the current public origin, so
		// local and custom-domain deployments can use short one-line commands.
		if s.tryServeCLIWrapper(c) {
			return
		}

		cleanPath := strings.TrimPrefix(reqPath, "/")
		if cleanPath == "" {
			cleanPath = "index.html"
		}

		// For index.html or SPA routes, serve with injected settings
		if cleanPath == "index.html" || !s.fileExists(cleanPath) {
			s.serveIndexHTML(c)
			return
		}

		// Serve static files normally
		s.fileServer.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
}

// tryServeExternalCLI 处理 /saiai-cli/* 外部资产白名单请求：命中时从
// s.cliDir 读 filesystem 返回 (true)；未命中返回 false 由后续逻辑处理。
func (s *FrontendServer) tryServeExternalCLI(c *gin.Context) bool {
	req := c.Request
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}

	const prefix = "/saiai-cli/"
	if !strings.HasPrefix(req.URL.Path, prefix) {
		return false
	}
	name := strings.TrimPrefix(req.URL.Path, prefix)
	if _, ok := saiaiCLIExternalFiles[name]; !ok {
		return false
	}
	// 路径穿越双保险：白名单已保证名字，但仍拒绝任何包含分隔符的请求。
	if name != path.Base(name) || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		c.String(http.StatusBadRequest, "invalid saiai binary path")
		c.Abort()
		return true
	}

	if s.cliDir == "" {
		c.String(http.StatusServiceUnavailable, "saiai binaries unavailable: SAIAI_CLIENT_DIR is not configured; run sync-saiai-cli.sh")
		c.Abort()
		return true
	}
	info, err := os.Stat(s.cliDir)
	if err != nil || !info.IsDir() {
		c.String(http.StatusServiceUnavailable, "saiai binaries unavailable: configured client bundle is missing; run sync-saiai-cli.sh")
		c.Abort()
		return true
	}

	http.ServeFile(c.Writer, req, filepath.Join(s.cliDir, name))
	c.Abort()
	return true
}

func (s *FrontendServer) tryServeCLIWrapper(c *gin.Context) bool {
	req := c.Request
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}

	cleanPath := strings.TrimPrefix(req.URL.Path, "/")
	contentType := ""
	switch cleanPath {
	case "saiai-cli/setup.sh":
		contentType = "text/x-shellscript; charset=utf-8"
	case "saiai-cli/setup.ps1":
		contentType = "text/plain; charset=utf-8"
	case "saiai-cli/setup.cmd":
		contentType = "text/plain; charset=utf-8"
	default:
		return false
	}

	data, err := fs.ReadFile(s.distFS, cleanPath)
	if err != nil {
		c.String(http.StatusNotFound, "saiai-cli wrapper not found")
		c.Abort()
		return true
	}

	rendered := renderCLIWrapperDownloadBase(data, publicRequestOrigin(req)+"/saiai-cli")
	c.Header("Cache-Control", "no-cache")
	if req.Method == http.MethodHead {
		c.Header("Content-Type", contentType)
		c.Status(http.StatusOK)
		c.Abort()
		return true
	}

	c.Data(http.StatusOK, contentType, rendered)
	c.Abort()
	return true
}

func renderCLIWrapperDownloadBase(data []byte, downloadBase string) []byte {
	downloadBase = strings.TrimRight(strings.TrimSpace(downloadBase), "/")
	if downloadBase == "" {
		return data
	}
	return bytes.ReplaceAll(data, []byte("https://api.saiai.top/saiai-cli"), []byte(downloadBase))
}

func publicRequestOrigin(req *http.Request) string {
	proto := firstForwardedHeaderValue(req.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		proto = forwardedHeaderParam(req.Header.Get("Forwarded"), "proto")
	}
	proto = normalizePublicProto(proto)
	if proto == "" && strings.EqualFold(strings.TrimSpace(req.Header.Get("X-Forwarded-Ssl")), "on") {
		proto = "https"
	}
	if proto == "" {
		if req.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}

	host := firstForwardedHeaderValue(req.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = forwardedHeaderParam(req.Header.Get("Forwarded"), "host")
	}
	host = normalizePublicHost(host)
	if host == "" {
		host = normalizePublicHost(req.Host)
	}
	if host == "" {
		host = "localhost"
	}

	return proto + "://" + host
}

func normalizePublicProto(proto string) string {
	switch strings.ToLower(strings.TrimSpace(proto)) {
	case "http", "https":
		return strings.ToLower(strings.TrimSpace(proto))
	default:
		return ""
	}
}

func normalizePublicHost(host string) string {
	host = strings.Trim(strings.TrimSpace(host), `"`)
	if host == "" || strings.ContainsAny(host, "/\\?#") || strings.ContainsAny(host, "\r\n\t ") {
		return ""
	}
	return host
}

func firstForwardedHeaderValue(value string) string {
	if value == "" {
		return ""
	}
	return strings.TrimSpace(strings.Split(value, ",")[0])
}

func forwardedHeaderParam(value string, key string) string {
	if value == "" || key == "" {
		return ""
	}
	first := firstForwardedHeaderValue(value)
	for _, part := range strings.Split(first, ";") {
		name, raw, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), key) {
			continue
		}
		return strings.Trim(strings.TrimSpace(raw), `"`)
	}
	return ""
}

func (s *FrontendServer) fileExists(path string) bool {
	file, err := s.distFS.Open(path)
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}

func (s *FrontendServer) serveIndexHTML(c *gin.Context) {
	// Get nonce from context (generated by SecurityHeaders middleware)
	nonce := middleware.GetNonceFromContext(c)

	// Check cache first
	cached := s.cache.Get()
	if cached != nil {
		// Check If-None-Match for 304 response
		if match := c.GetHeader("If-None-Match"); match == cached.ETag {
			c.Status(http.StatusNotModified)
			c.Abort()
			return
		}

		// Replace nonce placeholder with actual nonce before serving
		content := replaceNoncePlaceholder(cached.Content, nonce)

		c.Header("ETag", cached.ETag)
		c.Header("Cache-Control", "no-cache") // Must revalidate
		c.Data(http.StatusOK, "text/html; charset=utf-8", content)
		c.Abort()
		return
	}

	// Cache miss - fetch settings and render
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	settings, err := s.settings.GetPublicSettingsForInjection(ctx)
	if err != nil {
		// Fallback: serve without injection
		c.Data(http.StatusOK, "text/html; charset=utf-8", s.baseHTML)
		c.Abort()
		return
	}

	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		// Fallback: serve without injection
		c.Data(http.StatusOK, "text/html; charset=utf-8", s.baseHTML)
		c.Abort()
		return
	}

	rendered := s.injectSettings(settingsJSON)
	s.cache.Set(rendered, settingsJSON)

	// Replace nonce placeholder with actual nonce before serving
	content := replaceNoncePlaceholder(rendered, nonce)

	cached = s.cache.Get()
	if cached != nil {
		c.Header("ETag", cached.ETag)
	}
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", content)
	c.Abort()
}

func (s *FrontendServer) injectSettings(settingsJSON []byte) []byte {
	// Create the script tag to inject with nonce placeholder
	// The placeholder will be replaced with actual nonce at request time
	script := []byte(`<script nonce="` + NonceHTMLPlaceholder + `">window.__APP_CONFIG__=` + string(settingsJSON) + `;</script>`)

	// Inject before </head>
	headClose := []byte("</head>")
	result := bytes.Replace(s.baseHTML, headClose, append(script, headClose...), 1)

	// Replace <title> with custom site name so the browser tab shows it immediately
	result = injectSiteTitle(result, settingsJSON)

	return result
}

// injectSiteTitle replaces the static <title> in HTML with the configured site name.
// This ensures the browser tab shows the correct title before JS executes.
func injectSiteTitle(html, settingsJSON []byte) []byte {
	var cfg struct {
		SiteName string `json:"site_name"`
	}
	if err := json.Unmarshal(settingsJSON, &cfg); err != nil || cfg.SiteName == "" {
		return html
	}

	// Find and replace the existing <title>...</title>
	titleStart := bytes.Index(html, []byte("<title>"))
	titleEnd := bytes.Index(html, []byte("</title>"))
	if titleStart == -1 || titleEnd == -1 || titleEnd <= titleStart {
		return html
	}

	newTitle := []byte("<title>" + cfg.SiteName + " - AI API Gateway</title>")
	var buf bytes.Buffer
	buf.Write(html[:titleStart])
	buf.Write(newTitle)
	buf.Write(html[titleEnd+len("</title>"):])
	return buf.Bytes()
}

// replaceNoncePlaceholder replaces the nonce placeholder with actual nonce value
func replaceNoncePlaceholder(html []byte, nonce string) []byte {
	return bytes.ReplaceAll(html, []byte(NonceHTMLPlaceholder), []byte(nonce))
}

// ServeEmbeddedFrontend returns a middleware for serving embedded frontend
// This is the legacy function for backward compatibility when no settings provider is available
func ServeEmbeddedFrontend() gin.HandlerFunc {
	distFS, err := fs.Sub(frontendFS, "dist")
	if err != nil {
		panic("failed to get dist subdirectory: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(distFS))

	return func(c *gin.Context) {
		path := c.Request.URL.Path

		if shouldBypassEmbeddedFrontend(path) {
			c.Next()
			return
		}

		cleanPath := strings.TrimPrefix(path, "/")
		if cleanPath == "" {
			cleanPath = "index.html"
		}

		if file, err := distFS.Open(cleanPath); err == nil {
			_ = file.Close()
			fileServer.ServeHTTP(c.Writer, c.Request)
			c.Abort()
			return
		}

		serveIndexHTML(c, distFS)
	}
}

func shouldBypassEmbeddedFrontend(path string) bool {
	trimmed := strings.TrimSpace(path)
	return strings.HasPrefix(trimmed, "/api/") ||
		strings.HasPrefix(trimmed, "/v1/") ||
		strings.HasPrefix(trimmed, "/v1beta/") ||
		strings.HasPrefix(trimmed, "/sora/") ||
		strings.HasPrefix(trimmed, "/antigravity/") ||
		strings.HasPrefix(trimmed, "/setup/") ||
		trimmed == "/health" ||
		trimmed == "/responses" ||
		strings.HasPrefix(trimmed, "/responses/")
}

func serveIndexHTML(c *gin.Context, fsys fs.FS) {
	file, err := fsys.Open("index.html")
	if err != nil {
		c.String(http.StatusNotFound, "Frontend not found")
		c.Abort()
		return
	}
	defer func() { _ = file.Close() }()

	content, err := io.ReadAll(file)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to read index.html")
		c.Abort()
		return
	}

	c.Data(http.StatusOK, "text/html; charset=utf-8", content)
	c.Abort()
}

func HasEmbeddedFrontend() bool {
	_, err := frontendFS.ReadFile("dist/index.html")
	return err == nil
}
