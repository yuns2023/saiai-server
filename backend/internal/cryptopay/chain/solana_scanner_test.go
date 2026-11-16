package chain

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/mr-tron/base58"
)

const (
	sUSDC = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	sUSDT = "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"
)

func sKey(name string) string { // deterministic fake 32-byte pubkey
	h := sha256.Sum256([]byte("key:" + name))
	return base58.Encode(h[:])
}

func sSig(name string) string { // deterministic fake 64-byte signature
	a := sha256.Sum256([]byte("sig1:" + name))
	b := sha256.Sum256([]byte("sig2:" + name))
	return base58.Encode(append(a[:], b[:]...))
}

type tb struct {
	acct, mint, owner, program string
	amount                     string
}

type fakeSolTx struct {
	slot     uint64
	failed   bool // meta.err
	pre      []tb
	post     []tb
	null     bool
	listErr  bool // err in getSignaturesForAddress entry
	extraKey []string
}

func (tx fakeSolTx) json(sig string) any {
	if tx.null {
		return nil
	}
	idx := map[string]int{}
	var keys []map[string]any
	add := func(k string) int {
		if i, ok := idx[k]; ok {
			return i
		}
		idx[k] = len(keys)
		keys = append(keys, map[string]any{"pubkey": k, "signer": false, "writable": true, "source": "transaction"})
		return idx[k]
	}
	add(sKey("feepayer"))
	for _, k := range tx.extraKey {
		add(k)
	}
	conv := func(list []tb) []map[string]any {
		out := []map[string]any{}
		for _, b := range list {
			prog := b.program
			if prog == "" {
				prog = SolanaTokenProgram
			}
			out = append(out, map[string]any{
				"accountIndex": add(b.acct), "mint": b.mint, "owner": b.owner, "programId": prog,
				"uiTokenAmount": map[string]any{"amount": b.amount, "decimals": 6},
			})
		}
		return out
	}
	pre, post := conv(tx.pre), conv(tx.post)
	var metaErr any
	if tx.failed {
		metaErr = map[string]any{"InstructionError": []any{0, map[string]any{"Custom": 1}}}
	}
	bt := int64(1_790_000_000) + int64(tx.slot)
	return map[string]any{
		"slot": tx.slot, "blockTime": bt, "version": 0,
		"meta":        map[string]any{"err": metaErr, "preTokenBalances": pre, "postTokenBalances": post},
		"transaction": map[string]any{"signatures": []string{sig}, "message": map[string]any{"accountKeys": keys, "recentBlockhash": sKey("bh")}},
	}
}

type solChain struct {
	order       []string // newest first
	txs         map[string]fakeSolTx
	honorUntil  bool
	sigRequests []map[string]any
	txRequests  []map[string]any
}

func (c *solChain) handle(method string, params json.RawMessage) (any, *RPCError) {
	var p []json.RawMessage
	_ = json.Unmarshal(params, &p)
	switch method {
	case "getSignaturesForAddress":
		var opts map[string]any
		_ = json.Unmarshal(p[1], &opts)
		c.sigRequests = append(c.sigRequests, opts)
		limitF, _ := opts["limit"].(float64)
		limit := int(limitF)
		before, _ := opts["before"].(string)
		until, _ := opts["until"].(string)
		start := 0
		if before != "" {
			for i, s := range c.order {
				if s == before {
					start = i + 1
				}
			}
		}
		out := []map[string]any{}
		for _, s := range c.order[start:] {
			if c.honorUntil && s == until {
				break
			}
			if len(out) == limit {
				break
			}
			tx := c.txs[s]
			var e any
			if tx.listErr {
				e = map[string]any{"InstructionError": []any{0, "x"}}
			}
			out = append(out, map[string]any{"signature": s, "slot": tx.slot, "err": e, "confirmationStatus": "finalized"})
		}
		return out, nil
	case "getTransaction":
		var sig string
		var opts map[string]any
		_ = json.Unmarshal(p[0], &sig)
		_ = json.Unmarshal(p[1], &opts)
		c.txRequests = append(c.txRequests, opts)
		tx, ok := c.txs[sig]
		if !ok {
			return nil, nil
		}
		return tx.json(sig), nil
	}
	return nil, &RPCError{Code: -32601, Message: "method not found"}
}

