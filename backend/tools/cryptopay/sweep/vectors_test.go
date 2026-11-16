package sweep

import (
	"encoding/base64"
	"encoding/json"
	"math/big"
	"os"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
)

// testdata/xcheck_expected.json is produced by testdata/xcheck.py with
// independent implementations (eth-account 0.14.0; solders 0.29.0 = Rust
// solana-sdk message compiler + ed25519-dalek), from the same mnemonic and
// semantic inputs. The Go signer must produce byte-identical transactions.
type xcheck struct {
	EVM struct {
		Treasury string `json:"treasury"`
		Source0  string `json:"source0"`
		TopUpRaw string `json:"topup_raw"`
		SweepRaw string `json:"sweep_raw"`
	} `json:"evm"`
	Solana struct {
		Treasury string `json:"treasury"`
		Dest     string `json:"dest"`
		SendRaw  string `json:"send_raw"`
		SweepRaw string `json:"sweep_raw"`
	} `json:"solana"`
}

func loadXcheck(t *testing.T) *xcheck {
	data, err := os.ReadFile("testdata/xcheck_expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var x xcheck
	if err := json.Unmarshal(data, &x); err != nil {
		t.Fatal(err)
	}
	return &x
}

func TestCrossCheckEVMAgainstEthAccount(t *testing.T) {
	x := loadXcheck(t)
	if x.EVM.Treasury != evmTreasury(t) || x.EVM.Source0 != evmAddr(t, 0) {
		t.Fatal("independent key derivation disagrees")
	}
	if evmAddr(t, 0) != "0x9858effd232b4033e47d90003d41ec34ecaeda94" { // well-known abandon...about m/44'/60'/0'/0/0
		t.Fatal("deposit derivation")
	}
	p := evmSweepPlan(t)
	p.EVM.Sources = p.EVM.Sources[:1]
	op := mustBuildEVM(t, p, EVMCapsFor(false))
	st, err := SignEVM(op, kr(t))
	if err != nil {
		t.Fatal(err)
	}
	if st[0].Raw != x.EVM.TopUpRaw {
		t.Fatalf("top-up differs from eth-account:\n go %s\n py %s", st[0].Raw, x.EVM.TopUpRaw)
	}
	if st[1].Raw != x.EVM.SweepRaw {
		t.Fatalf("sweep differs from eth-account:\n go %s\n py %s", st[1].Raw, x.EVM.SweepRaw)
	}
}

func TestCrossCheckSolanaAgainstSolders(t *testing.T) {
	x := loadXcheck(t)
	if x.Solana.Treasury != solTreasury(t) || x.Solana.Dest != solOwner(t, 4000) {
		t.Fatal("independent SLIP-10 derivation disagrees")
	}
	// send
	dest := Destination{Label: "d", Network: chain.SolanaMainnet, Address: x.Solana.Dest}
	p := &Plan{Version: 1, Kind: KindSend, Network: chain.SolanaMainnet, Solana: &SolanaPlan{
		Treasury: solTreasury(t), CUPriceMicros: "1000",
		Send: &SolanaSendPlan{NonceAccount: testNonceAcct1, NonceValue: testNonceVal1, Symbol: "USDT", Mint: usdtSol.Contract, TreasuryBalance: "0"},
	}}
	op, err := BuildSolanaSend(p, kr(t), registered(testNonceAcct1), dest, big.NewInt(4_300_000))
	if err != nil {
		t.Fatal(err)
	}
	st, err := SignSolana(op, kr(t))
	if err != nil {
		t.Fatal(err)
	}
	if st[0].Raw != x.Solana.SendRaw {
		t.Fatalf("send differs from solders:\n go %s\n py %s", st[0].Raw, x.Solana.SendRaw)
	}
	// sweep (2 owners + ATA creation)
	_, st = signSol(t, solSweepPlan(t), registered(testNonceAcct1))
	if st[0].Raw != x.Solana.SweepRaw {
		goRaw, _ := base64.StdEncoding.DecodeString(st[0].Raw)
		pyRaw, _ := base64.StdEncoding.DecodeString(x.Solana.SweepRaw)
		t.Fatalf("sweep differs from solders (%d vs %d bytes)", len(goRaw), len(pyRaw))
	}
}
