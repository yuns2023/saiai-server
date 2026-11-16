package sweep

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
)

func evmOp(t *testing.T, p *Plan) *Operation {
	t.Helper()
	op, err := BuildEVMSweep(p, kr(t), EVMCapsFor(false))
	if err != nil {
		t.Fatal(err)
	}
	return &Operation{Network: p.Network, Kind: p.Kind, EVM: op}
}

// signAndCommit runs the same sequence as sweep-sign.
func signAndCommit(t *testing.T, st *LedgerStore, o *Operation, out string) (*LedgerOp, error) {
	t.Helper()
	if err := st.Lock(); err != nil {
		return nil, err
	}
	defer st.Unlock()
	l, err := st.Load()
	if err != nil {
		return nil, err
	}
	admit, err := l.CheckAdmit(o.Network, o.LedgerTxsPreview(), o.Budget())
	if err != nil {
		return nil, err
	}
	if err := o.CheckCumulative(admit.Cumulative, EVMCapsFor(false)); err != nil {
		return nil, err
	}
	signed, ltxs, err := o.Sign(kr(t))
	if err != nil {
		return nil, err
	}
	return CommitSigned(st, l, o, signed, ltxs, out, t0)
}

func load(t *testing.T, st *LedgerStore) *Ledger {
	t.Helper()
	l, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// ---- group 1: atomicity, concurrency, crash windows, corruption --------

func TestLedgerInitAndMissing(t *testing.T) {
	st := newStore(t)
	if err := InitLedger(st.Path); err == nil {
		t.Fatal("init must not overwrite")
	}
	if fi, err := os.Stat(st.Path); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("ledger mode %v %v", fi.Mode(), err)
	}
	missing := &LedgerStore{Path: filepath.Join(t.TempDir(), "nope.json")}
	if _, err := missing.Load(); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing ledger must be an error: %v", err)
	}
}

