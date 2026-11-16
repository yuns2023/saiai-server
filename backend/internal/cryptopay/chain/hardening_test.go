package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ---- RPC error redaction ----

const (
	secretPath  = "PATHKEYsecret0123456789"
	secretQuery = "QUERYKEYsecret9876543210"
)

func assertNoSecret(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected an error", what)
	}
	msg := err.Error()
	if strings.Contains(msg, secretPath) || strings.Contains(msg, secretQuery) || strings.Contains(msg, "api_key=") {
		t.Fatalf("%s: error leaks endpoint secret: %s", what, msg)
	}
}

func TestRPCErrorsNeverContainEndpointSecrets(t *testing.T) {
	ctx := context.Background()
	suffix := "/v2/" + secretPath + "?api_key=" + secretQuery

	// Connection refused.
	c, _ := newRPCClient(RPCOptions{URL: "http://127.0.0.1:1" + suffix, Timeout: 2 * time.Second})
	_, err := c.call(ctx, "eth_blockNumber", []any{})
	assertNoSecret(t, err, "connect failure")

	// Timeout: caller deadline must still be recognizable via errors.Is.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	c, _ = newRPCClient(RPCOptions{URL: slow.URL + suffix, Timeout: 5 * time.Second})
	dctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err = c.call(dctx, "eth_blockNumber", []any{})
	cancel()
	assertNoSecret(t, err, "timeout")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout must keep errors.Is(context.DeadlineExceeded): %v", err)
	}
	var to interface{ Timeout() bool }
	if !errors.As(err, &to) || !to.Timeout() {
		t.Fatalf("timeout must report Timeout(): %v", err)
	}

	// Non-200 bodies and JSON-RPC errors echoing the URL.
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.RawQuery, "mode=json"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":null,"error":{"code":403,"message":"key %s not allowed for %s","data":"%s"}}`,
				secretQuery, r.URL.Path, r.URL.String())
		case strings.Contains(r.URL.RawQuery, "mode=ok200"):
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"bad key %s"}}`, secretPath)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(w, "upstream failed for %s", r.URL.String())
		}
	}))
	defer echo.Close()
	for _, mode := range []string{"plain", "json", "ok200"} {
		c, _ = newRPCClient(RPCOptions{URL: echo.URL + suffix + "&mode=" + mode})
		_, err = c.call(ctx, "eth_blockNumber", []any{})
		assertNoSecret(t, err, "echo "+mode)
	}
	// Status classification still works through redaction.
	c, _ = newRPCClient(RPCOptions{URL: echo.URL + suffix})
	_, err = c.call(ctx, "eth_blockNumber", []any{})
	var he *HTTPStatusError
	if !errors.As(err, &he) || he.StatusCode != 500 {
		t.Fatalf("HTTPStatusError lost: %v", err)
	}
}

func TestRPCErrorResponseIDMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":4242,"error":{"code":-32005,"message":"query returned more than 10000 results"}}`))
	}))
	defer srv.Close()
	c, _ := newRPCClient(RPCOptions{URL: srv.URL})
	_, err := c.call(context.Background(), "eth_getLogs", []any{})
	if err == nil || isTooMuchDataError(err) {
		t.Fatalf("foreign error response must not be treated as ours: %v", err)
	}
}

// ---- provider "narrow the query" classification ----

