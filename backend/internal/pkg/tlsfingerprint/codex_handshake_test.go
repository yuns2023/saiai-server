package tlsfingerprint

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var codexPolicies = []TransportPolicy{CodexHTTPTransportPolicy(), CodexWSTransportPolicy()}

// teeListener records every byte the client sends on each accepted conn.
type teeListener struct {
	net.Listener
	mu   sync.Mutex
	seen []*bytes.Buffer
}

type teeConn struct {
	net.Conn
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (c *teeConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.mu.Lock()
	c.buf.Write(p[:n])
	c.mu.Unlock()
	return n, err
}

func (l *teeListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	buf := &bytes.Buffer{}
	l.mu.Lock()
	l.seen = append(l.seen, buf)
	l.mu.Unlock()
	return &teeConn{Conn: c, mu: &l.mu, buf: buf}, nil
}

func (l *teeListener) clientBytes(i int) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.seen[i].Bytes()...)
}

// handshakeRecords splits client bytes into its plaintext handshake records.
func handshakeRecords(b []byte) [][]byte {
	var out [][]byte
	for len(b) >= 5 {
		n := int(binary.BigEndian.Uint16(b[3:5]))
		if len(b) < 5+n {
			break
		}
		if b[0] == 22 {
			out = append(out, append([]byte(nil), b[:5+n]...))
		}
		if b[0] == 23 { // encrypted data follows; stop
			break
		}
		b = b[5+n:]
	}
	return out
}

func newCurveServer(t *testing.T, cfg *tls.Config) (*httptest.Server, *teeListener) {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto+" "+r.TLS.CurveID.String()+" "+tls.VersionName(r.TLS.Version))
	}))
	tee := &teeListener{Listener: server.Listener}
	server.Listener = tee
	cfg = cfg.Clone()
	// A dedicated example.com certificate: httptest's built-in one is shared
	// by every test server (including the proxies) and would mask trust bugs.
	cfg.Certificates = []tls.Certificate{exampleCertificate(t)}
	server.TLS = cfg
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, tee
}

var (
	exampleCertOnce sync.Once
	exampleCert     tls.Certificate
	exampleCertErr  error
)

func exampleCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	exampleCertOnce.Do(func() {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			exampleCertErr = err
			return
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example.com"},
			DNSNames: []string{"example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			BasicConstraintsValid: true, IsCA: true,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			exampleCertErr = err
			return
		}
		exampleCert = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	})
	if exampleCertErr != nil {
		t.Fatal(exampleCertErr)
	}
	return exampleCert
}

func roots(certs ...*x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	for _, c := range certs {
		pool.AddCert(c)
	}
	return pool
}

func policyClient(t *testing.T, d *PolicyDialer) *http.Client {
	t.Helper()
	tr := &http.Transport{DialTLSContext: d.DialTLSContext}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func directDialer(t *testing.T, p TransportPolicy, target string, pool *x509.CertPool) *PolicyDialer {
	t.Helper()
	d, err := NewPolicyDialer(p, PolicyDialerOptions{RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	d.dialDirect = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}
	return d
}

func TestCodexPoliciesCompleteVerifiedHandshakes(t *testing.T) {
	cases := []struct {
		name string
		cfg  *tls.Config
		want string
		hrr  bool
	}{
		{"tls13_hybrid", &tls.Config{MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519MLKEM768}}, "HTTP/1.1 X25519MLKEM768 TLS 1.3", false},
		{"tls13_x25519", &tls.Config{MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}}, "HTTP/1.1 X25519 TLS 1.3", false},
		{"tls13_hrr_p256", &tls.Config{MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.CurveP256}}, "HTTP/1.1 CurveP256 TLS 1.3", true},
		{"tls13_hrr_p384", &tls.Config{MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.CurveP384}}, "HTTP/1.1 CurveP384 TLS 1.3", true},
		{"tls12_ecdhe", &tls.Config{MaxVersion: tls.VersionTLS12, CurvePreferences: []tls.CurveID{tls.X25519}}, "HTTP/1.1 X25519 TLS 1.2", false},
		{"alpn_offered_by_server_ignored", &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"}}, "HTTP/1.1 X25519MLKEM768 TLS 1.3", false},
	}
	for _, p := range codexPolicies {
		for _, tc := range cases {
			t.Run(p.Baseline+"/"+tc.name, func(t *testing.T) {
				server, tee := newCurveServer(t, tc.cfg)
				d := directDialer(t, p, server.Listener.Addr().String(), roots(server.Certificate()))
				resp, err := policyClient(t, d).Get("https://example.com/probe")
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if string(body) != tc.want || resp.ProtoMajor != 1 {
					t.Fatalf("got %q (proto %s), want %q", body, resp.Proto, tc.want)
				}
				records := handshakeRecords(tee.clientBytes(0))
				if tc.hrr {
					if len(records) < 2 {
						t.Fatalf("expected a second ClientHello after HRR, got %d records", len(records))
					}
					first, second := parseRawHello(t, records[0]), parseRawHello(t, records[1])
					var kept []uint16
					for _, id := range second.extIDs() {
						if id != 44 { // cookie position is a documented residual
							kept = append(kept, id)
						}
					}
					if !reflect.DeepEqual(kept, first.extIDs()) {
						t.Fatalf("HRR changed extension order: %v -> %v", first.extIDs(), second.extIDs())
					}
					groups, keys := second.keyShares(t)
					if len(groups) != 1 || len(keys[groups[0]]) == 0 {
						t.Fatalf("retry must carry exactly the requested share, got %04x", groups)
					}
				}
			})
		}
	}
}

