package tlsfingerprint

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// codexMatrix lists every pinned Codex baseline with the role it serves.
var codexMatrix = []struct {
	baseline   string
	role       Role
	randomized bool
	shareX     bool // rustls aws-lc-rs: X25519 share reuses the hybrid key
	tls12Only  bool
}{
	{BaselineCodex0140WSRustlsRing, RoleWS, true, false, false},
	{BaselineCodex0155WSRustlsAwslcPQ, RoleWS, true, true, false},
	{BaselineCodex0163LinuxWSRustls, RoleWS, true, true, false},
	{BaselineCodex0142LinuxHTTPOpenSSL35, RoleHTTP, false, false, false},
	{BaselineCodex0163LinuxHTTPOpenSSL, RoleHTTP, false, false, false},
	{BaselineCodex0163MacOSHTTPSecureTransport, RoleHTTP, false, false, true},
	{BaselineCodex0163WindowsHTTPSChannel, RoleHTTP, false, false, true},
}

func matrixPolicy(baseline string, role Role) TransportPolicy {
	return TransportPolicy{Family: FamilyCodex, Baseline: baseline, Role: role}
}

func TestCodexMatrixCoversRegistry(t *testing.T) {
	if len(codexMatrix) != len(clientHelloBaselines) {
		t.Fatalf("matrix has %d baselines, registry %d", len(codexMatrix), len(clientHelloBaselines))
	}
	for _, m := range codexMatrix {
		if err := matrixPolicy(m.baseline, m.role).Validate(); err != nil {
			t.Fatal(err)
		}
		other := RoleHTTP
		if m.role == RoleHTTP {
			other = RoleWS
		}
		if matrixPolicy(m.baseline, other).Validate() == nil {
			t.Fatalf("%s accepted for role %s", m.baseline, other)
		}
	}
}

// Every baseline reproduces its capture byte for byte, except random,
// session id bytes and key material (rustls: with the capture's seed).
func TestCodexEveryBaselineMatchesCapture(t *testing.T) {
	for _, m := range codexMatrix {
		t.Run(m.baseline, func(t *testing.T) {
			fx, want, wantRecord := loadCodexFixture(t, m.baseline)
			var seed *uint16
			if m.randomized {
				s, ok := seedForOrder(want.extIDs())
				if !ok {
					t.Fatal("capture order not reproducible")
				}
				seed = &s
			}
			record, got := captureCodexHello(t, matrixPolicy(m.baseline, m.role), seed, "chatgpt.com")
			if len(record) != len(wantRecord) {
				t.Fatalf("ClientHello %d bytes, capture %d", len(record), len(wantRecord))
			}
			compareToCapture(t, got, want, true)
			if j := ja4(got); j != fx.JA4 || j != ja4(want) {
				t.Fatalf("JA4 %s, capture %s / %s", j, fx.JA4, ja4(want))
			}
			if m.tls12Only {
				if got.ext(43) != nil || got.ext(51) != nil || got.ext(45) != nil || len(got.SessionID) != 0 || got.LegacyVersion != 0x0303 {
					t.Fatal("TLS 1.2-only hello must have no supported_versions/key_share/psk modes and an empty session id")
				}
				return
			}
			if len(got.SessionID) != 32 {
				t.Fatalf("session id len %d", len(got.SessionID))
			}
			groups, _ := got.keyShares(t)
			if len(groups) == 2 {
				for name, h := range map[string]rawHello{"generated": got, "capture": want} {
					_, keys := h.keyShares(t)
					if shared := bytes.Equal(keys[0x11ec][1184:], keys[0x001d]); shared != m.shareX {
						t.Fatalf("%s: X25519 key shared=%v, want %v", name, shared, m.shareX)
					}
				}
			}
		})
	}
}