func TestIsTooMuchDataErrorTable(t *testing.T) {
	wrap := func(e error) error { return fmt.Errorf("chain: eth_getLogs: %w", e) }
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"geth/infura 10000", wrap(&RPCError{Code: -32005, Message: "query returned more than 10000 results"}), true},
		{"infura code only", wrap(&RPCError{Code: -32005, Message: "rate limited?"}), true},
		{"alchemy", wrap(&RPCError{Code: -32602, Message: "Log response size exceeded. You can make eth_getLogs requests with up to a 2K block range"}), true},
		{"quicknode", wrap(&RPCError{Code: -32614, Message: "eth_getLogs is limited to a 10000 blocks range"}), true},
		{"generic range too large", wrap(&RPCError{Code: -32000, Message: "range too large"}), true},
		{"response size cap", wrap(ErrResponseTooLarge), true},
		{"http 413", wrap(&HTTPStatusError{StatusCode: 413}), true},
		{"http 504", wrap(&HTTPStatusError{StatusCode: 504}), true},
		{"http 429", wrap(&HTTPStatusError{StatusCode: 429}), false},
		{"http 500", wrap(&HTTPStatusError{StatusCode: 500}), false},
		{"unknown rpc", wrap(&RPCError{Code: -32000, Message: "internal error"}), false},
		{"header not found", wrap(&RPCError{Code: -32000, Message: "header not found"}), false},
		{"deadline", wrap(context.DeadlineExceeded), false},
	}
	for _, c := range cases {
		if got := isTooMuchDataError(c.err); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestEVMSplitHalfFailureFailsWhole(t *testing.T) {
	c := &evmChain{finalized: 100, latest: 100, maxResults: 1, logs: []fakeLog{
		{block: 10, contract: tUSDC, from: tPayer, to: tAlice, units: 1, logIndex: 0, tx: "l"},
		{block: 90, contract: tUSDC, from: tPayer, to: tAlice, units: 2, logIndex: 0, tx: "r"},
	}}
	// The first (whole-range) query hits the result limit; after the split the
	// right half fails with a non-splittable error.
	calls := 0
	_, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
		if method == "eth_getLogs" {
			calls++
			if calls > 1 {
				c.failBlock = 90 // right half fails after the split
			}
		}
		return c.handle(method, params)
	})
	s, err := NewEVMScanner(EVMScannerConfig{Network: ArbitrumOne, RPC: RPCOptions{URL: srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	obs, err := s.ScanRange(context.Background(), 0, 100, []string{tAlice}, true)
	if err == nil || obs != nil {
		t.Fatalf("a failing half must yield (nil, error), got %v %v", obs, err)
	}
}

// ---- malformed Transfer logs of whitelisted contracts ----

func TestEVMMalformedWhitelistedTransferIsError(t *testing.T) {
	base := func() map[string]any {
		return fakeLog{block: 10, contract: tUSDC, from: tPayer, to: tAlice, units: 5, logIndex: 0, tx: "m"}.json()
	}
	cases := map[string]func(l map[string]any){
		"topic count short": func(l map[string]any) { l["topics"] = []string{TransferTopic0, padTopic(tPayer)} },
		"topic count 4": func(l map[string]any) {
			l["topics"] = []string{TransferTopic0, padTopic(tPayer), padTopic(tAlice), padTopic(tAlice)}
		},
		"to topic 31 bytes": func(l map[string]any) {
			l["topics"] = []string{TransferTopic0, padTopic(tPayer), "0x" + strings.Repeat("0", 22) + tAlice[2:]}
		},
		"data 31 bytes": func(l map[string]any) { l["data"] = "0x" + strings.Repeat("0", 60) + "05" },
		"data 33 bytes": func(l map[string]any) { l["data"] = "0x" + strings.Repeat("0", 64) + "05" },
	}
	run := func(mut func(l map[string]any)) ([]Observation, error) {
		c := &evmChain{finalized: 100, latest: 100}
		_, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
			if method == "eth_getLogs" {
				l := base()
				if mut != nil {
					mut(l)
				}
				return []any{l}, nil
			}
			return c.handle(method, params)
		})
		s, err := NewEVMScanner(EVMScannerConfig{Network: ArbitrumOne, RPC: RPCOptions{URL: srv.URL}})
		if err != nil {
			t.Fatal(err)
		}
		return s.ScanRange(context.Background(), 0, 100, []string{tAlice}, true)
	}
	for name, mut := range cases {
		if obs, err := run(mut); err == nil {
			t.Errorf("%s: expected error, got %+v", name, obs)
		}
	}
	// Mixed-case (checksummed) contract address is the same whitelisted asset.
	obs, err := run(func(l map[string]any) { l["address"] = "0xaf88d065e77c8cC2239327C5EDb3A432268e5831" })
	if err != nil || len(obs) != 1 || obs[0].Asset.Symbol != "USDC" || obs[0].Units.Int64() != 5 {
		t.Fatalf("mixed-case contract: %+v %v", obs, err)
	}
}

// ---- Solana paging edge cases ----

func pagedChain(owner, ata string, names []string, slot uint64) *solChain {
	c := &solChain{honorUntil: true, txs: map[string]fakeSolTx{}}
	amount := 0
	for _, n := range names { // oldest first
		pre := []tb{}
		if amount > 0 {
			pre = []tb{{ata, sUSDC, owner, "", fmt.Sprint(amount)}}
		}
		amount += 10
		c.order = append([]string{sSig(n)}, c.order...)
		c.txs[sSig(n)] = fakeSolTx{slot: slot, pre: pre, post: []tb{{ata, sUSDC, owner, "", fmt.Sprint(amount)}}}
	}
	return c
}

