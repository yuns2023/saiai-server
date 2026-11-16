package chain

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mr-tron/base58"
)

func testKey(b byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, 32))
}

func testAddr(b byte) string {
	return base58.Encode(testKey(b)[ed25519.SeedSize:])
}

func signedManifest(t *testing.T, mutate func(*SolanaPoolManifest), signer ed25519.PrivateKey) []byte {
	t.Helper()
	m := SolanaPoolManifest{
		Version:      1,
		Network:      SolanaMainnet,
		PathTemplate: SolanaPoolPathTemplate,
		Start:        10,
		Entries: []SolanaPoolEntry{
			{Index: 10, Address: testAddr(1)},
			{Index: 11, Address: testAddr(2)},
			{Index: 12, Address: testAddr(3)},
		},
		SignerPubkey: base58.Encode(signer[ed25519.SeedSize:]),
	}
	if mutate != nil {
		mutate(&m)
	}
	msg, err := SolanaPoolManifestSigningBytes(&m)
	if err != nil {
		t.Fatal(err)
	}
	m.Signature = base58.Encode(ed25519.Sign(signer, msg))
	raw, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSolanaPoolManifestCanonicalBytes(t *testing.T) {
	m := SolanaPoolManifest{Version: 1, Network: SolanaMainnet, PathTemplate: SolanaPoolPathTemplate, Start: 0,
		Entries: []SolanaPoolEntry{{Index: 0, Address: "A"}}, SignerPubkey: "S", Signature: "SIG"}
	got, err := SolanaPoolManifestSigningBytes(&m)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":1,"network":"solana-mainnet","path_template":"m/44'/501'/{i}'/0'","start":0,"entries":[{"index":0,"address":"A"}],"signer_pubkey":"S"}`
	if string(got) != want {
		t.Fatalf("canonical bytes changed (this breaks every signed manifest):\n got %s\nwant %s", got, want)
	}
	if m.Signature != "SIG" {
		t.Fatal("must not mutate the input")
	}
}

func TestVerifySolanaPoolManifest(t *testing.T) {
	signer := testKey(9)
	trusted := base58.Encode(signer[ed25519.SeedSize:])

	good := signedManifest(t, nil, signer)
	m, err := VerifySolanaPoolManifest(good, trusted)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 3 || m.Entries[0].Address != testAddr(1) {
		t.Fatalf("unexpected manifest %+v", m)
	}
	// Formatting of the file does not matter.
	var compact bytes.Buffer
	if err := json.Compact(&compact, good); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySolanaPoolManifest(compact.Bytes(), trusted); err != nil {
		t.Fatalf("compact form must verify: %v", err)
	}

	cases := map[string][]byte{
		"untrusted signer": signedManifest(t, nil, testKey(8)),
		"tampered address": bytes.Replace(good, []byte(testAddr(2)), []byte(testAddr(4)), 1),
		"unknown field":    bytes.Replace(good, []byte(`"version": 1,`), []byte(`"version": 1, "extra": 1,`), 1),
		"no signature": func() []byte {
			var mm SolanaPoolManifest
			_ = json.Unmarshal(good, &mm)
			mm.Signature = ""
			b, _ := json.Marshal(mm)
			return b
		}(),
		"trailing data":     append(append([]byte{}, good...), []byte(`{}`)...),
		"bad version":       signedManifest(t, func(m *SolanaPoolManifest) { m.Version = 2 }, signer),
		"evm network":       signedManifest(t, func(m *SolanaPoolManifest) { m.Network = ArbitrumOne }, signer),
		"unknown network":   signedManifest(t, func(m *SolanaPoolManifest) { m.Network = "solana-testnet" }, signer),
		"bad path template": signedManifest(t, func(m *SolanaPoolManifest) { m.PathTemplate = "m/44'/501'/0'/{i}'" }, signer),
		"empty":             signedManifest(t, func(m *SolanaPoolManifest) { m.Entries = nil }, signer),
		"start mismatch":    signedManifest(t, func(m *SolanaPoolManifest) { m.Start = 9 }, signer),
		"not increasing":    signedManifest(t, func(m *SolanaPoolManifest) { m.Entries[2].Index = 11 }, signer),
		"duplicate address": signedManifest(t, func(m *SolanaPoolManifest) { m.Entries[2].Address = testAddr(1) }, signer),
		"bad address":       signedManifest(t, func(m *SolanaPoolManifest) { m.Entries[1].Address = "notbase58!" }, signer),
		"short address":     signedManifest(t, func(m *SolanaPoolManifest) { m.Entries[1].Address = base58.Encode([]byte{1, 2, 3}) }, signer),
		"signer in pool": signedManifest(t, func(m *SolanaPoolManifest) {
			m.Entries[1].Address = base58.Encode(signer[ed25519.SeedSize:])
		}, signer),
		"hardened overflow": signedManifest(t, func(m *SolanaPoolManifest) {
			m.Start = 0x7fffffff
			m.Entries = []SolanaPoolEntry{{Index: 0x7fffffff, Address: testAddr(1)}, {Index: 0x80000000, Address: testAddr(2)}}
		}, signer),
	}
	for name, raw := range cases {
		if _, err := VerifySolanaPoolManifest(raw, trusted); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	if _, err := VerifySolanaPoolManifest(good, "bogus"); err == nil || !strings.Contains(err.Error(), "trusted") {
		t.Fatalf("bad trusted key must fail clearly: %v", err)
	}
}
