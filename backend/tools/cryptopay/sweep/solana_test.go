package sweep

import (
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"testing"

	solana "github.com/gagliardetto/solana-go"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
)

func TestProgramIDConstantsMatchLibrary(t *testing.T) {
	for got, want := range map[string]solana.PublicKey{
		SystemProgramID:              solana.SystemProgramID,
		ComputeBudgetProgramID:       solana.ComputeBudget,
		SysvarRecentBlockhashes:      solana.SysVarRecentBlockHashesPubkey,
		SysvarRent:                   solana.SysVarRentPubkey,
		chain.SolanaTokenProgram:     solana.TokenProgramID,
		chain.AssociatedTokenProgram: solana.SPLAssociatedTokenAccountProgramID,
	} {
		if got != want.String() {
			t.Errorf("%s != %s", got, want)
		}
	}
}

func TestSolanaReservedIndicesNeverSources(t *testing.T) {
	for _, idx := range []uint32{SolanaSignerIndex, SolanaTreasuryIndex, SolanaPoolIndexLimit, 1<<31 - 1} {
		if _, err := kr(t).SolanaPool(idx); err == nil {
			t.Fatalf("index %d accepted as pool source", idx)
		}
		p := solSweepPlan(t)
		k, _ := kr(t).solAt(idx)
		p.Solana.Txs[0].Transfers[0].Index = idx
		p.Solana.Txs[0].Transfers[0].Owner = k.Address
		if _, err := BuildSolanaSweep(p, kr(t), registered(testNonceAcct1)); err == nil {
			t.Fatalf("plan with reserved index %d accepted", idx)
		}
	}
	if _, err := kr(t).SolanaPool(SolanaPoolIndexLimit - 1); err != nil {
		t.Fatal(err)
	}
}

func signSol(t *testing.T, p *Plan, reg func(string) bool) (*SolanaOp, []SignedTx) {
	t.Helper()
	op, err := BuildSolanaSweep(p, kr(t), reg)
	if err != nil {
		t.Fatalf("BuildSolanaSweep: %v", err)
	}
	st, err := SignSolana(op, kr(t))
	if err != nil {
		t.Fatalf("SignSolana: %v", err)
	}
	return op, st
}

