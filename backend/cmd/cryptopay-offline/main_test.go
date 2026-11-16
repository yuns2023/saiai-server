package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyler-smith/go-bip39"
	"github.com/tyler-smith/go-bip39/wordlists"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/Wei-Shaw/sub2api/internal/cryptopay/hdkey"
)

const abandonAbout = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

// SHA-256 of https://raw.githubusercontent.com/bitcoin/bips/master/bip-0039/english.txt
// (fetched 2026-11-16, 2048 lines, LF endings).
const bip39EnglishSHA256 = "2f5eed53a4727b4bf8880d8f3f199efc90e58503646d9ff8eff3a2ed3b24dbda"

func TestBIP39WordlistMatchesBitcoinBips(t *testing.T) {
	words := wordlists.English
	if len(words) != 2048 {
		t.Fatalf("wordlist has %d words", len(words))
	}
	sum := sha256.Sum256([]byte(strings.Join(words, "\n") + "\n"))
	if got := hex.EncodeToString(sum[:]); got != bip39EnglishSHA256 {
		t.Fatalf("go-bip39 English wordlist sha256 %s != bitcoin/bips %s", got, bip39EnglishSHA256)
	}
}

// testdata/bip39_trezor_english.json is the "english" array of
// https://github.com/trezor/python-mnemonic/blob/master/vectors.json
// (fetched 2026-11-16; file sha256 fa3b937b...30f8). Passphrase "TREZOR".
func TestBIP39TrezorVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/bip39_trezor_english.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors [][4]string
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 24 {
		t.Fatalf("expected 24 vectors, got %d", len(vectors))
	}
	for _, v := range vectors {
		entropy, _ := hex.DecodeString(v[0])
		mn, err := bip39.NewMnemonic(entropy)
		if err != nil {
			t.Fatal(err)
		}
		if mn != v[1] {
			t.Errorf("entropy %s: mnemonic %q want %q", v[0], mn, v[1])
		}
		seed, err := bip39.NewSeedWithErrorChecking(v[1], "TREZOR")
		if err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(seed); got != v[2] {
			t.Errorf("seed mismatch for %q", v[1])
		}
		master, err := hdkey.NewMasterKey(seed)
		if err != nil {
			t.Fatal(err)
		}
		if got := master.String(); got != v[3] {
			t.Errorf("master xprv %s want %s", got, v[3])
		}
	}
	bad := strings.Replace(abandonAbout, "about", "abandon", 1)
	if _, err := seedFromMnemonic(bad); err == nil {
		t.Fatal("bad checksum must be rejected")
	}
}

// Expected addresses for the "abandon ... about" mnemonic (no passphrase) were
// derived independently with a separate Python implementation (hashlib PBKDF2,
// python-ecdsa secp256k1, pycryptodome keccak, pyca/cryptography ed25519,
// hand-written BIP32/SLIP-10) on 2026-11-16. Index 0 also matches the widely
// published MetaMask (0x9858EfFD...) and Phantom (HAgk14...) addresses.
var abandonEVM = []string{
	"0x9858effd232b4033e47d90003d41ec34ecaeda94",
	"0x6fac4d18c912343bf86fa7049364dd4e424ab9c0",
	"0xb6716976a3ebe8d39aceb04372f22ff8e6802d7a",
}

var abandonSolana = []string{
	"HAgk14JpMQLgt6rVgv7cBQFJWFto5Dqxi472uT3DKpqk",
	"Hh8QwFUA6MtVu1qAoq12ucvFHNwCcVTV7hpWjeY1Hztb",
	"7WktogJEd2wQ9eH2oWusmcoFTgeYi6rS632UviTBJ2jm",
}

