package service

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
)

// Claude device platform observation: normalization of one inbound request
// into (family, arch, major). Everything here is bounded and never fails the
// request; an unrecognized value becomes "unknown" (major: "").

const (
	ClaudeDevicePlatformFamilyWindows = "windows"
	ClaudeDevicePlatformFamilyMacOS   = "macos"
	ClaudeDevicePlatformFamilyLinux   = "linux"
	ClaudeDevicePlatformArchX64       = "x64"
	ClaudeDevicePlatformArchARM64     = "arm64"
	ClaudeDevicePlatformUnknown       = "unknown"

	// claudeDevicePlatformOther marks a value that is present but not one of
	// the known families. It never leaves this file: it only makes a
	// cross-check fail (family unknown).
	claudeDevicePlatformOther = "other"

	claudeDevicePlatformMaxSystemBlocks = 16
	claudeDevicePlatformMaxScanBytes    = 512 << 10
	claudeDevicePlatformMaxEnvLines     = 64
	claudeDevicePlatformMaxLineBytes    = 512
	claudeDevicePlatformMaxVersionDigit = 6
	claudeDevicePlatformMaxUserIDBytes  = 4096
	claudeDevicePlatformMaxHeaderBytes  = 32
	claudeDevicePlatformDisplayPrefix   = 8
)

// claudeDevicePlatformDarwinMajors maps the Darwin kernel major to the macOS
// marketing major. Anything not listed is unknown.
var claudeDevicePlatformDarwinMajors = map[int]string{
	19: "10.15",
	20: "11",
	21: "12",
	22: "13",
	23: "14",
	24: "15",
	25: "26",
}

// ClaudeDevicePlatformSample is the normalized platform of one request.
type ClaudeDevicePlatformSample struct {
	Family string // windows / macos / linux / unknown
	Arch   string // x64 / arm64 / unknown
	Major  string // "" when unknown (always "" for linux)
}

// claudeDeviceIdentity returns the sha256 digest (hex) of a valid Claude
// Code device id and its first 8 hex digits for display. A device id must
// be exactly 64 ASCII hex characters; anything else is not observed.
func claudeDeviceIdentity(deviceID string) (digest, prefix string, ok bool) {
	if len(deviceID) != 64 {
		return "", "", false
	}
	for i := 0; i < len(deviceID); i++ {
		ch := deviceID[i]
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return "", "", false
		}
	}
	lower := strings.ToLower(deviceID)
	sum := sha256.Sum256([]byte(lower))
	// Clone: the device id may share memory with the whole request body.
	return hex.EncodeToString(sum[:]), strings.Clone(lower[:claudeDevicePlatformDisplayPrefix]), true
}

// normalizeClaudeDevicePlatform combines the X-Stainless-* headers (present
// on every Claude Code request) with the system prompt "# Environment"
// section (main conversation requests only). overflow reports that a scan
// cap was hit (the sample is still usable).
func normalizeClaudeDevicePlatform(headers http.Header, system any) (ClaudeDevicePlatformSample, bool) {
	sample := ClaudeDevicePlatformSample{Family: ClaudeDevicePlatformUnknown, Arch: ClaudeDevicePlatformUnknown}
	var headerOS, headerArch string
	if headers != nil {
		headerOS = headers.Get("X-Stainless-Os")
		headerArch = headers.Get("X-Stainless-Arch")
	}
	// Real values are a few bytes; longer ones are "present but unknown"
	// and are never trimmed or lower-cased.
	if len(headerOS) > claudeDevicePlatformMaxHeaderBytes {
		headerOS = claudeDevicePlatformOther
	}
	if len(headerArch) > claudeDevicePlatformMaxHeaderBytes {
		headerArch = ""
	}
	switch strings.ToLower(strings.TrimSpace(headerArch)) {
	case "x64":
		sample.Arch = ClaudeDevicePlatformArchX64
	case "arm64":
		sample.Arch = ClaudeDevicePlatformArchARM64
	}

	platform, osVersion, overflow := extractClaudeEnvironmentPlatform(system)
	headerFamily := claudeDeviceFamilyFromHeader(headerOS)
	platformFamily := claudeDeviceFamilyFromPlatform(platform)
	osFamily := claudeDeviceFamilyFromOSVersion(osVersion)

	// The body family: Platform: and OS Version: must agree with each other.
	bodyFamily := platformFamily
	bodyConsistent := true
	if isKnownClaudeDeviceFamily(platformFamily) && isKnownClaudeDeviceFamily(osFamily) && platformFamily != osFamily {
		bodyConsistent = false
		bodyFamily = claudeDevicePlatformOther
	} else if bodyFamily == "" {
		bodyFamily = osFamily
	}

	switch {
	case isKnownClaudeDeviceFamily(headerFamily):
		// A header/body conflict drops the family for this sample.
		if bodyFamily == "" || bodyFamily == headerFamily {
			sample.Family = headerFamily
		}
	case headerFamily == "":
		if isKnownClaudeDeviceFamily(bodyFamily) {
			sample.Family = bodyFamily
		}
	}

	if bodyConsistent && isKnownClaudeDeviceFamily(sample.Family) && osFamily == sample.Family {
		sample.Major = claudeDeviceMajorFromOSVersion(sample.Family, osVersion)
	}
	return sample, overflow
}

func isKnownClaudeDeviceFamily(family string) bool {
	switch family {
	case ClaudeDevicePlatformFamilyWindows, ClaudeDevicePlatformFamilyMacOS, ClaudeDevicePlatformFamilyLinux:
		return true
	}
	return false
}