func TestCodexPolicyRejectsUntrustedCertificate(t *testing.T) {
	server, _ := newCurveServer(t, &tls.Config{})
	for _, p := range codexPolicies {
		d := directDialer(t, p, server.Listener.Addr().String(), x509.NewCertPool())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := d.DialTLSContext(ctx, "tcp", "example.com:443")
		cancel()
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("%s: untrusted certificate accepted: %v", p.Baseline, err)
		}
	}
}

// scriptedServer reads one ClientHello, replies with reply(sessionID), and
// then records anything else the client sends until EOF.
func scriptedServer(t *testing.T, reply func(sessionID []byte) []byte) (string, <-chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	rest := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			rest <- nil
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		hdr := make([]byte, 5)
		if _, err := io.ReadFull(c, hdr); err != nil {
			rest <- nil
			return
		}
		body := make([]byte, binary.BigEndian.Uint16(hdr[3:]))
		_, _ = io.ReadFull(c, body)
		hello := parseRawHello(t, append(hdr, body...))
		_, _ = c.Write(reply(hello.SessionID))
		after, _ := io.ReadAll(c)
		rest <- after
	}()
	return ln.Addr().String(), rest
}

var helloRetryRandom = []byte{
	0xCF, 0x21, 0xAD, 0x74, 0xE5, 0x9A, 0x61, 0x11, 0xBE, 0x1D, 0x8C, 0x02, 0x1E, 0x65, 0xB8, 0x91,
	0xC2, 0xA2, 0x11, 0x16, 0x7A, 0xBB, 0x8C, 0x5E, 0x07, 0x9E, 0x09, 0xE2, 0xC8, 0xA8, 0x33, 0x9C,
}

func serverHelloRecord(random []byte, sessionID []byte, suite uint16, exts []byte) []byte {
	var body []byte
	body = append(body, 0x03, 0x03)
	body = append(body, random...)
	body = append(body, byte(len(sessionID)))
	body = append(body, sessionID...)
	body = binary.BigEndian.AppendUint16(body, suite)
	body = append(body, 0)
	if exts != nil {
		body = binary.BigEndian.AppendUint16(body, uint16(len(exts)))
		body = append(body, exts...)
	}
	hs := append([]byte{2, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}, body...)
	rec := []byte{22, 3, 3}
	rec = binary.BigEndian.AppendUint16(rec, uint16(len(hs)))
	return append(rec, hs...)
}

func hrrSelecting(group uint16) func([]byte) []byte {
	return func(sid []byte) []byte {
		exts := []byte{0x00, 0x2b, 0x00, 0x02, 0x03, 0x04, 0x00, 0x33, 0x00, 0x02}
		exts = binary.BigEndian.AppendUint16(exts, group)
		return serverHelloRecord(helloRetryRandom, sid, 0x1301, exts)
	}
}