// Every captured extension order of every rustls generation is a rustls
// seeded order over the same declaration-order extension set.
func TestRustlsOrdersReproducedForEveryGeneration(t *testing.T) {
	for _, b := range []struct {
		baseline string
		minOrder int
	}{
		{BaselineCodex0140WSRustlsRing, 30},
		{BaselineCodex0155WSRustlsAwslcPQ, 30},
		{BaselineCodex0163LinuxWSRustls, 12},
	} {
		fx, _, _ := loadCodexFixture(t, b.baseline)
		if len(fx.ExtensionOrders) < b.minOrder {
			t.Fatalf("%s: only %d orders in fixture", b.baseline, len(fx.ExtensionOrders))
		}
		for order := range fx.ExtensionOrders {
			if _, ok := seedForOrder(parseOrder(t, order)); !ok {
				t.Fatalf("%s: no rustls seed reproduces captured order %s", b.baseline, order)
			}
		}
	}
}

func TestCodexEveryBaselineFreshPerConnection(t *testing.T) {
	for _, m := range codexMatrix {
		randoms := map[string]bool{}
		orders := map[string]bool{}
		for i := 0; i < 6; i++ {
			record, h := captureCodexHello(t, matrixPolicy(m.baseline, m.role), nil, "chatgpt.com")
			randoms[string(record[11:43])] = true
			orders[string(fmtIDs(h.extIDs()))] = true
		}
		if len(randoms) != 6 {
			t.Fatalf("%s: client random reused", m.baseline)
		}
		if m.randomized != (len(orders) > 1) {
			t.Fatalf("%s: %d distinct orders over 6 dials (randomized=%v)", m.baseline, len(orders), m.randomized)
		}
	}
}

func fmtIDs(ids []uint16) []byte {
	out := make([]byte, 0, 2*len(ids))
	for _, id := range ids {
		out = binary.BigEndian.AppendUint16(out, id)
	}
	return out
}

func TestCodexEveryBaselineOmitsSNIForIPLiteral(t *testing.T) {
	for _, m := range codexMatrix {
		_, h := captureCodexHello(t, matrixPolicy(m.baseline, m.role), nil, "127.0.0.1")
		if h.ext(0) != nil {
			t.Fatalf("%s sent SNI for an IP literal", m.baseline)
		}
	}
}

// --- handshakes ----------------------------------------------------------

var (
	rsaCertOnce sync.Once
	rsaCert     tls.Certificate
	rsaCertErr  error
)

func exampleRSACertificate(t *testing.T) tls.Certificate {
	t.Helper()
	rsaCertOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			rsaCertErr = err
			return
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "example.com"},
			DNSNames: []string{"example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			BasicConstraintsValid: true, IsCA: true,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			rsaCertErr = err
			return
		}
		rsaCert = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	})
	if rsaCertErr != nil {
		t.Fatal(rsaCertErr)
	}
	return rsaCert
}

// newMatrixServer serves the negotiated protocol, curve, version and suite.
func newMatrixServer(t *testing.T, cfg *tls.Config, cert tls.Certificate) (*httptest.Server, *teeListener, *x509.CertPool) {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto+" "+r.TLS.CurveID.String()+" "+tls.VersionName(r.TLS.Version)+" "+tls.CipherSuiteName(r.TLS.CipherSuite))
	}))
	tee := &teeListener{Listener: server.Listener}
	server.Listener = tee
	cfg = cfg.Clone()
	cfg.Certificates = []tls.Certificate{cert}
	server.TLS = cfg
	server.StartTLS()
	t.Cleanup(server.Close)
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return server, tee, roots(leaf)
}