func newTestSolScanner(t *testing.T, c *solChain, pageLimit int) *SolanaRPCScanner {
	_, srv := newFakeRPC(t, c.handle)
	s, err := NewSolanaScanner(SolanaScannerConfig{Network: SolanaMainnet, RPC: RPCOptions{URL: srv.URL}, PageLimit: pageLimit})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func usdcAsset() Asset { return MainnetAssets()[2] }

func TestSolanaScanTokenAccountPagination(t *testing.T) {
	owner := sKey("alice")
	ata, err := SolanaATA(owner, sUSDC, SolanaTokenProgram)
	if err != nil {
		t.Fatal(err)
	}
	other := sKey("other-acct")
	payer := sKey("payer")
	payerAcct := sKey("payer-acct")
	in := func(slot uint64, pre, post string) fakeSolTx {
		tx := fakeSolTx{slot: slot,
			post: []tb{{ata, sUSDC, owner, "", post}, {payerAcct, sUSDC, payer, "", "0"}},
			pre:  []tb{{payerAcct, sUSDC, payer, "", "100000000"}}}
		if pre != "" {
			tx.pre = append(tx.pre, tb{ata, sUSDC, owner, "", pre})
		}
		return tx
	}
	c := &solChain{txs: map[string]fakeSolTx{}}
	add := func(name string, tx fakeSolTx) {
		c.order = append([]string{sSig(name)}, c.order...) // newest first
		c.txs[sSig(name)] = tx
	}
	add("s0", in(10, "", "1"))       // older than untilSig: must not be returned
	add("s1", in(11, "1", "2"))      // older than untilSig
	add("s2", in(12, "2", "3"))      // untilSig
	add("s3", in(13, "", "5000000")) // account created in this tx: +5 USDC
	add("s4", fakeSolTx{slot: 14, listErr: true, failed: true, post: []tb{{ata, sUSDC, owner, "", "999999999"}}})
	add("s5", func() fakeSolTx { tx := in(15, "5000000", "6000000"); tx.failed = true; return tx }())                             // meta.err only
	add("s6", fakeSolTx{slot: 16, pre: []tb{{ata, sUSDC, owner, "", "5000000"}}, post: []tb{{ata, sUSDC, owner, "", "4000000"}}}) // outflow
	add("s7", in(17, "4000000", "4000001"))                                                                                       // +1 raw unit
	add("s8", fakeSolTx{slot: 18,                                                                                                 // unrelated account changes owner in the same tx: must not break our scan
		pre:  []tb{{ata, sUSDC, owner, "", "4000001"}, {other, sUSDT, sKey("x"), "", "1"}},
		post: []tb{{ata, sUSDC, owner, "", "4000011"}, {other, sUSDT, sKey("y"), "", "1"}}})
	add("s9", fakeSolTx{slot: 19, // another mint/owner only: no change for our account
		pre:  []tb{{ata, sUSDC, owner, "", "4000011"}},
		post: []tb{{ata, sUSDC, owner, "", "4000011"}, {other, sUSDT, sKey("y"), "", "7"}}})

	for _, honor := range []bool{true, false} {
		c.honorUntil = honor
		c.sigRequests, c.txRequests = nil, nil
		s := newTestSolScanner(t, c, 3)
		obs, newest, err := s.ScanTokenAccount(context.Background(), owner, ata, usdcAsset(), sSig("s2"), CommitmentFinalized)
		if err != nil {
			t.Fatal(err)
		}
		if newest != sSig("s9") {
			t.Fatalf("newest = %s", newest)
		}
		if len(c.sigRequests) != 3 {
			t.Fatalf("honorUntil=%v: want 3 signature pages, got %d", honor, len(c.sigRequests))
		}
		if c.sigRequests[0]["until"] != sSig("s2") || c.sigRequests[1]["before"] != sSig("s7") || c.sigRequests[0]["commitment"] != "finalized" {
			t.Fatalf("page params %v", c.sigRequests)
		}
		want := []struct {
			sig   string
			units int64
		}{{"s3", 5_000_000}, {"s7", 1}, {"s8", 10}}
		if len(obs) != len(want) {
			t.Fatalf("got %d obs: %+v", len(obs), obs)
		}
		for i, w := range want {
			o := obs[i]
			if o.TxHash != sSig(w.sig) || o.Units.Int64() != w.units || o.EventKey != "acct:"+ata || o.TokenAccount != ata ||
				o.ToAddress != owner || !o.Finalized || o.Asset.Symbol != "USDC" || o.Network != SolanaMainnet {
				t.Errorf("obs %d = %+v want %v", i, o, w)
			}
		}
		if obs[0].FromAddress != payer || obs[0].BlockNumber != 13 || obs[0].BlockTime.Unix() != 1_790_000_013 {
			t.Errorf("metadata: %+v", obs[0])
		}
		// s4 failed in the signature list: never fetched.
		if len(c.txRequests) != 6 {
			t.Errorf("getTransaction calls = %d, want 6 (s3,s5,s6,s7,s8,s9)", len(c.txRequests))
		}
		for _, r := range c.txRequests {
			if r["encoding"] != "jsonParsed" || r["maxSupportedTransactionVersion"] != float64(1) || r["commitment"] != "finalized" {
				t.Fatalf("getTransaction params %v", r)
			}
		}
	}

	// Nothing newer than untilSig: newest stays untilSig.
	c.honorUntil = true
	s := newTestSolScanner(t, c, 3)
	obs, newest, err := s.ScanTokenAccount(context.Background(), owner, ata, usdcAsset(), sSig("s9"), CommitmentFinalized)
	if err != nil || len(obs) != 0 || newest != sSig("s9") {
		t.Fatalf("no-new case: %v %q %v", obs, newest, err)
	}
	// Full history.
	obs, newest, err = s.ScanTokenAccount(context.Background(), owner, ata, usdcAsset(), "", CommitmentConfirmed)
	if err != nil || len(obs) != 6 || newest != sSig("s9") || obs[0].Finalized {
		t.Fatalf("full history: %d %q %v", len(obs), newest, err)
	}
}

func TestSolanaNullTransactionIsError(t *testing.T) {
	owner := sKey("bob")
	ata, _ := SolanaATA(owner, sUSDC, SolanaTokenProgram)
	c := &solChain{honorUntil: true, order: []string{sSig("missing")}, txs: map[string]fakeSolTx{}}
	// Listed but getTransaction returns null.
	c.txs[sSig("listed")] = fakeSolTx{}
	c.order = []string{sSig("missing")}
	s := newTestSolScanner(t, c, 1000)
	_, _, err := s.ScanTokenAccount(context.Background(), owner, ata, usdcAsset(), "", CommitmentFinalized)
	if !errors.Is(err, ErrTxNotFound) {
		t.Fatalf("null getTransaction must be ErrTxNotFound, got %v", err)
	}
	if _, err := s.ObservationsForTx(context.Background(), sSig("missing"), []string{owner}, CommitmentFinalized); !errors.Is(err, ErrTxNotFound) {
		t.Fatalf("ObservationsForTx null: %v", err)
	}
}

func TestSolanaScanRejectsBadInput(t *testing.T) {
	c := &solChain{txs: map[string]fakeSolTx{}}
	s := newTestSolScanner(t, c, 10)
	owner := sKey("o")
	ata, _ := SolanaATA(owner, sUSDC, SolanaTokenProgram)
	ctx := context.Background()
	if _, _, err := s.ScanTokenAccount(ctx, owner, ata, usdcAsset(), "", "processed"); err == nil {
		t.Error("processed commitment must be rejected")
	}
	fake := usdcAsset()
	fake.Contract = sKey("fake-mint")
	if _, _, err := s.ScanTokenAccount(ctx, owner, ata, fake, "", CommitmentFinalized); err == nil {
		t.Error("non-whitelisted asset must be rejected")
	}
	if _, _, err := s.ScanTokenAccount(ctx, "bad", ata, usdcAsset(), "", CommitmentFinalized); err == nil {
		t.Error("bad owner must be rejected")
	}
	if _, err := NewSolanaScanner(SolanaScannerConfig{Network: SolanaDevnet, RPC: RPCOptions{URL: "http://x"}}); err == nil {
		t.Error("devnet without injected assets must fail")
	}
	devMint := sKey("devnet-usdc")
	if _, err := NewSolanaScanner(SolanaScannerConfig{Network: SolanaDevnet, RPC: RPCOptions{URL: "http://x"},
		Assets: []Asset{{Network: SolanaDevnet, Symbol: "USDC", Contract: devMint, TokenProgram: SolanaTokenProgram, Decimals: 6}}}); err != nil {
		t.Errorf("devnet injected assets: %v", err)
	}
}

func TestSolanaObservationsForTx(t *testing.T) {
	alice, bob, eve := sKey("alice"), sKey("bob"), sKey("eve")
	aliceUSDC, _ := SolanaATA(alice, sUSDC, SolanaTokenProgram)
	bobUSDT, _ := SolanaATA(bob, sUSDT, SolanaTokenProgram)
	aliceFake := sKey("alice-fake-acct")
	eveUSDC, _ := SolanaATA(eve, sUSDC, SolanaTokenProgram)
	aliceNonATA := sKey("alice-aux-usdc")
	c := &solChain{txs: map[string]fakeSolTx{
		sSig("multi"): {slot: 50,
			pre: []tb{{eveUSDC, sUSDC, eve, "", "100"}, {aliceNonATA, sUSDC, alice, "", "5"}},
			post: []tb{
				{aliceUSDC, sUSDC, alice, "", "30"},
				{bobUSDT, sUSDT, bob, "", "40"},
				{aliceFake, sKey("fake-mint"), alice, "", "1000"},                      // fake token: ignored
				{sKey("alice-t22"), sUSDC, alice, sKey("token-2022-impostor"), "1000"}, // wrong program: ignored
				{eveUSDC, sUSDC, eve, "", "30"},                                        // not watched, outflow
				{aliceNonATA, sUSDC, alice, "", "5"},                                   // unchanged
			}},
		sSig("failed"): {slot: 51, failed: true, post: []tb{{aliceUSDC, sUSDC, alice, "", "1"}}},
	}}
	s := newTestSolScanner(t, c, 10)
	ctx := context.Background()
	obs, err := s.ObservationsForTx(ctx, sSig("multi"), []string{alice, bob}, CommitmentFinalized)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 2 {
		t.Fatalf("got %+v", obs)
	}
	byAcct := map[string]Observation{}
	for _, o := range obs {
		byAcct[o.TokenAccount] = o
	}
	if o := byAcct[aliceUSDC]; o.Units.Int64() != 30 || o.ToAddress != alice || o.Asset.Symbol != "USDC" || o.FromAddress != eve {
		t.Errorf("alice: %+v", o)
	}
	if o := byAcct[bobUSDT]; o.Units.Int64() != 40 || o.ToAddress != bob || o.Asset.Symbol != "USDT" || o.EventKey != "acct:"+bobUSDT {
		t.Errorf("bob: %+v", o)
	}
	if obs, err := s.ObservationsForTx(ctx, sSig("failed"), []string{alice}, CommitmentFinalized); err != nil || len(obs) != 0 {
		t.Fatalf("failed tx: %v %v", obs, err)
	}
	if _, err := s.ObservationsForTx(ctx, "not-a-sig", []string{alice}, CommitmentFinalized); err == nil {
		t.Fatal("bad signature must be rejected")
	}
}

// A real finalized mainnet version-1 transaction (getTransaction jsonParsed,
// fetched 2026-11-16): owner FJnaii... receives 104.319138 USDT on its ATA
// F2zDmA... from UUUXQ...; wSOL changes in the same tx must be ignored.
func TestSolanaObservationsForTxRealMainnetV1(t *testing.T) {
	raw, err := os.ReadFile("testdata/solana_mainnet_usdt_tx_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var txJSON any
	if err := json.Unmarshal(raw, &txJSON); err != nil {
		t.Fatal(err)
	}
	const sig = "2dAzhnpa3tEjDXnWWzG3eHsX25D9UmtYe3qozm7pF8yAJhcRZBGTAGkkenXHzduF6WAB5YwZQxydWVXYjpPSsn9C"
	_, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
		if method == "getTransaction" {
			return txJSON, nil
		}
		return nil, &RPCError{Code: -32601, Message: "nope"}
	})
	s, err := NewSolanaScanner(SolanaScannerConfig{Network: SolanaMainnet, RPC: RPCOptions{URL: srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	owners := []string{"FJnaiidSLXFweWkgbinxEHRykVHsnkzDcYbNDR3RF5LN", "297YyGytdAq2sDHh5gkpmeapbHEgbEBMfXUeAiUNGktV"}
	obs, err := s.ObservationsForTx(context.Background(), sig, owners, CommitmentFinalized)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 {
		t.Fatalf("got %+v", obs)
	}
	o := obs[0]
	if o.TokenAccount != "F2zDmA4GLsHtSDtEAFo9wWr5VBHLXtZjYzhRoGybWsFM" || o.ToAddress != owners[0] || o.Units.Int64() != 104319138 ||
		o.Asset.Symbol != "USDT" || o.FromAddress != "UUUXQdoC85FmgUgDMfYj3UsBWMLSjU7gVj4QmYGdMRx" ||
		o.BlockNumber != 469958832 || o.BlockTime.Unix() != 1794795018 {
		t.Fatalf("got %+v", o)
	}
	ata, _ := SolanaATA(owners[0], sUSDT, SolanaTokenProgram)
	if ata != o.TokenAccount {
		t.Fatalf("token account is not the owner's ATA: %s vs %s", ata, o.TokenAccount)
	}
}

func TestSolanaNullSignaturePageIsError(t *testing.T) {
	owner := sKey("carol")
	ata, _ := SolanaATA(owner, sUSDC, SolanaTokenProgram)
	c := &solChain{honorUntil: true, txs: map[string]fakeSolTx{}}
	for _, n := range []string{"n1", "n2", "n3"} {
		c.order = append([]string{sSig(n)}, c.order...)
		c.txs[sSig(n)] = fakeSolTx{slot: 1, post: []tb{{ata, sUSDC, owner, "", "1"}}, pre: []tb{}}
	}
	page := 0
	_, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
		if method == "getSignaturesForAddress" {
			page++
			if page == 2 {
				return nil, nil // provider glitch mid-walk
			}
		}
		return c.handle(method, params)
	})
	s, err := NewSolanaScanner(SolanaScannerConfig{Network: SolanaMainnet, RPC: RPCOptions{URL: srv.URL}, PageLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	obs, newest, err := s.ScanTokenAccount(context.Background(), owner, ata, usdcAsset(), "", CommitmentFinalized)
	if err == nil {
		t.Fatalf("null page must fail the scan, got %d obs newest %q", len(obs), newest)
	}
}

func TestSolanaMissingBalanceMetadataIsError(t *testing.T) {
	owner := sKey("dave")
	ata, _ := SolanaATA(owner, sUSDC, SolanaTokenProgram)
	sig := sSig("nometa")
	_, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
		tx, _ := fakeSolTx{slot: 3, post: []tb{{ata, sUSDC, owner, "", "50"}}}.json(sig).(map[string]any)
		meta, _ := tx["meta"].(map[string]any)
		meta["preTokenBalances"] = nil // pre list missing: must not be read as zero
		return tx, nil
	})
	s, _ := NewSolanaScanner(SolanaScannerConfig{Network: SolanaMainnet, RPC: RPCOptions{URL: srv.URL}})
	if _, err := s.ObservationsForTx(context.Background(), sig, []string{owner}, CommitmentFinalized); err == nil {
		t.Fatal("missing preTokenBalances must be an error")
	}
}

func TestSolanaResponseForOtherTxIsError(t *testing.T) {
	owner := sKey("erin")
	ata, _ := SolanaATA(owner, sUSDC, SolanaTokenProgram)
	_, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
		return fakeSolTx{slot: 3, pre: []tb{}, post: []tb{{ata, sUSDC, owner, "", "50"}}}.json(sSig("A")), nil
	})
	s, _ := NewSolanaScanner(SolanaScannerConfig{Network: SolanaMainnet, RPC: RPCOptions{URL: srv.URL}})
	if _, err := s.ObservationsForTx(context.Background(), sSig("B"), []string{owner}, CommitmentFinalized); err == nil {
		t.Fatal("response for another signature must be rejected")
	}
}

