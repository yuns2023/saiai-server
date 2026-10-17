package tlsfingerprint

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"golang.org/x/crypto/cryptobyte"
)

type helloKeyShare struct {
	Group    uint16 `json:"group"`
	KeyBytes int    `json:"key_bytes"`
}

type helloShape struct {
	LegacyVersion       uint16          `json:"legacy_version"`
	SessionIDBytes      int             `json:"session_id_bytes"`
	CipherSuites        []uint16        `json:"cipher_suites"`
	Compression         []uint8         `json:"compression"`
	Extensions          []uint16        `json:"extensions"`
	SNI                 string          `json:"sni"`
	SupportedGroups     []uint16        `json:"supported_groups"`
	PointFormats        []uint8         `json:"point_formats"`
	SignatureAlgorithms []uint16        `json:"signature_algorithms"`
	ALPN                []string        `json:"alpn"`
	SupportedVersions   []uint16        `json:"supported_versions"`
	PSKModes            []uint8         `json:"psk_modes"`
	KeyShares           []helloKeyShare `json:"key_shares"`
}

// Parse actual wire bytes independently of the uTLS preset builder. Random/session
// bytes and key material are deliberately excluded; lengths and ordering are not.
func parseHelloShape(t *testing.T, record []byte) helloShape {
	t.Helper()
	must := func(ok bool) {
		t.Helper()
		if !ok {
			t.Fatal("malformed ClientHello")
		}
	}
	vector := func(s *cryptobyte.String, width int) cryptobyte.String {
		var out cryptobyte.String
		switch width {
		case 1:
			must(s.ReadUint8LengthPrefixed(&out))
		case 2:
			must(s.ReadUint16LengthPrefixed(&out))
		case 3:
			must(s.ReadUint24LengthPrefixed(&out))
		}
		return out
	}
	words := func(s cryptobyte.String) []uint16 {
		var out []uint16
		for !s.Empty() {
			var v uint16
			must(s.ReadUint16(&v))
			out = append(out, v)
		}
		return out
	}
	s := cryptobyte.String(record)
	var typ uint8
	var version uint16
	must(s.ReadUint8(&typ) && typ == 22 && s.ReadUint16(&version))
	s = vector(&s, 2)
	must(s.ReadUint8(&typ) && typ == 1)
	s = vector(&s, 3)
	var h helloShape
	must(s.ReadUint16(&h.LegacyVersion) && s.Skip(32))
	h.SessionIDBytes = len(vector(&s, 1))
	h.CipherSuites = words(vector(&s, 2))
	h.Compression = []byte(vector(&s, 1))
	extensions := vector(&s, 2)
	must(s.Empty())
	for !extensions.Empty() {
		var id uint16
		must(extensions.ReadUint16(&id))
		d := vector(&extensions, 2)
		h.Extensions = append(h.Extensions, id)
		switch id {
		case 0:
			names := vector(&d, 2)
			must(names.ReadUint8(&typ) && typ == 0)
			h.SNI = string(vector(&names, 2))
			must(names.Empty())
		case 10:
			h.SupportedGroups = words(vector(&d, 2))
		case 11:
			h.PointFormats = []byte(vector(&d, 1))
		case 13:
			h.SignatureAlgorithms = words(vector(&d, 2))
		case 16:
			protocols := vector(&d, 2)
			for !protocols.Empty() {
				h.ALPN = append(h.ALPN, string(vector(&protocols, 1)))
			}
		case 43:
			h.SupportedVersions = words(vector(&d, 1))
		case 45:
			h.PSKModes = []byte(vector(&d, 1))
		case 51:
			shares := vector(&d, 2)
			for !shares.Empty() {
				var group uint16
				must(shares.ReadUint16(&group))
				h.KeyShares = append(h.KeyShares, helloKeyShare{group, len(vector(&shares, 2))})
			}
		}
	}
	return h
}

