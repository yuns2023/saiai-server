package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/mr-tron/base58"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/Wei-Shaw/sub2api/tools/cryptopay/sweep"
)

type fakeTokAcct struct {
	mint, owner string
	amount      uint64
}

type fakeSol struct {
	mu       sync.Mutex
	lamports map[string]uint64
	tok      map[string]*fakeTokAcct
	nonces   map[string][2]string // account -> {authority, value}
	statuses map[string]bool
	txs      map[string]map[string]any
	sends    int
	slot     uint64
	failNext bool
}

func newFakeSol() *fakeSol {
	return &fakeSol{lamports: map[string]uint64{}, tok: map[string]*fakeTokAcct{}, nonces: map[string][2]string{}, statuses: map[string]bool{}, txs: map[string]map[string]any{}, slot: 100}
}

func h32(s string) string { x := sha256.Sum256([]byte(s)); return base58.Encode(x[:]) }

func nonceData(auth, val string) string {
	b := make([]byte, 80)
	binary.LittleEndian.PutUint32(b[0:], 1)
	binary.LittleEndian.PutUint32(b[4:], 1)
	a, _ := base58.Decode(auth)
	v, _ := base58.Decode(val)
	copy(b[8:], a)
	copy(b[40:], v)
	return base64.StdEncoding.EncodeToString(b)
}

func (f *fakeSol) handle(method string, p []json.RawMessage) (any, *rpcError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	str := func(i int) string { var s string; _ = json.Unmarshal(p[i], &s); return s }
	switch method {
	case "getAccountInfo":
		a := str(0)
		if t := f.tok[a]; t != nil {
			return map[string]any{"value": map[string]any{"lamports": 2039280, "owner": chain.SolanaTokenProgram, "data": map[string]any{
				"parsed": map[string]any{"type": "account", "info": map[string]any{"mint": t.mint, "owner": t.owner, "state": "initialized",
					"tokenAmount": map[string]any{"amount": fmt.Sprint(t.amount)}}}}}}, nil
		}
		if n, ok := f.nonces[a]; ok {
			return map[string]any{"value": map[string]any{"lamports": 1447680, "owner": sweep.SystemProgramID, "data": []string{nonceData(n[0], n[1]), "base64"}}}, nil
		}
		return map[string]any{"value": nil}, nil
	case "getBalance":
		return map[string]any{"value": f.lamports[str(0)]}, nil
	case "getMinimumBalanceForRentExemption":
		var n int
		_ = json.Unmarshal(p[0], &n)
		return 890880 + 6960*n, nil
	case "getRecentPrioritizationFees":
		return []map[string]any{{"slot": 1, "prioritizationFee": 0}, {"slot": 2, "prioritizationFee": 7000}}, nil
	case "sendTransaction":
		f.sends++
		raw, _ := base64.StdEncoding.DecodeString(str(0))
		w, err := sweep.ParseSolanaWire(raw)
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: "bad tx"}
		}
		if f.statuses[w.Signature] {
			return w.Signature, nil
		}
		na := w.AdvanceNonceAccount()
		if n, ok := f.nonces[na]; !ok || n[1] != w.Blockhash || n[0] != w.Keys[0] {
			return nil, &rpcError{Code: -32002, Message: "Transaction simulation failed: Blockhash not found"}
		}
		pre := f.snapshot(w.Keys)
		if f.failNext {
			f.failNext = false
		} else {
			for _, ix := range w.Instructions {
				if ix.Program == chain.SolanaTokenProgram && ix.Data[0] == 12 {
					amt := binary.LittleEndian.Uint64(ix.Data[1:9])
					src, dst := f.tok[ix.Accounts[0].Key], ix.Accounts[2].Key
					if src == nil || src.amount < amt {
						return nil, &rpcError{Code: -32002, Message: "Transaction simulation failed: insufficient funds"}
					}
					src.amount -= amt
					if f.tok[dst] == nil {
						f.tok[dst] = &fakeTokAcct{mint: ix.Accounts[1].Key, owner: w.Keys[0]}
					}
					f.tok[dst].amount += amt
				}
			}
		}
		f.nonces[na] = [2]string{f.nonces[na][0], h32(w.Signature)}
		f.slot++
		f.statuses[w.Signature] = true
		f.txs[w.Signature] = map[string]any{
			"meta":        map[string]any{"err": nil, "preTokenBalances": pre, "postTokenBalances": f.snapshot(w.Keys)},
			"transaction": map[string]any{"message": map[string]any{"accountKeys": w.Keys}},
		}
		return w.Signature, nil
	case "getSignatureStatuses":
		var sigs []string
		_ = json.Unmarshal(p[0], &sigs)
		if f.statuses[sigs[0]] {
			return map[string]any{"value": []any{map[string]any{"err": nil, "confirmationStatus": "finalized", "slot": f.slot}}}, nil
		}
		return map[string]any{"value": []any{nil}}, nil
	case "getTransaction":
		return f.txs[str(0)], nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found " + method}
}

