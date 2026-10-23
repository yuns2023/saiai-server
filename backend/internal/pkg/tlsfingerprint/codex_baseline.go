package tlsfingerprint

import (
	"crypto/mlkem"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"

	utls "github.com/refraction-networking/utls"
)

// Family identifies the client whose initial ClientHello is simulated.
type Family string

// Role identifies the connection purpose inside a family. One client binary
// can use different TLS stacks per role, so the role is part of the identity.
type Role string

const (
	FamilyCodex Family = "codex"

	RoleHTTP Role = "http"
	RoleWS   Role = "ws"

	// Baselines are named after the official build whose capture is the
	// fixture; the version range each one serves is in codex_selection.go.
	// See docs/CODEX_TLS_FINGERPRINT.md for the matrix and evidence.

	// BaselineCodex0140WSRustlsRing is the model WebSocket hello of codex-cli
	// 0.116.0–0.140.x on every platform: rustls 0.23 with the ring provider
	// (X25519 key share only, no post-quantum group, no ML-DSA).
	BaselineCodex0140WSRustlsRing = "codex-0.140.0-ws-rustls-ring"
	// BaselineCodex0155WSRustlsAwslcPQ is the model WebSocket hello of
	// 0.141.0–0.155.x on every platform: rustls 0.23 with aws-lc-rs
	// (X25519MLKEM768 + X25519 shares, no ML-DSA signature schemes).
	BaselineCodex0155WSRustlsAwslcPQ = "codex-0.155.1-ws-rustls-awslc-pq"
	// BaselineCodex0163LinuxWSRustls is the model WebSocket hello of
	// >= 0.156.0 (rustls 0.23.45, aws-lc-rs, ML-DSA schemes advertised).
	// Captured on Linux first; macOS and Windows send the identical hello.
	BaselineCodex0163LinuxWSRustls = "codex-0.163.0-linux-ws-rustls"
	// BaselineCodex0142LinuxHTTPOpenSSL35 is the Linux HTTP/SSE and
	// control-plane hello of < 0.142.2 (native-tls, vendored OpenSSL 3.5:
	// ec_point_formats 0,1,2).
	BaselineCodex0142LinuxHTTPOpenSSL35 = "codex-0.142.1-linux-http-openssl35"
	// BaselineCodex0163LinuxHTTPOpenSSL is the Linux HTTP/SSE and
	// control-plane hello of >= 0.142.2 (native-tls, vendored OpenSSL 3.6).
	BaselineCodex0163LinuxHTTPOpenSSL = "codex-0.163.0-linux-http-openssl"
	// BaselineCodex0163MacOSHTTPSecureTransport is the macOS HTTP/SSE and
	// control-plane hello (native-tls on Security.framework / Secure
	// Transport): TLS 1.2 only.
	BaselineCodex0163MacOSHTTPSecureTransport = "codex-0.163.0-macos-http-securetransport"
	// BaselineCodex0163WindowsHTTPSChannel is the Windows HTTP/SSE and
	// control-plane hello (native-tls on SChannel): TLS 1.2 only.
	BaselineCodex0163WindowsHTTPSChannel = "codex-0.163.0-windows-http-schannel"
)

// TransportPolicy is an explicit, immutable transport identity. The zero value
// means "standard Go TLS"; any other value must name a pinned baseline and the
// role it was captured for. Callers pick it with CodexTransportPolicyFor from
// the role and the outbound Codex User-Agent; it never contains the UA.
type TransportPolicy struct {
	Family   Family
	Baseline string
	Role     Role
}

// CodexHTTPTransportPolicy is the newest Linux Codex HTTP/1.1 (OpenSSL)
// identity, which is also what an unparsable User-Agent selects.
func CodexHTTPTransportPolicy() TransportPolicy {
	return TransportPolicy{Family: FamilyCodex, Baseline: BaselineCodex0163LinuxHTTPOpenSSL, Role: RoleHTTP}
}

