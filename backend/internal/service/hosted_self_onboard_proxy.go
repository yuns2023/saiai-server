package service

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

// hostedBlockedPrefixes are non-public ranges beyond what netip's
// IsPrivate/IsLoopback/... cover.
var hostedBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved + broadcast
	netip.MustParsePrefix("::/96"),           // IPv4-compatible (deprecated)
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64 (can reach IPv4 internals)
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("100::/64"),        // discard-only
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2001::/23"),       // IETF protocol assignments (incl. Teredo)
	netip.MustParsePrefix("2002::/16"),       // 6to4 (embeds arbitrary IPv4)
	netip.MustParsePrefix("3fff::/20"),       // documentation (RFC 9637)
	netip.MustParsePrefix("5f00::/16"),       // SRv6 SIDs
	netip.MustParsePrefix("fec0::/10"),       // deprecated site-local
}

// IsPublicHostedProxyIP reports whether a resolved proxy address is a public
// unicast address. IPv4-mapped IPv6 is unmapped first; zones are refused.
func IsPublicHostedProxyIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	return isPublicHostedProxyAddr(addr)
}

func isPublicHostedProxyAddr(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, p := range hostedBlockedPrefixes {
		if p.Contains(addr) {
			return false
		}
	}
	return addr.IsGlobalUnicast()
}

// parseHostedIPLiteral parses an IP literal host (IPv6 without brackets).
// Zoned literals are rejected by the caller's public check.
func parseHostedIPLiteral(host string) (net.IP, error) {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil, err
	}
	if addr.Zone() != "" {
		return nil, errors.New("zoned address")
	}
	return net.IP(addr.AsSlice()), nil
}

// NormalizeHostedProxyInput validates a user-submitted proxy: protocol
// http/https/socks5/socks5h, a host name or IP literal (no zone, no
// userinfo/path), port 1–65535, username and password both or neither, no
// control characters, each at most 255 bytes. Resolution and the public
// address check happen separately.
func NormalizeHostedProxyInput(in *HostedProxyInput) (*HostedProxyInput, error) {
	if in == nil {
		return nil, ErrSelfOnboardProxyInvalid
	}
	protocol := strings.ToLower(strings.TrimSpace(in.Protocol))
	switch protocol {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, ErrSelfOnboardProxyInvalid
	}
	host := strings.TrimSpace(in.Host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
		if addr, err := netip.ParseAddr(host); err != nil || !addr.Is6() {
			return nil, ErrSelfOnboardProxyInvalid
		}
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !validHostedProxyHost(host) {
		return nil, ErrSelfOnboardProxyInvalid
	}
	if in.Port < 1 || in.Port > 65535 {
		return nil, ErrSelfOnboardProxyInvalid
	}
	if (in.Username == "") != (in.Password == "") {
		return nil, ErrSelfOnboardProxyInvalid
	}
	if !validHostedProxyCredential(in.Username) || !validHostedProxyCredential(in.Password) {
		return nil, ErrSelfOnboardProxyInvalid
	}
	return &HostedProxyInput{Protocol: protocol, Host: host, Port: in.Port, Username: in.Username, Password: in.Password}, nil
}

func validHostedProxyHost(host string) bool {
	if host == "" || len(host) > hostedProxyHostMaxLen || strings.Contains(host, "%") {
		return false
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.Zone() == ""
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if r >= 'A' && r <= 'Z' || !isHostedIDChar(r) {
				return false
			}
		}
	}
	return true
}

func validHostedProxyCredential(v string) bool {
	if v == "" {
		return true
	}
	// proxies.username/password are VARCHAR(100): the approval creates a
	// proxy row from these values.
	if len(v) > hostedProxyCredentialMaxBytes || !utf8.ValidString(v) || utf8.RuneCountInString(v) > hostedProxyCredentialMaxRunes {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}