func TestLedgerCorruptRefuses(t *testing.T) {
	good, _ := json.Marshal(NewLedger())
	cases := map[string]string{
		"garbage":        "not json",
		"empty":          "",
		"trailing":       string(good) + "{}",
		"unknown field":  `{"version":1,"ops":[],"nonce_accounts":[],"destinations":[],"x":1}`,
		"duplicate key":  `{"version":1,"ops":[],"ops":[],"nonce_accounts":[],"destinations":[]}`,
		"wrong version":  `{"version":2,"ops":[],"nonce_accounts":[],"destinations":[]}`,
		"missing ops":    `{"version":1,"nonce_accounts":[],"destinations":[]}`,
		"two pending":    twoPending(t),
		"bad budget":     strings.Replace(twoPendingBase(t, false), `"evm_burn_wei": "`, `"evm_burn_wei": "0`, 1),
		"truncated":      string(good[:len(good)-3]),
		"bad dest label": `{"version":1,"ops":[],"nonce_accounts":[],"destinations":[{"label":"a b","network":"arbitrum-one","address":"0x1111111111111111111111111111111111111111","registered_at":""}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "l.json")
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			st := &LedgerStore{Path: p}
			if _, err := st.Load(); !errors.Is(err, ErrCorruptLedger) {
				t.Fatalf("want ErrCorruptLedger, got %v", err)
			}
			// signing refuses too
			o := evmOp(t, evmSweepPlan(t))
			if _, err := signAndCommit(t, st, o, filepath.Join(t.TempDir(), "s.json")); err == nil {
				t.Fatal("signed with a corrupt ledger")
			}
		})
	}
	// oversized
	p := filepath.Join(t.TempDir(), "big.json")
	if err := os.WriteFile(p, make([]byte, MaxDocumentBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&LedgerStore{Path: p}).Load(); !errors.Is(err, ErrCorruptLedger) {
		t.Fatalf("oversized: %v", err)
	}
}

// twoPendingBase builds a valid one-op ledger JSON (pending=true) or the
// same with a second pending op of the same network (invalid).
func twoPendingBase(t *testing.T, second bool) string {
	l := NewLedger()
	o := evmOp(t, evmSweepPlan(t))
	_, ltxs, err := o.Sign(kr(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Admit(o.Network, o.Kind, "x", ltxs, o.Budget(), t0); err != nil {
		t.Fatal(err)
	}
	if second {
		c := l.Ops[0]
		c.ID = "arb-other"
		c.ChainRoot = c.ID
		l.Ops = append(l.Ops, c)
	}
	b, _ := json.MarshalIndent(l, "", "  ")
	return string(b)
}

func twoPending(t *testing.T) string { return twoPendingBase(t, true) }

func TestLedgerLockExclusive(t *testing.T) {
	st := newStore(t)
	var ok, locked atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := &LedgerStore{Path: st.Path}
			<-start
			if err := s.Lock(); err != nil {
				if errors.Is(err, ErrLedgerLocked) {
					locked.Add(1)
				}
				return
			}
			ok.Add(1)
			// hold the lock (never released in this test)
		}()
	}
	close(start)
	wg.Wait()
	if ok.Load() != 1 || locked.Load() != 15 {
		t.Fatalf("ok=%d locked=%d", ok.Load(), locked.Load())
	}
	// A held lock blocks signing.
	o := evmOp(t, evmSweepPlan(t))
	if _, err := signAndCommit(t, &LedgerStore{Path: st.Path}, o, filepath.Join(t.TempDir(), "s.json")); !errors.Is(err, ErrLedgerLocked) {
		t.Fatalf("want lock error, got %v", err)
	}
}

func TestCommitOrderCrashWindows(t *testing.T) {
	// (a) output path exists: nothing recorded.
	st := newStore(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "signed.json")
	if err := os.WriteFile(out, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := signAndCommit(t, st, evmOp(t, evmSweepPlan(t)), out); err == nil {
		t.Fatal("existing output overwritten")
	}
	if len(load(t, st).Ops) != 0 {
		t.Fatal("op recorded although output refused")
	}
	// (b) crash after the ledger write, before the output: pending op, no output.
	out2 := filepath.Join(dir, "signed2.json")
	testHookAfterLedgerSave = func() error { return errors.New("simulated crash") }
	_, err := signAndCommit(t, st, evmOp(t, evmSweepPlan(t)), out2)
	testHookAfterLedgerSave = nil
	if err == nil {
		t.Fatal("hook not hit")
	}
	if _, err := os.Stat(out2); !os.IsNotExist(err) {
		t.Fatal("output exists after crash before output write")
	}
	l := load(t, st)
	if len(l.Ops) != 1 || l.Ops[0].Status != StatusPending {
		t.Fatal("pending op must be recorded")
	}
	// Recovery: re-sign the same plan = mutually exclusive replacement.
	rec, err := signAndCommit(t, st, evmOp(t, evmSweepPlan(t)), out2)
	if err != nil {
		t.Fatal(err)
	}
	l = load(t, st)
	if rec.Replaces != l.Ops[0].ID || l.Ops[0].Status != StatusReplaced {
		t.Fatal("re-sign must replace")
	}
	// (c) ledger write fails (read-only dir): no output written.
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		st3 := newStore(t)
		ro := filepath.Dir(st3.Path)
		out3 := filepath.Join(t.TempDir(), "signed3.json")
		if err := st3.Lock(); err != nil {
			t.Fatal(err)
		}
		l3 := load(t, st3)
		o := evmOp(t, evmSweepPlan(t))
		signed, ltxs, err := o.Sign(kr(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		_, err = CommitSigned(st3, l3, o, signed, ltxs, out3, t0)
		_ = os.Chmod(ro, 0o700)
		st3.Unlock()
		if err == nil {
			t.Fatal("ledger write should have failed")
		}
		if _, err := os.Stat(out3); !os.IsNotExist(err) {
			t.Fatal("signed output exists without a ledger entry")
		}
		if len(load(t, st3).Ops) != 0 {
			t.Fatal("ledger changed")
		}
	}
	// No temp files left behind.
	ents, _ := os.ReadDir(filepath.Dir(st.Path))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

// Every signed output on disk has a ledger op with exactly its tx hashes.
func TestOutputAlwaysHasLedgerEntry(t *testing.T) {
	st := newStore(t)
	out := filepath.Join(t.TempDir(), "signed.json")
	rec, err := signAndCommit(t, st, evmOp(t, evmSweepPlan(t)), out)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	b, err := DecodeBundle(data)
	if err != nil {
		t.Fatal(err)
	}
	op := load(t, st).Find(b.OpID)
	if op == nil || op.ID != rec.ID || len(op.Txs) != len(b.Txs) {
		t.Fatal("bundle op not in ledger")
	}
	for i := range b.Txs {
		if op.Txs[i].Hash != b.Txs[i].Hash {
			t.Fatal("hash mismatch between ledger and output")
		}
	}
}

// ---- group 2: replacement, partial execution, old signatures, close -----

func TestLedgerReplacementRules(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	p := evmSweepPlan(t)
	op1, err := signAndCommit(t, st, evmOp(t, p), filepath.Join(dir, "1.json"))
	if err != nil {
		t.Fatal(err)
	}
	// New op on the same chain that does not cover all nonces: refused.
	sub := clonePlan(t, p)
	sub.EVM.Sources = sub.EVM.Sources[:1]
	if _, err := signAndCommit(t, st, evmOp(t, sub), filepath.Join(dir, "x.json")); err == nil || !strings.Contains(err.Error(), "still pending") {
		t.Fatalf("subset replacement must be refused: %v", err)
	}
	// Disjoint (fresh nonces) while pending: refused.
	fresh := clonePlan(t, p)
	fresh.EVM.TreasuryNonce += 10
	fresh.EVM.Sources[0].Nonce += 10
	fresh.EVM.Sources[1].Nonce += 10
	if _, err := signAndCommit(t, st, evmOp(t, fresh), filepath.Join(dir, "x.json")); err == nil {
		t.Fatal("second op while pending must be refused")
	}
	// Other network is independent.
	solOp, err := BuildSolanaSweep(solSweepPlan(t), kr(t), registered(testNonceAcct1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signAndCommit(t, st, &Operation{Network: chain.SolanaMainnet, Kind: KindSweep, Solana: solOp}, filepath.Join(dir, "sol.json")); err != nil {
		t.Fatalf("solana op blocked by arbitrum pending: %v", err)
	}
	// Superset (higher fee, extra token) = mutually exclusive replacement.
	sup := clonePlan(t, p)
	sup.EVM.MaxFeePerGas = "30000000"
	op2, err := signAndCommit(t, st, evmOp(t, sup), filepath.Join(dir, "2.json"))
	if err != nil {
		t.Fatal(err)
	}
	l := load(t, st)
	if l.Find(op1.ID).Status != StatusReplaced || op2.ChainRoot != op1.ID || op2.Replaces != op1.ID {
		t.Fatal("replacement bookkeeping")
	}
	cum, _ := op1.Budget.Add(op2.Budget)
	if op2.Cumulative != cum {
		t.Fatalf("cumulative %+v want %+v", op2.Cumulative, cum)
	}
	// Replaced op cannot be closed; pending can.
	if err := l.Close(op1.ID, t0); err == nil {
		t.Fatal("closing a replaced op must fail")
	}
	// Partial execution: A + B0 landed, B1 failed. Chain nonces moved past
	// some but not all; a new prepare reads new nonces -> refused until close.
	part := clonePlan(t, p)
	part.EVM.TreasuryNonce = 9
	part.EVM.Sources = part.EVM.Sources[1:]
	part.EVM.Sources[0].Nonce = 5
	if _, err := signAndCommit(t, st, evmOp(t, part), filepath.Join(dir, "3.json")); err == nil {
		t.Fatal("must close before signing new nonces")
	}
	closeOp(t, st, op2.ID)
	op3, err := signAndCommit(t, st, evmOp(t, part), filepath.Join(dir, "3.json"))
	if err != nil {
		t.Fatalf("after close: %v", err)
	}
	if op3.ChainRoot != op3.ID || op3.Cumulative != op3.Budget {
		t.Fatal("new chain must start a fresh cumulative budget")
	}
	closeOp(t, st, op3.ID)
	// Old signatures / stale plan: reusing any recorded (signer, nonce) is refused forever.
	if _, err := signAndCommit(t, st, evmOp(t, p), filepath.Join(dir, "4.json")); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("stale plan must be refused: %v", err)
	}
	// Shared treasury nonce between sweep top-up and send: dedupe is global.
	dest := Destination{Label: "b", Network: chain.ArbitrumOne, Address: evmAddr(t, 77)}
	sp := &Plan{Version: 1, Kind: KindSend, Network: chain.ArbitrumOne, EVM: &EVMPlan{
		ChainID: "42161", Treasury: evmTreasury(t), TreasuryNonce: 9, MaxFeePerGas: "20000000", MaxPriorityFeePerGas: "0",
		Send: &EVMSendPlan{Symbol: "USDT", Contract: usdtArb.Contract, GasLimit: "100000", TreasuryBalance: "1"},
	}}
	sop, err := BuildEVMSend(sp, kr(t), EVMCapsFor(false), dest, bigS("1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signAndCommit(t, st, &Operation{Network: chain.ArbitrumOne, Kind: KindSend, EVM: sop, Dest: &dest}, filepath.Join(dir, "5.json")); err == nil {
		t.Fatal("send reusing a sweep top-up treasury nonce must be refused")
	}
	sp.EVM.TreasuryNonce = 10
	if sop, err = BuildEVMSend(sp, kr(t), EVMCapsFor(false), dest, bigS("1")); err != nil {
		t.Fatal(err)
	}
	if _, err := signAndCommit(t, st, &Operation{Network: chain.ArbitrumOne, Kind: KindSend, EVM: sop, Dest: &dest}, filepath.Join(dir, "5.json")); err != nil {
		t.Fatal(err)
	}
}

func closeOp(t *testing.T, st *LedgerStore, id string) {
	t.Helper()
	if err := st.Lock(); err != nil {
		t.Fatal(err)
	}
	defer st.Unlock()
	l := load(t, st)
	if err := l.Close(id, t0); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(l); err != nil {
		t.Fatal(err)
	}
}

func TestCumulativeBudgetCapOnReplacement(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	p := evmSweepPlan(t)
	// Each signing spends ~0.0008 ETH: 3 replacements exceed the 0.002 default cap.
	p.EVM.MaxFeePerGas = "100000000"
	p.EVM.TopUpGasLimit = "1000000"
	p.EVM.Sources = p.EVM.Sources[:1]
	p.EVM.Sources[0].Tokens[0].GasLimit = "1000000"
	// spend per op = 1e14 + 1e14 = 2e14 -> 10 ops allowed, the 11th refused.
	var lastErr error
	n := 0
	for ; n < 20; n++ {
		if _, lastErr = signAndCommit(t, st, evmOp(t, p), filepath.Join(dir, "s"+string(rune('a'+n))+".json")); lastErr != nil {
			break
		}
	}
	if n != 10 || lastErr == nil || !strings.Contains(lastErr.Error(), "exceeds the cap") {
		t.Fatalf("signed %d replacements, last error %v", n, lastErr)
	}
}

// Solana ops are never replaced: a plan's nonce_value cannot be proven to be
// the stored durable nonce offline (an ordinary recent blockhash passes), so
// two different messages "sharing" it could both execute.
func TestSolanaReplacementRefused(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	p := solSweepPlan(t)
	reg := registered(testNonceAcct1)
	mk := func(p *Plan) *Operation {
		op, err := BuildSolanaSweep(p, kr(t), reg)
		if err != nil {
			t.Fatal(err)
		}
		return &Operation{Network: chain.SolanaMainnet, Kind: KindSweep, Solana: op}
	}
	op1, err := signAndCommit(t, st, mk(p), filepath.Join(dir, "1.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]*Plan{
		"identical plan":         clonePlan(t, p),
		"same nonce, new price":  func() *Plan { q := clonePlan(t, p); q.Solana.CUPriceMicros = "20000"; return q }(),
		"same nonce, new amount": func() *Plan { q := clonePlan(t, p); q.Solana.Txs[0].Transfers[0].Amount = "1"; return q }(),
		"new nonce value":        func() *Plan { q := clonePlan(t, p); q.Solana.Txs[0].NonceValue = testNonceVal2; return q }(),
	} {
		if _, err := signAndCommit(t, st, mk(q), filepath.Join(dir, "x.json")); err == nil || !strings.Contains(err.Error(), "never be replaced") {
			t.Fatalf("%s: Solana replacement accepted: %v", name, err)
		}
	}
	// A Solana send while a Solana sweep is pending is refused too.
	dest := Destination{Label: "d", Network: chain.SolanaMainnet, Address: solOwner(t, 4000)}
	sp := &Plan{Version: 1, Kind: KindSend, Network: chain.SolanaMainnet, Solana: &SolanaPlan{Treasury: solTreasury(t), CUPriceMicros: "0",
		Send: &SolanaSendPlan{NonceAccount: testNonceAcct1, NonceValue: testNonceVal1, Symbol: "USDT", Mint: usdtSol.Contract, TreasuryBalance: "0"}}}
	sop, err := BuildSolanaSend(sp, kr(t), reg, dest, bigS("1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signAndCommit(t, st, &Operation{Network: chain.SolanaMainnet, Kind: KindSend, Solana: sop, Dest: &dest}, filepath.Join(dir, "s.json")); err == nil {
		t.Fatal("send during pending Solana op accepted")
	}
	l := load(t, st)
	if len(l.Ops) != 1 || l.Ops[0].Status != StatusPending {
		t.Fatal("ledger changed by refused signings")
	}
	// Lost signed file: export the original signatures, nothing new.
	exp, err := ExportBundle(l, op1.ID)
	if err != nil {
		t.Fatal(err)
	}
	orig, _ := os.ReadFile(filepath.Join(dir, "1.json"))
	ob, _ := DecodeBundle(orig)
	eb, _ := DecodeBundle(exp)
	if eb.Txs[0].Raw != ob.Txs[0].Raw {
		t.Fatal("export differs from the original signature")
	}
	// After close, a new nonce value can be signed; old (account, value) never again.
	closeOp(t, st, op1.ID)
	r := clonePlan(t, p)
	r.Solana.Txs[0].NonceValue = testNonceVal2
	if _, err := signAndCommit(t, st, mk(r), filepath.Join(dir, "3.json")); err != nil {
		t.Fatal(err)
	}
	closeOp(t, st, load(t, st).Pending(chain.SolanaMainnet).ID)
	if _, err := signAndCommit(t, st, mk(p), filepath.Join(dir, "4.json")); err == nil {
		t.Fatal("old nonce value reused")
	}
	// A hand-made Solana replacement chain in the ledger file is corrupt.
	l = load(t, st)
	l.Ops[1].Replaces, l.Ops[1].ChainRoot, l.Ops[0].Status = l.Ops[0].ID, l.Ops[0].ID, StatusReplaced
	l.Ops[1].Cumulative, _ = l.Ops[1].Budget.Add(l.Ops[0].Cumulative)
	b, _ := json.Marshal(l)
	if _, err := DecodeLedger(b); !errors.Is(err, ErrCorruptLedger) {
		t.Fatalf("Solana replacement chain accepted: %v", err)
	}
}

func TestDestinationRegistry(t *testing.T) {
	l := NewLedger()
	good := Destination{Label: "binance-arb", Network: chain.ArbitrumOne, Address: evmAddr(t, 3)}
	if err := l.RegisterDestination(good); err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]Destination{
		"dup label":     good,
		"uppercase evm": {Label: "x1", Network: chain.ArbitrumOne, Address: strings.ToUpper(evmAddr(t, 3))},
		"zero":          {Label: "x2", Network: chain.ArbitrumOne, Address: "0x0000000000000000000000000000000000000000"},
		"bad label":     {Label: "a/b", Network: chain.ArbitrumOne, Address: evmAddr(t, 3)},
		"devnet":        {Label: "x3", Network: chain.SolanaDevnet, Address: solOwner(t, 1)},
	} {
		if err := l.RegisterDestination(d); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := l.Destination("nope"); err == nil {
		t.Fatal("unknown label resolved")
	}
}

func TestLedgerTamperedHistoryRefused(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	p := evmSweepPlan(t)
	if _, err := signAndCommit(t, st, evmOp(t, p), filepath.Join(dir, "1.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := signAndCommit(t, st, evmOp(t, p), filepath.Join(dir, "2.json")); err != nil {
		t.Fatal(err)
	}
	orig, err := os.ReadFile(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	var l Ledger
	if err := json.Unmarshal(orig, &l); err != nil {
		t.Fatal(err)
	}
	mut := map[string]func(l *Ledger){
		"cumulative zeroed":      func(l *Ledger) { l.Ops[1].Cumulative.EVMTreasurySpendWei = "0" },
		"root cumulative edited": func(l *Ledger) { l.Ops[0].Cumulative.EVMBurnWei = "1" },
		"replacement drops a nonce": func(l *Ledger) {
			l.Ops[1].Txs = l.Ops[1].Txs[1:]
		},
		"replacement chain fork": func(l *Ledger) {
			c := l.Ops[1]
			c.ID, c.Status = "arb-fork", StatusReplaced
			l.Ops = append(l.Ops, c)
		},
		"raw removed": func(l *Ledger) { l.Ops[0].Txs[0].Raw = "" },
		"nonce shared by chains": func(l *Ledger) {
			l.Ops[1].Replaces, l.Ops[1].ChainRoot, l.Ops[0].Status = "", l.Ops[1].ID, StatusClosed
		},
	}
	for name, m := range mut {
		var c Ledger
		_ = json.Unmarshal(orig, &c)
		m(&c)
		b, _ := json.Marshal(&c)
		if _, err := DecodeLedger(b); !errors.Is(err, ErrCorruptLedger) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	// Export rebuilds exactly what was written, without signing.
	out1, _ := os.ReadFile(filepath.Join(dir, "2.json"))
	b1, _ := DecodeBundle(out1)
	exp, err := ExportBundle(&l, b1.OpID)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := DecodeBundle(exp)
	if err != nil {
		t.Fatal(err)
	}
	if len(b2.Txs) != len(b1.Txs) || b2.Treasury != b1.Treasury {
		t.Fatal("export differs")
	}
	for i := range b1.Txs {
		if b1.Txs[i] != b2.Txs[i] {
			t.Fatalf("export tx %d differs", i)
		}
	}
}

func TestSolanaCloseByExpiryOnlyForSweeps(t *testing.T) {
	sw := &LedgerOp{ID: "s", Kind: KindSweep, CreatedAt: t0.Format(time.RFC3339)}
	if err := SolanaCloseByExpiryAllowed(sw, t0.Add(SolanaCloseExpiryWait-time.Second)); err == nil {
		t.Fatal("early close allowed")
	}
	if err := SolanaCloseByExpiryAllowed(sw, t0.Add(SolanaCloseExpiryWait)); err != nil {
		t.Fatal(err)
	}
	send := &LedgerOp{ID: "x", Kind: KindSend, CreatedAt: t0.Format(time.RFC3339)}
	if err := SolanaCloseByExpiryAllowed(send, t0.Add(24*time.Hour)); err == nil {
		t.Fatal("send closed by expiry")
	}
}
