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

	// BaselineCodex0163LinuxWSRustls is the model WebSocket connection of the
	// official codex-cli 0.163.0 Linux x86_64 build (rustls 0.23.45, aws-lc-rs)
	// with the default CA environment. See docs/CODEX_TLS_FINGERPRINT.md.
	BaselineCodex0163LinuxWSRustls = "codex-0.163.0-linux-ws-rustls"
	// BaselineCodex0163LinuxHTTPOpenSSL is the HTTP/SSE model and control-plane
	// connection of the same build (reqwest native-tls, vendored OpenSSL 3)
	// with the default CA environment.
	BaselineCodex0163LinuxHTTPOpenSSL = "codex-0.163.0-linux-http-openssl"
)

// TransportPolicy is an explicit, immutable transport identity. The zero value
// means "standard Go TLS"; any other value must name a pinned baseline and the
// role it was captured for. It is never derived from request headers.
type TransportPolicy struct {
	Family   Family
	Baseline string
	Role     Role
}

// CodexHTTPTransportPolicy is the pinned Codex HTTP/1.1 (OpenSSL) identity.
func CodexHTTPTransportPolicy() TransportPolicy {
	return TransportPolicy{Family: FamilyCodex, Baseline: BaselineCodex0163LinuxHTTPOpenSSL, Role: RoleHTTP}
}

// CodexWSTransportPolicy is the pinned Codex model WebSocket (rustls) identity.
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
	// X25519 share, as rustls 0.23.45 does.
	shareX25519 bool
}

var clientHelloBaselines = map[string]*clientHelloBaseline{
	BaselineCodex0163LinuxWSRustls: {
		name: BaselineCodex0163LinuxWSRustls, family: FamilyCodex, role: RoleWS,
		build: buildCodexRustlsSpec, randomizedOrder: true, shareX25519: true,
	},
	BaselineCodex0163LinuxHTTPOpenSSL: {
		name: BaselineCodex0163LinuxHTTPOpenSSL, family: FamilyCodex, role: RoleHTTP,
		build: buildCodexOpenSSLSpec,
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
// codex 0.163.0 Linux model WebSocket: rustls 0.23.45 (aws-lc-rs provider)
// ---------------------------------------------------------------------------

var codexRustlsCipherSuites = []uint16{
	0x1302, 0x1301, 0x1303,
	0xc02c, 0xc02b, 0xcca9,
	0xc030, 0xc02f, 0xcca8,
	0x00ff, // TLS_EMPTY_RENEGOTIATION_INFO_SCSV (rustls sends no ext 0xff01)
}

var codexRustlsSignatureAlgorithms = []utls.SignatureScheme{
	0x0503, 0x0403, 0x0603, 0x0807, 0x0806, 0x0805, 0x0804,
	0x0601, 0x0501, 0x0401, 0x0904, 0x0905, 0x0906,
}

var codexRustlsGroups = []utls.CurveID{utls.X25519MLKEM768, utls.X25519, utls.CurveP256, utls.CurveP384}

// codexRustlsExtensionIDs are the extensions present in the observed hello,
// listed in rustls 0.23.45 ClientExtensions field (declaration) order:
// server_name, status_request, named_groups, ec_point_formats,
// signature_schemes, extended_master_secret, session_ticket,
// supported_versions, preshared_key_modes, key_shares. collect_used() yields
// that order and sort_by_cached_key is stable, so it is the tie-break order.
var codexRustlsExtensionIDs = []uint16{0, 5, 10, 11, 13, 23, 35, 43, 45, 51}

func buildCodexRustlsSpec(omitSNI bool, seed uint16) *utls.ClientHelloSpec {
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
			ext = &utls.SupportedCurvesExtension{Curves: append([]utls.CurveID(nil), codexRustlsGroups...)}
		case 11:
			ext = &utls.SupportedPointsExtension{SupportedPoints: []uint8{0}}
		case 13:
			ext = &utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: append([]utls.SignatureScheme(nil), codexRustlsSignatureAlgorithms...)}
		case 23:
			ext = &utls.ExtendedMasterSecretExtension{}
		case 35:
			ext = &utls.SessionTicketExtension{}
		case 43:
			ext = &utls.SupportedVersionsExtension{Versions: []uint16{utls.VersionTLS13, utls.VersionTLS12}}
		case 45:
			ext = &utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}}
		case 51:
			ext = &utls.KeyShareExtension{KeyShares: []utls.KeyShare{{Group: utls.X25519MLKEM768}, {Group: utls.X25519}}}
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
// codex 0.163.0 Linux HTTP/SSE + control plane: native-tls / OpenSSL 3
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

func buildCodexOpenSSLSpec(omitSNI bool, _ uint16) *utls.ClientHelloSpec {
	exts := []utls.TLSExtension{
		&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},
	}
	if !omitSNI {
		exts = append(exts, &utls.SNIExtension{})
	}
	exts = append(exts,
		&utls.SupportedPointsExtension{SupportedPoints: []uint8{0}},
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
	uconn := utls.UClient(raw, cfg, utls.HelloCustom)
	if err := uconn.ApplyPreset(b.build(omitSNI, orderSeed)); err != nil {
		return nil, fmt.Errorf("apply %s preset: %w", b.name, err)
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
