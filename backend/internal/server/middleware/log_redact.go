package middleware

import "strings"

// aitpInstallCodeRedactedPath replaces /i/<code> in logs and traces.
const aitpInstallCodeRedactedPath = "/i/[redacted]"

// RedactedLogPath returns the request path as it may appear in logs. A one-time install
// code (/i/<code>) is a bearer secret for an API key until it is used, so it never reaches
// a log line. Extra leading slashes and upper case are covered too: such a request does not
// redeem, so its code is still live.
func RedactedLogPath(path string) string {
	if isAITPInstallCodeRedeemPath(path) {
		return aitpInstallCodeRedactedPath
	}
	return path
}

func isAITPInstallCodeRedeemPath(path string) bool {
	p := strings.TrimLeft(path, "/")
	return len(p) != len(path) && len(p) >= 2 && (p[0] == 'i' || p[0] == 'I') && p[1] == '/'
}

// isAITPInstallCodeCreatePath matches POST /api/v1/keys/:id/install-code, whose response
// carries a fresh code.
func isAITPInstallCodeCreatePath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/keys/") && strings.HasSuffix(strings.TrimRight(path, "/"), "/install-code")
}
