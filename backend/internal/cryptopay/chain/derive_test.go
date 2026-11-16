package chain

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/tyler-smith/go-bip39"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/hdkey"
)

func TestTransferTopic0(t *testing.T) {
	got := "0x" + hex.EncodeToString(Keccak256([]byte("Transfer(address,address,uint256)")))
	if got != TransferTopic0 {
		t.Fatalf("keccak256(Transfer(...)) = %s, const %s", got, TransferTopic0)
	}
}

func TestMainnetAssetsShape(t *testing.T) {
	as := MainnetAssets()
	if len(as) != 4 {
		t.Fatalf("want 4 assets, got %d", len(as))
	}
	for _, a := range as {
		switch a.Network.Family() {
		case FamilyEVM:
			if n, err := NormalizeEVMAddress(a.Contract); err != nil || n != a.Contract {
				t.Errorf("%s contract %s not lowercase 0x", a.Symbol, a.Contract)
			}
		case FamilySolana:
			if _, err := DecodeSolanaPubkey(a.Contract); err != nil {
				t.Error(err)
			}
			if a.TokenProgram != SolanaTokenProgram {
				t.Errorf("%s token program %s", a.Symbol, a.TokenProgram)
			}
		default:
			t.Errorf("unknown family for %s", a.Network)
		}
		if a.Decimals != 6 {
			t.Errorf("%s decimals %d", a.Symbol, a.Decimals)
		}
	}
}

// abandonAccountXpub derives the m/44'/60'/0' xpub of the "abandon ... about"
// test mnemonic (empty passphrase).
func abandonAccountXpub(t *testing.T) string {
	t.Helper()
	seed, err := bip39.NewSeedWithErrorChecking("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about", "")
	if err != nil {
		t.Fatal(err)
	}
	master, err := hdkey.NewMasterKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	acct, err := master.DerivePath(EVMAccountPath)
	if err != nil {
		t.Fatal(err)
	}
	return acct.Neuter().String()
}

func TestEVMAddressFromXpub(t *testing.T) {
	xpub := abandonAccountXpub(t)
	// Independently derived in Python (see tools/cryptopay/cmd/cryptopay-offline tests) and
	// the widely published MetaMask address 0x9858EfFD232B4033E47d90003D41EC34EcaEda94.
	want := []string{
		"0x9858effd232b4033e47d90003d41ec34ecaeda94",
		"0x6fac4d18c912343bf86fa7049364dd4e424ab9c0",
		"0xb6716976a3ebe8d39aceb04372f22ff8e6802d7a",
	}
	for i, w := range want {
		got, err := EVMAddressFromXpub(xpub, uint32(i))
		if err != nil {
			t.Fatal(err)
		}
		if got != w {
			t.Errorf("index %d: %s want %s", i, got, w)
		}
	}
	if _, err := EVMAddressFromXpub(xpub, hdkey.HardenedOffset); err == nil {
		t.Fatal("hardened index must fail")
	}
	// Wrong level: the BIP32 vector-1 master xpub (depth 0) must be refused.
	if _, err := EVMAddressFromXpub("xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8", 0); err == nil {
		t.Fatal("depth-0 xpub must be refused")
	}
	// A private key must be refused.
	if _, err := EVMAddressFromXpub("xprv9s21ZrQH143K3QTDL4LXw2F7HEK3wJUD2nW2nRk4stbPy6cq3jPPqjiChkVvvNKmPGJxWUtg6LnF5kejMRNNU3TGtRBeJgk33yuGBxrMPHi", 0); err == nil || !strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("xprv must be refused, got %v", err)
	}
}

// Real mainnet ATAs, observed in finalized transactions on 2026-11-16
// (getTransaction jsonParsed: accountKeys[accountIndex] with owner/mint/programId
// from postTokenBalances), recomputed independently in Python, and for
// A7hAgC... confirmed via getTokenAccountsByOwner(owner, {mint: USDC}).
func TestSolanaATAMainnetVectors(t *testing.T) {
	const (
		usdc = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
		usdt = "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"
	)
	cases := []struct{ owner, mint, ata string }{
		{"LASTvjDWkbXM1RwUCiniHqGLSEH5xJinDRs56wNPQr9", usdc, "2msMyhqmWkS866cU6NMiuyWiRLBfYxqq92cGL9GtRNMG"},  // bump 255
		{"A7hAgCzFw14fejgCp387JUJRMNyz4j89JKnhtKU8piqW", usdc, "fewxWzSMHpHhDT9c5FysEXnHXtxvWVeHPvFVTyZdPwh"},  // bump 253
		{"5YxQFdt3Tr9zJLvkFccqXVUwhdTWJQc1fFg2YPbxvxeD", usdc, "6oCkp6gpyjxVTeL6ahMYcekN2x2pzt1KY8g2LqemaTNE"}, // bump 254
		{"297YyGytdAq2sDHh5gkpmeapbHEgbEBMfXUeAiUNGktV", usdt, "3w54rL4kYoGbj6rRmy6AYWwBkN97EtPXrp5c1JF63q6H"}, // bump 252
	}
	for _, c := range cases {
		got, err := SolanaATA(c.owner, c.mint, SolanaTokenProgram)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.ata {
			t.Errorf("ATA(%s,%s) = %s want %s", c.owner, c.mint, got, c.ata)
		}
	}
	for _, bad := range []string{"", "0OIl", "11111111111111111111111111111111111111111111111", "3yZe7d"} {
		if _, err := SolanaATA(bad, usdc, SolanaTokenProgram); err == nil {
			t.Errorf("owner %q must be rejected", bad)
		}
	}
}

func TestIsOnEd25519Curve(t *testing.T) {
	// A real ed25519 public key is on the curve; a known ATA (PDA) is not.
	pk, _ := DecodeSolanaPubkey("HAgk14JpMQLgt6rVgv7cBQFJWFto5Dqxi472uT3DKpqk")
	if !IsOnEd25519Curve(pk) {
		t.Fatal("wallet pubkey must be on curve")
	}
	pda, _ := DecodeSolanaPubkey("2msMyhqmWkS866cU6NMiuyWiRLBfYxqq92cGL9GtRNMG")
	if IsOnEd25519Curve(pda) {
		t.Fatal("PDA must be off curve")
	}
}

func TestNormalizeEVMAddress(t *testing.T) {
	got, err := NormalizeEVMAddress("0xAF88d065e77c8cC2239327C5EDb3A432268e5831")
	if err != nil || got != "0xaf88d065e77c8cc2239327c5edb3a432268e5831" {
		t.Fatalf("%s %v", got, err)
	}
	for _, bad := range []string{"", "af88d065e77c8cc2239327c5edb3a432268e5831", "0x123", "0xzz88d065e77c8cc2239327c5edb3a432268e5831"} {
		if _, err := NormalizeEVMAddress(bad); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
}
