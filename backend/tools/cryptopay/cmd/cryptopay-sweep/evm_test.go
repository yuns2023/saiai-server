package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/tyler-smith/go-bip39"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/Wei-Shaw/sub2api/tools/cryptopay/sweep"
)

const abandon = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

func testKR(t *testing.T) *sweep.Keyring {
	seed, _ := bip39.NewSeedWithErrorChecking(abandon, "")
	k, err := sweep.NewKeyring(seed)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// fakeEVM is a tiny in-memory Arbitrum: it executes signed EIP-1559 txs
// (ETH transfers and ERC-20 transfer calls) and serves receipts.
type fakeEVM struct {
	mu          sync.Mutex
	block       uint64
	eth         map[string]*big.Int
	tok         map[string]map[string]*big.Int // contract -> owner -> units
	nonce       map[string]uint64
	receipts    map[string]map[string]any
	sends       int
	noOverride  bool
	revertNext  bool
	finalizeLag uint64
}

func newFakeEVM() *fakeEVM {
	return &fakeEVM{block: 1000, eth: map[string]*big.Int{}, tok: map[string]map[string]*big.Int{}, nonce: map[string]uint64{}, receipts: map[string]map[string]any{}}
}

func (f *fakeEVM) bal(m map[string]*big.Int, a string) *big.Int {
	if m[a] == nil {
		m[a] = new(big.Int)
	}
	return m[a]
}

func (f *fakeEVM) setTok(contract, owner string, v int64) {
	if f.tok[contract] == nil {
		f.tok[contract] = map[string]*big.Int{}
	}
	f.tok[contract][owner] = big.NewInt(v)
}

func (f *fakeEVM) handle(method string, p []json.RawMessage) (any, *rpcError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	str := func(i int) string { var s string; _ = json.Unmarshal(p[i], &s); return strings.ToLower(s) }
	switch method {
	case "eth_chainId":
		return "0xa4b1", nil
	case "eth_getTransactionCount":
		return hexutil.EncodeUint64(f.nonce[str(0)]), nil
	case "eth_getBalance":
		return hexutil.EncodeBig(f.bal(f.eth, str(0))), nil
	case "eth_call":
		var c map[string]string
		_ = json.Unmarshal(p[0], &c)
		owner := "0x" + strings.ToLower(c["data"][len(c["data"])-40:])
		v := new(big.Int)
		if m := f.tok[strings.ToLower(c["to"])]; m != nil && m[owner] != nil {
			v = m[owner]
		}
		return hexutil.Encode(common.LeftPadBytes(v.Bytes(), 32)), nil
	case "eth_getBlockByNumber":
		n := f.block
		if str(0) == "finalized" {
			n -= f.finalizeLag
		}
		return map[string]string{"number": hexutil.EncodeUint64(n), "hash": fmt.Sprintf("0x%064x", n), "baseFeePerGas": "0x989680"}, nil
	case "eth_estimateGas":
		if len(p) < 3 || f.noOverride {
			return nil, &rpcError{Code: -32602, Message: "too many arguments"}
		}
		var c map[string]string
		_ = json.Unmarshal(p[0], &c)
		if c["data"] != "" {
			return "0x186a0", nil // 100000
		}
		return "0x7530", nil // 30000 (L2 + L1 component)
	case "eth_sendRawTransaction":
		f.sends++
		raw, _ := hexutil.Decode(str(0))
		d, err := sweep.DecodeEVMSigned(raw)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: "invalid tx"}
		}
		if _, ok := f.receipts[strings.ToLower(d.Hash)]; ok {
			return nil, &rpcError{Code: -32000, Message: "already known"}
		}
		if d.Nonce != f.nonce[d.From] {
			return nil, &rpcError{Code: -32000, Message: "nonce too low"}
		}
		fee := new(big.Int).Mul(big.NewInt(50_000), big.NewInt(10_000_000))
		need := new(big.Int).Add(fee, d.Value)
		if f.bal(f.eth, d.From).Cmp(need) < 0 {
			return nil, &rpcError{Code: -32000, Message: "insufficient funds for gas * price + value"}
		}
		f.nonce[d.From]++
		f.block++
		f.bal(f.eth, d.From).Sub(f.eth[d.From], need)
		f.bal(f.eth, d.To).Add(f.eth[d.To], d.Value)
		status := "0x1"
		var logs []map[string]any
		if f.revertNext {
			status, f.revertNext = "0x0", false
		} else if d.IsTransfer {
			src := f.tok[d.To][d.From]
			if src == nil || src.Cmp(d.Amount) < 0 {
				status = "0x0"
			} else {
				src.Sub(src, d.Amount)
				f.bal(f.tok[d.To], d.Recipient).Add(f.tok[d.To][d.Recipient], d.Amount)
				logs = append(logs, map[string]any{"address": d.To, "topics": []string{chain.TransferTopic0,
					"0x" + strings.Repeat("0", 24) + d.From[2:], "0x" + strings.Repeat("0", 24) + d.Recipient[2:]},
					"data": hexutil.Encode(common.LeftPadBytes(d.Amount.Bytes(), 32))})
			}
		}
		f.receipts[strings.ToLower(d.Hash)] = map[string]any{"status": status, "blockNumber": hexutil.EncodeUint64(f.block),
			"blockHash": fmt.Sprintf("0x%064x", f.block), "transactionHash": d.Hash, "logs": logs}
		return d.Hash, nil
	case "eth_getTransactionReceipt":
		r, ok := f.receipts[str(0)]
		if !ok {
			return nil, nil
		}
		return r, nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found"}
}