func captureHello(t *testing.T, profile *Profile) helloShape {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	deadline := time.Now().Add(5 * time.Second)
	_ = server.SetDeadline(deadline)
	dialer := NewDialer(profile, func(context.Context, string, string) (net.Conn, error) { return client, nil })
	done := make(chan error, 1)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	go func() {
		conn, err := dialer.DialTLSContext(ctx, "tcp", "api.anthropic.com:443")
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	header := make([]byte, 5)
	if _, err := io.ReadFull(server, header); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, binary.BigEndian.Uint16(header[3:]))
	if _, err := io.ReadFull(server, body); err != nil {
		t.Fatal(err)
	}
	_ = server.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected EOF from capture-only server")
		}
	case <-ctx.Done():
		t.Fatal("dialer did not terminate after capture")
	}
	return parseHelloShape(t, append(header, body...))
}

func TestDefaultClientHelloMatchesClaude2300(t *testing.T) {
	data, err := os.ReadFile("testdata/claude-2.1.300-linux-model-hello.json")
	if err != nil {
		t.Fatal(err)
	}
	var want helloShape
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		profile *Profile
	}{
		{"nil", nil}, {"builtin", DefaultProfile()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := captureHello(t, tc.profile)
			if !reflect.DeepEqual(got, want) {
				a, _ := json.MarshalIndent(got, "", "  ")
				b, _ := json.MarshalIndent(want, "", "  ")
				t.Fatalf("wire ClientHello differs from native model fixture\ngot: %s\nwant: %s", a, b)
			}
		})
	}
}

func TestDefaultClientHelloCompletesVerifiedHandshake(t *testing.T) {
	for _, userAgent := range []string{"claude-cli/2.1.301", "claude-cli/2.1.302"} {
		for _, tc := range []struct {
			name    string
			version uint16
			group   tls.CurveID
		}{
			{"TLS13_hybrid", tls.VersionTLS13, tls.X25519MLKEM768},
			{"TLS13_classical", tls.VersionTLS13, tls.X25519},
			{"TLS12", tls.VersionTLS12, tls.X25519},
		} {
			t.Run(tc.name, func(t *testing.T) {
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
				server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tc.version, CurvePreferences: []tls.CurveID{tc.group}}
				if tc.version == tls.VersionTLS13 {
					server.TLS.MinVersion = tls.VersionTLS13
				}
				server.StartTLS()
				defer server.Close()
				pool := x509.NewCertPool()
				pool.AddCert(server.Certificate())
				// httptest's certificate is valid for example.com. Only routing is changed.
				dialer := NewDialer(ProfileForUserAgent(DefaultProfile(), userAgent), func(ctx context.Context, network, addr string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
				}).WithRootCAs(pool)
				client := &http.Client{Transport: &http.Transport{DialTLSContext: dialer.DialTLSContext}, Timeout: 5 * time.Second}
				defer client.CloseIdleConnections()
				resp, err := client.Get("https://example.com/probe")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = resp.Body.Close() }()
				if resp.StatusCode != http.StatusNoContent {
					t.Fatalf("status = %d", resp.StatusCode)
				}
			})
		}
	}
}

func TestCustomProfileKeySharesRespectGroups(t *testing.T) {
	for _, tc := range []struct {
		name   string
		groups []uint16
		shares []helloKeyShare
	}{
		{"classical", []uint16{29, 23, 24}, []helloKeyShare{{29, 32}}},
		{"p256", []uint16{23}, []helloKeyShare{{23, 65}}},
		{"hybrid_only", []uint16{4588}, []helloKeyShare{{4588, 1216}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := captureHello(t, &Profile{Curves: tc.groups})
			if !reflect.DeepEqual(got.SupportedGroups, tc.groups) || !reflect.DeepEqual(got.KeyShares, tc.shares) {
				t.Fatalf("groups/shares do not honor custom profile: %+v", got)
			}
		})
	}
}