func TestCodexEveryBaselineCompletesHandshakes(t *testing.T) {
	type tc struct {
		name string
		cfg  *tls.Config
		rsa  bool
		want string // prefix of "proto curve version suite"
	}
	tls13 := []tc{
		{"tls13_default", &tls.Config{MinVersion: tls.VersionTLS13}, false, "HTTP/1.1 "},
		{"tls13_x25519", &tls.Config{MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}}, false, "HTTP/1.1 X25519 TLS 1.3"},
		{"tls13_hrr_p256", &tls.Config{MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.CurveP256}}, false, "HTTP/1.1 CurveP256 TLS 1.3"},
	}
	//nolint:gosec // local TLS 1.2 fixtures: TLS 1.2 is the behavior under test.
	tls12 := func(curve string) []tc {
		return []tc{
			{"tls12_ecdhe_ecdsa_aes128gcm", &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12, CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}}, false, "HTTP/1.1 " + curve + " TLS 1.2 TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"},
			{"tls12_ecdhe_ecdsa_aes256gcm", &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12, CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384}}, false, "HTTP/1.1 " + curve + " TLS 1.2 TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384"},
			{"tls12_ecdhe_rsa_aes128gcm", &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12, CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}}, true, "HTTP/1.1 " + curve + " TLS 1.2 TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"},
			{"tls12_ecdhe_rsa_aes256gcm", &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12, CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384}}, true, "HTTP/1.1 " + curve + " TLS 1.2 TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384"},
		}
	}
	cases := map[string][]tc{}
	for _, m := range codexMatrix {
		switch m.baseline {
		case BaselineCodex0163MacOSHTTPSecureTransport:
			// TLS 1.3-capable server: the client caps at 1.2 and accepts the
			// server's downgrade sentinel (its own max is 1.2).
			cases[m.baseline] = append(tls12("CurveP256"),
				tc{"tls13_capable_server_rsa", &tls.Config{MinVersion: tls.VersionTLS12}, true, "HTTP/1.1 CurveP256 TLS 1.2 TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"},
				tc{"tls13_capable_server_ecdsa", &tls.Config{MinVersion: tls.VersionTLS12}, false, "HTTP/1.1 CurveP256 TLS 1.2 TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"})
		case BaselineCodex0163WindowsHTTPSChannel:
			cases[m.baseline] = append(tls12("X25519"),
				tc{"tls13_capable_server_rsa", &tls.Config{MinVersion: tls.VersionTLS12}, true, "HTTP/1.1 X25519 TLS 1.2 TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"},
				tc{"tls13_capable_server_ecdsa", &tls.Config{MinVersion: tls.VersionTLS12}, false, "HTTP/1.1 X25519 TLS 1.2 TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"})
		default:
			cases[m.baseline] = append(append([]tc(nil), tls13...), tls12("X25519")[0], tls12("X25519")[2])
		}
	}
	for _, m := range codexMatrix {
		for _, c := range cases[m.baseline] {
			t.Run(m.baseline+"/"+c.name, func(t *testing.T) {
				cert := exampleCertificate(t)
				if c.rsa {
					cert = exampleRSACertificate(t)
				}
				server, tee, pool := newMatrixServer(t, c.cfg, cert)
				d := directDialer(t, matrixPolicy(m.baseline, m.role), server.Listener.Addr().String(), pool)
				resp, err := policyClient(t, d).Get("https://example.com/probe")
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if !strings.HasPrefix(string(body), c.want) || resp.ProtoMajor != 1 {
					t.Fatalf("got %q (proto %s), want prefix %q", body, resp.Proto, c.want)
				}
				records := handshakeRecords(tee.clientBytes(0))
				if len(records) == 0 {
					t.Fatal("no ClientHello seen by server")
				}
				// The server saw the baseline's hello, not a Go default one.
				fx, want, _ := loadCodexFixture(t, m.baseline)
				first := parseRawHello(t, records[0])
				if !bytes.Equal(first.RecordHeader[:3], want.RecordHeader[:3]) || ja4(first) != fx.JA4 {
					t.Fatalf("server saw record %x ja4 %s, want %x %s", first.RecordHeader[:3], ja4(first), want.RecordHeader[:3], fx.JA4)
				}
			})
		}
	}
}