// claudeDeviceFamilyFromHeader maps X-Stainless-Os; "" = header missing.
func claudeDeviceFamilyFromHeader(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	switch strings.ToLower(value) {
	case "windows":
		return ClaudeDevicePlatformFamilyWindows
	case "macos":
		return ClaudeDevicePlatformFamilyMacOS
	case "linux":
		return ClaudeDevicePlatformFamilyLinux
	}
	return claudeDevicePlatformOther
}

// claudeDeviceFamilyFromPlatform maps the Node process.platform value.
func claudeDeviceFamilyFromPlatform(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	switch strings.ToLower(value) {
	case "win32":
		return ClaudeDevicePlatformFamilyWindows
	case "darwin":
		return ClaudeDevicePlatformFamilyMacOS
	case "linux":
		return ClaudeDevicePlatformFamilyLinux
	}
	return claudeDevicePlatformOther
}

func claudeDeviceFamilyFromOSVersion(value string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(value), " ")
	switch first {
	case "Darwin":
		return ClaudeDevicePlatformFamilyMacOS
	case "Windows":
		return ClaudeDevicePlatformFamilyWindows
	case "Linux":
		return ClaudeDevicePlatformFamilyLinux
	}
	return ""
}

func claudeDeviceMajorFromOSVersion(family, value string) string {
	fields := strings.Fields(value)
	switch family {
	case ClaudeDevicePlatformFamilyMacOS:
		// "Darwin 24.3.0": the kernel major selects the macOS major.
		if len(fields) < 2 || fields[0] != "Darwin" {
			return ""
		}
		digits, _, _ := strings.Cut(fields[1], ".")
		n, ok := parseClaudeDeviceVersionNumber(digits, 3)
		if !ok {
			return ""
		}
		return claudeDevicePlatformDarwinMajors[n]
	case ClaudeDevicePlatformFamilyWindows:
		// "Windows 11 Pro 10.0.26200": only the NT build counts; the
		// marketing text is not trusted. Server editions are unknown.
		if strings.Contains(strings.ToLower(value), "server") {
			return ""
		}
		for _, field := range fields {
			parts := strings.Split(field, ".")
			if len(parts) != 3 || parts[0] != "10" || parts[1] != "0" {
				continue
			}
			build, ok := parseClaudeDeviceVersionNumber(parts[2], claudeDevicePlatformMaxVersionDigit)
			if !ok {
				return ""
			}
			switch {
			case build >= 22000:
				return "11"
			case build >= 10240:
				return "10"
			}
			return ""
		}
	}
	return ""
}

func parseClaudeDeviceVersionNumber(digits string, maxDigits int) (int, bool) {
	if digits == "" || len(digits) > maxDigits {
		return 0, false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(digits)
	return n, err == nil
}

// extractClaudeEnvironmentPlatform searches each system text block on its
// own (never a concatenation) for a "# Environment" section and returns its
// Platform: and OS Version: values. The first block with a section that has
// either value wins. overflow reports a hit scan cap.
func extractClaudeEnvironmentPlatform(system any) (platform, osVersion string, overflow bool) {
	var texts []string
	switch v := system.(type) {
	case string:
		texts = []string{v}
	case []any:
		for i, block := range v {
			if i >= claudeDevicePlatformMaxSystemBlocks {
				overflow = true
				break
			}
			m, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if kind, _ := m["type"].(string); kind != "text" {
				continue
			}
			if text, ok := m["text"].(string); ok && text != "" {
				texts = append(texts, text)
			}
		}
	default:
		return "", "", false
	}
	budget := claudeDevicePlatformMaxScanBytes
	for _, text := range texts {
		if budget <= 0 {
			overflow = true
			break
		}
		if len(text) > budget {
			// Never read a line cut by the budget as if it were complete.
			text = text[:budget]
			if cut := strings.LastIndexByte(text, '\n'); cut >= 0 {
				text = text[:cut]
			} else {
				text = ""
			}
			overflow = true
		}
		budget -= len(text)
		p, o, found, blockOverflow := scanClaudeEnvironmentSection(text)
		overflow = overflow || blockOverflow
		if found && (p != "" || o != "") {
			return p, o, overflow
		}
	}
	return "", "", overflow
}

// scanClaudeEnvironmentSection walks the lines once (linear in the text):
// the first line that is exactly "# Environment" (surrounding blanks
// allowed) opens the section, which ends at the next heading or after
// claudeDevicePlatformMaxEnvLines lines. Over-long lines are never parsed
// as values; an over-long line starting with "#" still ends the section.
func scanClaudeEnvironmentSection(text string) (platform, osVersion string, found, overflow bool) {
	const heading = "# Environment"
	inSection := false
	lines := 0
	for len(text) > 0 {
		var line string
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			line, text = text[:i], text[i+1:]
		} else {
			line, text = text, ""
		}
		if !inSection {
			if len(line) <= claudeDevicePlatformMaxLineBytes && strings.TrimSpace(line) == heading {
				inSection, found = true, true
			}
			continue
		}
		lines++
		if lines > claudeDevicePlatformMaxEnvLines {
			overflow = true
			break
		}
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			break
		}
		if len(line) > claudeDevicePlatformMaxLineBytes {
			overflow = true
			continue
		}
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		if value, ok := strings.CutPrefix(trimmed, "Platform:"); ok && platform == "" {
			platform = strings.TrimSpace(value)
		} else if value, ok := strings.CutPrefix(trimmed, "OS Version:"); ok && osVersion == "" {
			osVersion = strings.TrimSpace(value)
		}
	}
	return platform, osVersion, found, overflow
}
