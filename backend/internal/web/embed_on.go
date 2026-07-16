//go:build embed

package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// saiaiCLIExternalFiles 是完整、原子激活的 SAIAI client bundle 白名单。
// 二进制、manifest 和 wrapper 必须全部来自同一个 SAIAI_CLIENT_DIR。
var saiaiCLIExternalFiles = map[string]struct{}{
	"saiai-linux-x86_64":        {},
	"saiai-linux-aarch64":       {},
	"saiai-macos-x86_64":        {},
	"saiai-macos-aarch64":       {},
	"saiai-windows-x86_64.exe":  {},
	"saiai-windows-aarch64.exe": {},
	"manifest.json":             {},
	"setup.sh":                  {},
	"setup.ps1":                 {},
	"setup.cmd":                 {},
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
	// Forwarded origin headers are executable-wrapper inputs, so accept them
	// only from the same explicitly configured proxy boundary used by Gin.
	trustedProxyPrefixes []netip.Prefix
}

// NewFrontendServer creates a new frontend server with settings injection.
// cliDir 指定完整 SAIAI client bundle 所在目录（例如
// /var/lib/saiai-server/client-runtime/saiai-cli/）。为空或 bundle 不完整时，
// /saiai-cli/* 返回 503，提示运维先运行 sync-saiai-cli.sh。
func NewFrontendServer(settingsProvider PublicSettingsProvider, cliDir string, trustedProxies ...string) (*FrontendServer, error) {
	trustedProxyPrefixes, err := parseTrustedProxyPrefixes(trustedProxies)
	if err != nil {
		return nil, err
	}
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
		distFS:               distFS,
		fileServer:           http.FileServer(http.FS(distFS)),
		baseHTML:             baseHTML,
		cache:                cache,
		settings:             settingsProvider,
		cliDir:               strings.TrimSpace(cliDir),
		trustedProxyPrefixes: trustedProxyPrefixes,
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

		// 完整 SAIAI bundle 从 SAIAI_CLIENT_DIR 读取。wrapper 也从这里读取，
		// 但会按当前可信公开 origin 渲染默认下载地址。
		if s.tryServeExternalCLI(c) {
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

// tryServeExternalCLI 处理 /saiai-cli/* 完整外部 bundle 白名单请求。
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

	// These are mutable public URLs whose contents change only when the complete
	// client bundle is atomically activated. Do not let a browser, CDN, or proxy
	// combine a cached manifest from one release with a binary from another, and
	// do not cache a temporary deployment error for these paths.
	c.Header("Cache-Control", "no-store")
	if s.cliDir == "" {
		c.String(http.StatusServiceUnavailable, "saiai client bundle unavailable: SAIAI_CLIENT_DIR is not configured; run sync-saiai-cli.sh")
		c.Abort()
		return true
	}
	info, err := os.Stat(s.cliDir)
	if err != nil || !info.IsDir() {
		c.String(http.StatusServiceUnavailable, "saiai client bundle unavailable: configured client bundle is missing; run sync-saiai-cli.sh")
		c.Abort()
		return true
	}

	assetPath := filepath.Join(s.cliDir, name)
	assetInfo, err := os.Lstat(assetPath)
	if err != nil || !assetInfo.Mode().IsRegular() {
		c.String(http.StatusServiceUnavailable, "saiai client bundle unavailable: configured bundle is incomplete; run sync-saiai-cli.sh")
		c.Abort()
		return true
	}

	contentType := ""
	switch name {
	case "setup.sh":
		contentType = "text/x-shellscript; charset=utf-8"
	case "setup.ps1", "setup.cmd":
		contentType = "text/plain; charset=utf-8"
	}
	if contentType == "" {
		http.ServeFile(c.Writer, req, assetPath)
		c.Abort()
		return true
	}

	data, err := os.ReadFile(assetPath)
	if err != nil {
		c.String(http.StatusServiceUnavailable, "saiai client bundle unavailable: wrapper cannot be read; run sync-saiai-cli.sh")
		c.Abort()
		return true
	}

	origin := publicRequestOrigin(req, s.trustedProxyPrefixes)
	if origin == "" {
		c.String(http.StatusBadRequest, "invalid public request origin")
		c.Abort()
		return true
	}
	rendered := renderCLIWrapperDownloadBase(data, origin+"/saiai-cli")
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

func publicRequestOrigin(req *http.Request, trustedProxyPrefixes []netip.Prefix) string {
	trustedForwarder := requestFromTrustedProxy(req, trustedProxyPrefixes)
	host := normalizePublicHost(req.Host)
	if host == "" {
		return ""
	}

	proto := "http"
	if req.TLS != nil {
		proto = "https"
		if trustedForwarder && len(req.Header.Values("X-Forwarded-Proto")) > 0 {
			forwardedProto, ok := strictForwardedProto(req.Header)
			if !ok || forwardedProto != "https" {
				return ""
			}
		}
	} else if trustedForwarder {
		if len(req.Header.Values("X-Forwarded-Proto")) == 0 {
			// A raw plaintext TCP relay cannot add proxy headers. Preserve the
			// documented LAN helper path only when the request authority itself is
			// an explicitly local IP literal; public names/addresses still require
			// the trusted HTTP proxy to provide an unambiguous scheme.
			if !isPrivateOrLocalIPLiteralAuthority(host) {
				return ""
			}
		} else if forwardedProto, ok := strictForwardedProto(req.Header); !ok {
			return ""
		} else {
			proto = forwardedProto
		}
	}

	// The HTTP Host field is already carried end-to-end by a correctly
	// configured reverse proxy. Never choose a download authority from
	// X-Forwarded-Host/Forwarded: append-style proxy configurations can retain a
	// client-supplied, syntactically valid attacker domain as the first value.
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

func strictForwardedProto(header http.Header) (string, bool) {
	values := header.Values("X-Forwarded-Proto")
	if len(values) != 1 {
		return "", false
	}
	raw := values[0]
	if raw != strings.TrimSpace(raw) || strings.Contains(raw, ",") {
		return "", false
	}
	proto := normalizePublicProto(raw)
	return proto, proto != ""
}

func isPrivateOrLocalIPLiteralAuthority(host string) bool {
	hostPart := host
	if strings.HasPrefix(host, "[") {
		closing := strings.IndexByte(host, ']')
		if closing <= 1 {
			return false
		}
		hostPart = host[1:closing]
	} else if strings.Count(host, ":") == 1 {
		hostPart, _, _ = strings.Cut(host, ":")
	}

	address, err := netip.ParseAddr(hostPart)
	if err != nil || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	return address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast()
}

func normalizePublicHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" || len(host) > 320 || strings.ContainsAny(host, "\r\n\t ") {
		return ""
	}

	if strings.HasPrefix(host, "[") {
		closing := strings.IndexByte(host, ']')
		if closing <= 1 {
			return ""
		}
		address, err := netip.ParseAddr(host[1:closing])
		if err != nil || !address.Is6() || address.Zone() != "" {
			return ""
		}
		port, ok := normalizePublicPort(host[closing+1:])
		if !ok {
			return ""
		}
		return "[" + address.String() + "]" + port
	}

	hostPart := host
	port := ""
	if strings.Count(host, ":") == 1 {
		var rawPort string
		hostPart, rawPort, _ = strings.Cut(host, ":")
		var ok bool
		port, ok = normalizePublicPort(":" + rawPort)
		if !ok {
			return ""
		}
	} else if strings.Contains(host, ":") {
		// HTTP Host requires brackets around an IPv6 literal.
		return ""
	}

	if address, err := netip.ParseAddr(hostPart); err == nil {
		if !address.Is4() || address.Zone() != "" {
			return ""
		}
		return address.String() + port
	}
	if !validPublicDNSName(hostPart) {
		return ""
	}
	return strings.ToLower(hostPart) + port
}

func normalizePublicPort(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	if !strings.HasPrefix(raw, ":") || len(raw) == 1 {
		return "", false
	}
	portText := raw[1:]
	for _, character := range portText {
		if character < '0' || character > '9' {
			return "", false
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", false
	}
	return ":" + strconv.Itoa(port), true
}

func validPublicDNSName(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') ||
				(character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func parseTrustedProxyPrefixes(values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || value != raw {
			return nil, fmt.Errorf("invalid trusted proxy %q", raw)
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		if address, err := netip.ParseAddr(value); err == nil && address.Zone() == "" {
			prefixes = append(prefixes, netip.PrefixFrom(address, address.BitLen()))
			continue
		}
		return nil, fmt.Errorf("invalid trusted proxy %q", value)
	}
	return prefixes, nil
}

func requestFromTrustedProxy(req *http.Request, prefixes []netip.Prefix) bool {
	if req == nil || len(prefixes) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(req.RemoteAddr))
	if err != nil {
		host = strings.Trim(strings.TrimSpace(req.RemoteAddr), "[]")
	}
	address, err := netip.ParseAddr(host)
	if err != nil || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	for _, prefix := range prefixes {
		if prefix.Addr().Unmap().BitLen() != address.BitLen() {
			continue
		}
		candidate := prefix
		if prefix.Addr().Is4In6() {
			candidate = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96).Masked()
		}
		if candidate.Contains(address) {
			return true
		}
	}
	return false
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