func TestCodexTLS12OnlyBaselinesFailLoudly(t *testing.T) {
	native := []string{BaselineCodex0163MacOSHTTPSecureTransport, BaselineCodex0163WindowsHTTPSChannel}
	for _, b := range native {
		p := matrixPolicy(b, RoleHTTP)
		// A TLS 1.3-only server cannot talk to a TLS 1.2-only client: the
		// dial fails, it is not retried with another hello.
		t.Run(b+"/tls13_only_server", func(t *testing.T) {
			server, tee, pool := newMatrixServer(t, &tls.Config{MinVersion: tls.VersionTLS13}, exampleCertificate(t))
			d := directDialer(t, p, server.Listener.Addr().String(), pool)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if c, err := d.DialTLSContext(ctx, "tcp", "example.com:443"); err == nil {
				_ = c.Close()
				t.Fatal("TLS 1.3-only server accepted")
			}
			time.Sleep(50 * time.Millisecond)
			if n := len(handshakeRecords(tee.clientBytes(0))); n != 1 {
				t.Fatalf("client sent %d handshake records, want exactly the one hello", n)
			}
		})
		for _, tc := range []struct {
			name  string
			suite uint16
			want  string // the specific client-side rejection
		}{
			{"ecdhe_ecdsa_3des", 0xc008, "server chose an unconfigured cipher suite"},       // advertised by Secure Transport, not implemented
			{"ecdhe_ecdsa_cbc_sha384", 0xc024, "server chose an unconfigured cipher suite"}, // advertised, not implemented
			{"tls13_suite_in_tls12", 0x1301, "server chose an unconfigured cipher suite"},
			{"unadvertised_dhe_suite", 0x009f, "server chose an unconfigured cipher suite"},
		} {
			t.Run(b+"/"+tc.name, func(t *testing.T) {
				srv := newCountingScriptedServer(t, func(sid []byte) []byte {
					return serverHelloRecord(make([]byte, 32), sid, tc.suite, nil)
				})
				d := directDialer(t, p, srv.addr, x509.NewCertPool())
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				conn, err := d.DialTLSContext(ctx, "tcp", "example.com:443")
				if err == nil {
					_ = conn.Close()
					t.Fatal("unsupported selection must fail the dial")
				}
				// Only the specific rejection counts: a timeout or EOF would
				// mean the client never evaluated the ServerHello.
				msg := err.Error()
				if !strings.Contains(msg, tc.want) || errors.Is(err, context.DeadlineExceeded) ||
					errors.Is(err, io.EOF) || strings.Contains(msg, "timeout") {
					t.Fatalf("dial error %q, want rejection %q", msg, tc.want)
				}
				after := srv.rest()
				if len(after) < 7 || after[0] != 21 || after[5] != 2 {
					t.Fatalf("client must send a fatal alert, got %x", after)
				}
				for len(after) >= 5 {
					if after[0] == 22 {
						t.Fatalf("client sent another handshake record after failure: %x", after[:5])
					}
					after = after[5+int(binary.BigEndian.Uint16(after[3:5])):]
				}
				time.Sleep(50 * time.Millisecond)
				if n := srv.accepts.Load(); n != 1 {
					t.Fatalf("server saw %d connections, want 1 (no redial)", n)
				}
			})
		}
	}
}

// countingScriptedServer is scriptedServer that keeps accepting and counts
// connections, so a silent redial would be visible.
type countingScriptedServer struct {
	addr    string
	accepts atomic.Int32
	first   chan []byte
}

func (s *countingScriptedServer) rest() []byte {
	select {
	case b := <-s.first:
		return b
	case <-time.After(5 * time.Second):
		return nil
	}
}

