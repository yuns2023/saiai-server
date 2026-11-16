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

// ---- RPC errors carry no provider text ----

const (
	secretPath   = "PATHKEYsecret0123456789"
	secretQuery  = "QUERYKEYsecret9876543210"
	secretBearer = "BEARERtokenSecret55555"
)

var allSecrets = []string{secretPath, secretQuery, secretBearer, "api_key", "Bearer"}

// assertCleanChain walks the whole Unwrap chain and checks every Error().
func assertCleanChain(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected an error", what)
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		for _, sec := range allSecrets {
			if strings.Contains(e.Error(), sec) {
				t.Fatalf("%s: error chain leaks %q: %s", what, sec, e.Error())
			}
		}
	}
}

// cannedRPC answers according to the "case" query parameter, echoing the
// request id unless the case says otherwise.
func cannedRPC(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		id := string(req.ID)
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		write := func(status int, body string) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
		rpcErr := func(status int, idv string, code int, msg string) {
			write(status, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":%q}}`, idv, code, msg))
		}
		switch r.URL.Query().Get("case") {
		case "echo-bearer-500":
			write(500, "bad token "+auth+" for "+r.URL.String())
		case "echo-bearer-rpc":
			rpcErr(200, id, -32000, "token "+auth+" path "+r.URL.Path+" query "+r.URL.RawQuery)
		case "span-512":
			write(502, strings.Repeat("x", 500)+secretQuery+secretPath)
		case "escaped-data":
			escaped := ""
			for _, ch := range secretQuery {
				escaped += fmt.Sprintf(`\u%04x`, ch)
			}
			write(200, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"x","data":"%s"}}`, id, escaped))
		case "429-rpc":
			rpcErr(429, id, -32005, "rate limited, query returned more than 10000 results")
		case "504-rpc":
			rpcErr(504, id, -32603, "internal error")
		case "413":
			write(413, "too big")
		case "200-limit":
			rpcErr(200, id, -32005, "query returned more than 10000 results")
		case "200-quicknode":
			rpcErr(200, id, -32614, "eth_getLogs is limited to a 10000 blocks range")
		case "200-ratelimited":
			rpcErr(200, id, -32005, "Too many requests, rate limit exceeded")
		case "200-internal":
			rpcErr(200, id, -32000, "internal error")
		case "500-plain":
			write(500, "oops")
		case "500-foreign-id":
			rpcErr(500, "999999", -32005, "query returned more than 10000 results")
		case "200-foreign-id":
			rpcErr(200, "999999", -32005, "query returned more than 10000 results")
		case "200-null-id":
			rpcErr(200, "null", -32700, "parse error")
		default:
			write(200, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":"0x1"}`, id))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRPCErrorsCarryNoProviderText(t *testing.T) {
	ctx := context.Background()
	suffix := "/v2/" + secretPath + "?api_key=" + secretQuery
	headers := map[string]string{"Authorization": "Bearer " + secretBearer}

	// Connection refused.
	c, _ := newRPCClient(RPCOptions{URL: "http://127.0.0.1:1" + suffix, Timeout: 2 * time.Second, Headers: headers})
	_, err := c.call(ctx, "eth_blockNumber", []any{})
	assertCleanChain(t, err, "connect failure")
	var ce *RPCCallError
	if !errors.As(err, &ce) || ce.Kind != RPCKindTransport {
		t.Fatalf("connect failure kind: %v", err)
	}

	// Caller deadline: errors.Is and Timeout() preserved.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	c, _ = newRPCClient(RPCOptions{URL: slow.URL + suffix, Timeout: 5 * time.Second, Headers: headers})
	dctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err = c.call(dctx, "eth_blockNumber", []any{})
	cancel()
	assertCleanChain(t, err, "timeout")
	var to interface{ Timeout() bool }
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &to) || !to.Timeout() {
		t.Fatalf("timeout semantics lost: %v", err)
	}
	cctx, ccancel := context.WithCancel(ctx)
	ccancel()
	_, err = c.call(cctx, "eth_blockNumber", []any{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel semantics lost: %v", err)
	}

	srv := cannedRPC(t)
	for _, cs := range []string{"echo-bearer-500", "echo-bearer-rpc", "span-512", "escaped-data"} {
		c, _ = newRPCClient(RPCOptions{URL: srv.URL + suffix + "&case=" + cs, Headers: headers})
		_, err = c.call(ctx, "eth_getLogs", []any{})
		assertCleanChain(t, err, cs)
	}
}

