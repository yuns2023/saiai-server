package tlsfingerprint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/cryptobyte"
)

// rawHello is an independent wire view of one ClientHello record. Random,
// session id and key-share bytes are kept so tests can assert on them, but
// comparisons against captures mask them explicitly.
type rawHello struct {
	RecordHeader  []byte
	LegacyVersion uint16
	SessionID     []byte
	CipherSuites  []uint16
	Compression   []byte
	Extensions    []rawExtension
}

type rawExtension struct {
	ID   uint16
	Data []byte
}

func (h rawHello) ext(id uint16) []byte {
	for _, e := range h.Extensions {
		if e.ID == id {
			return e.Data
		}
	}
	return nil
}

func (h rawHello) extIDs() []uint16 {
	ids := make([]uint16, 0, len(h.Extensions))
	for _, e := range h.Extensions {
		ids = append(ids, e.ID)
	}
	return ids
}

// keyShares returns group -> key bytes in wire order.
func (h rawHello) keyShares(t *testing.T) ([]uint16, map[uint16][]byte) {
	t.Helper()
	d := cryptobyte.String(h.ext(51))
	var list cryptobyte.String
	if !d.ReadUint16LengthPrefixed(&list) {
		t.Fatal("malformed key_share")
	}
	var groups []uint16
	keys := map[uint16][]byte{}
	for !list.Empty() {
		var g uint16
		var k cryptobyte.String
		if !list.ReadUint16(&g) || !list.ReadUint16LengthPrefixed(&k) {
			t.Fatal("malformed key_share entry")
		}
		groups = append(groups, g)
		keys[g] = append([]byte(nil), k...)
	}
	return groups, keys
}

func parseRawHello(t *testing.T, record []byte) rawHello {
	t.Helper()
	must := func(ok bool, what string) {
		t.Helper()
		if !ok {
			t.Fatalf("malformed ClientHello: %s", what)
		}
	}
	must(len(record) >= 5, "short record")
	h := rawHello{RecordHeader: append([]byte(nil), record[:5]...)}
	s := cryptobyte.String(record)
	var typ uint8
	var recVersion uint16
	var body, hs cryptobyte.String
	must(s.ReadUint8(&typ) && typ == 22 && s.ReadUint16(&recVersion) && s.ReadUint16LengthPrefixed(&body), "record")
	must(s.Empty(), "trailing bytes after first record")
	must(body.ReadUint8(&typ) && typ == 1 && body.ReadUint24LengthPrefixed(&hs) && body.Empty(), "handshake header")
	var random, sid, suites, comp, exts cryptobyte.String
	must(hs.ReadUint16(&h.LegacyVersion) && hs.ReadBytes((*[]byte)(&random), 32), "version/random")
	must(hs.ReadUint8LengthPrefixed(&sid), "session id")
	h.SessionID = append([]byte(nil), sid...)
	must(hs.ReadUint16LengthPrefixed(&suites), "cipher suites")
	for !suites.Empty() {
		var c uint16
		must(suites.ReadUint16(&c), "cipher")
		h.CipherSuites = append(h.CipherSuites, c)
	}
	must(hs.ReadUint8LengthPrefixed(&comp), "compression")
	h.Compression = append([]byte(nil), comp...)
	must(hs.ReadUint16LengthPrefixed(&exts) && hs.Empty(), "extensions")
	for !exts.Empty() {
		var id uint16
		var data cryptobyte.String
		must(exts.ReadUint16(&id) && exts.ReadUint16LengthPrefixed(&data), "extension")
		h.Extensions = append(h.Extensions, rawExtension{ID: id, Data: append([]byte(nil), data...)})
	}
	return h
}

func u16s(b []byte) []uint16 {
	out := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		out = append(out, binary.BigEndian.Uint16(b[i:]))
	}
	return out
}

