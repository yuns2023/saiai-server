package openai

import (
	"strconv"
	"strings"
)

// CodexSemver is a parsed Semantic Versioning 2.0.0 version. Build metadata
// is accepted but ignored for precedence.
type CodexSemver struct {
	Major, Minor, Patch uint64
	Prerelease          []string
}

// ParseCodexSemver parses MAJOR.MINOR.PATCH[-PRERELEASE][+BUILD] strictly:
// no "v" prefix, no leading zeros in numeric identifiers, no empty
// identifiers.
func ParseCodexSemver(value string) (CodexSemver, bool) {
	var v CodexSemver
	if value == "" {
		return v, false
	}
	if core, build, found := strings.Cut(value, "+"); found {
		if !validSemverIdentifiers(build, false) {
			return v, false
		}
		value = core
	}
	core, pre, hasPre := strings.Cut(value, "-")
	if hasPre {
		if !validSemverIdentifiers(pre, true) {
			return v, false
		}
		v.Prerelease = strings.Split(pre, ".")
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	numbers := [3]uint64{}
	for i, part := range parts {
		n, ok := parseSemverNumber(part)
		if !ok {
			return v, false
		}
		numbers[i] = n
	}
	v.Major, v.Minor, v.Patch = numbers[0], numbers[1], numbers[2]
	return v, true
}

// String renders the version without build metadata.
func (v CodexSemver) String() string {
	s := strconv.FormatUint(v.Major, 10) + "." + strconv.FormatUint(v.Minor, 10) + "." + strconv.FormatUint(v.Patch, 10)
	if len(v.Prerelease) > 0 {
		s += "-" + strings.Join(v.Prerelease, ".")
	}
	return s
}

// Compare returns -1, 0 or 1 by SemVer precedence: a prerelease sorts before
// its release, numeric identifiers compare numerically and before
// alphanumeric ones, and a longer identifier list wins a common prefix.
func (v CodexSemver) Compare(other CodexSemver) int {
	for _, pair := range [][2]uint64{{v.Major, other.Major}, {v.Minor, other.Minor}, {v.Patch, other.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(v.Prerelease) == 0 && len(other.Prerelease) == 0:
		return 0
	case len(v.Prerelease) == 0:
		return 1
	case len(other.Prerelease) == 0:
		return -1
	}
	for i := 0; i < len(v.Prerelease) && i < len(other.Prerelease); i++ {
		if c := compareSemverIdentifier(v.Prerelease[i], other.Prerelease[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(v.Prerelease) < len(other.Prerelease):
		return -1
	case len(v.Prerelease) > len(other.Prerelease):
		return 1
	}
	return 0
}

func compareSemverIdentifier(a, b string) int {
	aNumeric, bNumeric := isSemverDigits(a), isSemverDigits(b)
	switch {
	case aNumeric && bNumeric:
		// Arbitrary size without overflow: no leading zeros, so the longer
		// number is larger and equal lengths compare lexically.
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case aNumeric:
		return -1
	case bNumeric:
		return 1
	}
	return strings.Compare(a, b)
}

func parseSemverNumber(value string) (uint64, bool) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	return n, err == nil
}

func isSemverDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func validSemverIdentifiers(value string, prerelease bool) bool {
	if value == "" {
		return false
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		numeric := true
		for i := 0; i < len(identifier); i++ {
			c := identifier[i]
			switch {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-':
				numeric = false
			default:
				return false
			}
		}
		if prerelease && numeric && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
	}
	return true
}

// CodexEngineVersion identifies an official Codex client request (the same
// family identification as IsCodexOfficialClientByHeaders) and returns its
// User-Agent product and engine version. The engine version is known only
// when the `version` header and the User-Agent main version both parse as
// SemVer and are identical; otherwise known is false. isCodex is false for
// requests outside the Codex family.
func CodexEngineVersion(userAgent, originator, versionHeader string) (product string, version CodexSemver, isCodex, known bool) {
	if !IsCodexOfficialClientByHeaders(userAgent, originator) {
		return "", version, false, false
	}
	product, rest, _ := strings.Cut(strings.TrimSpace(userAgent), "/")
	product = strings.ToLower(strings.TrimSpace(product))
	uaVersion := rest
	if fields := strings.Fields(rest); len(fields) > 0 {
		uaVersion = fields[0]
	}
	headerVersion := strings.TrimSpace(versionHeader)
	if headerVersion == "" || headerVersion != uaVersion {
		return product, version, true, false
	}
	parsed, ok := ParseCodexSemver(headerVersion)
	if !ok {
		return product, version, true, false
	}
	return product, parsed, true, true
}