// CodexWSTransportPolicy is the newest Codex model WebSocket (rustls)
// identity, which is also what an unparsable User-Agent selects.
func CodexWSTransportPolicy() TransportPolicy {
	return TransportPolicy{Family: FamilyCodex, Baseline: BaselineCodex0163LinuxWSRustls, Role: RoleWS}
}

// Enabled reports whether the policy requests a simulated handshake.
func (p TransportPolicy) Enabled() bool { return p != TransportPolicy{} }

// Key is a stable cache/pool identity. Connections negotiated under different
// keys must never be handed out interchangeably.
func (p TransportPolicy) Key() string {
	if !p.Enabled() {
		return ""
	}
	return string(p.Family) + "/" + p.Baseline + "/" + string(p.Role)
}

// Validate rejects unknown baselines and family/role mismatches.
func (p TransportPolicy) Validate() error {
	_, err := lookupBaseline(p)
	return err
}

// ErrUnknownTransportPolicy is returned for any policy that does not name a
// pinned baseline. Callers must fail the request; there is no std-TLS fallback.
var ErrUnknownTransportPolicy = errors.New("tlsfingerprint: unknown transport policy")

type clientHelloBaseline struct {
	name   string
	family Family
	role   Role
	// build returns a fresh spec. It must not share mutable extension or key
	// share objects with any other connection: ApplyPreset writes into them.
	build func(omitSNI bool, seed uint16) *utls.ClientHelloSpec
	// randomizedOrder: per-connection rustls extension order seed.
	randomizedOrder bool
	// shareX25519 reuses the hybrid share's X25519 key for the standalone
	// X25519 share, as rustls with aws-lc-rs does.
	shareX25519 bool
	// emptySessionID sends a zero-length legacy session id (native TLS 1.2
	// stacks without a cached session); otherwise 32 random bytes.
	emptySessionID bool
	// recordVersion, when non-zero, is the record-layer version of the
	// ClientHello record (Go writes 0x0301).
	recordVersion uint16
}

var clientHelloBaselines = map[string]*clientHelloBaseline{
	BaselineCodex0140WSRustlsRing: {
		name: BaselineCodex0140WSRustlsRing, family: FamilyCodex, role: RoleWS,
		build: rustlsSpecBuilder(codexRustlsRing), randomizedOrder: true,
	},
	BaselineCodex0155WSRustlsAwslcPQ: {
		name: BaselineCodex0155WSRustlsAwslcPQ, family: FamilyCodex, role: RoleWS,
		build: rustlsSpecBuilder(codexRustlsAwslcPQ), randomizedOrder: true, shareX25519: true,
	},
	BaselineCodex0163LinuxWSRustls: {
		name: BaselineCodex0163LinuxWSRustls, family: FamilyCodex, role: RoleWS,
		build: rustlsSpecBuilder(codexRustlsAwslcPQMLDSA), randomizedOrder: true, shareX25519: true,
	},
	BaselineCodex0142LinuxHTTPOpenSSL35: {
		name: BaselineCodex0142LinuxHTTPOpenSSL35, family: FamilyCodex, role: RoleHTTP,
		build: openSSLSpecBuilder([]uint8{0, 1, 2}),
	},
	BaselineCodex0163LinuxHTTPOpenSSL: {
		name: BaselineCodex0163LinuxHTTPOpenSSL, family: FamilyCodex, role: RoleHTTP,
		build: openSSLSpecBuilder([]uint8{0}),
	},
	BaselineCodex0163MacOSHTTPSecureTransport: {
		name: BaselineCodex0163MacOSHTTPSecureTransport, family: FamilyCodex, role: RoleHTTP,
		build: buildCodexSecureTransportSpec, emptySessionID: true,
	},
	BaselineCodex0163WindowsHTTPSChannel: {
		name: BaselineCodex0163WindowsHTTPSChannel, family: FamilyCodex, role: RoleHTTP,
		build: buildCodexSChannelSpec, emptySessionID: true, recordVersion: utls.VersionTLS12,
	},
}