// ja4 follows the FoxIO JA4 definition (same as the capture parser).
func ja4(h rawHello) string {
	top := "00"
	if sv := h.ext(43); len(sv) >= 3 {
		switch binary.BigEndian.Uint16(sv[1:]) {
		case 0x0304:
			top = "13"
		case 0x0303:
			top = "12"
		}
	}
	sni := "i"
	if h.ext(0) != nil {
		sni = "d"
	}
	alpn := "00"
	if a := h.ext(16); len(a) > 3 {
		n := int(a[2])
		proto := string(a[3 : 3+n])
		alpn = proto[:1] + proto[len(proto)-1:]
	}
	a := fmt.Sprintf("t%s%s%02d%02d%s", top, sni, len(h.CipherSuites), len(h.Extensions), alpn)
	hash := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:])[:12] }
	ciphers := make([]string, 0, len(h.CipherSuites))
	for _, c := range h.CipherSuites {
		ciphers = append(ciphers, fmt.Sprintf("%04x", c))
	}
	sort.Strings(ciphers)
	var exts []string
	for _, id := range h.extIDs() {
		if id != 0 && id != 16 {
			exts = append(exts, fmt.Sprintf("%04x", id))
		}
	}
	sort.Strings(exts)
	c := strings.Join(exts, ",")
	if sa := h.ext(13); len(sa) > 2 {
		var sigs []string
		for _, v := range u16s(sa[2:]) {
			sigs = append(sigs, fmt.Sprintf("%04x", v))
		}
		c += "_" + strings.Join(sigs, ",")
	}
	return a + "_" + hash(strings.Join(ciphers, ",")) + "_" + hash(c)
}

type codexCaptureFixture struct {
	JA4             string         `json:"ja4"`
	ExtensionOrders map[string]int `json:"extension_orders_seen"`
	CipherSuites    []struct {
		ID string `json:"id"`
	} `json:"cipher_suites"`
	ClientHelloBytes []int `json:"clienthello_total_bytes_seen"`
}

func loadCodexFixture(t *testing.T, baseline string) (codexCaptureFixture, rawHello, []byte) {
	t.Helper()
	data, err := os.ReadFile("testdata/" + baseline + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var fx codexCaptureFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/" + baseline + ".clienthello.bin")
	if err != nil {
		t.Fatal(err)
	}
	return fx, parseRawHello(t, raw), raw
}

// captureCodexHello dials through the real PolicyDialer (direct mode, routed
// to a capture-only listener) and returns the first record it sends.
func captureCodexHello(t *testing.T, policy TransportPolicy, seed *uint16, serverName string) ([]byte, rawHello) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		hdr := make([]byte, 5)
		if _, err := io.ReadFull(c, hdr); err != nil {
			got <- nil
			return
		}
		body := make([]byte, binary.BigEndian.Uint16(hdr[3:]))
		if _, err := io.ReadFull(c, body); err != nil {
			got <- nil
			return
		}
		got <- append(hdr, body...)
	}()
	d, err := NewPolicyDialer(policy, PolicyDialerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d.dialDirect = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, ln.Addr().String())
	}
	if seed != nil {
		s := *seed
		d.seed = func() (uint16, error) { return s, nil }
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := d.DialTLSContext(ctx, "tcp", net.JoinHostPort(serverName, "443"))
	if err == nil {
		_ = conn.Close()
		t.Fatal("capture-only server must not complete a handshake")
	}
	record := <-got
	if record == nil {
		t.Fatal("no ClientHello captured")
	}
	return record, parseRawHello(t, record)
}

func parseOrder(t *testing.T, s string) []uint16 {
	t.Helper()
	var out []uint16
	for _, part := range strings.Split(s, ",") {
		v, err := strconv.Atoi(part)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, uint16(v))
	}
	return out
}

func seedForOrder(order []uint16) (uint16, bool) {
	for s := 0; s <= 0xffff; s++ {
		if reflect.DeepEqual(RustlsExtensionOrder(uint16(s), codexRustlsExtensionIDs), order) {
			return uint16(s), true
		}
	}
	return 0, false
}

func TestRustlsExtensionOrderReproducesEveryCapturedOrder(t *testing.T) {
	fx, captured, _ := loadCodexFixture(t, BaselineCodex0163LinuxWSRustls)
	if len(fx.ExtensionOrders) != 12 {
		t.Fatalf("fixture has %d orders, want the 12 captured", len(fx.ExtensionOrders))
	}
	for order := range fx.ExtensionOrders {
		want := parseOrder(t, order)
		seed, ok := seedForOrder(want)
		if !ok {
			t.Fatalf("no rustls seed reproduces captured order %s", order)
		}
		t.Logf("order %s <- seed %d", order, seed)
	}
	if _, ok := seedForOrder(captured.extIDs()); !ok {
		t.Fatal("raw capture order not reproducible")
	}
}

