package chain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"testing"
)

const (
	tUSDC  = "0xaf88d065e77c8cc2239327c5edb3a432268e5831"
	tUSDT  = "0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9"
	tFake  = "0x1111111111111111111111111111111111111111"
	tAlice = "0xaaaa00000000000000000000000000000000aaaa"
	tBob   = "0xbbbb00000000000000000000000000000000bbbb"
	tEve   = "0xeeee00000000000000000000000000000000eeee"
	tPayer = "0x9999000000000000000000000000000000009999"
)

type fakeLog struct {
	block    uint64
	contract string
	from, to string
	units    int64
	logIndex uint64
	tx       string
	removed  bool
	topic0   string
}

func hexQ(n uint64) string { return "0x" + strconv.FormatUint(n, 16) }

func padTopic(addr string) string { return "0x000000000000000000000000" + addr[2:] }

func blockHash(n uint64) string { return fmt.Sprintf("0x%064x", n+0xb10c) }

func txHash(s string) string { return fmt.Sprintf("0x%064x", new(big.Int).SetBytes([]byte(s))) }

func (l fakeLog) json() map[string]any {
	t0 := TransferTopic0
	if l.topic0 != "" {
		t0 = l.topic0
	}
	return map[string]any{
		"address":         l.contract,
		"topics":          []string{t0, padTopic(l.from), padTopic(l.to)},
		"data":            fmt.Sprintf("0x%064x", l.units),
		"blockNumber":     hexQ(l.block),
		"blockHash":       blockHash(l.block),
		"transactionHash": txHash(l.tx),
		"logIndex":        hexQ(l.logIndex),
		"removed":         l.removed,
	}
}

type evmChain struct {
	finalized  uint64
	latest     uint64
	logs       []fakeLog
	maxResults int    // >0: error when a getLogs query would match more
	failBlock  uint64 // >0: generic error for any query covering this block
	getLogsReq []string
	receipts   map[string]map[string]any
}