func lookupBaseline(p TransportPolicy) (*clientHelloBaseline, error) {
	b, ok := clientHelloBaselines[p.Baseline]
	if !ok || b.family != p.Family || b.role != p.Role {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTransportPolicy, p.Key())
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// codex model WebSocket: rustls 0.23 (three crypto-provider generations)
// ---------------------------------------------------------------------------

var codexRustlsCipherSuites = []uint16{
	0x1302, 0x1301, 0x1303,
	0xc02c, 0xc02b, 0xcca9,
	0xc030, 0xc02f, 0xcca8,
	0x00ff, // TLS_EMPTY_RENEGOTIATION_INFO_SCSV (rustls sends no ext 0xff01)
}

// rustlsVariant is what differs between the captured rustls generations; the
// cipher suites, extension set and seeded ordering are identical.
type rustlsVariant struct {
	groups    []utls.CurveID
	sigalgs   []utls.SignatureScheme
	keyShares []utls.CurveID
}

var (
	// 0.116.0–0.140.x: ring provider.
	codexRustlsRing = rustlsVariant{
		groups: []utls.CurveID{utls.X25519, utls.CurveP256, utls.CurveP384},
		sigalgs: []utls.SignatureScheme{
			0x0503, 0x0403, 0x0807, 0x0806, 0x0805, 0x0804, 0x0601, 0x0501, 0x0401,
		},
		keyShares: []utls.CurveID{utls.X25519},
	}
	// 0.141.0–0.155.x: aws-lc-rs with X25519MLKEM768, no ML-DSA.
	codexRustlsAwslcPQ = rustlsVariant{
		groups: []utls.CurveID{utls.X25519MLKEM768, utls.X25519, utls.CurveP256, utls.CurveP384},
		sigalgs: []utls.SignatureScheme{
			0x0503, 0x0403, 0x0603, 0x0807, 0x0806, 0x0805, 0x0804, 0x0601, 0x0501, 0x0401,
		},
		keyShares: []utls.CurveID{utls.X25519MLKEM768, utls.X25519},
	}
	// >= 0.156.0: aws-lc-rs with ML-DSA signature schemes.
	codexRustlsAwslcPQMLDSA = rustlsVariant{
		groups: []utls.CurveID{utls.X25519MLKEM768, utls.X25519, utls.CurveP256, utls.CurveP384},
		sigalgs: []utls.SignatureScheme{
			0x0503, 0x0403, 0x0603, 0x0807, 0x0806, 0x0805, 0x0804,
			0x0601, 0x0501, 0x0401, 0x0904, 0x0905, 0x0906,
		},
		keyShares: []utls.CurveID{utls.X25519MLKEM768, utls.X25519},
	}
)

// codexRustlsExtensionIDs are the extensions present in the observed hello,
// listed in rustls 0.23.45 ClientExtensions field (declaration) order:
// server_name, status_request, named_groups, ec_point_formats,
// signature_schemes, extended_master_secret, session_ticket,
// supported_versions, preshared_key_modes, key_shares. collect_used() yields
// that order and sort_by_cached_key is stable, so it is the tie-break order.
// Every captured generation (0.116.0 onward) sends the same set.
var codexRustlsExtensionIDs = []uint16{0, 5, 10, 11, 13, 23, 35, 43, 45, 51}

func rustlsSpecBuilder(v rustlsVariant) func(bool, uint16) *utls.ClientHelloSpec {
	return func(omitSNI bool, seed uint16) *utls.ClientHelloSpec { return buildCodexRustlsSpec(v, omitSNI, seed) }
}

func buildCodexRustlsSpec(v rustlsVariant, omitSNI bool, seed uint16) *utls.ClientHelloSpec {
	ids := make([]uint16, 0, len(codexRustlsExtensionIDs))
	for _, id := range codexRustlsExtensionIDs {
		if id == 0 && omitSNI {
			continue // rustls does not send SNI for IP literals
		}
		ids = append(ids, id)
	}
	exts := make([]utls.TLSExtension, 0, len(ids))
	for _, id := range RustlsExtensionOrder(seed, ids) {
		var ext utls.TLSExtension
		switch id {
		case 0:
			ext = &utls.SNIExtension{}
		case 5:
			ext = &utls.StatusRequestExtension{}
		case 10:
			ext = &utls.SupportedCurvesExtension{Curves: append([]utls.CurveID(nil), v.groups...)}
		case 11:
			ext = &utls.SupportedPointsExtension{SupportedPoints: []uint8{0}}
		case 13:
			ext = &utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: append([]utls.SignatureScheme(nil), v.sigalgs...)}
		case 23:
			ext = &utls.ExtendedMasterSecretExtension{}
		case 35:
			ext = &utls.SessionTicketExtension{}
		case 43:
			ext = &utls.SupportedVersionsExtension{Versions: []uint16{utls.VersionTLS13, utls.VersionTLS12}}
		case 45:
			ext = &utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}}
		case 51:
			shares := make([]utls.KeyShare, 0, len(v.keyShares))
			for _, g := range v.keyShares {
				shares = append(shares, utls.KeyShare{Group: g})
			}
			ext = &utls.KeyShareExtension{KeyShares: shares}
		}
		exts = append(exts, ext)
	}
	return &utls.ClientHelloSpec{
		CipherSuites:       append([]uint16(nil), codexRustlsCipherSuites...),
		CompressionMethods: []uint8{0},
		Extensions:         exts,
		TLSVersMin:         utls.VersionTLS12,
		TLSVersMax:         utls.VersionTLS13,
	}
}