// compareToCapture checks every wire byte except the per-connection random,
// session id and key-share key bytes (whose groups and lengths must match).
func compareToCapture(t *testing.T, got, want rawHello, wantOrder bool) {
	t.Helper()
	if !bytes.Equal(got.RecordHeader[:3], want.RecordHeader[:3]) {
		t.Fatalf("record header %x, want %x", got.RecordHeader[:3], want.RecordHeader[:3])
	}
	if got.LegacyVersion != want.LegacyVersion || len(got.SessionID) != len(want.SessionID) ||
		!reflect.DeepEqual(got.CipherSuites, want.CipherSuites) || !bytes.Equal(got.Compression, want.Compression) {
		t.Fatalf("hello header differs:\n got %04x sid=%d %04x %x\nwant %04x sid=%d %04x %x",
			got.LegacyVersion, len(got.SessionID), got.CipherSuites, got.Compression,
			want.LegacyVersion, len(want.SessionID), want.CipherSuites, want.Compression)
	}
	if wantOrder && !reflect.DeepEqual(got.extIDs(), want.extIDs()) {
		t.Fatalf("extension order %v, want %v", got.extIDs(), want.extIDs())
	}
	gotIDs, wantIDs := append([]uint16(nil), got.extIDs()...), append([]uint16(nil), want.extIDs()...)
	sort.Slice(gotIDs, func(i, j int) bool { return gotIDs[i] < gotIDs[j] })
	sort.Slice(wantIDs, func(i, j int) bool { return wantIDs[i] < wantIDs[j] })
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("extension set %v, want %v", gotIDs, wantIDs)
	}
	for _, e := range want.Extensions {
		if e.ID == 51 {
			gg, gk := got.keyShares(t)
			wg, wk := want.keyShares(t)
			if !reflect.DeepEqual(gg, wg) {
				t.Fatalf("key share groups %04x, want %04x", gg, wg)
			}
			for _, g := range wg {
				if len(gk[g]) != len(wk[g]) {
					t.Fatalf("key share %04x len %d, want %d", g, len(gk[g]), len(wk[g]))
				}
			}
			continue
		}
		if !bytes.Equal(got.ext(e.ID), e.Data) {
			t.Fatalf("extension %d = %x, want %x", e.ID, got.ext(e.ID), e.Data)
		}
	}
	if !bytes.Contains(got.ext(0), []byte("chatgpt.com")) {
		t.Fatal("SNI missing")
	}
	if got.ext(16) != nil || got.ext(41) != nil || got.ext(21) != nil {
		t.Fatal("ALPN, PSK and padding must be absent")
	}
}

func TestCodexWSHelloMatchesRustlsCapture(t *testing.T) {
	fx, want, wantRecord := loadCodexFixture(t, BaselineCodex0163LinuxWSRustls)
	seed, ok := seedForOrder(want.extIDs())
	if !ok {
		t.Fatal("capture order not reproducible")
	}
	record, got := captureCodexHello(t, CodexWSTransportPolicy(), &seed, "chatgpt.com")
	if len(record) != len(wantRecord) {
		t.Fatalf("ClientHello %d bytes, capture %d", len(record), len(wantRecord))
	}
	compareToCapture(t, got, want, true)
	if j := ja4(got); j != fx.JA4 || j != ja4(want) {
		t.Fatalf("JA4 %s, capture %s / %s", j, fx.JA4, ja4(want))
	}
	// rustls 0.23.45 reuses the hybrid X25519 key for the standalone share;
	// so does the capture.
	for name, h := range map[string]rawHello{"generated": got, "capture": want} {
		_, keys := h.keyShares(t)
		if !bytes.Equal(keys[0x11ec][1184:], keys[0x001d]) {
			t.Fatalf("%s: X25519 share is not the hybrid X25519 key", name)
		}
	}
}