func TestSolanaPagingSameSlotAndNewHead(t *testing.T) {
	owner := sKey("pager")
	ata, _ := SolanaATA(owner, sUSDC, SolanaTokenProgram)
	names := []string{"p1", "p2", "p3", "p4", "p5"}
	c := pagedChain(owner, ata, names, 777) // all in one slot, spread over 3 pages of 2
	inserted := false
	_, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
		res, rerr := c.handle(method, params)
		if method == "getSignaturesForAddress" && !inserted {
			inserted = true // a new signature lands after the first page was served
			c.order = append([]string{sSig("late")}, c.order...)
			c.txs[sSig("late")] = fakeSolTx{slot: 778, pre: []tb{{ata, sUSDC, owner, "", "50"}}, post: []tb{{ata, sUSDC, owner, "", "60"}}}
		}
		return res, rerr
	})
	s, _ := NewSolanaScanner(SolanaScannerConfig{Network: SolanaMainnet, RPC: RPCOptions{URL: srv.URL}, PageLimit: 2})
	obs, newest, err := s.ScanTokenAccount(context.Background(), owner, ata, usdcAsset(), "", CommitmentFinalized)
	if err != nil {
		t.Fatal(err)
	}
	if newest != sSig("p5") {
		t.Fatalf("newest must be the head seen at the first page, got %s", newest)
	}
	if len(obs) != 5 {
		t.Fatalf("want 5 same-slot inflows exactly once, got %d", len(obs))
	}
	seen := map[string]bool{}
	for _, o := range obs {
		if seen[o.TxHash] || o.Units.Int64() != 10 {
			t.Fatalf("dup or wrong amount: %+v", o)
		}
		seen[o.TxHash] = true
	}
	// Next round picks up the late signature only.
	obs, newest, err = s.ScanTokenAccount(context.Background(), owner, ata, usdcAsset(), sSig("p5"), CommitmentFinalized)
	if err != nil || len(obs) != 1 || obs[0].TxHash != sSig("late") || newest != sSig("late") {
		t.Fatalf("second round: %+v %q %v", obs, newest, err)
	}
}

func TestSolanaPagingFailuresGiveNoCursor(t *testing.T) {
	owner := sKey("pager2")
	ata, _ := SolanaATA(owner, sUSDC, SolanaTokenProgram)
	for _, failOn := range []string{"page2", "tx"} {
		c := pagedChain(owner, ata, []string{"q1", "q2", "q3", "q4"}, 5)
		pages := 0
		_, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
			if method == "getSignaturesForAddress" {
				pages++
				if failOn == "page2" && pages == 2 {
					return nil, &RPCError{Code: -32000, Message: "node behind"}
				}
			}
			if method == "getTransaction" && failOn == "tx" && strings.Contains(string(params), sSig("q2")) {
				return nil, &RPCError{Code: -32000, Message: "temporarily unavailable"}
			}
			return c.handle(method, params)
		})
		s, _ := NewSolanaScanner(SolanaScannerConfig{Network: SolanaMainnet, RPC: RPCOptions{URL: srv.URL}, PageLimit: 2})
		obs, newest, err := s.ScanTokenAccount(context.Background(), owner, ata, usdcAsset(), "", CommitmentFinalized)
		if err == nil || obs != nil || newest != "" {
			t.Fatalf("%s: want (nil, \"\", err), got %v %q %v", failOn, obs, newest, err)
		}
	}
}

func TestSolanaReceiveThenSendSameTx(t *testing.T) {
	owner := sKey("rts")
	ata, _ := SolanaATA(owner, sUSDC, SolanaTokenProgram)
	c := &solChain{honorUntil: true, txs: map[string]fakeSolTx{}}
	add := func(name, pre, post string) {
		c.order = append([]string{sSig(name)}, c.order...)
		c.txs[sSig(name)] = fakeSolTx{slot: 1, pre: []tb{{ata, sUSDC, owner, "", pre}}, post: []tb{{ata, sUSDC, owner, "", post}}}
	}
	add("net-positive", "10", "15") // e.g. +20 in, -15 out within the tx
	add("net-zero", "15", "15")     // +5 in, -5 out
	add("net-negative", "15", "3")  // +2 in, -14 out
	s := newTestSolScanner(t, c, 10)
	obs, _, err := s.ScanTokenAccount(context.Background(), owner, ata, usdcAsset(), "", CommitmentFinalized)
	if err != nil || len(obs) != 1 || obs[0].TxHash != sSig("net-positive") || obs[0].Units.Int64() != 5 {
		t.Fatalf("only the positive net change is an inflow: %+v %v", obs, err)
	}
}

// ---- v0 transaction with the receiving account loaded from a lookup table ----