func TestAbandonMnemonicAddresses(t *testing.T) {
	seed, err := seedFromMnemonic("  " + strings.ToUpper(abandonAbout) + "\n")
	if err != nil {
		t.Fatal(err)
	}
	xpub, addrs, err := evmAccountXpub(seed, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := range abandonEVM {
		if addrs[i] != abandonEVM[i] {
			t.Errorf("evm %d: %s want %s", i, addrs[i], abandonEVM[i])
		}
		// Server path: chain.EVMAddressFromXpub on the exported xpub.
		got, err := chain.EVMAddressFromXpub(xpub, uint32(i))
		if err != nil || got != abandonEVM[i] {
			t.Errorf("server derivation %d: %s %v", i, got, err)
		}
	}
	for i := range abandonSolana {
		got, err := solanaAddress(seed, uint32(i))
		if err != nil || got != abandonSolana[i] {
			t.Errorf("solana %d: %s %v want %s", i, got, err, abandonSolana[i])
		}
	}
}

func TestSolanaPoolManifestRoundTrip(t *testing.T) {
	seed, err := seedFromMnemonic(abandonAbout)
	if err != nil {
		t.Fatal(err)
	}
	raw, signer, err := buildSolanaPool(seed, chain.SolanaMainnet, 0, 3, DefaultSignerIndex)
	if err != nil {
		t.Fatal(err)
	}
	m, err := chain.VerifySolanaPoolManifest(raw, signer)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range m.Entries {
		if e.Address != abandonSolana[i] || e.Index != uint32(i) {
			t.Errorf("entry %d = %+v", i, e)
		}
	}
	signerAddr, _ := solanaAddress(seed, DefaultSignerIndex)
	if signer != signerAddr {
		t.Fatalf("signer %s != m/44'/501'/K'/0' %s", signer, signerAddr)
	}
	if _, _, err := buildSolanaPool(seed, chain.SolanaMainnet, 2147482999, 5, DefaultSignerIndex); err == nil {
		t.Fatal("signer inside pool range must be rejected")
	}
}

func TestCLIEndToEnd(t *testing.T) {
	dir := t.TempDir()
	mf := filepath.Join(dir, "m.txt")
	var out, errb bytes.Buffer
	if err := run([]string{"gen-mnemonic", "--out", mf}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(mf)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mnemonic file mode %v", st.Mode().Perm())
	}
	data, _ := os.ReadFile(mf)
	if n := len(strings.Fields(string(data))); n != 24 || !bip39.IsMnemonicValid(strings.TrimSpace(string(data))) {
		t.Fatalf("generated mnemonic invalid (%d words)", n)
	}
	if strings.Contains(out.String(), strings.Fields(string(data))[0]+" ") {
		t.Fatal("--out must not echo the mnemonic")
	}
	if err := run([]string{"gen-mnemonic", "--out", mf}, &out, &errb); err == nil {
		t.Fatal("must refuse to overwrite an existing mnemonic file")
	}
	if err := run([]string{"gen-mnemonic"}, &out, &errb); err == nil {
		t.Fatal("gen-mnemonic without --out/--print must fail")
	}

	// Fixed mnemonic for deterministic output checks.
	fixed := filepath.Join(dir, "abandon.txt")
	if err := os.WriteFile(fixed, []byte(abandonAbout+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"evm-xpub", "--mnemonic-file", fixed}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "xpub6") || !strings.Contains(out.String(), abandonEVM[2]) {
		t.Fatalf("evm-xpub output: %s", out.String())
	}
	out.Reset()
	if err := run([]string{"verify-solana", "--mnemonic-file", fixed, "--index", "1"}, &out, &errb); err != nil || !strings.Contains(out.String(), abandonSolana[1]) {
		t.Fatalf("verify-solana: %v %s", err, out.String())
	}
	out.Reset()
	if err := run([]string{"verify-evm", "--mnemonic-file", fixed, "--index", "2"}, &out, &errb); err != nil || !strings.Contains(out.String(), abandonEVM[2]) {
		t.Fatalf("verify-evm: %v %s", err, out.String())
	}
	manifest := filepath.Join(dir, "pool.json")
	out.Reset()
	if err := run([]string{"solana-pool", "--mnemonic-file", fixed, "--start", "0", "--count", "3", "--out", manifest}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(manifest)
	signerOut := &bytes.Buffer{}
	if err := run([]string{"signer-pubkey", "--mnemonic-file", fixed}, signerOut, &errb); err != nil {
		t.Fatal(err)
	}
	signer := strings.Fields(signerOut.String())[1]
	if _, err := chain.VerifySolanaPoolManifest(raw, signer); err != nil {
		t.Fatalf("manifest from CLI does not verify: %v", err)
	}
	if strings.Contains(string(raw), "abandon") {
		t.Fatal("manifest leaks mnemonic")
	}
}
