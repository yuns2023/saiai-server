// Package tlsfingerprint provides TLS fingerprint simulation for HTTP clients.
// It uses the utls library to create TLS connections that mimic Node.js/Claude Code clients.
package tlsfingerprint

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/proxy"
)

// Profile contains TLS fingerprint configuration.
type Profile struct {
	Name         string // Profile name for identification
	CipherSuites []uint16
	Curves       []uint16
	PointFormats []uint8
	EnableGREASE bool
}

// Dialer creates TLS connections with custom fingerprints.
type Dialer struct {
	profile    *Profile
	baseDialer func(ctx context.Context, network, addr string) (net.Conn, error)
	rootCAs    *x509.CertPool
}

// HTTPProxyDialer creates TLS connections through HTTP/HTTPS proxies with custom fingerprints.
// It handles the CONNECT tunnel establishment before performing TLS handshake.
type HTTPProxyDialer struct {
	profile  *Profile
	proxyURL *url.URL
	rootCAs  *x509.CertPool
}

// SOCKS5ProxyDialer creates TLS connections through SOCKS5 proxies with custom fingerprints.
// It uses golang.org/x/net/proxy to establish the SOCKS5 tunnel.
type SOCKS5ProxyDialer struct {
	profile  *Profile
	proxyURL *url.URL
	rootCAs  *x509.CertPool
}

// Default TLS fingerprint values captured from Claude Code 2.1.81 (Bun 1.3.11 + BoringSSL)
// Captured using: raw TCP ClientHello parser against Bun fetch()
//
// Claude Code switched from Node.js to Bun around v2.x, which uses BoringSSL
// instead of OpenSSL, producing a significantly different TLS fingerprint.
var (
	// defaultCipherSuites contains the 17 cipher suites from Bun 1.3.11 (BoringSSL)
	// Order is critical for JA3 fingerprint matching
	defaultCipherSuites = []uint16{
		// TLS 1.3 cipher suites (BoringSSL order: AES_128 first)
		0x1301, // TLS_AES_128_GCM_SHA256
		0x1302, // TLS_AES_256_GCM_SHA384
		0x1303, // TLS_CHACHA20_POLY1305_SHA256

		// ECDHE + AES-GCM
		0xc02b, // TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256
		0xc02f, // TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
		0xc02c, // TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
		0xc030, // TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384

		// ChaCha20-Poly1305
		0xcca9, // TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256
		0xcca8, // TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256

		// ECDHE + AES-CBC-SHA (legacy)
		0xc009, // TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA
		0xc013, // TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA
		0xc00a, // TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA
		0xc014, // TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA

		// RSA + AES-GCM (non-PFS)
		0x009c, // TLS_RSA_WITH_AES_128_GCM_SHA256
		0x009d, // TLS_RSA_WITH_AES_256_GCM_SHA384

		// RSA + AES-CBC-SHA (non-PFS, legacy)
		0x002f, // TLS_RSA_WITH_AES_128_CBC_SHA
		0x0035, // TLS_RSA_WITH_AES_256_CBC_SHA
	}

	// defaultCurves contains the 3 supported groups from Bun/BoringSSL
	defaultCurves = []utls.CurveID{
		utls.X25519,    // 0x001d
		utls.CurveP256, // 0x0017 (secp256r1)
		utls.CurveP384, // 0x0018 (secp384r1)
	}

	// defaultPointFormats contains the single point format from Bun/BoringSSL
	defaultPointFormats = []uint8{
		0, // uncompressed
	}

	// defaultSignatureAlgorithms contains the 9 signature algorithms from Bun/BoringSSL
	defaultSignatureAlgorithms = []utls.SignatureScheme{
		0x0403, // ecdsa_secp256r1_sha256
		0x0804, // rsa_pss_rsae_sha256
		0x0401, // rsa_pkcs1_sha256
		0x0503, // ecdsa_secp384r1_sha384
		0x0805, // rsa_pss_rsae_sha384
		0x0501, // rsa_pkcs1_sha384
		0x0806, // rsa_pss_rsae_sha512
		0x0601, // rsa_pkcs1_sha512
		0x0201, // rsa_pkcs1_sha1
	}
)

