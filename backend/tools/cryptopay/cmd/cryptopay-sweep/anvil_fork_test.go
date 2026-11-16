package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/tyler-smith/go-bip39"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/Wei-Shaw/sub2api/tools/cryptopay/sweep"
)

// TestAnvilArbitrumForkSweep runs prepare -> offline sign -> broadcast A->B
// against a local anvil fork of Arbitrum One for both USDC and USDT0 (real
// token contract code). Opt-in:
//
//	CRYPTOPAY_ANVIL=/path/to/anvil CRYPTOPAY_ARB_FORK_RPC=https://arb1.arbitrum.io/rpc \
//	  go test ./cmd/cryptopay-sweep -run AnvilArbitrumFork -v
//
// Nothing is broadcast to mainnet: every transaction goes to the local fork.
func TestAnvilArbitrumForkSweep(t *testing.T) {
	anvil, forkURL := os.Getenv("CRYPTOPAY_ANVIL"), os.Getenv("CRYPTOPAY_ARB_FORK_RPC")
	if anvil == "" || forkURL == "" {
		t.Skip("set CRYPTOPAY_ANVIL and CRYPTOPAY_ARB_FORK_RPC to run the Arbitrum fork test")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	cmd := exec.Command(anvil, "--fork-url", forkURL, "--port", fmt.Sprint(port), "--silent", "--no-rate-limit")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	c, err := newRPC(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	rpc := evmRPC{c}
	ctx := context.Background()
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := rpc.chainID(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("anvil did not start")
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err := checkEVMChain(ctx, rpc); err != nil {
		t.Fatal(err)
	}
	// A fresh random mnemonic: the public "abandon ... about" addresses carry
	// real mainnet history (EIP-7702 delegations that forward incoming ETH).
	entropy, _ := bip39.NewEntropy(256)
	mn, _ := bip39.NewMnemonic(entropy)
	seed, _ := bip39.NewSeedWithErrorChecking(mn, "")
	kr, err := sweep.NewKeyring(seed)
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := kr.EVMTreasury()
	s0, _ := kr.EVMDeposit(0)
	s1, _ := kr.EVMDeposit(1)
	usdc, usdt := chain.MainnetAssets()[0], chain.MainnetAssets()[1]
	if err := c.call(ctx, "anvil_setBalance", []any{tr.Address, "0x2386f26fc10000"}, nil); err != nil { // 0.01 ETH
		t.Fatal(err)
	}
	// Fund the test sources like forge's deal(): find the balance mapping
	// slot by writing a candidate slot and checking balanceOf (restoring the
	// slot when it is not the one).
	fund := func(asset chain.Asset, to string, amount int64) {
		want := common.LeftPadBytes(big.NewInt(amount).Bytes(), 32)
		for slot := int64(0); slot < 64; slot++ {
			key := crypto.Keccak256(common.LeftPadBytes(common.HexToAddress(to).Bytes(), 32), common.LeftPadBytes(big.NewInt(slot).Bytes(), 32))
			var old string
			if err := c.call(ctx, "eth_getStorageAt", []any{asset.Contract, hexutil.Encode(key), "latest"}, &old); err != nil {
				t.Fatal(err)
			}
			if err := c.call(ctx, "anvil_setStorageAt", []any{asset.Contract, hexutil.Encode(key), hexutil.Encode(want)}, nil); err != nil {
				t.Fatal(err)
			}
			if b, err := rpc.tokenBalance(ctx, asset.Contract, to); err == nil && b.Int64() == amount {
				return
			}
			_ = c.call(ctx, "anvil_setStorageAt", []any{asset.Contract, hexutil.Encode(key), old}, nil)
		}
		t.Fatalf("balance slot of %s not found", asset.Symbol)
	}
	fund(usdt, s0.Address, 2_700_000)
	fund(usdc, s1.Address, 5_000_000)
	fund(usdt, s1.Address, 1_000_000)
	before := map[string]*big.Int{}
	for _, a := range []chain.Asset{usdc, usdt} {
		before[a.Symbol], _ = rpc.tokenBalance(ctx, a.Contract, tr.Address)
	}

	plan, err := prepareEVMSweep(ctx, rpc, tr.Address, []sourceArg{{0, s0.Address}, {1, s1.Address}}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(plan)
	if _, err := sweep.DecodePlan(data); err != nil {
		t.Fatal(err)
	}
	op, err := sweep.BuildEVMSweep(plan, kr, sweep.EVMCapsFor(false))
	if err != nil {
		t.Fatal(err)
	}
	st, err := sweep.SignEVM(op, kr)
	if err != nil {
		t.Fatal(err)
	}
	b := &sweep.SignedBundle{Version: 1, Network: chain.ArbitrumOne, Kind: sweep.KindSweep, Txs: st}
	res := &result{path: t.TempDir() + "/r.json"}
	opts := bcastOpts{log: io.Discard, poll: 200 * time.Millisecond, receiptTimeout: 60 * time.Second}
	if err := broadcastEVM(ctx, rpc, b, res, opts); err != nil {
		bal, _ := rpc.balance(ctx, s0.Address)
		r, _ := rpc.receipt(ctx, st[0].Hash)
		var tx map[string]any
		_ = c.call(ctx, "eth_getTransactionByHash", []any{st[0].Hash}, &tx)
		var blk map[string]any
		_ = c.call(ctx, "eth_getBlockByNumber", []any{"latest", false}, &blk)
		t.Logf("debug: bal=%s receipt=%+v tx=%v latest=%v %v", bal, r, tx, blk["number"], blk["transactions"])
		t.Fatal(err)
	}
	for i, x := range res.Txs {
		if x.Status != "executed" {
			t.Fatalf("tx %d %+v", i, x)
		}
	}
	for _, x := range []struct {
		a    chain.Asset
		want int64
	}{{usdc, 5_000_000}, {usdt, 3_700_000}} {
		after, _ := rpc.tokenBalance(ctx, x.a.Contract, tr.Address)
		if new(big.Int).Sub(after, before[x.a.Symbol]).Int64() != x.want {
			t.Fatalf("%s: treasury +%s, want %d", x.a.Symbol, new(big.Int).Sub(after, before[x.a.Symbol]), x.want)
		}
	}
	// Re-broadcast is a no-op; the signed raws were never altered.
	if err := broadcastEVM(ctx, rpc, b, &result{path: t.TempDir() + "/r.json"}, opts); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(common.HexToAddress(tr.Address).Hex(), sweep.DisplayAddress(chain.ArbitrumOne, tr.Address)) {
		t.Fatal("display")
	}
}
