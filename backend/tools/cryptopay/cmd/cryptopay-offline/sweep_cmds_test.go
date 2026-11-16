package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyler-smith/go-bip39"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/Wei-Shaw/sub2api/tools/cryptopay/sweep"
)

func withInput(t *testing.T, lines ...string) {
	t.Helper()
	stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	input = nil
	t.Cleanup(func() { stdin = os.Stdin; input = nil })
}

func testKeyring(t *testing.T) *sweep.Keyring {
	seed, _ := bip39.NewSeedWithErrorChecking(abandonAbout, "")
	k, err := sweep.NewKeyring(seed)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func writePlan(t *testing.T, dir string, p *sweep.Plan) string {
	t.Helper()
	b, _ := json.MarshalIndent(p, "", " ")
	f := filepath.Join(dir, "plan.json")
	_ = os.Remove(f)
	if err := os.WriteFile(f, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestOfflineSweepCLI(t *testing.T) {
	dir := t.TempDir()
	mf := filepath.Join(dir, "m.txt")
	if err := os.WriteFile(mf, []byte(abandonAbout+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(dir, "ledger.json")
	var out, errb bytes.Buffer
	run1 := func(args ...string) error {
		out.Reset()
		return run(args, &out, &errb)
	}
	kr := testKeyring(t)
	et, _ := kr.EVMTreasury()
	st, _ := kr.SolanaTreasury()
	if err := run1("treasury-address", "--mnemonic-file", mf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), st.Address) || !strings.Contains(strings.ToLower(out.String()), et.Address) {
		t.Fatalf("treasury-address: %s", out.String())
	}
	// Signing without a ledger is refused.
	src0, _ := kr.EVMDeposit(0)
	usdt := chain.MainnetAssets()[1]
	plan := &sweep.Plan{Version: 1, Kind: sweep.KindSweep, Network: chain.ArbitrumOne, EVM: &sweep.EVMPlan{
		ChainID: "42161", Treasury: et.Address, TreasuryNonce: 0, MaxFeePerGas: "20000000", MaxPriorityFeePerGas: "0", TopUpGasLimit: "60000",
		Sources: []sweep.EVMSource{{Index: 0, Address: src0.Address, Nonce: 0, ETHBalance: "0", Tokens: []sweep.EVMToken{{Symbol: "USDT", Contract: usdt.Contract, Amount: "2700000", GasLimit: "150000"}}}},
	}}
	pf := writePlan(t, dir, plan)
	signed := filepath.Join(dir, "signed.json")
	withInput(t, et.Address[len(et.Address)-6:])
	if err := run1("sweep-sign", "--mnemonic-file", mf, "--ledger", ledger, "--plan", pf, "--out", signed); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing ledger: %v", err)
	}
	if err := run1("ledger", "init", "--ledger", ledger); err != nil {
		t.Fatal(err)
	}
	// Wrong confirmation: nothing signed, ledger unchanged.
	withInput(t, "abcdef")
	if err := run1("sweep-sign", "--mnemonic-file", mf, "--ledger", ledger, "--plan", pf, "--out", signed); err == nil {
		t.Fatal("wrong confirmation accepted")
	}
	if _, err := os.Stat(signed); !os.IsNotExist(err) {
		t.Fatal("output written without confirmation")
	}
	// Right confirmation (case-insensitive tail of the EIP-55 treasury).
	withInput(t, strings.ToUpper(et.Address[len(et.Address)-6:]))
	if err := run1("sweep-sign", "--mnemonic-file", mf, "--ledger", ledger, "--plan", pf, "--out", signed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "金库") || !strings.Contains(out.String(), "2.700000") {
		t.Fatalf("summary: %s", out.String())
	}
	data, _ := os.ReadFile(signed)
	b, err := sweep.DecodeBundle(data)
	if err != nil || len(b.Txs) != 2 || strings.Contains(string(data), "abandon") {
		t.Fatalf("bundle %v %v", b, err)
	}
	// ledger export rewrites the same signed transactions (new file only).
	exp := filepath.Join(dir, "recovered.json")
	if err := run1("ledger", "export", "--ledger", ledger, "--op", b.OpID, "--out", signed); err == nil {
		t.Fatal("export overwrote an existing file")
	}
	if err := run1("ledger", "export", "--ledger", ledger, "--op", b.OpID, "--out", exp); err != nil {
		t.Fatal(err)
	}
	ed, _ := os.ReadFile(exp)
	eb, err := sweep.DecodeBundle(ed)
	if err != nil || len(eb.Txs) != len(b.Txs) || eb.Txs[0].Raw != b.Txs[0].Raw || eb.Txs[1].Raw != b.Txs[1].Raw {
		t.Fatalf("export: %v", err)
	}
	// ledger list / close
	if err := run1("ledger", "list", "--ledger", ledger); err != nil || !strings.Contains(out.String(), "[pending]") || !strings.Contains(out.String(), "arbiscan.io/tx/") {
		t.Fatalf("list: %v %s", err, out.String())
	}
	withInput(t, "wrong-id")
	if err := run1("ledger", "close", "--ledger", ledger, "--op", b.OpID); err == nil {
		t.Fatal("close with wrong typed id accepted")
	}
	withInput(t, b.OpID)
	if err := run1("ledger", "close", "--ledger", ledger, "--op", b.OpID); err != nil {
		t.Fatal(err)
	}

	// --allow-high-fee needs the second confirmation.
	plan.EVM.MaxFeePerGas = "200000000"
	plan.EVM.TreasuryNonce, plan.EVM.Sources[0].Nonce = 50, 50
	pf = writePlan(t, dir, plan)
	withInput(t, et.Address[len(et.Address)-6:], "no")
	if err := run1("sweep-sign", "--mnemonic-file", mf, "--ledger", ledger, "--plan", pf, "--out", filepath.Join(dir, "hf.json"), "--allow-high-fee"); err == nil || !strings.Contains(err.Error(), "high-fee") {
		t.Fatalf("high fee without HIGH-FEE: %v", err)
	}
	withInput(t, et.Address[len(et.Address)-6:])
	if err := run1("sweep-sign", "--mnemonic-file", mf, "--ledger", ledger, "--plan", pf, "--out", filepath.Join(dir, "hf.json")); err == nil || !strings.Contains(err.Error(), "exceeds the cap") {
		t.Fatalf("0.2 gwei without --allow-high-fee: %v", err)
	}
	withInput(t, et.Address[len(et.Address)-6:], "HIGH-FEE")
	if err := run1("sweep-sign", "--mnemonic-file", mf, "--ledger", ledger, "--plan", pf, "--out", filepath.Join(dir, "hf.json"), "--allow-high-fee"); err != nil {
		t.Fatal(err)
	}
	hfData, _ := os.ReadFile(filepath.Join(dir, "hf.json"))
	hb, err := sweep.DecodeBundle(hfData)
	if err != nil {
		t.Fatal(err)
	}
	withInput(t, hb.OpID)
	if err := run1("ledger", "close", "--ledger", ledger, "--op", hb.OpID); err != nil {
		t.Fatal(err)
	}
	// dest-register: typed twice; mismatch / bad checksum refused.
	dest, _ := kr.EVMDeposit(4242)
	checksummed := sweep.DisplayAddress(chain.ArbitrumOne, dest.Address)
	withInput(t, checksummed, checksummed[:41]+"0")
	if err := run1("dest-register", "--ledger", ledger, "--network", "arbitrum-one", "--label", "binance-arb"); err == nil {
		t.Fatal("mismatched entries accepted")
	}
	bad := []byte(checksummed)
	for i := 2; i < len(bad); i++ { // flip the case of one letter: EIP-55 must catch it
		if bad[i] >= 'a' && bad[i] <= 'f' {
			bad[i] -= 32
			break
		} else if bad[i] >= 'A' && bad[i] <= 'F' {
			bad[i] += 32
			break
		}
	}
	withInput(t, string(bad), string(bad), "yes")
	if err := run1("dest-register", "--ledger", ledger, "--network", "arbitrum-one", "--label", "binance-arb"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("bad checksum: %v", err)
	}
	withInput(t, checksummed, checksummed, "yes")
	if err := run1("dest-register", "--ledger", ledger, "--network", "arbitrum-one", "--label", "binance-arb"); err != nil {
		t.Fatal(err)
	}
	// send-sign to the registered label; amount typed offline.
	sendPlan := &sweep.Plan{Version: 1, Kind: sweep.KindSend, Network: chain.ArbitrumOne, EVM: &sweep.EVMPlan{
		ChainID: "42161", Treasury: et.Address, TreasuryNonce: 1, MaxFeePerGas: "20000000", MaxPriorityFeePerGas: "0",
		Send: &sweep.EVMSendPlan{Symbol: "USDT", Contract: usdt.Contract, GasLimit: "100000", TreasuryBalance: "2700000"},
	}}
	pf = writePlan(t, dir, sendPlan)
	sendOut := filepath.Join(dir, "send.json")
	if err := run1("send-sign", "--mnemonic-file", mf, "--ledger", ledger, "--plan", pf, "--dest", "nope", "--amount", "2.7", "--out", sendOut); err == nil {
		t.Fatal("unregistered label accepted")
	}
	withInput(t, checksummed[len(checksummed)-6:])
	if err := run1("send-sign", "--mnemonic-file", mf, "--ledger", ledger, "--plan", pf, "--dest", "binance-arb", "--amount", "2.7", "--out", sendOut); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(sendOut)
	sb, err := sweep.DecodeBundle(data)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := hex.DecodeString(strings.TrimPrefix(sb.Txs[0].Raw, "0x"))
	d, err := sweep.DecodeEVMSigned(raw)
	if err != nil || d.Recipient != dest.Address || d.Amount.Int64() != 2_700_000 {
		t.Fatalf("send tx %+v %v", d, err)
	}
	// Sweep plan given to send-sign is refused.
	if err := run1("send-sign", "--mnemonic-file", mf, "--ledger", ledger, "--plan", writePlan(t, dir, plan), "--dest", "binance-arb", "--amount", "1", "--out", filepath.Join(dir, "x.json")); err == nil {
		t.Fatal("kind mismatch accepted")
	}

	// nonce-register
	withInput(t, "yes")
	na := st.Address // any valid pubkey works for the registry
	if err := run1("nonce-register", "--ledger", ledger, "--address", na); err != nil {
		t.Fatal(err)
	}
	withInput(t, "no")
	if err := run1("nonce-register", "--ledger", ledger, "--address", src0.Address); err == nil {
		t.Fatal("unconfirmed / invalid nonce register accepted")
	}
}

func TestSolanaCloseConditions(t *testing.T) {
	dir := t.TempDir()
	mf := filepath.Join(dir, "m.txt")
	if err := os.WriteFile(mf, []byte(abandonAbout+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(dir, "ledger.json")
	var out, errb bytes.Buffer
	run1 := func(args ...string) error { out.Reset(); return run(args, &out, &errb) }
	t0 := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return t0 }
	t.Cleanup(func() { now = time.Now })
	if err := run1("ledger", "init", "--ledger", ledger); err != nil {
		t.Fatal(err)
	}
	kr := testKeyring(t)
	st, _ := kr.SolanaTreasury()
	owner, _ := kr.SolanaPool(0)
	nonceAcct, _ := kr.SolanaPool(4999) // any valid pubkey works for the registry
	withInput(t, "yes")
	if err := run1("nonce-register", "--ledger", ledger, "--address", nonceAcct.Address); err != nil {
		t.Fatal(err)
	}
	usdt := chain.MainnetAssets()[3]
	plan := &sweep.Plan{Version: 1, Kind: sweep.KindSweep, Network: chain.SolanaMainnet, Solana: &sweep.SolanaPlan{
		Treasury: st.Address, CUPriceMicros: "1000", Txs: []sweep.SolanaTxPlan{{NonceAccount: nonceAcct.Address, NonceValue: owner.Address,
			Transfers: []sweep.SolanaTransfer{{Index: 0, Owner: owner.Address, Symbol: "USDT", Mint: usdt.Contract, Amount: "4300000"}}}}}}
	pf := writePlan(t, dir, plan)
	withInput(t, st.Address[len(st.Address)-6:])
	if err := run1("sweep-sign", "--mnemonic-file", mf, "--ledger", ledger, "--plan", pf, "--out", filepath.Join(dir, "s.json")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "s.json"))
	b, _ := sweep.DecodeBundle(data)
	// Re-signing the same plan is refused (no Solana replacement).
	withInput(t, st.Address[len(st.Address)-6:])
	if err := run1("sweep-sign", "--mnemonic-file", mf, "--ledger", ledger, "--plan", pf, "--out", filepath.Join(dir, "s2.json")); err == nil || !strings.Contains(err.Error(), "never be replaced") {
		t.Fatalf("Solana re-sign: %v", err)
	}
	// Path B before 30 minutes: refused.
	now = func() time.Time { return t0.Add(29 * time.Minute) }
	withInput(t, "B", b.OpID)
	if err := run1("ledger", "close", "--ledger", ledger, "--op", b.OpID); err == nil || !strings.Contains(err.Error(), "path B needs") {
		t.Fatalf("early path B: %v", err)
	}
	withInput(t, "x", b.OpID)
	if err := run1("ledger", "close", "--ledger", ledger, "--op", b.OpID); err == nil {
		t.Fatal("no path chosen but closed")
	}
	// After 30 minutes path B works.
	now = func() time.Time { return t0.Add(31 * time.Minute) }
	withInput(t, "B", b.OpID)
	if err := run1("ledger", "close", "--ledger", ledger, "--op", b.OpID); err != nil {
		t.Fatal(err)
	}
}