// RustlsLowQualityIntegerHash is rustls 0.23.45 low_quality_integer_hash.
func RustlsLowQualityIntegerHash(x uint32) uint32 {
	x = x + 0x7ed55d16 + (x << 12)
	x = (x ^ 0xc761c23c) ^ (x >> 19)
	x = x + 0x165667b1 + (x << 5)
	x = (x + 0xd3a2646c) ^ (x << 9)
	x = x + 0xfd7046c5 + (x << 3)
	x = (x ^ 0xb55a4f09) ^ (x >> 16)
	return x
}

// RustlsExtensionOrder reproduces rustls 0.23.45
// ClientExtensions::order_insensitive_extensions_in_random_order for a set
// with no contiguous/ECH/PSK extensions: a stable sort of ids (which must be
// in rustls declaration order) by low_quality_integer_hash((seed<<16)|id).
func RustlsExtensionOrder(seed uint16, ids []uint16) []uint16 {
	out := append([]uint16(nil), ids...)
	keys := make(map[uint16]uint32, len(out))
	for _, id := range out {
		keys[id] = RustlsLowQualityIntegerHash(uint32(seed)<<16 | uint32(id))
	}
	sort.SliceStable(out, func(i, j int) bool { return keys[out[i]] < keys[out[j]] })
	return out
}

// ---------------------------------------------------------------------------
// codex Linux HTTP/SSE + control plane: native-tls / vendored OpenSSL 3
// ---------------------------------------------------------------------------

var codexOpenSSLCipherSuites = []uint16{
	0x1302, 0x1303, 0x1301,
	0xc02c, 0xc030, 0x009f, 0xcca9, 0xcca8, 0xccaa, 0xc02b, 0xc02f, 0x009e,
	0xc024, 0xc028, 0x006b, 0xc023, 0xc027, 0x0067,
	0xc00a, 0xc014, 0x0039, 0xc009, 0xc013, 0x0033,
	0x009d, 0x009c, 0x003d, 0x003c, 0x0035, 0x002f,
}