// NewDialer creates a new TLS fingerprint dialer.
// baseDialer is used for TCP connection establishment (supports proxy scenarios).
// If baseDialer is nil, direct TCP dial is used.
func NewDialer(profile *Profile, baseDialer func(ctx context.Context, network, addr string) (net.Conn, error)) *Dialer {
	if baseDialer == nil {
		baseDialer = (&net.Dialer{}).DialContext
	}
	return &Dialer{profile: profile, baseDialer: baseDialer}
}

// WithRootCAs sets additional trusted root certificates for server verification.
func (d *Dialer) WithRootCAs(rootCAs *x509.CertPool) *Dialer {
	if d != nil {
		d.rootCAs = rootCAs
	}
	return d
}

// NewHTTPProxyDialer creates a new TLS fingerprint dialer that works through HTTP/HTTPS proxies.
// It establishes a CONNECT tunnel before performing TLS handshake with custom fingerprint.
func NewHTTPProxyDialer(profile *Profile, proxyURL *url.URL) *HTTPProxyDialer {
	return &HTTPProxyDialer{profile: profile, proxyURL: proxyURL}
}

// WithRootCAs sets additional trusted root certificates for server verification.
func (d *HTTPProxyDialer) WithRootCAs(rootCAs *x509.CertPool) *HTTPProxyDialer {
	if d != nil {
		d.rootCAs = rootCAs
	}
	return d
}

// NewSOCKS5ProxyDialer creates a new TLS fingerprint dialer that works through SOCKS5 proxies.
// It establishes a SOCKS5 tunnel before performing TLS handshake with custom fingerprint.
func NewSOCKS5ProxyDialer(profile *Profile, proxyURL *url.URL) *SOCKS5ProxyDialer {
	return &SOCKS5ProxyDialer{profile: profile, proxyURL: proxyURL}
}

// WithRootCAs sets additional trusted root certificates for server verification.
func (d *SOCKS5ProxyDialer) WithRootCAs(rootCAs *x509.CertPool) *SOCKS5ProxyDialer {
	if d != nil {
		d.rootCAs = rootCAs
	}
	return d
}