// serveRPC exposes a handler as JSON-RPC; the URL carries a fake API key so
// tests can check it never leaks.
func serveRPC(t *testing.T, h func(string, []json.RawMessage) (any, *rpcError)) *rpcClient {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		res, e := h(req.Method, req.Params)
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if e != nil {
			resp["error"] = e
		} else {
			resp["result"] = res
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	c, err := newRPC(srv.URL + "/v2/SECRETKEY123456?api-key=" + fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func fastOpts() bcastOpts {
	return bcastOpts{log: io.Discard, poll: time.Millisecond, receiptTimeout: time.Second, finalityTimeout: time.Second}
}

func TestEVMPrepareSignBroadcastEndToEnd(t *testing.T) {
	kr := testKR(t)
	tr, _ := kr.EVMTreasury()
	s0, _ := kr.EVMDeposit(0)
	s1, _ := kr.EVMDeposit(1)
	s2, _ := kr.EVMDeposit(2)
	usdc, usdt := chain.MainnetAssets()[0], chain.MainnetAssets()[1]
	f := newFakeEVM()
	f.eth[tr.Address] = big.NewInt(1e15) // 0.001 ETH
	f.setTok(usdt.Contract, s0.Address, 2_700_000)
	f.setTok(usdc.Contract, s1.Address, 5_000_000)
	f.setTok(usdt.Contract, s1.Address, 1)
	rpc := evmRPC{serveRPC(t, func(m string, p []json.RawMessage) (any, *rpcError) { return f.handle(m, p) })}
	ctx := context.Background()
	plan, err := prepareEVMSweep(ctx, rpc, tr.Address, []sourceArg{{0, s0.Address}, {1, s1.Address}, {2, s2.Address}}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.EVM.Sources) != 2 || plan.EVM.MaxFeePerGas != "20000000" || plan.EVM.TopUpGasLimit != "45000" || plan.EVM.Sources[0].Tokens[0].GasLimit != "150000" {
		t.Fatalf("plan %+v", plan.EVM)
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
	b := &sweep.SignedBundle{Version: 1, OpID: "arb-test", Network: chain.ArbitrumOne, Kind: sweep.KindSweep, Treasury: tr.Address, Txs: st}
	res := &result{path: t.TempDir() + "/r.json"}
	if err := broadcastEVM(ctx, rpc, b, res, fastOpts()); err != nil {
		t.Fatal(err)
	}
	for i, x := range res.Txs {
		if !x.Submitted || x.Status != "executed" || !x.Finalized {
			t.Fatalf("tx %d %+v", i, x)
		}
	}
	if f.tok[usdt.Contract][tr.Address].Int64() != 2_700_001 || f.tok[usdc.Contract][tr.Address].Int64() != 5_000_000 {
		t.Fatal("treasury did not receive the tokens")
	}
	if f.sends != 5 {
		t.Fatalf("sends %d", f.sends)
	}
	// Re-running is idempotent: nothing is sent again.
	res2 := &result{path: t.TempDir() + "/r.json"}
	if err := broadcastEVM(ctx, rpc, b, res2, fastOpts()); err != nil || f.sends != 5 {
		t.Fatalf("rerun: %v sends=%d", err, f.sends)
	}
	// A stale bundle (nonce consumed by a different tx) stops without sending.
	stale := *b
	stale.Txs = append([]sweep.SignedTx(nil), b.Txs...)
	op2 := *op
	op2.Intents = append([]sweep.EVMIntent(nil), op.Intents...)
	op2.Intents[0].MaxFee = big.NewInt(30_000_000) // same nonce, different tx
	st2, err := sweep.SignEVM(&op2, kr)
	if err != nil {
		t.Fatal(err)
	}
	stale.Txs[0] = st2[0]
	err = broadcastEVM(ctx, rpc, &stale, &result{path: t.TempDir() + "/r.json"}, fastOpts())
	if err == nil || !strings.Contains(err.Error(), "consumed by a different transaction") || f.sends != 5 {
		t.Fatalf("stale: %v sends=%d", err, f.sends)
	}
}

func TestEVMBroadcastStopsOnRevertAndMissingTopUp(t *testing.T) {
	kr := testKR(t)
	tr, _ := kr.EVMTreasury()
	s0, _ := kr.EVMDeposit(0)
	usdt := chain.MainnetAssets()[1]
	f := newFakeEVM()
	f.eth[tr.Address] = big.NewInt(1e15)
	f.setTok(usdt.Contract, s0.Address, 2_700_000)
	rpc := evmRPC{serveRPC(t, func(m string, p []json.RawMessage) (any, *rpcError) { return f.handle(m, p) })}
	ctx := context.Background()
	plan, err := prepareEVMSweep(ctx, rpc, tr.Address, []sourceArg{{0, s0.Address}}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	op, _ := sweep.BuildEVMSweep(plan, kr, sweep.EVMCapsFor(false))
	st, _ := sweep.SignEVM(op, kr)
	// Only B (no top-up): source has 0 ETH -> stop before sending.
	b := &sweep.SignedBundle{Version: 1, Network: chain.ArbitrumOne, Txs: st[1:]}
	err = broadcastEVM(ctx, rpc, b, &result{path: t.TempDir() + "/r.json"}, fastOpts())
	if err == nil || !strings.Contains(err.Error(), "top-up missing") || f.sends != 0 {
		t.Fatalf("missing top-up: %v sends=%d", err, f.sends)
	}
	// Top-up reverts -> stop, B never sent.
	f.revertNext = true
	b.Txs = st
	err = broadcastEVM(ctx, rpc, b, &result{path: t.TempDir() + "/r.json"}, fastOpts())
	if err == nil || !strings.Contains(err.Error(), "status is not 1") || f.sends != 1 {
		t.Fatalf("revert: %v sends=%d", err, f.sends)
	}
}

func TestEVMPrepareFailures(t *testing.T) {
	kr := testKR(t)
	tr, _ := kr.EVMTreasury()
	s0, _ := kr.EVMDeposit(0)
	usdt := chain.MainnetAssets()[1]
	f := newFakeEVM()
	f.setTok(usdt.Contract, s0.Address, 2_700_000)
	rpc := evmRPC{serveRPC(t, func(m string, p []json.RawMessage) (any, *rpcError) { return f.handle(m, p) })}
	ctx := context.Background()
	if _, err := prepareEVMSweep(ctx, rpc, tr.Address, []sourceArg{{0, s0.Address}}, io.Discard); err == nil || !strings.Contains(err.Error(), "treasury ETH") {
		t.Fatalf("unfunded treasury: %v", err)
	}
	f.eth[tr.Address] = big.NewInt(1e15)
	f.noOverride = true
	_, err := prepareEVMSweep(ctx, rpc, tr.Address, []sourceArg{{0, s0.Address}}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "state override") {
		t.Fatalf("no override support: %v", err)
	}
	if strings.Contains(err.Error(), "SECRETKEY") {
		t.Fatal("RPC key leaked into an error")
	}
}

func TestRPCSecretsNeverPrinted(t *testing.T) {
	c, err := newRPC("https://mainnet.helius-rpc.com/?api-key=" + fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.scrub("dial https://mainnet.helius-rpc.com/?api-key=" + fakeKey + ": x SECRETKEY123456"); strings.Contains(got, "SECRET") {
		t.Fatalf("scrub: %s", got)
	}
	// Transport errors include the URL in Go's net/http messages: must be scrubbed.
	c2, _ := newRPC("http://127.0.0.1:1/v2/SECRETKEY123456?api-key=" + fakeKey)
	err = c2.call(context.Background(), "eth_chainId", []any{}, nil)
	if err == nil || strings.Contains(err.Error(), "SECRETKEY") {
		t.Fatalf("transport error leaks: %v", err)
	}
	if _, err := newRPC("not a url SECRETKEY123456"); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("bad url error leaks: %v", err)
	}
}

func TestScrubAndBroadcastLock(t *testing.T) {
	if _, err := newRPC("https://rpc.example/v2/PATHSECRET99?k=QS1"); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	_, _ = scrubWriter{&sb}.Write([]byte("invalid key PATHSECRET99"))
	if strings.Contains(sb.String(), "PATHSECRET99") {
		t.Fatalf("path secret leaked: %s", sb.String())
	}
	// Short, common values are not treated as secrets (no mangled output).
	if _, err := newRPC("https://rpc.example/rpc?feature=true"); err != nil {
		t.Fatal(err)
	}
	if got := scrubAll(`{"submitted": true, "network": "solana-mainnet"}`); got != `{"submitted": true, "network": "solana-mainnet"}` {
		t.Fatalf("scrub mangled output: %s", got)
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	kr := testKR(t)
	tr, _ := kr.EVMTreasury()
	dest := sweep.Destination{Label: "d", Network: chain.ArbitrumOne, Address: "0x1111111111111111111111111111111111111111"}
	p := &sweep.Plan{Version: 1, Kind: sweep.KindSend, Network: chain.ArbitrumOne, EVM: &sweep.EVMPlan{
		ChainID: "42161", Treasury: tr.Address, TreasuryNonce: 1, MaxFeePerGas: "20000000", MaxPriorityFeePerGas: "0",
		Send: &sweep.EVMSendPlan{Symbol: "USDT", Contract: chain.MainnetAssets()[1].Contract, GasLimit: "100000", TreasuryBalance: "1"}}}
	op, err := sweep.BuildEVMSend(p, kr, sweep.EVMCapsFor(false), dest, big.NewInt(1))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := sweep.SignEVM(op, kr)
	b := &sweep.SignedBundle{Version: 1, Network: chain.ArbitrumOne, Txs: st}
	unlock, err := lockBroadcast(b)
	if err != nil {
		t.Fatal(err)
	}
	// A different bundle (other file, same treasury) is blocked.
	if _, err := lockBroadcast(&sweep.SignedBundle{Version: 1, Network: chain.ArbitrumOne, Txs: st}); err == nil {
		t.Fatal("parallel broadcast for the same treasury allowed")
	}
	unlock()
	u2, err := lockBroadcast(b)
	if err != nil {
		t.Fatal(err)
	}
	u2()
}

// fakeKey is a test-only placeholder, split so secret scanners do not flag it.
const fakeKey = "SECRETKEY" + "123456"
