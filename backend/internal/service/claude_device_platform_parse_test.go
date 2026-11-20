package service

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func cdpHeaders(os, arch string) http.Header {
	h := http.Header{}
	if os != "" {
		h.Set("X-Stainless-Os", os)
	}
	if arch != "" {
		h.Set("X-Stainless-Arch", arch)
	}
	return h
}

func cdpEnvBlock(platform, osVersion string) string {
	return "Here is useful information about the environment you are running in:\n# Environment\nYou have been invoked in the following environment: \n - Primary working directory: /tmp/x\n - Is a git repository: true\n - Platform: " + platform + "\n - Shell: zsh\n - OS Version: " + osVersion + "\n# Next section\n - Platform: ignored\n"
}

func TestClaudeDevicePlatformParse_RealLines(t *testing.T) {
	cases := []struct {
		name, headerOS, arch, platform, osVersion string
		want                                      ClaudeDevicePlatformSample
	}{
		{"mac", "MacOS", "arm64", "darwin", "Darwin 24.3.0", ClaudeDevicePlatformSample{"macos", "arm64", "15"}},
		{"windows", "Windows", "x64", "win32", "Windows 11 Pro 10.0.26200", ClaudeDevicePlatformSample{"windows", "x64", "11"}},
		{"linux", "Linux", "x64", "linux", "Linux 7.0.0-43-generic", ClaudeDevicePlatformSample{"linux", "x64", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, overflow := normalizeClaudeDevicePlatform(cdpHeaders(tc.headerOS, tc.arch), cdpEnvBlock(tc.platform, tc.osVersion))
			require.False(t, overflow)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestClaudeDevicePlatformParse_FormattingVariants(t *testing.T) {
	crlf := "# Environment\r\n  - Platform: darwin\r\n\t- OS Version: Darwin 25.0.0\r\n"
	p, o, overflow := extractClaudeEnvironmentPlatform(crlf)
	require.False(t, overflow)
	require.Equal(t, "darwin", p)
	require.Equal(t, "Darwin 25.0.0", o)

	noDash := "  # Environment  \nPlatform: linux\nOS Version: Linux 6.1\n"
	p, o, _ = extractClaudeEnvironmentPlatform(noDash)
	require.Equal(t, "linux", p)
	require.Equal(t, "Linux 6.1", o)

	// Lines after the next heading are not part of the section.
	stop := "# Environment\n - Shell: bash\n## Details\n - Platform: win32\n"
	p, o, _ = extractClaudeEnvironmentPlatform(stop)
	require.Empty(t, p)
	require.Empty(t, o)

	// "## Environment" and inline mentions are not the heading.
	inline := "see # Environment below\n## Environment\n - Platform: win32\n"
	p, _, _ = extractClaudeEnvironmentPlatform(inline)
	require.Empty(t, p)
}

func TestClaudeDevicePlatformParse_ArraySystemSecondBlock(t *testing.T) {
	system := []any{
		map[string]any{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.80.000;"},
		map[string]any{"type": "text", "text": "You are Claude Code.\n" + cdpEnvBlock("win32", "Windows 10 Pro 10.0.19045")},
	}
	got, overflow := normalizeClaudeDevicePlatform(cdpHeaders("Windows", "x64"), system)
	require.False(t, overflow)
	require.Equal(t, ClaudeDevicePlatformSample{"windows", "x64", "10"}, got)

	// Blocks are searched independently: a heading in one block and the
	// values in the next are not combined.
	split := []any{
		map[string]any{"type": "text", "text": "intro\n# Environment"},
		map[string]any{"type": "text", "text": " - Platform: darwin\n - OS Version: Darwin 24.3.0\n"},
	}
	p, o, _ := extractClaudeEnvironmentPlatform(split)
	require.Empty(t, p)
	require.Empty(t, o)
}

func TestClaudeDevicePlatformParse_MissingEnvUsesHeaders(t *testing.T) {
	got, _ := normalizeClaudeDevicePlatform(cdpHeaders("MacOS", "x64"), []any{map[string]any{"type": "text", "text": "subagent prompt"}})
	require.Equal(t, ClaudeDevicePlatformSample{"macos", "x64", ""}, got)

	got, _ = normalizeClaudeDevicePlatform(cdpHeaders("", ""), nil)
	require.Equal(t, ClaudeDevicePlatformSample{"unknown", "unknown", ""}, got)

	// Header missing: family (and major) from the body.
	got, _ = normalizeClaudeDevicePlatform(cdpHeaders("", ""), cdpEnvBlock("darwin", "Darwin 24.3.0"))
	require.Equal(t, ClaudeDevicePlatformSample{"macos", "unknown", "15"}, got)

	// Unknown arch values.
	got, _ = normalizeClaudeDevicePlatform(cdpHeaders("Linux", "ia32"), nil)
	require.Equal(t, ClaudeDevicePlatformSample{"linux", "unknown", ""}, got)
}

func TestClaudeDevicePlatformParse_Conflicts(t *testing.T) {
	// Header says macOS, body says Windows: family unknown, no major, arch kept.
	got, _ := normalizeClaudeDevicePlatform(cdpHeaders("MacOS", "arm64"), cdpEnvBlock("win32", "Windows 11 Pro 10.0.26200"))
	require.Equal(t, ClaudeDevicePlatformSample{"unknown", "arm64", ""}, got)

	// Platform and OS Version disagree: body unusable, conflicts with header.
	got, _ = normalizeClaudeDevicePlatform(cdpHeaders("MacOS", "arm64"), cdpEnvBlock("darwin", "Windows 11 Pro 10.0.26200"))
	require.Equal(t, ClaudeDevicePlatformSample{"unknown", "arm64", ""}, got)

	// Unrecognized header value.
	got, _ = normalizeClaudeDevicePlatform(cdpHeaders("FreeBSD", "x64"), cdpEnvBlock("linux", "Linux 6.1"))
	require.Equal(t, ClaudeDevicePlatformSample{"unknown", "x64", ""}, got)

	// Unrecognized body platform conflicts with a known header.
	got, _ = normalizeClaudeDevicePlatform(cdpHeaders("Linux", "x64"), cdpEnvBlock("freebsd", "FreeBSD 14"))
	require.Equal(t, ClaudeDevicePlatformSample{"unknown", "x64", ""}, got)
}

func TestClaudeDevicePlatformParse_WindowsBuilds(t *testing.T) {
	cases := map[string]string{
		"Windows 10 Pro 10.0.19045":                  "10",
		"Windows 11 Home 10.0.22000":                 "11",
		"Windows 10 Pro 10.0.21999":                  "10",
		"Windows 10 10.0.10240":                      "10",
		"Windows 10 10.0.10239":                      "",
		"Windows Server 2022 Datacenter 10.0.20348":  "",
		"Windows 11 Pro":                             "",
		"Windows 8.1 6.3.9600":                       "",
		"Windows 11 10.0.abc":                        "",
		"Windows 11 10.0.1234567":                    "",
		"Windows 11 10.0." + strings.Repeat("9", 40): "",
		"Windows garbage":                            "",
	}
	for osVersion, want := range cases {
		got, _ := normalizeClaudeDevicePlatform(cdpHeaders("Windows", "x64"), cdpEnvBlock("win32", osVersion))
		require.Equal(t, want, got.Major, osVersion)
		require.Equal(t, "windows", got.Family, osVersion)
	}
}

func TestClaudeDevicePlatformParse_DarwinMajors(t *testing.T) {
	cases := map[string]string{
		"Darwin 19.6.0": "10.15",
		"Darwin 20.1.0": "11",
		"Darwin 24.3.0": "15",
		"Darwin 25.0.0": "26",
		"Darwin 30.0.0": "",
		"Darwin 18.7.0": "",
		"Darwin 1234.0": "",
		"Darwin":        "",
		"Darwin x.y":    "",
	}
	for osVersion, want := range cases {
		got, _ := normalizeClaudeDevicePlatform(cdpHeaders("MacOS", "arm64"), cdpEnvBlock("darwin", osVersion))
		require.Equal(t, want, got.Major, osVersion)
	}
}

func TestClaudeDevicePlatformParse_Caps(t *testing.T) {
	long := "# Environment\n - Platform: " + strings.Repeat("d", claudeDevicePlatformMaxLineBytes) + "\n - OS Version: Darwin 24.3.0\n"
	p, o, overflow := extractClaudeEnvironmentPlatform(long)
	require.True(t, overflow)
	require.Empty(t, p)
	require.Equal(t, "Darwin 24.3.0", o)

	// Values beyond the line limit of the section are not read.
	many := "# Environment\n" + strings.Repeat(" - Shell: zsh\n", claudeDevicePlatformMaxEnvLines+5) + " - Platform: darwin\n"
	p, _, overflow = extractClaudeEnvironmentPlatform(many)
	require.True(t, overflow)
	require.Empty(t, p)

	// Blocks beyond the block cap are not scanned.
	blocks := make([]any, 0, claudeDevicePlatformMaxSystemBlocks+1)
	for i := 0; i < claudeDevicePlatformMaxSystemBlocks; i++ {
		blocks = append(blocks, map[string]any{"type": "text", "text": "filler"})
	}
	blocks = append(blocks, map[string]any{"type": "text", "text": cdpEnvBlock("darwin", "Darwin 24.3.0")})
	p, _, overflow = extractClaudeEnvironmentPlatform(blocks)
	require.True(t, overflow)
	require.Empty(t, p)

	// Bytes beyond the scan budget are not scanned.
	huge := strings.Repeat("x", claudeDevicePlatformMaxScanBytes) + "\n" + cdpEnvBlock("darwin", "Darwin 24.3.0")
	p, _, overflow = extractClaudeEnvironmentPlatform(huge)
	require.True(t, overflow)
	require.Empty(t, p)

	// A line cut by the scan budget is not read as complete.
	cut := strings.Repeat("x", claudeDevicePlatformMaxScanBytes-40) + "\n# Environment\n - Platform: darwin-and-more"
	p, _, overflow = extractClaudeEnvironmentPlatform(cut)
	require.True(t, len(cut) > claudeDevicePlatformMaxScanBytes)
	require.True(t, overflow)
	require.Empty(t, p)

	// The budget is shared by all blocks, also when a block has no newline.
	twoBlocks := []any{
		map[string]any{"type": "text", "text": strings.Repeat("x", claudeDevicePlatformMaxScanBytes+10)},
		map[string]any{"type": "text", "text": cdpEnvBlock("darwin", "Darwin 24.3.0")},
	}
	p, _, overflow = extractClaudeEnvironmentPlatform(twoBlocks)
	require.True(t, overflow)
	require.Empty(t, p)

	// Many heading candidates on one huge line stay linear (finishes fast).
	start := time.Now()
	p, _, _ = extractClaudeEnvironmentPlatform(strings.Repeat("# Environment ", claudeDevicePlatformMaxScanBytes/14))
	require.Empty(t, p)
	require.Less(t, time.Since(start), 2*time.Second)

	// An over-long heading line still ends the section.
	longHeading := "# Environment\n - Shell: zsh\n# " + strings.Repeat("h", claudeDevicePlatformMaxLineBytes) + "\n - Platform: win32\n"
	p, _, _ = extractClaudeEnvironmentPlatform(longHeading)
	require.Empty(t, p)

	// Non-text blocks in an array are ignored.
	p, _, _ = extractClaudeEnvironmentPlatform([]any{map[string]any{"type": "image", "text": cdpEnvBlock("win32", "Windows 11 Pro 10.0.26200")}})
	require.Empty(t, p)

	// Over-long headers are unknown, never parsed.
	got, _ := normalizeClaudeDevicePlatform(cdpHeaders("Linux"+strings.Repeat(" ", 64), "x64"+strings.Repeat(" ", 64)), nil)
	require.Equal(t, ClaudeDevicePlatformSample{"unknown", "unknown", ""}, got)

	// Non-text values are ignored.
	p, _, overflow = extractClaudeEnvironmentPlatform(map[string]any{"text": "x"})
	require.False(t, overflow)
	require.Empty(t, p)
}

func TestClaudeDeviceIdentity(t *testing.T) {
	id := strings.Repeat("A", 8) + strings.Repeat("b", 56)
	digest, prefix, ok := claudeDeviceIdentity(id)
	require.True(t, ok)
	require.Equal(t, "aaaaaaaa", prefix)
	require.Len(t, digest, 64)
	lowerDigest, _, _ := claudeDeviceIdentity(strings.ToLower(id))
	require.Equal(t, digest, lowerDigest, "case-insensitive device id")
	require.NotContains(t, digest, strings.ToLower(id))

	for _, bad := range []string{"", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("g", 64), "user_" + strings.Repeat("a", 59)} {
		_, _, ok := claudeDeviceIdentity(bad)
		require.False(t, ok, bad)
	}
}