func TestCodexHTTPHelloMatchesOpenSSLCapture(t *testing.T) {
	fx, want, wantRecord := loadCodexFixture(t, BaselineCodex0163LinuxHTTPOpenSSL)
	record, got := captureCodexHello(t, CodexHTTPTransportPolicy(), nil, "chatgpt.com")
	if len(record) != len(wantRecord) {
		t.Fatalf("ClientHello %d bytes, capture %d", len(record), len(wantRecord))
	}
	compareToCapture(t, got, want, true)
	if j := ja4(got); j != fx.JA4 || j != ja4(want) {
		t.Fatalf("JA4 %s, capture %s / %s", j, fx.JA4, ja4(want))
	}
	// OpenSSL generates an independent X25519 key (as captured).
	for name, h := range map[string]rawHello{"generated": got, "capture": want} {
		_, keys := h.keyShares(t)
		if bytes.Equal(keys[0x11ec][1184:], keys[0x001d]) {
			t.Fatalf("%s: OpenSSL shape must not share the X25519 key", name)
		}
	}
}

func TestCodexWSUsesFreshSeedAndKeysPerConnection(t *testing.T) {
	orders := map[string]bool{}
	sessionIDs := map[string]bool{}
	hybridKeys := map[string]bool{}
	x25519Keys := map[string]bool{}
	for i := 0; i < 8; i++ {
		_, h := captureCodexHello(t, CodexWSTransportPolicy(), nil, "chatgpt.com")
		orders[fmt.Sprint(h.extIDs())] = true
		sessionIDs[string(h.SessionID)] = true
		_, keys := h.keyShares(t)
		hybridKeys[string(keys[0x11ec])] = true
		x25519Keys[string(keys[0x001d])] = true
		if _, ok := seedForOrder(h.extIDs()); !ok {
			t.Fatalf("order %v is not a rustls order", h.extIDs())
		}
	}
	if len(orders) < 2 || len(sessionIDs) != 8 || len(hybridKeys) != 8 || len(x25519Keys) != 8 {
		t.Fatalf("expected per-connection randomness, got %d orders / %d session ids / %d hybrid keys / %d x25519 keys",
			len(orders), len(sessionIDs), len(hybridKeys), len(x25519Keys))
	}
}

func TestCodexSpecFactoryReturnsIndependentObjects(t *testing.T) {
	for _, b := range clientHelloBaselines {
		a, c := b.build(false, 1), b.build(false, 1)
		for i := range a.Extensions {
			// Zero-size extension structs may share an address; they hold no state.
			if reflect.TypeOf(a.Extensions[i]).Elem().Size() > 0 && a.Extensions[i] == c.Extensions[i] {
				t.Fatalf("%s: extension %d shared between specs", b.name, i)
			}
		}
		if &a.CipherSuites[0] == &c.CipherSuites[0] {
			t.Fatalf("%s: cipher slice shared", b.name)
		}
	}
}

func TestCodexIPLiteralOmitsSNI(t *testing.T) {
	for _, p := range []TransportPolicy{CodexHTTPTransportPolicy(), CodexWSTransportPolicy()} {
		_, h := captureCodexHello(t, p, nil, "127.0.0.1")
		if h.ext(0) != nil {
			t.Fatalf("%s sent SNI for an IP literal", p.Baseline)
		}
	}
}

func TestTransportPolicyValidation(t *testing.T) {
	for _, p := range []TransportPolicy{
		{},
		{Family: FamilyCodex, Baseline: BaselineCodex0163LinuxWSRustls, Role: RoleHTTP},
		{Family: FamilyCodex, Baseline: BaselineCodex0163LinuxHTTPOpenSSL, Role: RoleWS},
		{Family: "claude", Baseline: BaselineCodex0163LinuxHTTPOpenSSL, Role: RoleHTTP},
		{Family: FamilyCodex, Baseline: "codex-0.164.0-linux-http-openssl", Role: RoleHTTP},
	} {
		if err := p.Validate(); err == nil {
			t.Fatalf("policy %+v must be rejected", p)
		}
		if _, err := NewPolicyDialer(p, PolicyDialerOptions{}); err == nil {
			t.Fatalf("dialer for %+v must be rejected", p)
		}
	}
	for _, p := range []TransportPolicy{CodexHTTPTransportPolicy(), CodexWSTransportPolicy()} {
		if err := p.Validate(); err != nil || !p.Enabled() || p.Key() == "" {
			t.Fatalf("pinned policy %+v invalid: %v", p, err)
		}
	}
	if CodexHTTPTransportPolicy().Key() == CodexWSTransportPolicy().Key() || (TransportPolicy{}).Key() != "" {
		t.Fatal("policy keys must be distinct and zero for std TLS")
	}
}