var codexOpenSSLGroups = []utls.CurveID{
	utls.X25519MLKEM768, utls.X25519, utls.CurveP256, 0x001e /* x448 */, utls.CurveP384, utls.CurveP521,
	0x0100 /* ffdhe2048 */, 0x0101, /* ffdhe3072 */
}

var codexOpenSSLSignatureAlgorithms = []utls.SignatureScheme{
	0x0905, 0x0906, 0x0904, // ML-DSA (advertised, not negotiable by uTLS)
	0x0403, 0x0503, 0x0603, 0x0807, 0x0808,
	0x081a, 0x081b, 0x081c,
	0x0809, 0x080a, 0x080b,
	0x0804, 0x0805, 0x0806,
	0x0401, 0x0501, 0x0601,
	0x0303, 0x0301, 0x0302, 0x0402, 0x0502, 0x0602,
}

// openSSLSpecBuilder returns the native-tls/OpenSSL 3 builder. The captured
// OpenSSL generations differ only in ec_point_formats: 3.5 (< 0.142.2) sends
// uncompressed, ansiX962_compressed_prime, ansiX962_compressed_char2; 3.6
// sends uncompressed only.
func openSSLSpecBuilder(pointFormats []uint8) func(bool, uint16) *utls.ClientHelloSpec {
	return func(omitSNI bool, _ uint16) *utls.ClientHelloSpec {
		return buildCodexOpenSSLSpec(pointFormats, omitSNI)
	}
}

func buildCodexOpenSSLSpec(pointFormats []uint8, omitSNI bool) *utls.ClientHelloSpec {
	exts := []utls.TLSExtension{
		&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},
	}
	if !omitSNI {
		exts = append(exts, &utls.SNIExtension{})
	}
	exts = append(exts,
		&utls.SupportedPointsExtension{SupportedPoints: append([]uint8(nil), pointFormats...)},
		&utls.SupportedCurvesExtension{Curves: append([]utls.CurveID(nil), codexOpenSSLGroups...)},
		&utls.SessionTicketExtension{},
		&utls.GenericExtension{Id: 22}, // encrypt_then_mac (TLS 1.2 CBC only)
		&utls.ExtendedMasterSecretExtension{},
		&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: append([]utls.SignatureScheme(nil), codexOpenSSLSignatureAlgorithms...)},
		&utls.SupportedVersionsExtension{Versions: []uint16{utls.VersionTLS13, utls.VersionTLS12}},
		&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},
		&utls.KeyShareExtension{KeyShares: []utls.KeyShare{{Group: utls.X25519MLKEM768}, {Group: utls.X25519}}},
	)
	return &utls.ClientHelloSpec{
		CipherSuites:       append([]uint16(nil), codexOpenSSLCipherSuites...),
		CompressionMethods: []uint8{0},
		Extensions:         exts,
		TLSVersMin:         utls.VersionTLS12,
		TLSVersMax:         utls.VersionTLS13,
	}
}

// ---------------------------------------------------------------------------
// codex macOS / Windows HTTP/SSE + control plane: native-tls on the OS stack
// ---------------------------------------------------------------------------
//
// Both OS stacks, as driven by native-tls, offer TLS 1.2 only: no
// supported_versions, no key_share, legacy version 0x0303, an empty legacy
// session id (no cached session) and no ALPN (HTTP/1.1). uTLS advertises some
// suites/schemes it cannot negotiate (3DES, CBC-SHA384, DSA); a server that
// selects one fails the handshake loudly.

var codexSecureTransportCipherSuites = []uint16{
	0x00ff, // TLS_EMPTY_RENEGOTIATION_INFO_SCSV (no ff01 extension)
	0xc02c, 0xc02b, 0xc024, 0xc023, 0xc00a, 0xc009, 0xc008,
	0xc030, 0xc02f, 0xc028, 0xc027, 0xc014, 0xc013, 0xc012,
	0x009d, 0x009c, 0x003d, 0x003c, 0x0035, 0x002f, 0x000a,
}