func newCountingScriptedServer(t *testing.T, reply func(sessionID []byte) []byte) *countingScriptedServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &countingScriptedServer{addr: ln.Addr().String(), first: make(chan []byte, 1)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n := s.accepts.Add(1)
			go func(c net.Conn, n int32) {
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				hdr := make([]byte, 5)
				if _, err := io.ReadFull(c, hdr); err != nil {
					return
				}
				body := make([]byte, binary.BigEndian.Uint16(hdr[3:]))
				if _, err := io.ReadFull(c, body); err != nil {
					return
				}
				var sid []byte
				if len(body) > 38 {
					l := int(body[38])
					sid = append([]byte(nil), body[39:39+l]...)
				}
				_, _ = c.Write(reply(sid))
				after, _ := io.ReadAll(c)
				if n == 1 {
					s.first <- after
				}
			}(c, n)
		}
	}()
	return s
}

// helloRecordVersionConn rewrites exactly the first write and passes every
// later write through untouched; a first write that is not a ClientHello
// record is refused without sending anything.
func TestHelloRecordVersionConnBoundaries(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	got := make(chan []byte, 2)
	go func() {
		for i := 0; i < 2; i++ {
			buf := make([]byte, 64)
			n, err := server.Read(buf)
			if err != nil {
				return
			}
			got <- buf[:n]
		}
	}()
	w := &helloRecordVersionConn{Conn: client, version: 0x0303}
	first := []byte{22, 3, 1, 0, 1, 1}
	if n, err := w.Write(first); err != nil || n != len(first) {
		t.Fatalf("first write: %d %v", n, err)
	}
	if b := <-got; !bytes.Equal(b, []byte{22, 3, 3, 0, 1, 1}) {
		t.Fatalf("first record %x", b)
	}
	if !bytes.Equal(first, []byte{22, 3, 1, 0, 1, 1}) {
		t.Fatal("caller buffer mutated")
	}
	second := []byte{22, 3, 1, 0, 1, 2} // later records are never touched
	if _, err := w.Write(second); err != nil {
		t.Fatal(err)
	}
	if b := <-got; !bytes.Equal(b, second) {
		t.Fatalf("second record %x", b)
	}

	for _, bad := range [][]byte{{23, 3, 1, 0, 1, 0}, {22, 3, 3, 0, 1, 0}, {22, 3}} {
		c, s := net.Pipe()
		w := &helloRecordVersionConn{Conn: c, version: 0x0303}
		_ = c.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		if n, err := w.Write(bad); err == nil || n != 0 {
			t.Fatalf("first write %x must be refused, got %d %v", bad, n, err)
		}
		_ = c.Close()
		_ = s.Close()
	}
}

// The SChannel record version rewrite also applies inside proxy tunnels.
func TestCodexSChannelRecordVersionThroughProxies(t *testing.T) {
	//nolint:gosec // local TLS 1.2 fixture under test
	server, tee, pool := newMatrixServer(t, &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}, exampleCertificate(t))
	target := server.Listener.Addr().String()
	httpProxy := connectProxy(t, target, false, false)
	httpsProxy := connectProxy(t, target, true, false)
	proxies := map[string]string{
		"http":   "http://user:pass@" + strings.TrimPrefix(httpProxy.URL, "http://"),
		"https":  "https://user:pass@" + strings.TrimPrefix(httpsProxy.URL, "https://"),
		"socks5": "socks5h://" + socks5Proxy(t, target, false),
	}
	pool.AddCert(httpsProxy.Certificate())
	for name, raw := range proxies {
		t.Run(name, func(t *testing.T) {
			u, _ := url.Parse(raw)
			before := len(tee.seen)
			d, err := NewPolicyDialer(matrixPolicy(BaselineCodex0163WindowsHTTPSChannel, RoleHTTP), PolicyDialerOptions{ProxyURL: u, RootCAs: pool})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := policyClient(t, d).Get("https://example.com/probe")
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			records := handshakeRecords(tee.clientBytes(before))
			if len(records) == 0 || !bytes.Equal(records[0][:3], []byte{22, 3, 3}) {
				t.Fatalf("tunnelled SChannel hello record header %x", records[0][:3])
			}
		})
	}
}