// DialTLSContext establishes a TLS connection through SOCKS5 proxy with the configured fingerprint.
// Flow: SOCKS5 CONNECT to target -> TLS handshake with utls on the tunnel
func (d *SOCKS5ProxyDialer) DialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	slog.Debug("tls_fingerprint_socks5_connecting", "proxy", d.proxyURL.Host, "target", addr)

	// Step 1: Create SOCKS5 dialer
	var auth *proxy.Auth
	if d.proxyURL.User != nil {
		username := d.proxyURL.User.Username()
		password, _ := d.proxyURL.User.Password()
		auth = &proxy.Auth{
			User:     username,
			Password: password,
		}
	}

	// Determine proxy address
	proxyAddr := d.proxyURL.Host
	if d.proxyURL.Port() == "" {
		proxyAddr = net.JoinHostPort(d.proxyURL.Hostname(), "1080") // Default SOCKS5 port
	}

	socksDialer, err := proxy.SOCKS5("tcp", proxyAddr, auth, proxy.Direct)
	if err != nil {
		slog.Debug("tls_fingerprint_socks5_dialer_failed", "error", err)
		return nil, fmt.Errorf("create SOCKS5 dialer: %w", err)
	}

	// Step 2: Establish SOCKS5 tunnel to target
	slog.Debug("tls_fingerprint_socks5_establishing_tunnel", "target", addr)
	conn, err := socksDialer.Dial("tcp", addr)
	if err != nil {
		slog.Debug("tls_fingerprint_socks5_connect_failed", "error", err)
		return nil, fmt.Errorf("SOCKS5 connect: %w", err)
	}
	slog.Debug("tls_fingerprint_socks5_tunnel_established")

	// Step 3: Perform TLS handshake on the tunnel with utls fingerprint
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	slog.Debug("tls_fingerprint_socks5_starting_handshake", "host", host)

	// Build ClientHello specification from profile (Node.js/Claude CLI fingerprint)
	spec := buildClientHelloSpecFromProfile(d.profile)
	slog.Debug("tls_fingerprint_socks5_clienthello_spec",
		"cipher_suites", len(spec.CipherSuites),
		"extensions", len(spec.Extensions),
		"compression_methods", spec.CompressionMethods,
		"tls_vers_max", spec.TLSVersMax,
		"tls_vers_min", spec.TLSVersMin)

	if d.profile != nil {
		slog.Debug("tls_fingerprint_socks5_using_profile", "name", d.profile.Name, "grease", d.profile.EnableGREASE)
	}

	// Create uTLS connection on the tunnel
	tlsConn := utls.UClient(conn, &utls.Config{
		ServerName: host,
		RootCAs:    d.rootCAs,
	}, utls.HelloCustom)

	if err := tlsConn.ApplyPreset(spec); err != nil {
		slog.Debug("tls_fingerprint_socks5_apply_preset_failed", "error", err)
		_ = conn.Close()
		return nil, fmt.Errorf("apply TLS preset: %w", err)
	}

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		slog.Debug("tls_fingerprint_socks5_handshake_failed", "error", err)
		_ = conn.Close()
		return nil, fmt.Errorf("TLS handshake failed: %w", err)
	}

	state := tlsConn.ConnectionState()
	slog.Debug("tls_fingerprint_socks5_handshake_success",
		"version", state.Version,
		"cipher_suite", state.CipherSuite,
		"alpn", state.NegotiatedProtocol)

	return tlsConn, nil
}

// DialTLSContext establishes a TLS connection through HTTP proxy with the configured fingerprint.
// Flow: TCP connect to proxy -> CONNECT tunnel -> TLS handshake with utls
func (d *HTTPProxyDialer) DialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	slog.Debug("tls_fingerprint_http_proxy_connecting", "proxy", d.proxyURL.Host, "target", addr)

	// Step 1: TCP connect to proxy server
	var proxyAddr string
	if d.proxyURL.Port() != "" {
		proxyAddr = d.proxyURL.Host
	} else {
		// Default ports
		if d.proxyURL.Scheme == "https" {
			proxyAddr = net.JoinHostPort(d.proxyURL.Hostname(), "443")
		} else {
			proxyAddr = net.JoinHostPort(d.proxyURL.Hostname(), "80")
		}
	}

	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		slog.Debug("tls_fingerprint_http_proxy_connect_failed", "error", err)
		return nil, fmt.Errorf("connect to proxy: %w", err)
	}
	slog.Debug("tls_fingerprint_http_proxy_connected", "proxy_addr", proxyAddr)

	// Step 2: Send CONNECT request to establish tunnel
	req := &http.Request{
		Method: "CONNECT",
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}

	// Add proxy authentication if present
	if d.proxyURL.User != nil {
		username := d.proxyURL.User.Username()
		password, _ := d.proxyURL.User.Password()
		auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		req.Header.Set("Proxy-Authorization", "Basic "+auth)
	}

	slog.Debug("tls_fingerprint_http_proxy_sending_connect", "target", addr)
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		slog.Debug("tls_fingerprint_http_proxy_write_failed", "error", err)
		return nil, fmt.Errorf("write CONNECT request: %w", err)
	}

	// Step 3: Read CONNECT response
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = conn.Close()
		slog.Debug("tls_fingerprint_http_proxy_read_response_failed", "error", err)
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		slog.Debug("tls_fingerprint_http_proxy_connect_failed_status", "status_code", resp.StatusCode, "status", resp.Status)
		return nil, fmt.Errorf("proxy CONNECT failed: %s", resp.Status)
	}
	slog.Debug("tls_fingerprint_http_proxy_tunnel_established")

	// Step 4: Perform TLS handshake on the tunnel with utls fingerprint
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	slog.Debug("tls_fingerprint_http_proxy_starting_handshake", "host", host)

	// Build ClientHello specification (reuse the shared method)
	spec := buildClientHelloSpecFromProfile(d.profile)
	slog.Debug("tls_fingerprint_http_proxy_clienthello_spec",
		"cipher_suites", len(spec.CipherSuites),
		"extensions", len(spec.Extensions))

	if d.profile != nil {
		slog.Debug("tls_fingerprint_http_proxy_using_profile", "name", d.profile.Name, "grease", d.profile.EnableGREASE)
	}

	// Create uTLS connection on the tunnel
	// Note: TLS 1.3 cipher suites are handled automatically by utls when TLS 1.3 is in SupportedVersions
	tlsConn := utls.UClient(conn, &utls.Config{
		ServerName: host,
		RootCAs:    d.rootCAs,
	}, utls.HelloCustom)

	if err := tlsConn.ApplyPreset(spec); err != nil {
		slog.Debug("tls_fingerprint_http_proxy_apply_preset_failed", "error", err)
		_ = conn.Close()
		return nil, fmt.Errorf("apply TLS preset: %w", err)
	}

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		slog.Debug("tls_fingerprint_http_proxy_handshake_failed", "error", err)
		_ = conn.Close()
		return nil, fmt.Errorf("TLS handshake failed: %w", err)
	}

	state := tlsConn.ConnectionState()
	slog.Debug("tls_fingerprint_http_proxy_handshake_success",
		"version", state.Version,
		"cipher_suite", state.CipherSuite,
		"alpn", state.NegotiatedProtocol)

	return tlsConn, nil
}