func (c *evmChain) handle(method string, params json.RawMessage) (any, *RPCError) {
	switch method {
	case "eth_blockNumber":
		return hexQ(c.latest), nil
	case "eth_getBlockByNumber":
		var p []any
		_ = json.Unmarshal(params, &p)
		tag, _ := p[0].(string)
		var n uint64
		if tag == "finalized" {
			n = c.finalized
		} else {
			n, _ = strconv.ParseUint(strings.TrimPrefix(tag, "0x"), 16, 64)
		}
		return map[string]any{"number": hexQ(n), "hash": blockHash(n), "timestamp": hexQ(1_700_000_000 + n)}, nil
	case "eth_getLogs":
		var p []struct {
			FromBlock string   `json:"fromBlock"`
			ToBlock   string   `json:"toBlock"`
			Address   []string `json:"address"`
			Topics    []any    `json:"topics"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &RPCError{Code: -32602, Message: err.Error()}
		}
		f := p[0]
		from, _ := parseHexUint64(f.FromBlock)
		to, _ := parseHexUint64(f.ToBlock)
		tos := map[string]bool{}
		list, _ := f.Topics[2].([]any)
		for _, x := range list {
			s, _ := x.(string)
			tos[s] = true
		}
		if f.Topics[0] != TransferTopic0 || f.Topics[1] != nil {
			return nil, &RPCError{Code: -32602, Message: "bad topics"}
		}
		contracts := map[string]bool{}
		for _, a := range f.Address {
			contracts[a] = true
		}
		c.getLogsReq = append(c.getLogsReq, fmt.Sprintf("%d-%d/%d", from, to, len(tos)))
		if c.failBlock > 0 && from <= c.failBlock && c.failBlock <= to {
			return nil, &RPCError{Code: -32000, Message: "internal error"}
		}
		out := []map[string]any{}
		for _, l := range c.logs {
			// The fake node deliberately also returns logs for non-whitelisted
			// contracts it was not asked for, to prove client-side filtering.
			if l.block < from || l.block > to || !tos[padTopic(l.to)] {
				continue
			}
			if !contracts[l.contract] && l.contract != tFake {
				continue
			}
			out = append(out, l.json())
		}
		if c.maxResults > 0 && len(out) > c.maxResults {
			return nil, &RPCError{Code: -32005, Message: fmt.Sprintf("query returned more than %d results", c.maxResults)}
		}
		return out, nil
	case "eth_getTransactionReceipt":
		var p []string
		_ = json.Unmarshal(params, &p)
		if r, ok := c.receipts[p[0]]; ok {
			return r, nil
		}
		return nil, nil
	}
	return nil, &RPCError{Code: -32601, Message: "method not found"}
}

func newTestEVMScanner(t *testing.T, c *evmChain, cfg EVMScannerConfig) (*EVMRPCScanner, *fakeRPC) {
	f, srv := newFakeRPC(t, c.handle)
	cfg.Network = ArbitrumOne
	cfg.RPC.URL = srv.URL
	s, err := NewEVMScanner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

func TestEVMScanRangeFiltering(t *testing.T) {
	c := &evmChain{finalized: 200, latest: 210, logs: []fakeLog{
		// Multiple Transfer logs in one tx, to alice and to bob (two watched addresses).
		{block: 100, contract: tUSDC, from: tPayer, to: tAlice, units: 5_000_000, logIndex: 3, tx: "t1"},
		{block: 100, contract: tUSDT, from: tPayer, to: tAlice, units: 1, logIndex: 4, tx: "t1"},
		{block: 100, contract: tUSDC, from: tPayer, to: tBob, units: 7_123_456, logIndex: 5, tx: "t1"},
		// Zero-value transfer: ignored.
		{block: 101, contract: tUSDC, from: tEve, to: tAlice, units: 0, logIndex: 0, tx: "t2"},
		// Fake token contract: ignored.
		{block: 102, contract: tFake, from: tEve, to: tAlice, units: 9_000_000, logIndex: 0, tx: "t3"},
		// Removed (reorged) log: ignored.
		{block: 103, contract: tUSDC, from: tEve, to: tAlice, units: 9_000_000, logIndex: 1, tx: "t4", removed: true},
		// Not watched.
		{block: 104, contract: tUSDC, from: tPayer, to: tEve, units: 1_000_000, logIndex: 0, tx: "t5"},
	}}
	s, f := newTestEVMScanner(t, c, EVMScannerConfig{})
	obs, err := s.ScanRange(context.Background(), 90, 150, []string{strings.ToUpper(tAlice[:2]) + tAlice[2:], tBob, tBob}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 3 {
		t.Fatalf("want 3 observations, got %d: %+v", len(obs), obs)
	}
	want := []struct {
		key, to, sym string
		units        int64
	}{{"log:3", tAlice, "USDC", 5_000_000}, {"log:4", tAlice, "USDT", 1}, {"log:5", tBob, "USDC", 7_123_456}}
	for i, w := range want {
		o := obs[i]
		if o.EventKey != w.key || o.ToAddress != w.to || o.Asset.Symbol != w.sym || o.Units.Int64() != w.units {
			t.Errorf("obs %d = %+v, want %+v", i, o, w)
		}
		if o.TxHash != txHash("t1") || o.FromAddress != tPayer || o.BlockNumber != 100 || o.BlockHash != blockHash(100) ||
			o.BlockTime.Unix() != 1_700_000_100 || !o.Finalized || o.Network != ArbitrumOne {
			t.Errorf("obs %d metadata wrong: %+v", i, o)
		}
	}
	// Block header fetched once for block 100 (cached), plus the finalized head.
	if n := f.count("eth_getBlockByNumber"); n != 2 {
		t.Errorf("eth_getBlockByNumber calls = %d, want 2", n)
	}
	for _, ua := range f.ua {
		if ua != DefaultUserAgent {
			t.Fatalf("user agent %q", ua)
		}
	}
}

func TestEVMScanRangeFinalizedGuard(t *testing.T) {
	c := &evmChain{finalized: 100, latest: 120}
	s, _ := newTestEVMScanner(t, c, EVMScannerConfig{})
	_, err := s.ScanRange(context.Background(), 90, 101, []string{tAlice}, true)
	if !errors.Is(err, ErrNotFinalized) {
		t.Fatalf("range above finalized head must fail with ErrNotFinalized, got %v", err)
	}
	if _, err := s.ScanRange(context.Background(), 90, 120, []string{tAlice}, false); err != nil {
		t.Fatalf("non-finalized scan may go to latest: %v", err)
	}
}

func TestEVMScanRangeSplitsOnTooManyResults(t *testing.T) {
	c := &evmChain{finalized: 1000, latest: 1000, maxResults: 2}
	// 4 logs to alice in distinct blocks, 3 logs to alice+bob in block 500.
	for i, b := range []uint64{10, 200, 300, 900} {
		c.logs = append(c.logs, fakeLog{block: b, contract: tUSDC, from: tPayer, to: tAlice, units: int64(i + 1), logIndex: 0, tx: fmt.Sprintf("a%d", i)})
	}
	c.logs = append(c.logs,
		fakeLog{block: 500, contract: tUSDC, from: tPayer, to: tAlice, units: 10, logIndex: 0, tx: "b"},
		fakeLog{block: 500, contract: tUSDC, from: tPayer, to: tBob, units: 20, logIndex: 1, tx: "b"},
		fakeLog{block: 500, contract: tUSDT, from: tPayer, to: tBob, units: 30, logIndex: 2, tx: "b"},
	)
	s, _ := newTestEVMScanner(t, c, EVMScannerConfig{MaxBlockRange: 10_000})
	obs, err := s.ScanRange(context.Background(), 0, 1000, []string{tAlice, tBob}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 7 {
		t.Fatalf("want all 7 logs after splitting, got %d (requests %v)", len(obs), c.getLogsReq)
	}
	total := int64(0)
	for _, o := range obs {
		total += o.Units.Int64()
	}
	if total != 1+2+3+4+10+20+30 {
		t.Fatalf("units total %d", total)
	}
	// Block 500 alone (3 logs) exceeds the limit, so the address list was split.
	foundAddrSplit := false
	for _, r := range c.getLogsReq {
		if r == "500-500/1" {
			foundAddrSplit = true
		}
	}
	if !foundAddrSplit {
		t.Fatalf("expected an address split at block 500, requests %v", c.getLogsReq)
	}
}

func TestEVMScanRangeSingleBlockSingleAddressTooLargeFails(t *testing.T) {
	c := &evmChain{finalized: 100, latest: 100, maxResults: 1, logs: []fakeLog{
		{block: 50, contract: tUSDC, from: tPayer, to: tAlice, units: 1, logIndex: 0, tx: "x"},
		{block: 50, contract: tUSDT, from: tPayer, to: tAlice, units: 1, logIndex: 1, tx: "x"},
	}}
	s, _ := newTestEVMScanner(t, c, EVMScannerConfig{})
	if _, err := s.ScanRange(context.Background(), 0, 100, []string{tAlice}, true); err == nil {
		t.Fatal("unsplittable query must error, never return partial data")
	}
}

func TestEVMScanRangePartialFailureReturnsError(t *testing.T) {
	c := &evmChain{finalized: 5000, latest: 5000, failBlock: 3500, logs: []fakeLog{
		{block: 10, contract: tUSDC, from: tPayer, to: tAlice, units: 1, logIndex: 0, tx: "ok"},
	}}
	s, _ := newTestEVMScanner(t, c, EVMScannerConfig{MaxBlockRange: 1000})
	obs, err := s.ScanRange(context.Background(), 0, 4999, []string{tAlice}, true)
	if err == nil || obs != nil {
		t.Fatalf("a failing chunk must fail the whole scan, got obs=%v err=%v", obs, err)
	}
}

func TestEVMScanRangeChunksAddresses(t *testing.T) {
	c := &evmChain{finalized: 100, latest: 100}
	s, _ := newTestEVMScanner(t, c, EVMScannerConfig{MaxAddressesPerQuery: 2})
	addrs := []string{tAlice, tBob, tEve, tPayer, "0x5555000000000000000000000000000000005555"}
	if _, err := s.ScanRange(context.Background(), 0, 100, addrs, true); err != nil {
		t.Fatal(err)
	}
	if len(c.getLogsReq) != 3 {
		t.Fatalf("want 3 address chunks, got %v", c.getLogsReq)
	}
}

func TestEVMObservationsForTx(t *testing.T) {
	h := txHash("r1")
	logs := []any{
		fakeLog{block: 90, contract: tUSDC, from: tPayer, to: tAlice, units: 2_500_000, logIndex: 7, tx: "r1"}.json(),
		fakeLog{block: 90, contract: tFake, from: tPayer, to: tAlice, units: 99, logIndex: 8, tx: "r1"}.json(),
		fakeLog{block: 90, contract: tUSDT, from: tPayer, to: tBob, units: 3, logIndex: 9, tx: "r1"}.json(),
	}
	c := &evmChain{finalized: 100, latest: 120, receipts: map[string]map[string]any{
		h:              {"transactionHash": h, "blockNumber": hexQ(90), "blockHash": blockHash(90), "status": "0x1", "logs": logs},
		txHash("late"): {"transactionHash": txHash("late"), "blockNumber": hexQ(110), "blockHash": blockHash(110), "status": "0x1", "logs": []any{}},
		txHash("rev"):  {"transactionHash": txHash("rev"), "blockNumber": hexQ(50), "blockHash": blockHash(50), "status": "0x0", "logs": []any{}},
	}}
	s, _ := newTestEVMScanner(t, c, EVMScannerConfig{})
	ctx := context.Background()
	obs, err := s.ObservationsForTx(ctx, strings.ToUpper(h), []string{tAlice, tBob}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 2 || obs[0].EventKey != "log:7" || obs[1].EventKey != "log:9" || obs[1].Asset.Symbol != "USDT" {
		t.Fatalf("got %+v", obs)
	}
	obs, err = s.ObservationsForTx(ctx, h, []string{tBob}, true)
	if err != nil || len(obs) != 1 || obs[0].ToAddress != tBob {
		t.Fatalf("filter by address: %+v %v", obs, err)
	}
	if _, err := s.ObservationsForTx(ctx, txHash("late"), []string{tAlice}, true); !errors.Is(err, ErrNotFinalized) {
		t.Fatalf("tx above finalized head: %v", err)
	}
	if _, err := s.ObservationsForTx(ctx, txHash("late"), []string{tAlice}, false); err != nil {
		t.Fatalf("non-finalized lookup: %v", err)
	}
	if _, err := s.ObservationsForTx(ctx, txHash("unknown"), []string{tAlice}, true); !errors.Is(err, ErrTxNotFound) {
		t.Fatalf("unknown tx: %v", err)
	}
	if obs, err := s.ObservationsForTx(ctx, txHash("rev"), []string{tAlice}, true); err != nil || len(obs) != 0 {
		t.Fatalf("reverted tx: %v %v", obs, err)
	}
}

func TestEVMReorgBetweenLogAndHeaderIsError(t *testing.T) {
	c := &evmChain{finalized: 100, latest: 100, logs: []fakeLog{
		{block: 50, contract: tUSDC, from: tPayer, to: tAlice, units: 1, logIndex: 0, tx: "x"},
	}}
	f, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
		res, rerr := c.handle(method, params)
		if method == "eth_getLogs" && rerr == nil {
			list, _ := res.([]map[string]any)
			for _, l := range list {
				l["blockHash"] = fmt.Sprintf("0x%064x", 1) // stale hash
			}
		}
		return res, rerr
	})
	_ = f
	s, err := NewEVMScanner(EVMScannerConfig{Network: ArbitrumOne, RPC: RPCOptions{URL: srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScanRange(context.Background(), 0, 100, []string{tAlice}, true); err == nil || !strings.Contains(err.Error(), "reorg") {
		t.Fatalf("block hash mismatch must error, got %v", err)
	}
}

func TestEVMScannerInjectedTestnetAssets(t *testing.T) {
	if _, err := NewEVMScanner(EVMScannerConfig{Network: ArbitrumSepolia, RPC: RPCOptions{URL: "http://x"}}); err == nil {
		t.Fatal("sepolia without injected assets must fail")
	}
	sepUSDC := "0x2222222222222222222222222222222222222222" // placeholder, not a real deployment
	s, err := NewEVMScanner(EVMScannerConfig{Network: ArbitrumSepolia, RPC: RPCOptions{URL: "http://x"},
		Assets: []Asset{{Network: ArbitrumSepolia, Symbol: "USDC", Contract: sepUSDC, Decimals: 6}}})
	if err != nil || len(s.contracts) != 1 || s.contracts[0] != sepUSDC {
		t.Fatalf("%v %v", s, err)
	}
	if _, err := NewEVMScanner(EVMScannerConfig{Network: ArbitrumSepolia, RPC: RPCOptions{URL: "http://x"},
		Assets: MainnetAssets()}); err == nil {
		t.Fatal("mainnet assets must not be accepted for sepolia")
	}
}

func TestRPCResponseSizeLimitTriggersSplit(t *testing.T) {
	c := &evmChain{finalized: 100, latest: 100}
	for i := 0; i < 6; i++ {
		c.logs = append(c.logs, fakeLog{block: uint64(10 + i*10), contract: tUSDC, from: tPayer, to: tAlice, units: 1, logIndex: 0, tx: fmt.Sprint(i)})
	}
	f, srv := newFakeRPC(t, c.handle)
	_ = f
	// ~420 bytes per log in JSON: a 1.2 KB cap forces bisection down to <=2 logs.
	s, err := NewEVMScanner(EVMScannerConfig{Network: ArbitrumOne, RPC: RPCOptions{URL: srv.URL, MaxResponseBytes: 1200}})
	if err != nil {
		t.Fatal(err)
	}
	obs, err := s.ScanRange(context.Background(), 0, 100, []string{tAlice}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 6 {
		t.Fatalf("got %d", len(obs))
	}
	if len(c.getLogsReq) < 3 {
		t.Fatalf("expected bisection, requests %v", c.getLogsReq)
	}
}