func (f *fakeSol) snapshot(keys []string) []map[string]any {
	out := []map[string]any{}
	for i, k := range keys {
		if t := f.tok[k]; t != nil {
			out = append(out, map[string]any{"accountIndex": i, "mint": t.mint, "owner": t.owner, "uiTokenAmount": map[string]any{"amount": fmt.Sprint(t.amount)}})
		}
	}
	return out
}

func TestSolanaPrepareSignBroadcastEndToEnd(t *testing.T) {
	kr := testKR(t)
	tr, _ := kr.SolanaTreasury()
	usdt := chain.MainnetAssets()[3]
	f := newFakeSol()
	f.lamports[tr.Address] = 50_000_000
	var srcs []sourceArg
	for i := uint32(0); i < 7; i++ {
		k, _ := kr.SolanaPool(i)
		ata, _ := chain.SolanaATA(k.Address, usdt.Contract, usdt.TokenProgram)
		f.tok[ata] = &fakeTokAcct{mint: usdt.Contract, owner: k.Address, amount: uint64(1_000_000 + i)}
		srcs = append(srcs, sourceArg{i, k.Address})
	}
	n1, n2 := h32("na1"), h32("na2")
	f.nonces[n1] = [2]string{tr.Address, h32("v1")}
	f.nonces[n2] = [2]string{tr.Address, h32("v2")}
	rpc := solRPC{serveRPC(t, func(m string, p []json.RawMessage) (any, *rpcError) { return f.handle(m, p) })}
	ctx := context.Background()
	plan, err := prepareSolanaSweep(ctx, rpc, tr.Address, srcs, []string{n1, n2}, 0, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Solana.Txs) != 2 || len(plan.Solana.Txs[0].Transfers)+len(plan.Solana.Txs[1].Transfers) != 7 || plan.Solana.CUPriceMicros != "7000" {
		t.Fatalf("packing: %+v", plan.Solana)
	}
	if len(plan.Solana.Txs[0].CreateTreasuryATA) != 1 || len(plan.Solana.Txs[1].CreateTreasuryATA) != 0 {
		t.Fatal("treasury ATA must be created once, in the first tx")
	}
	data, _ := json.Marshal(plan)
	if _, err := sweep.DecodePlan(data); err != nil {
		t.Fatal(err)
	}
	op, err := sweep.BuildSolanaSweep(plan, kr, func(a string) bool { return a == n1 || a == n2 })
	if err != nil {
		t.Fatal(err)
	}
	st, err := sweep.SignSolana(op, kr)
	if err != nil {
		t.Fatal(err)
	}
	b := &sweep.SignedBundle{Version: 1, Network: chain.SolanaMainnet, Kind: sweep.KindSweep, Txs: st}
	res := &result{path: t.TempDir() + "/r.json"}
	if err := broadcastSolana(ctx, rpc, b, res, fastOpts()); err != nil {
		t.Fatal(err)
	}
	tATA, _ := chain.SolanaATA(tr.Address, usdt.Contract, usdt.TokenProgram)
	if f.tok[tATA] == nil || f.tok[tATA].amount != 7_000_021 || f.sends != 2 {
		t.Fatalf("treasury got %+v sends %d", f.tok[tATA], f.sends)
	}
	for _, x := range res.Txs {
		if x.Status != "executed" || !x.Finalized {
			t.Fatalf("%+v", x)
		}
	}
	// Rerun: statuses exist -> nothing sent.
	if err := broadcastSolana(ctx, rpc, b, &result{path: t.TempDir() + "/r.json"}, fastOpts()); err != nil || f.sends != 2 {
		t.Fatalf("rerun %v sends %d", err, f.sends)
	}
	// A replacement signed for the old nonce value: nonce advanced -> stop, nothing sent.
	op.Txs[0].CUPriceMicros = 8000
	st2, err := sweep.SignSolana(op, kr)
	if err != nil {
		t.Fatal(err)
	}
	b2 := &sweep.SignedBundle{Version: 1, Network: chain.SolanaMainnet, Txs: st2[:1]}
	err = broadcastSolana(ctx, rpc, b2, &result{path: t.TempDir() + "/r.json"}, fastOpts())
	if err == nil || !strings.Contains(err.Error(), "can never land") || f.sends != 2 {
		t.Fatalf("stale nonce: %v sends %d", err, f.sends)
	}
	// Effect verification catches a tx whose balances did not move.
	f.nonces[n1] = [2]string{tr.Address, h32("v9")}
	op.Txs = op.Txs[:1]
	op.Txs[0].NonceValue = h32("v9")
	for i := range op.Txs[0].Transfers {
		op.Txs[0].Transfers[i].Amount = 0
	}
	op.Txs[0].Transfers = op.Txs[0].Transfers[:1]
	op.Txs[0].Transfers[0].Amount = 1
	f.tok[op.Txs[0].Transfers[0].SourceATA].amount = 5
	st3, err := sweep.SignSolana(op, kr)
	if err != nil {
		t.Fatal(err)
	}
	f.failNext = true
	err = broadcastSolana(ctx, rpc, &sweep.SignedBundle{Version: 1, Network: chain.SolanaMainnet, Txs: st3}, &result{path: t.TempDir() + "/r.json"}, fastOpts())
	if err == nil || !strings.Contains(err.Error(), "changed by") {
		t.Fatalf("effects: %v", err)
	}
}

