package sweep

import (
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mr-tron/base58"
	"github.com/tyler-smith/go-bip39"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
)

// Public BIP39 test mnemonic. Never use it for real funds.
const testMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

var (
	krOnce sync.Once
	testKR *Keyring
)

func kr(t testing.TB) *Keyring {
	t.Helper()
	krOnce.Do(func() {
		seed, err := bip39.NewSeedWithErrorChecking(testMnemonic, "")
		if err != nil {
			panic(err)
		}
		testKR, err = NewKeyring(seed)
		if err != nil {
			panic(err)
		}
	})
	return testKR
}

func otherKR(t testing.TB) *Keyring {
	t.Helper()
	seed, err := bip39.NewSeedWithErrorChecking("legal winner thank year wave sausage worth useful legal winner thank yellow", "")
	if err != nil {
		t.Fatal(err)
	}
	k, err := NewKeyring(seed)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

var (
	usdcArb = chain.MainnetAssets()[0]
	usdtArb = chain.MainnetAssets()[1]
	usdcSol = chain.MainnetAssets()[2]
	usdtSol = chain.MainnetAssets()[3]
)

func evmAddr(t testing.TB, i uint32) string {
	t.Helper()
	k, err := kr(t).EVMDeposit(i)
	if err != nil {
		t.Fatal(err)
	}
	return k.Address
}

func evmTreasury(t testing.TB) string {
	t.Helper()
	k, err := kr(t).EVMTreasury()
	if err != nil {
		t.Fatal(err)
	}
	return k.Address
}

func solOwner(t testing.TB, i uint32) string {
	t.Helper()
	k, err := kr(t).SolanaPool(i)
	if err != nil {
		t.Fatal(err)
	}
	return k.Address
}

func solTreasury(t testing.TB) string {
	t.Helper()
	k, err := kr(t).SolanaTreasury()
	if err != nil {
		t.Fatal(err)
	}
	return k.Address
}

// evmSweepPlan: sources 0 (USDT 2.7, 0 ETH) and 1 (USDC 5 + USDT 1, 0 ETH).
func evmSweepPlan(t testing.TB) *Plan {
	return &Plan{
		Version: PlanVersion, Kind: KindSweep, Network: chain.ArbitrumOne, CreatedAt: "2026-11-16T00:00:00Z",
		EVM: &EVMPlan{
			ChainID: "42161", Treasury: evmTreasury(t), TreasuryNonce: 7,
			MaxFeePerGas: "20000000", MaxPriorityFeePerGas: "0", TopUpGasLimit: "60000",
			Sources: []EVMSource{
				{Index: 0, Address: evmAddr(t, 0), Nonce: 0, ETHBalance: "0", Tokens: []EVMToken{
					{Symbol: "USDT", Contract: usdtArb.Contract, Amount: "2700000", GasLimit: "150000"},
				}},
				{Index: 1, Address: evmAddr(t, 1), Nonce: 3, ETHBalance: "0", Tokens: []EVMToken{
					{Symbol: "USDC", Contract: usdcArb.Contract, Amount: "5000000", GasLimit: "120000"},
					{Symbol: "USDT", Contract: usdtArb.Contract, Amount: "1000000", GasLimit: "150000"},
				}},
			},
		},
	}
}

func fakeKey(label string) string {
	h := sha256.Sum256([]byte(label))
	return base58.Encode(h[:])
}

var (
	testNonceAcct1 = fakeKey("nonce-account-1")
	testNonceAcct2 = fakeKey("nonce-account-2")
	testNonceVal1  = fakeKey("nonce-value-1")
	testNonceVal2  = fakeKey("nonce-value-2")
)

func solSweepPlan(t testing.TB) *Plan {
	return &Plan{
		Version: PlanVersion, Kind: KindSweep, Network: chain.SolanaMainnet, CreatedAt: "2026-11-16T00:00:00Z",
		Solana: &SolanaPlan{
			Treasury: solTreasury(t), CUPriceMicros: "10000",
			Txs: []SolanaTxPlan{{
				NonceAccount: testNonceAcct1, NonceValue: testNonceVal1, CreateTreasuryATA: []string{"USDT"},
				Transfers: []SolanaTransfer{
					{Index: 0, Owner: solOwner(t, 0), Symbol: "USDT", Mint: usdtSol.Contract, Amount: "4300000"},
					{Index: 1, Owner: solOwner(t, 1), Symbol: "USDT", Mint: usdtSol.Contract, Amount: "1"},
				},
			}},
		},
	}
}

func registered(addrs ...string) func(string) bool {
	return func(a string) bool {
		for _, x := range addrs {
			if x == a {
				return true
			}
		}
		return false
	}
}

func clonePlan(t testing.TB, p *Plan) *Plan {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var q Plan
	if err := json.Unmarshal(b, &q); err != nil {
		t.Fatal(err)
	}
	return &q
}

func newStore(t *testing.T) *LedgerStore {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ledger.json")
	if err := InitLedger(p); err != nil {
		t.Fatal(err)
	}
	return &LedgerStore{Path: p}
}

var t0 = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func bigS(s string) *big.Int {
	v, _ := new(big.Int).SetString(s, 10)
	return v
}