// Group 5: durable nonce lifecycle.
func TestSolanaNonceLifecycle(t *testing.T) {
	p := solSweepPlan(t)
	if _, err := BuildSolanaSweep(p, kr(t), registered()); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unregistered nonce account must be refused: %v", err)
	}
	op, st := signSol(t, p, registered(testNonceAcct1))
	raw, _ := base64.StdEncoding.DecodeString(st[0].Raw)
	if len(raw) > SolanaMaxPacketBytes {
		t.Fatalf("tx %d bytes", len(raw))
	}
	w, err := ParseSolanaWire(raw)
	if err != nil {
		t.Fatal(err)
	}
	if w.AdvanceNonceAccount() != testNonceAcct1 || w.Blockhash != testNonceVal1 {
		t.Fatal("first instruction must advance the registered nonce; blockhash must be the nonce value")
	}
	if w.Instructions[0].Accounts[2].Key != solTreasury(t) || !w.Instructions[0].Accounts[2].Signer {
		t.Fatal("nonce authority must be the treasury and sign")
	}
	if w.Keys[0] != solTreasury(t) || w.Signature != st[0].Hash {
		t.Fatal("fee payer must be the treasury")
	}
	// fee = 3 signatures * 5000 + ceil(200000*10000/1e6)=2000
	if op.Budget.SolanaFeeLamports != "17000" || op.Budget.SolanaRentLamports != "2039280" {
		t.Fatalf("budget %+v", op.Budget)
	}
	// Two nonce accounts must differ; a nonce account may not be used twice.
	p2 := solSweepPlan(t)
	p2.Solana.Txs = append(p2.Solana.Txs, SolanaTxPlan{NonceAccount: testNonceAcct1, NonceValue: testNonceVal2, Transfers: []SolanaTransfer{
		{Index: 2, Owner: solOwner(t, 2), Symbol: "USDC", Mint: usdcSol.Contract, Amount: "5"},
	}})
	if _, err := BuildSolanaSweep(p2, kr(t), registered(testNonceAcct1, testNonceAcct2)); err == nil {
		t.Fatal("same nonce account twice must be refused")
	}
	p2.Solana.Txs[1].NonceAccount = testNonceAcct2
	if _, st := signSol(t, p2, registered(testNonceAcct1, testNonceAcct2)); len(st) != 2 {
		t.Fatal("want 2 txs")
	}
	// Register at most 5.
	l := NewLedger()
	for i := 0; i < SolanaMaxRegisteredNonces; i++ {
		if err := l.RegisterNonceAccount(fakeKey(fmt.Sprint("n", i)), t0); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.RegisterNonceAccount(fakeKey("n9"), t0); err == nil {
		t.Fatal("6th nonce account accepted")
	}
	if err := l.RegisterNonceAccount(fakeKey("n0"), t0); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestSolanaTxSizeLimit(t *testing.T) {
	p := solSweepPlan(t)
	p.Solana.Txs[0].Transfers = nil
	for i := uint32(0); i < 12; i++ {
		p.Solana.Txs[0].Transfers = append(p.Solana.Txs[0].Transfers, SolanaTransfer{Index: i, Owner: solOwner(t, i), Symbol: "USDT", Mint: usdtSol.Contract, Amount: "1"})
	}
	op, err := BuildSolanaSweep(p, kr(t), registered(testNonceAcct1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SignSolana(op, kr(t)); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("oversized tx must be refused: %v", err)
	}
	// Find the largest packable count and check it signs and fits.
	for n := 11; n > 0; n-- {
		op.Txs[0].Transfers = op.Txs[0].Transfers[:n]
		size, err := SolanaTxSize(&op.Txs[0])
		if err != nil {
			t.Fatal(err)
		}
		if size <= SolanaMaxPacketBytes {
			st, err := SignSolana(op, kr(t))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := base64.StdEncoding.DecodeString(st[0].Raw)
			if len(raw) != size {
				t.Fatalf("SolanaTxSize %d != signed %d", size, len(raw))
			}
			t.Logf("max sources per tx (1 mint, with ATA create): %d (%d bytes)", n, size)
			return
		}
	}
	t.Fatal("nothing fits")
}

func TestSolanaBudgetBoundaries(t *testing.T) {
	// priority fee rounding: ceil(200000*price/1e6)
	for price, want := range map[uint64]int64{0: 5000, 1: 5001, 5: 5001, 6: 5002, 50_000: 15_000} {
		if got := SolanaTxFeeLamports(1, price).Int64(); got != want {
			t.Errorf("price %d: fee %d want %d", price, got, want)
		}
	}
	p := solSweepPlan(t)
	p.Solana.CUPriceMicros = "50000"
	if _, err := BuildSolanaSweep(p, kr(t), registered(testNonceAcct1)); err != nil {
		t.Fatalf("cap price must pass: %v", err)
	}
	p.Solana.CUPriceMicros = "50001"
	if _, err := BuildSolanaSweep(p, kr(t), registered(testNonceAcct1)); err == nil {
		t.Fatal("price above cap accepted")
	}
	// 6 txs refused
	q := solSweepPlan(t)
	var regs []string
	q.Solana.Txs = nil
	for i := 0; i < SolanaMaxTxsPerOp+1; i++ {
		na := fakeKey(fmt.Sprint("na", i))
		regs = append(regs, na)
		q.Solana.Txs = append(q.Solana.Txs, SolanaTxPlan{NonceAccount: na, NonceValue: testNonceVal1, Transfers: []SolanaTransfer{
			{Index: uint32(i), Owner: solOwner(t, uint32(i)), Symbol: "USDC", Mint: usdcSol.Contract, Amount: "1"},
		}})
	}
	if _, err := BuildSolanaSweep(q, kr(t), registered(regs...)); err == nil {
		t.Fatal("6 txs accepted")
	}
	q.Solana.Txs = q.Solana.Txs[:SolanaMaxTxsPerOp]
	if _, err := BuildSolanaSweep(q, kr(t), registered(regs...)); err != nil {
		t.Fatal(err)
	}
	// Cap on fees+rent (cumulative budgets use the same check).
	at := Budget{SolanaFeeLamports: "7960720", SolanaRentLamports: "2039280"}
	if err := at.CheckSolana(); err != nil {
		t.Fatalf("exactly 0.01 SOL must pass: %v", err)
	}
	over := Budget{SolanaFeeLamports: "7960721", SolanaRentLamports: "2039280"}
	if err := over.CheckSolana(); err == nil {
		t.Fatal("0.01 SOL + 1 lamport accepted")
	}
	// Duplicate ATA creation / creation without transfer of that mint.
	d := solSweepPlan(t)
	d.Solana.Txs[0].CreateTreasuryATA = []string{"USDT", "USDT"}
	if _, err := BuildSolanaSweep(d, kr(t), registered(testNonceAcct1)); err == nil {
		t.Fatal("duplicate create accepted")
	}
	d.Solana.Txs[0].CreateTreasuryATA = []string{"USDC"}
	if _, err := BuildSolanaSweep(d, kr(t), registered(testNonceAcct1)); err == nil {
		t.Fatal("create for an unused mint accepted")
	}
	// Duplicate source/mint, zero and u64-overflow amounts.
	e := solSweepPlan(t)
	e.Solana.Txs[0].Transfers[1] = e.Solana.Txs[0].Transfers[0]
	if _, err := BuildSolanaSweep(e, kr(t), registered(testNonceAcct1)); err == nil {
		t.Fatal("duplicate source accepted")
	}
	for _, amt := range []string{"0", "18446744073709551616", "-1", "01"} {
		e := solSweepPlan(t)
		e.Solana.Txs[0].Transfers[0].Amount = amt
		if _, err := BuildSolanaSweep(e, kr(t), registered(testNonceAcct1)); err == nil {
			t.Fatalf("amount %s accepted", amt)
		}
	}
}

// Group 4 (Solana): tampering never redirects funds.
func TestSolanaPlanTamperingNeverRedirects(t *testing.T) {
	atk, _ := otherKR(t).SolanaTreasury()
	atkSrc, _ := otherKR(t).SolanaPool(0)
	reg := registered(testNonceAcct1)
	for name, mut := range map[string]func(p *Plan){
		"treasury replaced":      func(p *Plan) { p.Solana.Treasury = atk.Address },
		"owner not from index":   func(p *Plan) { p.Solana.Txs[0].Transfers[0].Owner = solOwner(t, 7) },
		"foreign owner":          func(p *Plan) { p.Solana.Txs[0].Transfers[0].Owner = atkSrc.Address },
		"mint replaced":          func(p *Plan) { p.Solana.Txs[0].Transfers[0].Mint = fakeKey("evil-mint") },
		"symbol/mint swapped":    func(p *Plan) { p.Solana.Txs[0].Transfers[0].Mint = usdcSol.Contract },
		"unregistered nonce":     func(p *Plan) { p.Solana.Txs[0].NonceAccount = testNonceAcct2 },
		"nonce value not base58": func(p *Plan) { p.Solana.Txs[0].NonceValue = "0OIl" },
		"treasury index as owner": func(p *Plan) {
			p.Solana.Txs[0].Transfers[0].Index = SolanaTreasuryIndex
			p.Solana.Txs[0].Transfers[0].Owner = solTreasury(t)
		},
		"devnet": func(p *Plan) { p.Network = chain.SolanaDevnet },
	} {
		p := solSweepPlan(t)
		mut(p)
		if _, err := BuildSolanaSweep(p, kr(t), reg); err == nil {
			t.Errorf("%s: tampered plan accepted", name)
		}
	}
	// Free fields: amounts, nonce value, price. Every transfer must pay the treasury ATA.
	p := solSweepPlan(t)
	p.Solana.Txs[0].Transfers[0].Amount = "18446744073709551615"
	p.Solana.Txs[0].NonceValue = testNonceVal2
	op, st := signSol(t, p, reg)
	raw, _ := base64.StdEncoding.DecodeString(st[0].Raw)
	w, err := VerifySolanaSigned(raw, &op.Txs[0])
	if err != nil {
		t.Fatal(err)
	}
	tATA, _ := chain.SolanaATA(solTreasury(t), usdtSol.Contract, usdtSol.TokenProgram)
	n := 0
	for _, ix := range w.Instructions {
		if ix.Program == chain.SolanaTokenProgram {
			n++
			if ix.Accounts[2].Key != tATA {
				t.Fatalf("transfer to %s, not the treasury ATA", ix.Accounts[2].Key)
			}
		}
		if ix.Program != SystemProgramID && ix.Program != ComputeBudgetProgramID && ix.Program != chain.SolanaTokenProgram && ix.Program != chain.AssociatedTokenProgram {
			t.Fatalf("unexpected program %s", ix.Program)
		}
	}
	if n != 2 {
		t.Fatalf("%d transfers", n)
	}
	// Verification catches a mutated intent.
	bad := op.Txs[0]
	bad.Transfers = append([]SolanaTransferIntent(nil), bad.Transfers...)
	bad.Transfers[0].Amount = 1
	if _, err := VerifySolanaSigned(raw, &bad); err == nil {
		t.Fatal("amount mismatch not detected")
	}
	bad = op.Txs[0]
	bad.NonceValue = testNonceVal1
	if _, err := VerifySolanaSigned(raw, &bad); err == nil {
		t.Fatal("nonce mismatch not detected")
	}
	// A flipped signature byte fails.
	raw2 := append([]byte(nil), raw...)
	raw2[5] ^= 1
	if _, err := ParseSolanaWire(raw2); err == nil {
		t.Fatal("bad signature accepted")
	}
	// Trailing byte fails.
	if _, err := ParseSolanaWire(append(append([]byte(nil), raw...), 0)); err == nil {
		t.Fatal("trailing byte accepted")
	}
}

func TestSolanaSendTargetsRegisteredDestinationOnly(t *testing.T) {
	dest := Destination{Label: "binance-sol", Network: chain.SolanaMainnet, Address: solOwner(t, 4000)}
	p := &Plan{Version: 1, Kind: KindSend, Network: chain.SolanaMainnet, Solana: &SolanaPlan{
		Treasury: solTreasury(t), CUPriceMicros: "1000",
		Send: &SolanaSendPlan{NonceAccount: testNonceAcct1, NonceValue: testNonceVal1, Symbol: "USDT", Mint: usdtSol.Contract, TreasuryBalance: "4300000"},
	}}
	op, err := BuildSolanaSend(p, kr(t), registered(testNonceAcct1), dest, big.NewInt(4_300_000))
	if err != nil {
		t.Fatal(err)
	}
	st, err := SignSolana(op, kr(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(st[0].Raw)
	w, err := ParseSolanaWire(raw)
	if err != nil {
		t.Fatal(err)
	}
	dATA, _ := chain.SolanaATA(dest.Address, usdtSol.Contract, usdtSol.TokenProgram)
	last := w.Instructions[len(w.Instructions)-1]
	if last.Accounts[2].Key != dATA || last.Accounts[3].Key != solTreasury(t) || w.NumSigners != 1 {
		t.Fatal("send must be treasury-signed to the registered destination's ATA")
	}
	if op.Budget.SolanaFeeLamports != "5200" {
		t.Fatalf("fee %s", op.Budget.SolanaFeeLamports)
	}
	// Mismatched hint, PDA destination, treasury as destination, EVM destination.
	p.Solana.Send.DestHint = solOwner(t, 1)
	if _, err := BuildSolanaSend(p, kr(t), registered(testNonceAcct1), dest, big.NewInt(1)); err == nil {
		t.Fatal("hint mismatch accepted")
	}
	p.Solana.Send.DestHint = ""
	pda := Destination{Label: "ata", Network: chain.SolanaMainnet, Address: dATA}
	if _, err := BuildSolanaSend(p, kr(t), registered(testNonceAcct1), pda, big.NewInt(1)); err == nil {
		t.Fatal("token-account (off-curve) destination accepted")
	}
	self := Destination{Label: "self", Network: chain.SolanaMainnet, Address: solTreasury(t)}
	if _, err := BuildSolanaSend(p, kr(t), registered(testNonceAcct1), self, big.NewInt(1)); err == nil {
		t.Fatal("treasury destination accepted")
	}
	if _, err := BuildSolanaSend(p, kr(t), registered(), dest, big.NewInt(1)); err == nil {
		t.Fatal("unregistered nonce accepted")
	}
}