func TestSolanaPrepareRefusesForeignNonceAuthority(t *testing.T) {
	kr := testKR(t)
	tr, _ := kr.SolanaTreasury()
	k, _ := kr.SolanaPool(0)
	usdt := chain.MainnetAssets()[3]
	f := newFakeSol()
	f.lamports[tr.Address] = 50_000_000
	ata, _ := chain.SolanaATA(k.Address, usdt.Contract, usdt.TokenProgram)
	f.tok[ata] = &fakeTokAcct{mint: usdt.Contract, owner: k.Address, amount: 5}
	n1 := h32("na1")
	f.nonces[n1] = [2]string{h32("attacker"), h32("v1")}
	rpc := solRPC{serveRPC(t, func(m string, p []json.RawMessage) (any, *rpcError) { return f.handle(m, p) })}
	if _, err := prepareSolanaSweep(context.Background(), rpc, tr.Address, []sourceArg{{0, k.Address}}, []string{n1}, 0, io.Discard); err == nil || !strings.Contains(err.Error(), "authority") {
		t.Fatalf("foreign authority: %v", err)
	}
	// Treasury SOL too low.
	f.nonces[n1] = [2]string{tr.Address, h32("v1")}
	f.lamports[tr.Address] = 900_000
	if _, err := prepareSolanaSweep(context.Background(), rpc, tr.Address, []sourceArg{{0, k.Address}}, []string{n1}, 0, io.Discard); err == nil || !strings.Contains(err.Error(), "too low") {
		t.Fatalf("low SOL: %v", err)
	}
	// Send to a destination without a token account is refused.
	f.lamports[tr.Address] = 50_000_000
	tATA, _ := chain.SolanaATA(tr.Address, usdt.Contract, usdt.TokenProgram)
	f.tok[tATA] = &fakeTokAcct{mint: usdt.Contract, owner: tr.Address, amount: 9}
	dest, _ := kr.SolanaPool(77)
	if _, err := prepareSolanaSend(context.Background(), rpc, tr.Address, "USDT", dest.Address, n1, 0, io.Discard); err == nil || !strings.Contains(err.Error(), "no USDT token account") {
		t.Fatalf("dest without ATA: %v", err)
	}
	dATA, _ := chain.SolanaATA(dest.Address, usdt.Contract, usdt.TokenProgram)
	f.tok[dATA] = &fakeTokAcct{mint: usdt.Contract, owner: dest.Address}
	p, err := prepareSolanaSend(context.Background(), rpc, tr.Address, "USDT", dest.Address, n1, 0, io.Discard)
	if err != nil || p.Solana.Send.TreasuryBalance != "9" || new(big.Int).SetUint64(1).Sign() != 1 {
		t.Fatalf("send plan %v %v", p, err)
	}
}

func TestParseNonceAccount(t *testing.T) {
	good := &solAccountInfo{Owner: sweep.SystemProgramID, Data: json.RawMessage(fmt.Sprintf(`[%q,"base64"]`, nonceData(h32("a"), h32("v"))))}
	ns, err := parseNonceAccount(good)
	if err != nil || ns.authority != h32("a") || ns.value != h32("v") {
		t.Fatalf("%+v %v", ns, err)
	}
	bad := *good
	bad.Owner = chain.SolanaTokenProgram
	if _, err := parseNonceAccount(&bad); err == nil {
		t.Fatal("non-system owner accepted")
	}
	if _, err := parseNonceAccount(nil); err == nil {
		t.Fatal("missing account accepted")
	}
	b := make([]byte, 80)
	bad2 := &solAccountInfo{Owner: sweep.SystemProgramID, Data: json.RawMessage(fmt.Sprintf(`[%q,"base64"]`, base64.StdEncoding.EncodeToString(b)))}
	if _, err := parseNonceAccount(bad2); err == nil {
		t.Fatal("uninitialized accepted")
	}
}
