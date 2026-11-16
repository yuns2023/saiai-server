package sweep

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
)

func mustBuildEVM(t *testing.T, p *Plan, caps EVMCaps) *EVMOp {
	t.Helper()
	op, err := BuildEVMSweep(p, kr(t), caps)
	if err != nil {
		t.Fatalf("BuildEVMSweep: %v", err)
	}
	return op
}

func TestEVMSweepBudgetFormula(t *testing.T) {
	op := mustBuildEVM(t, evmSweepPlan(t), EVMCapsFor(false))
	// order: A0, B0, A1, B1(USDC), B1(USDT)
	roles := []string{}
	for _, in := range op.Intents {
		roles = append(roles, in.Role)
	}
	if strings.Join(roles, ",") != "topup,sweep,topup,sweep,sweep" {
		t.Fatalf("order %v", roles)
	}
	fee := big.NewInt(20_000_000)
	a0 := new(big.Int).Mul(big.NewInt(150_000), fee)
	a1 := new(big.Int).Mul(big.NewInt(120_000+150_000), fee)
	if op.Intents[0].Value.Cmp(a0) != 0 || op.Intents[2].Value.Cmp(a1) != 0 {
		t.Fatalf("A values %s %s want %s %s", op.Intents[0].Value, op.Intents[2].Value, a0, a1)
	}
	if op.Intents[0].Nonce != 7 || op.Intents[2].Nonce != 8 {
		t.Fatal("treasury nonces must be consecutive in source order")
	}
	if op.Intents[3].Nonce != 3 || op.Intents[4].Nonce != 4 {
		t.Fatal("source nonces must be consecutive per source")
	}
	aFee := new(big.Int).Mul(big.NewInt(60_000), fee)
	spend := new(big.Int).Add(a0, a1)
	spend.Add(spend, new(big.Int).Mul(aFee, big.NewInt(2)))
	if op.Budget.EVMTreasurySpendWei != spend.String() {
		t.Fatalf("spend %s want %s", op.Budget.EVMTreasurySpendWei, spend)
	}
	burn := new(big.Int).Add(new(big.Int).Mul(aFee, big.NewInt(2)), new(big.Int).Add(a0, a1))
	if op.Budget.EVMBurnWei != burn.String() {
		t.Fatalf("burn %s want %s", op.Budget.EVMBurnWei, burn)
	}
	for _, in := range op.Intents {
		if in.Role == RoleSweep && in.Recipient != evmTreasury(t) {
			t.Fatal("sweep recipient must be the treasury")
		}
		if in.Role == RoleTopUp && in.From != evmTreasury(t) {
			t.Fatal("top-up must come from the treasury")
		}
	}
}

func TestEVMExistingETHReducesTopUp(t *testing.T) {
	p := evmSweepPlan(t)
	need := new(big.Int).Mul(big.NewInt(150_000), big.NewInt(20_000_000))
	p.EVM.Sources[0].ETHBalance = need.String() // exactly enough: no A
	p.EVM.Sources[1].ETHBalance = "1"           // 1 wei short of nothing
	op := mustBuildEVM(t, p, EVMCapsFor(false))
	if op.Intents[0].Role != RoleSweep || op.Intents[0].SourceIndex != 0 {
		t.Fatal("source 0 must have no top-up")
	}
	if op.Intents[1].Role != RoleTopUp || op.Intents[1].Nonce != 7 {
		t.Fatal("source 1 top-up must take the first treasury nonce")
	}
	want := new(big.Int).Sub(new(big.Int).Mul(big.NewInt(270_000), big.NewInt(20_000_000)), big.NewInt(1))
	if op.Intents[1].Value.Cmp(want) != 0 {
		t.Fatalf("A1 %s want %s", op.Intents[1].Value, want)
	}
	// More ETH than needed: never negative.
	p.EVM.Sources[1].ETHBalance = "1000000000000000000"
	op = mustBuildEVM(t, p, EVMCapsFor(false))
	for _, in := range op.Intents {
		if in.Role == RoleTopUp {
			t.Fatal("no top-ups expected")
		}
	}
	if op.Budget.EVMTreasurySpendWei != "0" {
		t.Fatalf("spend %s", op.Budget.EVMTreasurySpendWei)
	}
}

func TestEVMCapsBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(p *Plan)
		high    bool
		wantErr string
	}{
		{"fee at default cap", func(p *Plan) { p.EVM.MaxFeePerGas = "100000000"; small(p) }, false, ""},
		{"fee above default cap", func(p *Plan) { p.EVM.MaxFeePerGas = "100000001"; small(p) }, false, "exceeds the cap"},
		{"fee above default cap with high-fee", func(p *Plan) { p.EVM.MaxFeePerGas = "100000001"; small(p) }, true, ""},
		{"fee at absolute cap", func(p *Plan) { p.EVM.MaxFeePerGas = "1000000000"; small(p) }, true, ""},
		{"fee above absolute cap", func(p *Plan) { p.EVM.MaxFeePerGas = "1000000001"; small(p) }, true, "exceeds the cap"},
		{"priority above max fee", func(p *Plan) { p.EVM.MaxPriorityFeePerGas = "20000001" }, false, "priority"},
		{"zero max fee", func(p *Plan) { p.EVM.MaxFeePerGas = "0" }, false, "zero"},
		{"gas at max", func(p *Plan) { p.EVM.Sources[0].Tokens[0].GasLimit = "1000000" }, false, ""},
		{"gas above max", func(p *Plan) { p.EVM.Sources[0].Tokens[0].GasLimit = "1000001" }, false, "gas limit"},
		{"gas below 21000", func(p *Plan) { p.EVM.TopUpGasLimit = "20999" }, false, "gas limit"},
		{"zero amount", func(p *Plan) { p.EVM.Sources[0].Tokens[0].Amount = "0" }, false, "zero"},
		{"amount > uint256", func(p *Plan) { p.EVM.Sources[0].Tokens[0].Amount = "1" + strings.Repeat("0", 78) }, false, "invalid integer"},
		{"amount 2^256", func(p *Plan) {
			p.EVM.Sources[0].Tokens[0].Amount = new(big.Int).Lsh(big.NewInt(1), 256).String()
		}, false, "out of range"},
		{"non-canonical amount", func(p *Plan) { p.EVM.Sources[0].Tokens[0].Amount = "02700000" }, false, "non-canonical"},
		{"signed amount", func(p *Plan) { p.EVM.Sources[0].Tokens[0].Amount = "+2700000" }, false, "invalid integer"},
		{"duplicate source index", func(p *Plan) { p.EVM.Sources[1].Index = 0; p.EVM.Sources[1].Address = p.EVM.Sources[0].Address }, false, "duplicate source"},
		{"duplicate token", func(p *Plan) { p.EVM.Sources[1].Tokens[1] = p.EVM.Sources[1].Tokens[0] }, false, "twice"},
		{"A without B", func(p *Plan) { p.EVM.Sources[0].Tokens = nil }, false, "no tokens"},
		{"nonce overflow", func(p *Plan) { p.EVM.Sources[1].Nonce = ^uint64(0) }, false, "overflow"},
		{"treasury nonce overflow", func(p *Plan) { p.EVM.TreasuryNonce = ^uint64(0) }, false, "overflow"},
		{"too many sources", func(p *Plan) {
			s := p.EVM.Sources[0]
			p.EVM.Sources = nil
			for i := uint32(0); i < EVMMaxSources+1; i++ {
				c := s
				c.Index = i
				c.Address = evmAddr(t, i)
				c.ETHBalance = "1000000000000000000"
				p.EVM.Sources = append(p.EVM.Sources, c)
			}
		}, false, "sources"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := evmSweepPlan(t)
			c.mut(p)
			_, err := BuildEVMSweep(p, kr(t), EVMCapsFor(c.high))
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

// small keeps the treasury spend tiny so only the fee-per-gas cap matters.
func small(p *Plan) {
	p.EVM.TopUpGasLimit = "21000"
	for i := range p.EVM.Sources {
		p.EVM.Sources[i].ETHBalance = "1000000000000000000"
	}
}

func TestEVMTreasurySpendCap(t *testing.T) {
	// One source, one token: spend = A.value + A.fee = gB*f + gA*f (ETH=0).
	p := evmSweepPlan(t)
	p.EVM.Sources = p.EVM.Sources[:1]
	p.EVM.MaxFeePerGas = "100000000" // 0.1 gwei
	p.EVM.TopUpGasLimit = "1000000"
	// spend = (gB + 1e6) * 1e8 ; cap 2e15 -> gB + 1e6 <= 2e7 is always true for gB<=1e6, so use ETH
	// balance to tune: A.value = gB*f - eth.
	p.EVM.Sources[0].Tokens[0].GasLimit = "1000000"
	// spend = 1e6*1e8 - eth + 1e6*1e8 = 2e14 - eth: under cap. Push it over with high-fee rates:
	if _, err := BuildEVMSweep(p, kr(t), EVMCapsFor(false)); err != nil {
		t.Fatal(err)
	}
	caps := EVMCaps{MaxFeePerGas: EVMDefaultMaxFeePerGas, MaxTreasurySpend: bigS("200000000000000")} // exactly 2e14
	if _, err := BuildEVMSweep(p, kr(t), caps); err != nil {
		t.Fatalf("spend exactly at cap must pass: %v", err)
	}
	caps.MaxTreasurySpend = bigS("199999999999999")
	if _, err := BuildEVMSweep(p, kr(t), caps); err == nil || !strings.Contains(err.Error(), "treasury max spend") {
		t.Fatalf("want spend cap error, got %v", err)
	}
	// The real default cap: 50 sources at the absolute fee rate exceed 0.002 ETH.
	q := evmSweepPlan(t)
	q.EVM.MaxFeePerGas = "1000000000"
	q.EVM.TopUpGasLimit = "1000000"
	q.EVM.Sources[0].Tokens[0].GasLimit = "1000000"
	q.EVM.Sources[1].Tokens[0].GasLimit = "1000000"
	q.EVM.Sources[1].Tokens[1].GasLimit = "1000000"
	// spend = (1e6*1e9 + 1e6*1e9) + (2e6*1e9 + 1e6*1e9) = 5e15 = absolute cap exactly
	if _, err := BuildEVMSweep(q, kr(t), EVMCapsFor(true)); err != nil {
		t.Fatalf("exactly the absolute cap must pass: %v", err)
	}
	if _, err := BuildEVMSweep(q, kr(t), EVMCapsFor(false)); err == nil || !strings.Contains(err.Error(), "exceeds the cap") {
		t.Fatalf("default caps must refuse 1 gwei: %v", err)
	}
	q.EVM.Sources[0].ETHBalance = "0"
	q.EVM.Sources = append(q.EVM.Sources, EVMSource{Index: 2, Address: evmAddr(t, 2), ETHBalance: "0", Tokens: []EVMToken{{Symbol: "USDC", Contract: usdcArb.Contract, Amount: "1", GasLimit: "21000"}}})
	if _, err := BuildEVMSweep(q, kr(t), EVMCapsFor(true)); err == nil || !strings.Contains(err.Error(), "treasury max spend") {
		t.Fatalf("absolute cap must still bind: %v", err)
	}
}

func TestEVMSendBudgetAndTarget(t *testing.T) {
	dest := Destination{Label: "binance-arb", Network: chain.ArbitrumOne, Address: evmAddr(t, 999)}
	p := &Plan{Version: 1, Kind: KindSend, Network: chain.ArbitrumOne, EVM: &EVMPlan{
		ChainID: "42161", Treasury: evmTreasury(t), TreasuryNonce: 9, MaxFeePerGas: "20000000", MaxPriorityFeePerGas: "0",
		Send: &EVMSendPlan{Symbol: "USDT", Contract: usdtArb.Contract, GasLimit: "100000", TreasuryBalance: "2700000"},
	}}
	op, err := BuildEVMSend(p, kr(t), EVMCapsFor(false), dest, big.NewInt(2_700_000))
	if err != nil {
		t.Fatal(err)
	}
	if op.Budget.EVMTreasurySpendWei != "2000000000000" || op.Intents[0].Recipient != dest.Address || op.Intents[0].Nonce != 9 {
		t.Fatalf("bad send op %+v", op.Intents[0])
	}
	st, err := SignEVM(op, kr(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := hex.DecodeString(st[0].Raw[2:])
	d, err := DecodeEVMSigned(raw)
	if err != nil || d.Recipient != dest.Address || d.Amount.Int64() != 2_700_000 || d.From != evmTreasury(t) || d.Value.Sign() != 0 {
		t.Fatalf("decoded send %+v %v", d, err)
	}
	// dest_hint mismatch is refused; wrong-network destination refused.
	p.EVM.Send.DestHint = evmAddr(t, 5)
	if _, err := BuildEVMSend(p, kr(t), EVMCapsFor(false), dest, big.NewInt(1)); err == nil {
		t.Fatal("dest_hint mismatch must fail")
	}
	p.EVM.Send.DestHint = ""
	if _, err := BuildEVMSend(p, kr(t), EVMCapsFor(false), Destination{Label: "x", Network: chain.SolanaMainnet, Address: solOwner(t, 1)}, big.NewInt(1)); err == nil {
		t.Fatal("solana destination on an EVM send must fail")
	}
	if _, err := BuildEVMSend(p, kr(t), EVMCapsFor(false), Destination{Label: "self", Network: chain.ArbitrumOne, Address: evmTreasury(t)}, big.NewInt(1)); err == nil {
		t.Fatal("sending to the treasury itself must fail")
	}
	if _, err := BuildEVMSend(p, kr(t), EVMCapsFor(false), dest, big.NewInt(0)); err == nil {
		t.Fatal("zero amount must fail")
	}
	p.EVM.Send.GasLimit = "1000001"
	if _, err := BuildEVMSend(p, kr(t), EVMCapsFor(false), dest, big.NewInt(1)); err == nil {
		t.Fatal("gas above cap must fail")
	}
}

// Group 4: tampering with any plan field can never redirect funds.
func TestEVMPlanTamperingNeverRedirects(t *testing.T) {
	treasury := evmTreasury(t)
	attacker := otherKR(t)
	atk, _ := attacker.EVMTreasury()
	atkSrc, _ := attacker.EVMDeposit(0)
	refused := []struct {
		name string
		mut  func(p *Plan)
	}{
		{"treasury replaced", func(p *Plan) { p.EVM.Treasury = atk.Address }},
		{"treasury replaced by a source", func(p *Plan) { p.EVM.Treasury = evmAddr(t, 0) }},
		{"source address not derived from index", func(p *Plan) { p.EVM.Sources[0].Address = evmAddr(t, 5) }},
		{"foreign source", func(p *Plan) { p.EVM.Sources[0].Address = atkSrc.Address }},
		{"token contract replaced", func(p *Plan) { p.EVM.Sources[0].Tokens[0].Contract = "0x1111111111111111111111111111111111111111" }},
		{"token symbol unknown", func(p *Plan) { p.EVM.Sources[0].Tokens[0].Symbol = "DAI" }},
		{"symbol/contract swapped", func(p *Plan) { p.EVM.Sources[0].Tokens[0].Contract = usdcArb.Contract }},
		{"chain id", func(p *Plan) { p.EVM.ChainID = "1" }},
		{"chain id leading zero", func(p *Plan) { p.EVM.ChainID = "042161" }},
		{"wrong network", func(p *Plan) { p.Network = chain.ArbitrumSepolia }},
		{"kind send", func(p *Plan) { p.Kind = KindSend }},
		{"index 2^31", func(p *Plan) { p.EVM.Sources[0].Index = 1 << 31 }},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			p := evmSweepPlan(t)
			c.mut(p)
			if _, err := BuildEVMSweep(p, kr(t), EVMCapsFor(true)); err == nil {
				t.Fatal("tampered plan accepted")
			}
		})
	}
	// Fields the online side legitimately controls: whatever their value,
	// every signed transfer pays the treasury and every top-up goes to a
	// derived source from the treasury.
	free := []func(p *Plan){
		func(p *Plan) { p.EVM.Sources[0].Tokens[0].Amount = "999999999999" },
		func(p *Plan) { p.EVM.Sources[0].Nonce = 12345 },
		func(p *Plan) { p.EVM.TreasuryNonce = 0 },
		func(p *Plan) { p.EVM.Sources[1].ETHBalance = "300" },
		func(p *Plan) {
			p.EVM.Treasury = strings.ToUpper(p.EVM.Treasury[:2]) + strings.ToUpper(p.EVM.Treasury[2:])
		},
		func(p *Plan) { p.EVM.Sources[0].Address = strings.ToUpper(p.EVM.Sources[0].Address) },
	}
	sources := map[string]bool{evmAddr(t, 0): true, evmAddr(t, 1): true}
	for i, mut := range free {
		p := evmSweepPlan(t)
		mut(p)
		op, err := BuildEVMSweep(p, kr(t), EVMCapsFor(false))
		if err != nil {
			continue // refusing is always safe
		}
		st, err := SignEVM(op, kr(t))
		if err != nil {
			t.Fatalf("case %d: sign: %v", i, err)
		}
		for j, s := range st {
			raw, _ := hex.DecodeString(s.Raw[2:])
			d, err := DecodeEVMSigned(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyEVMSigned(raw, &op.Intents[j]); err != nil {
				t.Fatalf("case %d tx %d: decoded tx differs from confirmed intent: %v", i, j, err)
			}
			switch {
			case d.IsTransfer:
				if d.Recipient != treasury || !sources[d.From] {
					t.Fatalf("case %d: transfer %s -> %s", i, d.From, d.Recipient)
				}
				if d.To != usdcArb.Contract && d.To != usdtArb.Contract {
					t.Fatalf("case %d: token %s", i, d.To)
				}
			default:
				if d.From != treasury || !sources[d.To] {
					t.Fatalf("case %d: top-up %s -> %s", i, d.From, d.To)
				}
			}
		}
	}
}

func TestVerifyEVMSignedDetectsMismatch(t *testing.T) {
	op := mustBuildEVM(t, evmSweepPlan(t), EVMCapsFor(false))
	st, err := SignEVM(op, kr(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := hex.DecodeString(st[1].Raw[2:])
	for name, mut := range map[string]func(in *EVMIntent){
		"recipient": func(in *EVMIntent) { in.Recipient = evmAddr(t, 3) },
		"amount":    func(in *EVMIntent) { in.Amount = big.NewInt(1) },
		"nonce":     func(in *EVMIntent) { in.Nonce++ },
		"from":      func(in *EVMIntent) { in.From = evmAddr(t, 3) },
		"gas":       func(in *EVMIntent) { in.GasLimit++ },
		"contract":  func(in *EVMIntent) { in.To = usdcArb.Contract },
	} {
		in := op.Intents[1]
		mut(&in)
		if err := VerifyEVMSigned(raw, &in); err == nil {
			t.Errorf("%s: mismatch not detected", name)
		}
	}
	// Garbage / legacy / other chain rejected by the decoder.
	if _, err := DecodeEVMSigned([]byte{0x02, 0xc0}); err == nil {
		t.Fatal("garbage accepted")
	}
}
