package tlsfingerprint

import (
	"regexp"
	"strconv"

	utls "github.com/refraction-networking/utls"
)

var claudeUserAgentVersion = regexp.MustCompile(`^claude-cli/([0-9]+)\.([0-9]+)\.([0-9]+)(?:$|[ (])`)

// ProfileForUserAgent selects the built-in model handshake without mutating the
// supplied profile. Only built-in profiles are used by the Gateway.
// Unknown/malformed user agents retain the legacy default. Later versions use
// the newest observed shape; the compatibility monitor must still detect drift.
func ProfileForUserAgent(profile *Profile, userAgent string) *Profile {
	if profile == nil {
		return profile
	}
	selected := *profile
	selected.claude2302 = false
	match := claudeUserAgentVersion.FindStringSubmatch(userAgent)
	if match == nil {
		return &selected
	}
	var version [3]int
	for i := range version {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return &selected
		}
		version[i] = n
	}
	selected.claude2302 = version[0] > 2 ||
		(version[0] == 2 && (version[1] > 1 || (version[1] == 1 && version[2] >= 302)))
	return &selected
}

// TransportVariant keeps connections negotiated with different ClientHellos
// in different pools, including when an account alternates client versions.
func (p *Profile) TransportVariant() string {
	if p != nil && p.claude2302 {
		return ":claude-2.1.302"
	}
	return ""
}

func signatureAlgorithmsForProfile(profile *Profile) []utls.SignatureScheme {
	if profile != nil && profile.claude2302 {
		return []utls.SignatureScheme{
			0x0403, 0x0804, 0x0401, 0x0503, 0x0805, 0x0501,
			0x0806, 0x0601, 0x0807, 0x0603, 0x0201,
		}
	}
	return defaultSignatureAlgorithms
}