// DialTLSContext establishes a TLS connection with the configured fingerprint.
// This method is designed to be used as http.Transport.DialTLSContext.
func (d *Dialer) DialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	// Establish TCP connection using base dialer (supports proxy)
	slog.Debug("tls_fingerprint_dialing_tcp", "addr", addr)
	conn, err := d.baseDialer(ctx, network, addr)
	if err != nil {
		slog.Debug("tls_fingerprint_tcp_dial_failed", "error", err)
		return nil, err
	}
	slog.Debug("tls_fingerprint_tcp_connected", "addr", addr)

	// Extract hostname for SNI
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	slog.Debug("tls_fingerprint_sni_hostname", "host", host)

	// Build ClientHello specification
	spec := d.buildClientHelloSpec()
	slog.Debug("tls_fingerprint_clienthello_spec",
		"cipher_suites", len(spec.CipherSuites),
		"extensions", len(spec.Extensions))

	// Log profile info
	if d.profile != nil {
		slog.Debug("tls_fingerprint_using_profile", "name", d.profile.Name, "grease", d.profile.EnableGREASE)
	} else {
		slog.Debug("tls_fingerprint_using_default_profile")
	}

	// Create uTLS connection
	// Note: TLS 1.3 cipher suites are handled automatically by utls when TLS 1.3 is in SupportedVersions
	tlsConn := utls.UClient(conn, &utls.Config{
		ServerName: host,
		RootCAs:    d.rootCAs,
	}, utls.HelloCustom)

	// Apply fingerprint
	if err := tlsConn.ApplyPreset(spec); err != nil {
		slog.Debug("tls_fingerprint_apply_preset_failed", "error", err)
		_ = conn.Close()
		return nil, err
	}
	slog.Debug("tls_fingerprint_preset_applied")

	// Perform TLS handshake
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		slog.Debug("tls_fingerprint_handshake_failed",
			"error", err,
			"local_addr", conn.LocalAddr(),
			"remote_addr", conn.RemoteAddr())
		_ = conn.Close()
		return nil, fmt.Errorf("TLS handshake failed: %w", err)
	}

	// Log successful handshake details
	state := tlsConn.ConnectionState()
	slog.Debug("tls_fingerprint_handshake_success",
		"version", state.Version,
		"cipher_suite", state.CipherSuite,
		"alpn", state.NegotiatedProtocol)

	return tlsConn, nil
}

