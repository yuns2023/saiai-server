package tlsfingerprint

import (
	"regexp"
	"strconv"
	"strings"
)

// CodexPlatform is the OS family named in a Codex User-Agent.
type CodexPlatform string

const (
	CodexPlatformLinux   CodexPlatform = "linux"
	CodexPlatformMacOS   CodexPlatform = "macos"
	CodexPlatformWindows CodexPlatform = "windows"
)

// CodexClient is what baseline selection reads from a Codex User-Agent.
type CodexClient struct {
	// Version is major.minor.patch; a prerelease suffix (0.163.0-alpha.10.2)
	// selects like its base version.
	Version  [3]int
	Platform CodexPlatform
}

// codexUserAgentPattern matches codex-rs get_codex_user_agent():
//
//	<originator>/<version> (<os type> <os version>; <arch>) <terminal> ...
//
// e.g. "codex-tui/0.163.0 (Windows 10.0.26200; x86_64) unknown (codex-tui; 0.163.0)"
// or "Codex Desktop/0.163.0-alpha.10.2 (Mac OS 15.5.0; arm64) Apple_Terminal/455.1 ...".
var codexUserAgentPattern = regexp.MustCompile(`^([^/]+)/(\d+)\.(\d+)\.(\d+)(?:-\S+)? \(([^;)]*); ([^)]+)\)`)

// ParseCodexUserAgent extracts version and platform. ok is false when the
// User-Agent does not have the codex-rs shape (including the bare
// "codex_cli_rs/x.y.z" the gateway itself sends for synthetic requests).
func ParseCodexUserAgent(userAgent string) (CodexClient, bool) {
	m := codexUserAgentPattern.FindStringSubmatch(strings.TrimSpace(userAgent))
	if m == nil {
		return CodexClient{}, false
	}
	var c CodexClient
	for i := range c.Version {
		n, err := strconv.Atoi(m[i+2])
		if err != nil {
			return CodexClient{}, false
		}
		c.Version[i] = n
	}
	// os_info::Type display names: "Mac OS", "Windows", or a Linux
	// distribution / other Unix name.
	switch osField := strings.TrimSpace(m[5]); {
	case strings.HasPrefix(osField, "Mac OS"), strings.HasPrefix(osField, "macOS"):
		c.Platform = CodexPlatformMacOS
	case strings.HasPrefix(osField, "Windows"):
		c.Platform = CodexPlatformWindows
	default:
		c.Platform = CodexPlatformLinux
	}
	return c, true
}

// codexBaselineRule: baseline serves role on platform ("" = all platforms)
// from version since (inclusive) up to the next rule's since.
type codexBaselineRule struct {
	role     Role
	platform CodexPlatform
	since    [3]int
	baseline string
}

// codexBaselineRules is the version/platform matrix; boundaries are the first
// official release with the new hello (docs/CODEX_TLS_FINGERPRINT.md).
var codexBaselineRules = []codexBaselineRule{
	// Model WebSocket: rustls on every platform.
	{RoleWS, "", [3]int{0, 116, 0}, BaselineCodex0140WSRustlsRing},
	{RoleWS, "", [3]int{0, 141, 0}, BaselineCodex0155WSRustlsAwslcPQ},
	{RoleWS, "", [3]int{0, 156, 0}, BaselineCodex0163LinuxWSRustls},
	// HTTP/SSE + control plane: native-tls, i.e. the OS TLS stack.
	{RoleHTTP, CodexPlatformLinux, [3]int{0, 0, 0}, BaselineCodex0142LinuxHTTPOpenSSL35},
	{RoleHTTP, CodexPlatformLinux, [3]int{0, 142, 2}, BaselineCodex0163LinuxHTTPOpenSSL},
	{RoleHTTP, CodexPlatformMacOS, [3]int{0, 0, 0}, BaselineCodex0163MacOSHTTPSecureTransport},
	{RoleHTTP, CodexPlatformWindows, [3]int{0, 0, 0}, BaselineCodex0163WindowsHTTPSChannel},
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// CodexTransportPolicyFor selects the Codex baseline for role from the
// User-Agent the gateway actually sends upstream on that request.
//
//   - Parsed: the newest rule for (role, platform) whose since <= version.
//     A version older than every rule of the role (a WebSocket dial for a
//     pre-0.116 client, which never used WebSockets by default) gets the
//     oldest rule.
//   - Unparsable (non-Codex or bare "codex_cli_rs/x.y.z"): the newest Linux
//     baseline for the role.
//
// The result always names a pinned baseline for a known role; an unknown
// role yields the zero policy (callers only pass RoleHTTP or RoleWS).
func CodexTransportPolicyFor(role Role, userAgent string) TransportPolicy {
	client, ok := ParseCodexUserAgent(userAgent)
	if !ok {
		client = CodexClient{Version: [3]int{1 << 30, 0, 0}, Platform: CodexPlatformLinux}
	}
	var best, oldest *codexBaselineRule
	for i := range codexBaselineRules {
		r := &codexBaselineRules[i]
		if r.role != role || (r.platform != "" && r.platform != client.Platform) {
			continue
		}
		if oldest == nil || versionLess(r.since, oldest.since) {
			oldest = r
		}
		if !versionLess(client.Version, r.since) && (best == nil || versionLess(best.since, r.since)) {
			best = r
		}
	}
	if best == nil {
		best = oldest
	}
	if best == nil {
		return TransportPolicy{}
	}
	return TransportPolicy{Family: FamilyCodex, Baseline: best.baseline, Role: role}
}