func TestRPCClassificationThroughHTTP(t *testing.T) {
	srv := cannedRPC(t)
	cases := []struct {
		name    string
		kind    RPCErrorKind
		status  int
		code    int
		hasCode bool
		tooMuch bool
	}{
		{"429-rpc", RPCKindHTTPStatus, 429, -32005, true, false},
		{"504-rpc", RPCKindTooMuchData, 504, -32603, true, true},
		{"413", RPCKindTooMuchData, 413, 0, false, true},
		{"200-limit", RPCKindTooMuchData, 200, -32005, true, true},
		{"200-quicknode", RPCKindTooMuchData, 200, -32614, true, true},
		{"200-ratelimited", RPCKindRPCError, 200, -32005, true, false},
		{"200-internal", RPCKindRPCError, 200, -32000, true, false},
		{"500-plain", RPCKindHTTPStatus, 500, 0, false, false},
		{"500-foreign-id", RPCKindHTTPStatus, 500, 0, false, false},
		{"200-foreign-id", RPCKindDecode, 200, 0, false, false},
		{"200-null-id", RPCKindRPCError, 200, -32700, true, false},
	}
	for _, tc := range cases {
		c, _ := newRPCClient(RPCOptions{URL: srv.URL + "/?case=" + tc.name})
		_, err := c.call(context.Background(), "eth_getLogs", []any{})
		var ce *RPCCallError
		if !errors.As(err, &ce) {
			t.Fatalf("%s: not an RPCCallError: %v", tc.name, err)
		}
		if ce.Kind != tc.kind || ce.HTTPStatus != tc.status || ce.HasRPCCode != tc.hasCode || ce.RPCCode != tc.code {
			t.Errorf("%s: got %+v", tc.name, ce)
		}
		if isTooMuchDataError(err) != tc.tooMuch {
			t.Errorf("%s: too-much-data = %v", tc.name, !tc.tooMuch)
		}
	}
	c, _ := newRPCClient(RPCOptions{URL: srv.URL + "/?case=ok"})
	if res, err := c.call(context.Background(), "eth_blockNumber", []any{}); err != nil || string(res) != `"0x1"` {
		t.Fatalf("ok case: %s %v", res, err)
	}
}

// A 429 must never trigger a split (it would multiply requests while rate limited).
func TestEVMRateLimitedDoesNotSplit(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "eth_getBlockByNumber":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"number":"0x64","hash":"%s","timestamp":"0x1"}}`, req.ID, blockHash(100))
		default:
			calls++
			w.WriteHeader(429)
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32005,"message":"rate limited"}}`, req.ID)
		}
	}))
	defer srv.Close()
	s, _ := NewEVMScanner(EVMScannerConfig{Network: ArbitrumOne, RPC: RPCOptions{URL: srv.URL}})
	if _, err := s.ScanRange(context.Background(), 0, 100, []string{tAlice, tBob}, true); err == nil {
		t.Fatal("rate limit must be an error")
	}
	if calls != 1 {
		t.Fatalf("429 caused %d eth_getLogs calls, want 1 (no split)", calls)
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
	// The manual rescan path must agree for every case.
	want := map[string]int64{"net-positive": 5, "net-zero": 0, "net-negative": 0}
	for name, units := range want {
		byTx, err := s.ObservationsForTx(context.Background(), sSig(name), []string{owner}, CommitmentFinalized)
		if err != nil {
			t.Fatal(err)
		}
		if units == 0 {
			if len(byTx) != 0 {
				t.Errorf("%s: ObservationsForTx returned %+v", name, byTx)
			}
			continue
		}
		if len(byTx) != 1 || byTx[0].Units.Int64() != units || byTx[0].EventKey != obs[0].EventKey || byTx[0].TokenAccount != ata {
			t.Errorf("%s: ObservationsForTx %+v disagrees with scan %+v", name, byTx, obs[0])
		}
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
	// The receiving accounts really are in the lookup-table segment.
	msg, _ := txJSON["transaction"].(map[string]any)["message"].(map[string]any)
	keys, _ := msg["accountKeys"].([]any)
	for _, idx := range []int{15, 20} {
		k, _ := keys[idx].(map[string]any)
		if k["source"] != "lookupTable" {
			t.Fatalf("account %d source = %v, want lookupTable", idx, k["source"])
		}
	}
	if k, _ := keys[15].(map[string]any); k["pubkey"] != ata {
		t.Fatalf("index 15 is %v", k["pubkey"])
	}
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