var codexSecureTransportSignatureAlgorithms = []utls.SignatureScheme{
	0x0401, 0x0201, 0x0501, 0x0601, 0x0403, 0x0203, 0x0503, 0x0603,
}

// buildCodexSecureTransportSpec: macOS Security.framework (Secure Transport)
// as captured on macOS 15.3.1 (171-byte hello for chatgpt.com). Record
// version 0x0301 (Go's default for the first record).
func buildCodexSecureTransportSpec(omitSNI bool, _ uint16) *utls.ClientHelloSpec {
	var exts []utls.TLSExtension
	if !omitSNI {
		exts = append(exts, &utls.SNIExtension{})
	}
	exts = append(exts,
		&utls.SupportedCurvesExtension{Curves: []utls.CurveID{utls.CurveP256, utls.CurveP384, utls.CurveP521}},
		&utls.SupportedPointsExtension{SupportedPoints: []uint8{0}},
		&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: append([]utls.SignatureScheme(nil), codexSecureTransportSignatureAlgorithms...)},
		&utls.StatusRequestExtension{},
		&utls.SCTExtension{},
		&utls.ExtendedMasterSecretExtension{},
	)
	return &utls.ClientHelloSpec{
		CipherSuites:       append([]uint16(nil), codexSecureTransportCipherSuites...),
		CompressionMethods: []uint8{0},
		Extensions:         exts,
		TLSVersMin:         utls.VersionTLS12,
		TLSVersMax:         utls.VersionTLS12,
	}
}

var codexSChannelCipherSuites = []uint16{
	0xc02c, 0xc02b, 0xc030, 0xc02f, 0xc024, 0xc023, 0xc028, 0xc027,
	0xc00a, 0xc009, 0xc014, 0xc013,
	0x009d, 0x009c, 0x003d, 0x003c, 0x0035, 0x002f,
}

var codexSChannelSignatureAlgorithms = []utls.SignatureScheme{
	0x0804, 0x0805, 0x0806, 0x0401, 0x0501, 0x0201,
	0x0403, 0x0503, 0x0203, 0x0202, 0x0601, 0x0603,
}

// buildCodexSChannelSpec: Windows SChannel as captured on Windows 11 build
// 26200 (167-byte hello for chatgpt.com). Record version 0x0303 (applied by
// the dialer), renegotiation_info extension instead of the SCSV.
func buildCodexSChannelSpec(omitSNI bool, _ uint16) *utls.ClientHelloSpec {
	var exts []utls.TLSExtension
	if !omitSNI {
		exts = append(exts, &utls.SNIExtension{})
	}
	exts = append(exts,
		&utls.SupportedCurvesExtension{Curves: []utls.CurveID{utls.X25519, utls.CurveP256, utls.CurveP384}},
		&utls.SupportedPointsExtension{SupportedPoints: []uint8{0}},
		&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: append([]utls.SignatureScheme(nil), codexSChannelSignatureAlgorithms...)},
		&utls.SessionTicketExtension{},
		&utls.ExtendedMasterSecretExtension{},
		&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},
	)
	return &utls.ClientHelloSpec{
		CipherSuites:       append([]uint16(nil), codexSChannelCipherSuites...),
		CompressionMethods: []uint8{0},
		Extensions:         exts,
		TLSVersMin:         utls.VersionTLS12,
		TLSVersMax:         utls.VersionTLS12,
	}
}

// ---------------------------------------------------------------------------
// per-connection application
// ---------------------------------------------------------------------------

func randomExtensionOrderSeed() (uint16, error) {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b[:]), nil
}