// testdata/solana_mainnet_usdc_tx_v0_alt.json: finalized mainnet v0 tx
// 54XCin9c... (fetched 2026-11-16). Account index 15 (6oCkp..., the USDC ATA of
// 5YxQF...) and index 20 (HLmqe..., owner 5Q544...) are loaded via an address
// lookup table. Verified independently: the "json" encoding of the same tx has
// 15 static keys + loadedAddresses (7 writable, 9 readonly) in exactly the
// jsonParsed order, and getAccountInfo(6oCkp...) reports owner 5YxQF..., mint USDC.
func TestSolanaV0LookupTableAccountBothPathsAgree(t *testing.T) {
	raw, err := os.ReadFile("testdata/solana_mainnet_usdc_tx_v0_alt.json")
	if err != nil {
		t.Fatal(err)
	}
	var txJSON map[string]any
	if err := json.Unmarshal(raw, &txJSON); err != nil {
		t.Fatal(err)
	}
	const (
		sig    = "54XCin9cVEqzbhsnpaW2W1oLVjpqjhkFsCxe2FBBqfRbnaxmw7VtZu7FvwjdrdahabtYntQh5CSX8zBJGtp6yRc6"
		owner  = "5YxQFdt3Tr9zJLvkFccqXVUwhdTWJQc1fFg2YPbxvxeD"
		ata    = "6oCkp6gpyjxVTeL6ahMYcekN2x2pzt1KY8g2LqemaTNE"
		owner2 = "5Q544fKrFoe6tsEbD7S8EmxGTJYAKtTVhAW5Q5pge4j1"
	)
	_, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
		switch method {
		case "getSignaturesForAddress":
			return []map[string]any{{"signature": sig, "slot": 469969559, "err": nil}}, nil
		case "getTransaction":
			return txJSON, nil
		}
		return nil, &RPCError{Code: -32601, Message: "nope"}
	})
	s, _ := NewSolanaScanner(SolanaScannerConfig{Network: SolanaMainnet, RPC: RPCOptions{URL: srv.URL}})
	ctx := context.Background()
	scanned, newest, err := s.ScanTokenAccount(ctx, owner, ata, usdcAsset(), "", CommitmentFinalized)
	if err != nil || len(scanned) != 1 || newest != sig {
		t.Fatalf("scan: %+v %q %v", scanned, newest, err)
	}
	byTx, err := s.ObservationsForTx(ctx, sig, []string{owner, owner2}, CommitmentFinalized)
	if err != nil || len(byTx) != 2 {
		t.Fatalf("by tx: %+v %v", byTx, err)
	}
	var fromTx Observation
	for _, o := range byTx {
		if o.TokenAccount == ata {
			fromTx = o
		} else if o.TokenAccount != "HLmqeL62xR1QoZ1HKKbXRrdN1p3phKpxRMb2VVopvBBz" || o.ToAddress != owner2 || o.Units.Int64() != 13620924 {
			t.Errorf("second loaded account: %+v", o)
		}
	}
	a := scanned[0]
	if a.Units.Int64() != 65518 || a.ToAddress != owner || a.TokenAccount != ata || a.EventKey != "acct:"+ata ||
		a.BlockNumber != 469969559 || a.BlockTime.Unix() != 1794797308 {
		t.Fatalf("scan observation: %+v", a)
	}
	if fromTx.Units.Cmp(a.Units) != 0 || fromTx.ToAddress != a.ToAddress || fromTx.TokenAccount != a.TokenAccount ||
		fromTx.EventKey != a.EventKey || fromTx.TxHash != a.TxHash || fromTx.BlockNumber != a.BlockNumber || fromTx.Asset != a.Asset {
		t.Fatalf("paths disagree:\n scan %+v\n  tx  %+v", a, fromTx)
	}
}

// ---- manifest canonicalization ----

func TestManifestEquivalentEncodingsAndTamper(t *testing.T) {
	signer := testKey(9)
	trusted := testAddr(9)
	good := signedManifest(t, nil, signer)

	// Same values with a unicode escape and keys in another order: the signed
	// content is the decoded struct, so these verify and decode identically.
	escaped := bytes.Replace(good, []byte(`"solana-mainnet"`), []byte(`"solana\u002dmainnet"`), 1)
	if bytes.Equal(escaped, good) {
		t.Fatal("escape replacement did not apply")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(good, &fields); err != nil {
		t.Fatal(err)
	}
	order := []string{"signature", "signer_pubkey", "entries", "start", "path_template", "network", "version"}
	var buf bytes.Buffer
	_ = buf.WriteByte('{')
	for i, k := range order {
		if i > 0 {
			_ = buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, "%q:%s", k, fields[k])
	}
	_ = buf.WriteByte('}')
	want, err := VerifySolanaPoolManifest(good, trusted)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"unicode escape": escaped, "reordered": buf.Bytes()} {
		m, err := VerifySolanaPoolManifest(raw, trusted)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if m.Network != want.Network || len(m.Entries) != len(want.Entries) || m.Entries[2] != want.Entries[2] {
			t.Fatalf("%s decoded differently: %+v", name, m)
		}
	}
	// Changing the network to another valid Solana network breaks the signature.
	tampered := bytes.Replace(good, []byte(`"solana-mainnet"`), []byte(`"solana-devnet"`), 1)
	if _, err := VerifySolanaPoolManifest(tampered, trusted); err == nil {
		t.Fatal("tampered network must fail verification")
	}
}