// buildClientHelloSpec constructs the ClientHello specification based on the profile.
func (d *Dialer) buildClientHelloSpec() *utls.ClientHelloSpec {
	return buildClientHelloSpecFromProfile(d.profile)
}

// toUTLSCurves converts uint16 slice to utls.CurveID slice.
func toUTLSCurves(curves []uint16) []utls.CurveID {
	result := make([]utls.CurveID, len(curves))
	for i, c := range curves {
		result[i] = utls.CurveID(c)
	}
	return result
}

// buildClientHelloSpecFromProfile constructs ClientHelloSpec from a Profile.
// This is a standalone function that can be used by both Dialer and HTTPProxyDialer.
func buildClientHelloSpecFromProfile(profile *Profile) *utls.ClientHelloSpec {
	// Get cipher suites
	var cipherSuites []uint16
	if profile != nil && len(profile.CipherSuites) > 0 {
		cipherSuites = profile.CipherSuites
	} else {
		cipherSuites = defaultCipherSuites
	}

	// Get curves
	var curves []utls.CurveID
	if profile != nil && len(profile.Curves) > 0 {
		curves = toUTLSCurves(profile.Curves)
	} else {
		curves = defaultCurves
	}

	// Get point formats
	var pointFormats []uint8
	if profile != nil && len(profile.PointFormats) > 0 {
		pointFormats = profile.PointFormats
	} else {
		pointFormats = defaultPointFormats
	}

	// Check if GREASE is enabled
	enableGREASE := profile != nil && profile.EnableGREASE

	extensions := make([]utls.TLSExtension, 0, 16)

	if enableGREASE {
		extensions = append(extensions, &utls.UtlsGREASEExtension{})
	}

	// Bun 1.3.11 (BoringSSL) extension order (captured from fetch()):
	// encrypted_client_hello(0xfe0d), extended_master_secret(23),
	// renegotiation_info(0xff01), supported_groups(10), ec_point_formats(11),
	// session_ticket(35), alpn(16), status_request(5),
	// signature_algorithms(13), signed_certificate_timestamp(18),
	// key_share(51), psk_key_exchange_modes(45), supported_versions(43),
	// padding(21)
	//
	// SNI (0) 必须显式添加。uTLS HelloCustom 模式下不会自动插入，
	// 只会填充已存在的 SNIExtension 的 ServerName。
	// BoringSSL 的顺序: SNI 在最前面，ECH GREASE 紧随其后。

	extensions = append(extensions,
		&utls.SNIExtension{},
		// BoringSSL sends ECH GREASE (0xfe0d) even without real ECH keys.
		// utls supports this via GREASEEncryptedClientHelloExtension.
		&utls.GREASEEncryptedClientHelloExtension{},
		&utls.ExtendedMasterSecretExtension{},
		&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},
		&utls.SupportedCurvesExtension{Curves: curves},
		&utls.SupportedPointsExtension{SupportedPoints: pointFormats},
		&utls.SessionTicketExtension{},
		&utls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}},
		&utls.StatusRequestExtension{},
		&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: defaultSignatureAlgorithms},
		&utls.SCTExtension{},
		&utls.KeyShareExtension{KeyShares: []utls.KeyShare{
			{Group: utls.X25519},
		}},
		&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},
		&utls.SupportedVersionsExtension{Versions: []uint16{
			utls.VersionTLS13,
			utls.VersionTLS12,
		}},
		&utls.UtlsPaddingExtension{GetPaddingLen: utls.BoringPaddingStyle},
	)

	if enableGREASE {
		extensions = append(extensions, &utls.UtlsGREASEExtension{})
	}

	return &utls.ClientHelloSpec{
		CipherSuites:       cipherSuites,
		CompressionMethods: []uint8{0}, // null compression only (standard)
		Extensions:         extensions,
		TLSVersMax:         utls.VersionTLS13,
		TLSVersMin:         utls.VersionTLS12, // BoringSSL minimum is TLS 1.2
	}
}