// An attacker re-assigns a token account (any mint) to our owner in the same tx
// as a real USDC deposit: the manual rescan must still see the real deposit.
func TestSolanaObservationsForTxIgnoresHijackedNonATA(t *testing.T) {
	owner, attacker := sKey("frank"), sKey("attacker")
	ata, _ := SolanaATA(owner, sUSDC, SolanaTokenProgram)
	junkUSDC, junkFake := sKey("junk-usdc"), sKey("junk-fake")
	sig := sSig("hijack")
	c := &solChain{txs: map[string]fakeSolTx{sig: {slot: 9,
		pre:  []tb{{junkUSDC, sUSDC, attacker, "", "1"}, {junkFake, sKey("fake-mint"), attacker, "", "1"}},
		post: []tb{{ata, sUSDC, owner, "", "70"}, {junkUSDC, sUSDC, owner, "", "1"}, {junkFake, sKey("fake-mint"), owner, "", "1"}}}}}
	s := newTestSolScanner(t, c, 10)
	obs, err := s.ObservationsForTx(context.Background(), sig, []string{owner}, CommitmentFinalized)
	if err != nil || len(obs) != 1 || obs[0].TokenAccount != ata || obs[0].Units.Int64() != 70 {
		t.Fatalf("got %+v %v", obs, err)
	}
}