// Advertised-but-unimplemented algorithms must fail the dial, and the client
// must not send another ClientHello (no silent re-negotiation or fallback).
func TestCodexUnsupportedServerSelectionFailsLoudly(t *testing.T) {
	cases := []struct {
		name   string
		policy TransportPolicy
		reply  func([]byte) []byte
	}{
		{"openssl_hrr_x448", CodexHTTPTransportPolicy(), hrrSelecting(0x001e)},
		{"openssl_hrr_ffdhe2048", CodexHTTPTransportPolicy(), hrrSelecting(0x0100)},
		{"openssl_tls12_dhe_suite", CodexHTTPTransportPolicy(), func(sid []byte) []byte {
			return serverHelloRecord(make([]byte, 32), sid, 0x009f, nil)
		}},
		{"rustls_hrr_unadvertised_x448", CodexWSTransportPolicy(), hrrSelecting(0x001e)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, rest := scriptedServer(t, tc.reply)
			d := directDialer(t, tc.policy, addr, x509.NewCertPool())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := d.DialTLSContext(ctx, "tcp", "example.com:443")
			if err == nil {
				_ = conn.Close()
				t.Fatal("unsupported selection must fail the dial")
			}
			after := <-rest
			for len(after) >= 5 {
				if after[0] == 22 {
					t.Fatalf("client sent another handshake record after failure: %x", after[:5])
				}
				after = after[5+int(binary.BigEndian.Uint16(after[3:5])):]
			}
		})
	}
}

// --- proxies -------------------------------------------------------------

func connectProxy(t *testing.T, target string, useTLS bool, hang bool) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "example.com:443" || (useTLS && r.TLS == nil) {
			http.Error(w, "bad CONNECT", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" {
			http.Error(w, "auth", http.StatusProxyAuthRequired)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if hang {
			_, _ = io.Copy(io.Discard, conn)
			return
		}
		upstream, err := net.Dial("tcp", target)
		if err != nil {
			return
		}
		defer func() { _ = upstream.Close() }()
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go func() { _, _ = io.Copy(upstream, conn); _ = upstream.Close() }()
		_, _ = io.Copy(conn, upstream)
	})
	var s *httptest.Server
	if useTLS {
		s = httptest.NewTLSServer(h)
	} else {
		s = httptest.NewServer(h)
	}
	t.Cleanup(s.Close)
	return s
}

// socks5Proxy is a minimal no-auth SOCKS5 CONNECT server mapping every
// destination to target (hang=true never answers the request).
func socks5Proxy(t *testing.T, target string, hang bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				buf := make([]byte, 262)
				if _, err := io.ReadFull(c, buf[:2]); err != nil {
					return
				}
				if _, err := io.ReadFull(c, buf[:buf[1]]); err != nil {
					return
				}
				if hang {
					_, _ = io.Copy(io.Discard, c)
					return
				}
				_, _ = c.Write([]byte{5, 0})
				if _, err := io.ReadFull(c, buf[:4]); err != nil || buf[1] != 1 {
					return
				}
				switch buf[3] {
				case 1:
					_, _ = io.ReadFull(c, buf[:6])
				case 3:
					_, _ = io.ReadFull(c, buf[:1])
					n := int(buf[0])
					_, _ = io.ReadFull(c, buf[:n+2])
					if string(buf[:n]) != "example.com" {
						return
					}
				default:
					return
				}
				upstream, err := net.Dial("tcp", target)
				if err != nil {
					_, _ = c.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer func() { _ = upstream.Close() }()
				_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				go func() { _, _ = io.Copy(upstream, c); _ = upstream.Close() }()
				_, _ = io.Copy(c, upstream)
			}(c)
		}
	}()
	return ln.Addr().String()
}