// newBaselineUConn builds a uTLS client for one connection: a fresh spec, a
// fresh extension-order seed and (for rustls) the shared X25519 key.
func newBaselineUConn(b *clientHelloBaseline, raw net.Conn, cfg *utls.Config, seed func() (uint16, error)) (*utls.UConn, error) {
	var orderSeed uint16
	if b.randomizedOrder {
		if seed == nil {
			seed = randomExtensionOrderSeed
		}
		s, err := seed()
		if err != nil {
			return nil, fmt.Errorf("extension order seed: %w", err)
		}
		orderSeed = s
	}
	omitSNI := net.ParseIP(cfg.ServerName) != nil
	if b.recordVersion != 0 {
		raw = &helloRecordVersionConn{Conn: raw, version: b.recordVersion}
	}
	uconn := utls.UClient(raw, cfg, utls.HelloCustom)
	if err := uconn.ApplyPreset(b.build(omitSNI, orderSeed)); err != nil {
		return nil, fmt.Errorf("apply %s preset: %w", b.name, err)
	}
	if b.emptySessionID {
		// ApplyPreset always generates a 32-byte id; native TLS 1.2 stacks
		// without a cached session send none. Nothing is resumed either way.
		uconn.HandshakeState.Hello.SessionId = nil
	}
	if b.shareX25519 {
		if err := shareHybridX25519Key(uconn); err != nil {
			return nil, err
		}
	}
	return uconn, nil
}

// shareHybridX25519Key makes the standalone X25519 share carry the X25519
// component of the X25519MLKEM768 share (draft-ietf-tls-hybrid-design-09
// §3.2), and points the X25519 private key state at the same key so a server
// selecting plain X25519 still completes the handshake.
func shareHybridX25519Key(uconn *utls.UConn) error {
	keys := uconn.HandshakeState.State13.KeyShareKeys
	if keys == nil || keys.MlkemEcdhe == nil {
		return errors.New("tlsfingerprint: hybrid key share state missing")
	}
	var ks *utls.KeyShareExtension
	for _, ext := range uconn.Extensions {
		if k, ok := ext.(*utls.KeyShareExtension); ok {
			ks = k
		}
	}
	if ks == nil {
		return errors.New("tlsfingerprint: key_share extension missing")
	}
	pub := keys.MlkemEcdhe.PublicKey().Bytes()
	hybrid, classical := -1, -1
	for i := range ks.KeyShares {
		switch ks.KeyShares[i].Group {
		case utls.X25519MLKEM768:
			hybrid = i
		case utls.X25519:
			classical = i
		}
	}
	if hybrid < 0 || classical < 0 {
		return errors.New("tlsfingerprint: hybrid and X25519 shares required")
	}
	hd := ks.KeyShares[hybrid].Data
	if len(hd) != mlkem.EncapsulationKeySize768+len(pub) || string(hd[mlkem.EncapsulationKeySize768:]) != string(pub) {
		return errors.New("tlsfingerprint: unexpected hybrid key share layout")
	}
	ks.KeyShares[classical].Data = append([]byte(nil), pub...)
	keys.Ecdhe = keys.MlkemEcdhe
	return nil
}

// helloRecordVersionConn rewrites the record-layer version of the first
// record written (the ClientHello) from Go's fixed 0x0301 to version. The
// record header is not part of the handshake transcript. Any other first
// write is refused rather than sent with the wrong shape.
type helloRecordVersionConn struct {
	net.Conn
	version uint16
	done    bool
}

func (c *helloRecordVersionConn) Write(b []byte) (int, error) {
	if c.done {
		return c.Conn.Write(b)
	}
	c.done = true
	if len(b) < 5 || b[0] != 22 || binary.BigEndian.Uint16(b[1:3]) != utls.VersionTLS10 {
		return 0, errors.New("tlsfingerprint: first record is not a ClientHello record")
	}
	out := append([]byte(nil), b...)
	binary.BigEndian.PutUint16(out[1:3], c.version)
	return c.Conn.Write(out)
}