func TestSolanaObservationsForTxNonATAAndMalformed(t *testing.T) {
	owner := sKey("gina")
	aux := sKey("gina-aux-usdc") // a non-ATA USDC account owned by our owner
	ok, bad := sSig("nonata"), sSig("malformed")
	c := &solChain{txs: map[string]fakeSolTx{
		ok: {slot: 1, pre: []tb{{aux, sUSDC, owner, "", "5"}}, post: []tb{{aux, sUSDC, owner, "", "8"}}},
		// Missing programId on an account (RPC fault): must be an error, not "no inflow".
		bad: {slot: 2, pre: []tb{{aux, sUSDC, owner, "", "5"}}, post: []tb{{aux, sUSDC, owner, "", "8"}}},
	}}
	_, srv := newFakeRPC(t, func(method string, params json.RawMessage) (any, *RPCError) {
		res, rerr := c.handle(method, params)
		if tx, _ := res.(map[string]any); tx != nil && tx["slot"] == uint64(2) {
			meta, _ := tx["meta"].(map[string]any)
			post, _ := meta["postTokenBalances"].([]map[string]any)
			post[0]["programId"] = ""
		}
		return res, rerr
	})
	s, _ := NewSolanaScanner(SolanaScannerConfig{Network: SolanaMainnet, RPC: RPCOptions{URL: srv.URL}})
	obs, err := s.ObservationsForTx(context.Background(), ok, []string{owner}, CommitmentFinalized)
	if err != nil || len(obs) != 1 || obs[0].TokenAccount != aux || obs[0].Units.Int64() != 3 {
		t.Fatalf("non-ATA inflow: %+v %v", obs, err)
	}
	if _, err := s.ObservationsForTx(context.Background(), bad, []string{owner}, CommitmentFinalized); err == nil {
		t.Fatal("malformed balance metadata must be an error")
	}
}
