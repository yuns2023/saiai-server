package web

import (
	"fmt"
	"path"
	"strings"
)

// UserUI selects which embedded frontend serves the customer-facing pages.
//
// classic: the original SPA serves every page (default; existing sites).
// aog:     the AOGCode SPA (dist-aog) serves customer pages while the classic
//
//	SPA keeps serving the admin console (/admin, /admin/*) and the
//	first-install wizard entry (/setup).
type UserUI string

const (
	UserUIClassic UserUI = "classic"
	UserUIAOG     UserUI = "aog"
)

// aogAssetPrefix is the URL namespace owned exclusively by the AOG build
// (vite base assets + public files). The classic build never writes here.
const aogAssetPrefix = "/_aog/"

// ParseUserUI normalizes a configured user UI value. Empty means classic.
func ParseUserUI(raw string) (UserUI, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(UserUIClassic):
		return UserUIClassic, nil
	case string(UserUIAOG):
		return UserUIAOG, nil
	default:
		return "", fmt.Errorf("unsupported user UI %q (want classic or aog)", raw)
	}
}

// FrontendOptions configures NewFrontendServerWithOptions.
type FrontendOptions struct {
	CLIDir         string
	TrustedProxies []string
	UserUI         UserUI
}

// uiRoute is where an aog-mode request goes after API bypass and CLI checks.
type uiRoute int

const (
	// routeAOGIndex: render the AOG index.html (SPA route of the new UI).
	routeAOGIndex uiRoute = iota
	// routeClassicIndex: render the classic index.html (admin console, setup).
	routeClassicIndex
	// routeAOGAsset: a file under /_aog/; served from the AOG build or 404.
	routeAOGAsset
	// routeClassicAsset: a file the classic build owns (e.g. /assets/*, /logo.png).
	routeClassicAsset
	// routeNotFound: a request inside an asset namespace that has no file.
	routeNotFound
)

// isClassicOwnedPage reports whether a page path belongs to the classic SPA
// in aog mode. The classic router applies the same rule (keep in sync with
// frontend/src/router/userUiHandoff.ts) so the two never bounce a page.
func isClassicOwnedPage(p string) bool {
	return p == "/admin" || strings.HasPrefix(p, "/admin/") || p == "/setup"
}

// classifyAOGPath decides ownership of a decoded URL path in aog mode.
// classicFileExists / aogFileExists receive a cleaned, slash-free relative
// name and must report regular files only (never directories).
func classifyAOGPath(reqPath string, classicFileExists, aogFileExists func(string) bool) (uiRoute, string) {
	// Normalize once; every decision below uses the same cleaned view so the
	// router, the file lookup and the browser cannot disagree about "..".
	cleaned := path.Clean("/" + strings.TrimSpace(reqPath))

	if strings.HasPrefix(cleaned+"/", aogAssetPrefix) {
		name := strings.TrimPrefix(cleaned, "/")
		if cleaned != strings.TrimSuffix(aogAssetPrefix, "/") && aogFileExists(name) {
			return routeAOGAsset, name
		}
		return routeNotFound, ""
	}

	if isClassicOwnedPage(cleaned) {
		return routeClassicIndex, ""
	}

	if cleaned == "/" || cleaned == "/index.html" {
		return routeAOGIndex, ""
	}

	name := strings.TrimPrefix(cleaned, "/")
	if classicFileExists(name) {
		return routeClassicAsset, name
	}
	// Missing files inside the classic chunk directory must not turn into an
	// HTML page: a stale dynamic import would otherwise parse HTML as JS.
	if cleaned == "/assets" || strings.HasPrefix(cleaned, "/assets/") {
		return routeNotFound, ""
	}
	return routeAOGIndex, ""
}
