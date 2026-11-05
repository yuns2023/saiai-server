package openai

import (
	"regexp"
	"strings"
)

// CodexCLIUserAgentPrefixes matches Codex CLI User-Agent patterns
// Examples: "codex_vscode/1.0.0", "codex_cli_rs/0.1.2"
var CodexCLIUserAgentPrefixes = []string{
	"codex_vscode/",
	"codex_cli_rs/",
	"codex-tui/",
}

// CodexTerminalUserAgentPrefixes is the strict terminal-only subset used by
// group ingress policy. Keep this separate from CodexCLIUserAgentPrefixes,
// whose historical compatibility semantics also include the VSCode client.
var CodexTerminalUserAgentPrefixes = []string{
	"codex_cli_rs/",
	"codex-tui/",
	"codex_exec/",
}

// codexClientProductPattern matches the Codex client family product names
// used as the User-Agent product token and as the originator header, such as
// "codex_cli_rs", "codex-tui", "codex-browser-use" and "codex desktop". New
// official surfaces keep this naming scheme, so admission matches the family
// rather than an enumerated list that rejects each new surface until updated.
// These caller-controlled fields are compatibility signals; an allowlist of
// product names would not make them harder to forge.
var codexClientProductPattern = regexp.MustCompile(`^codex(?:[ _-][a-z0-9]+)+$`)

// IsCodexCLIRequest retains the historical substring check for compatibility
// header rewriting. Admission must use IsCodexOfficialClientByHeaders instead.
func IsCodexCLIRequest(userAgent string) bool {
	ua := normalizeCodexClientHeader(userAgent)
	if ua == "" {
		return false
	}
	return matchCodexClientHeaderPrefixes(ua, CodexCLIUserAgentPrefixes)
}

// IsCodexTerminalRequest checks for the native Codex terminal/exec clients and
// intentionally excludes VSCode, desktop, SDK, and other official clients.
func IsCodexTerminalRequest(userAgent string) bool {
	ua := normalizeCodexClientHeader(userAgent)
	if ua == "" {
		return false
	}
	return matchCodexUserAgentPrefixes(ua, CodexTerminalUserAgentPrefixes)
}

// IsCodexOfficialClientRequest checks if the User-Agent indicates a Codex 官方客户端请求。
// 与 IsCodexCLIRequest 解耦，避免影响历史兼容逻辑。
func IsCodexOfficialClientRequest(userAgent string) bool {
	ua := normalizeCodexClientHeader(userAgent)
	if ua == "" {
		return false
	}
	product, _, found := strings.Cut(ua, "/")
	if !found || !codexClientProductPattern.MatchString(product) {
		return false
	}
	return matchCodexUserAgentPrefixes(ua, []string{product + "/"})
}

// IsCodexOfficialClientOriginator checks if originator indicates a Codex 官方客户端请求。
func IsCodexOfficialClientOriginator(originator string) bool {
	return codexClientProductPattern.MatchString(normalizeCodexClientHeader(originator))
}

// IsCodexOfficialClientByHeaders checks whether the request headers indicate an
// official Codex client request. A Codex family UA is accepted when a present
// originator also names a Codex client product; otherwise only the app-server
// host shape of IsCodexEmbeddedClientByHeaders is accepted. Missing UAs fail
// closed, even when originator claims Codex.
// These caller-controlled fields do not provide cryptographic attestation.
func IsCodexOfficialClientByHeaders(userAgent, originator string) bool {
	if IsCodexOfficialClientRequest(userAgent) &&
		(normalizeCodexClientHeader(originator) == "" || IsCodexOfficialClientOriginator(originator)) {
		return true
	}
	return IsCodexEmbeddedClientByHeaders(userAgent, originator)
}

// codexEmbeddedUserAgentPattern is the full codex-rs User-Agent:
// "<originator>/<version> (<os> <os version>; <arch>) <terminal> (<client name>; <client version>)".
var codexEmbeddedUserAgentPattern = regexp.MustCompile(`^([^/()]+)/(\d+\.\d+\.\d+(?:[-+][0-9a-z.+-]+)?) \([^;()]+; [^;()]+\) \S+ \([^;()]+; [^;()]+\)$`)

// IsCodexEmbeddedClientByHeaders recognises the official Codex binary run as
// `codex app-server` by a third-party host such as the Claude Code Codex
// plugin. The app-server uses the host's clientInfo.name as originator and as
// the User-Agent product, so the product is not a Codex name; the rest of the
// User-Agent keeps the codex-rs shape including the client suffix that only
// app-server adds. SAIAI's own hosts (saiai_daemon_check) stay unrecognised so
// that their name is never forwarded to an upstream account. Like the family
// check this is a compatibility signal, not attestation.
func IsCodexEmbeddedClientByHeaders(userAgent, originator string) bool {
	originator = normalizeCodexClientHeader(originator)
	if originator == "" || strings.HasPrefix(originator, "saiai") {
		return false
	}
	m := codexEmbeddedUserAgentPattern.FindStringSubmatch(normalizeCodexClientHeader(userAgent))
	return m != nil && strings.TrimSpace(m[1]) == originator
}

func matchCodexUserAgentPrefixes(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if !strings.HasPrefix(value, prefix) {
			continue
		}
		// Official UAs append platform/app details after the product/version.
		// Reject missing versions, nested products and arbitrary suffix text in
		// the version token; release/prerelease/build versions remain accepted.
		versionAndDetails := strings.TrimPrefix(value, prefix)
		if versionAndDetails == "" || versionAndDetails[0] < '0' || versionAndDetails[0] > '9' {
			return false
		}
		parts := strings.Fields(versionAndDetails)
		for _, r := range parts[0] {
			if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || r == '.' || r == '-' || r == '+' {
				continue
			}
			return false
		}
		return true
	}
	return false
}

func normalizeCodexClientHeader(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func matchCodexClientHeaderPrefixes(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		normalizedPrefix := normalizeCodexClientHeader(prefix)
		if normalizedPrefix == "" {
			continue
		}
		// 优先前缀匹配；若 UA/Originator 被网关拼接为复合字符串时，退化为包含匹配。
		if strings.HasPrefix(value, normalizedPrefix) || strings.Contains(value, normalizedPrefix) {
			return true
		}
	}
	return false
}