func TestCodexPolicyThroughProxies(t *testing.T) {
	server, tee := newCurveServer(t, &tls.Config{MinVersion: tls.VersionTLS13})
	target := server.Listener.Addr().String()
	httpProxy := connectProxy(t, target, false, false)
	httpsProxy := connectProxy(t, target, true, false)
	socks := socks5Proxy(t, target, false)
	proxies := map[string]string{
		"http":   "http://user:pass@" + strings.TrimPrefix(httpProxy.URL, "http://"),
		"https":  "https://user:pass@" + strings.TrimPrefix(httpsProxy.URL, "https://"),
		"socks5": "socks5h://" + socks,
	}
	for _, p := range codexPolicies {
		for name, raw := range proxies {
			t.Run(p.Baseline+"/"+name, func(t *testing.T) {
				u, _ := url.Parse(raw)
				before := len(tee.seen)
				d, err := NewPolicyDialer(p, PolicyDialerOptions{ProxyURL: u, RootCAs: roots(server.Certificate(), httpsProxy.Certificate())})
				if err != nil {
					t.Fatal(err)
				}
				resp, err := policyClient(t, d).Get("https://example.com/probe")
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if !strings.HasPrefix(string(body), "HTTP/1.1 X25519MLKEM768") {
					t.Fatalf("body %q", body)
				}
				records := handshakeRecords(tee.clientBytes(before))
				if len(records) == 0 || parseRawHello(t, records[0]).ext(16) != nil {
					t.Fatal("tunnelled hello must be the policy hello (no ALPN)")
				}
				// Target certificate is still verified inside the tunnel.
				bad, _ := NewPolicyDialer(p, PolicyDialerOptions{ProxyURL: u, RootCAs: roots(httpsProxy.Certificate())})
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if c, err := bad.DialTLSContext(ctx, "tcp", "example.com:443"); err == nil {
					_ = c.Close()
					t.Fatal("untrusted target accepted through proxy")
				}
			})
		}
	}
	// An https proxy with an untrusted certificate fails before CONNECT.
	u, _ := url.Parse(proxies["https"])
	d, _ := NewPolicyDialer(CodexHTTPTransportPolicy(), PolicyDialerOptions{ProxyURL: u, RootCAs: roots(server.Certificate())})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := d.DialTLSContext(ctx, "tcp", "example.com:443"); err == nil || !strings.Contains(err.Error(), "proxy TLS handshake") {
		t.Fatalf("untrusted proxy: %v", err)
	}
}

func TestCodexPolicyProxyCancellation(t *testing.T) {
	httpHang := connectProxy(t, "", false, true)
	socksHang := socks5Proxy(t, "", true)
	for name, raw := range map[string]string{
		"http":   "http://user:pass@" + strings.TrimPrefix(httpHang.URL, "http://"),
		"socks5": "socks5h://" + socksHang,
	} {
		t.Run(name, func(t *testing.T) {
			u, _ := url.Parse(raw)
			d, err := NewPolicyDialer(CodexWSTransportPolicy(), PolicyDialerOptions{ProxyURL: u})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			timer := time.AfterFunc(100*time.Millisecond, cancel)
			defer timer.Stop()
			done := make(chan error, 1)
			go func() { _, err := d.DialTLSContext(ctx, "tcp", "example.com:443"); done <- err }()
			select {
			case err := <-done:
				// x/net/proxy reports cancellation as an expired deadline;
				// what matters is a prompt failure.
				if err == nil {
					t.Fatal("expected cancellation error")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("dial ignored cancellation")
			}
		})
	}
	// The handshake timeout also bounds a silent HTTP proxy without ctx deadline.
	u, _ := url.Parse("http://user:pass@" + strings.TrimPrefix(httpHang.URL, "http://"))
	d, _ := NewPolicyDialer(CodexHTTPTransportPolicy(), PolicyDialerOptions{ProxyURL: u, HandshakeTimeout: 150 * time.Millisecond})
	start := time.Now()
	if _, err := d.DialTLSContext(context.Background(), "tcp", "example.com:443"); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("handshake timeout not enforced: %v after %s", err, time.Since(start))
	}
}

func TestCodexPolicyRejectsUnsupportedProxyScheme(t *testing.T) {
	for _, raw := range []string{"ftp://proxy:21", "socks4://proxy:1080"} {
		u, _ := url.Parse(raw)
		if _, err := NewPolicyDialer(CodexHTTPTransportPolicy(), PolicyDialerOptions{ProxyURL: u}); err == nil {
			t.Fatalf("%s must be rejected", raw)
		}
	}
	if _, err := NewPolicyDialer(CodexHTTPTransportPolicy(), PolicyDialerOptions{}); err != nil {
		t.Fatal(err)
	}
}
