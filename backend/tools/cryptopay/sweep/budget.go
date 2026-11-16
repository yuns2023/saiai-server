package sweep

import (
	"fmt"
	"math/big"
)

// Hard-coded limits. A plan can never raise them; --allow-high-fee (with a
// second typed confirmation) moves EVM from the default to the absolute caps.
var (
	// EVM
	EVMDefaultMaxFeePerGas  = big.NewInt(100_000_000)   // 0.1 gwei
	EVMAbsoluteMaxFeePerGas = big.NewInt(1_000_000_000) // 1 gwei
	EVMMinGasLimit          = uint64(21_000)
	EVMMaxGasLimit          = uint64(1_000_000)
	// Treasury maximum spend per operation chain (cumulative across replacements).
	EVMDefaultMaxTreasurySpend  = mustBig("2000000000000000") // 0.002 ETH
	EVMAbsoluteMaxTreasurySpend = mustBig("5000000000000000") // 0.005 ETH
)

const (
	EVMMaxSources = 50

	SolanaComputeUnitLimit     = uint32(200_000)
	SolanaMaxCUPriceMicros     = uint64(50_000)
	SolanaLamportsPerSignature = uint64(5_000)
	SolanaATARentLamports      = uint64(2_039_280)  // rent-exempt minimum for a 165-byte token account
	SolanaMaxBudgetLamports    = uint64(10_000_000) // 0.01 SOL, fees + ATA rent, cumulative
	SolanaMaxTxsPerOp          = 5
	SolanaMaxRegisteredNonces  = 5
	SolanaMaxPacketBytes       = 1232
	lamportsPerSOLDecimals     = 9
	weiPerETHDecimals          = 18
	solanaMicroLamportsPerLamp = 1_000_000
)

// EVMCaps are the caps in force for one signing.
type EVMCaps struct {
	MaxFeePerGas     *big.Int
	MaxTreasurySpend *big.Int
}

// EVMCapsFor returns default caps, or absolute caps when highFee is set.
func EVMCapsFor(highFee bool) EVMCaps {
	if highFee {
		return EVMCaps{MaxFeePerGas: EVMAbsoluteMaxFeePerGas, MaxTreasurySpend: EVMAbsoluteMaxTreasurySpend}
	}
	return EVMCaps{MaxFeePerGas: EVMDefaultMaxFeePerGas, MaxTreasurySpend: EVMDefaultMaxTreasurySpend}
}

// Budget is the worst-case cost of one operation (or a cumulative chain).
// All values are decimal strings of base units so the ledger can persist them.
type Budget struct {
	// EVM: wei
	EVMTreasurySpendWei string `json:"evm_treasury_spend_wei,omitempty"` // Σ(A.value + A.gas*maxFee) + send fee
	EVMBurnWei          string `json:"evm_burn_wei,omitempty"`           // Σ(all tx gas*maxFee)
	// Solana: lamports
	SolanaFeeLamports  string `json:"solana_fee_lamports,omitempty"`  // signature + priority fees
	SolanaRentLamports string `json:"solana_rent_lamports,omitempty"` // ATA creation rent (capital, not burned)
}

func addStr(a, b string) (string, error) {
	x, err := budgetVal(a)
	if err != nil {
		return "", err
	}
	y, err := budgetVal(b)
	if err != nil {
		return "", err
	}
	if x == nil && y == nil {
		return "", nil
	}
	if x == nil {
		x = new(big.Int)
	}
	if y == nil {
		y = new(big.Int)
	}
	return new(big.Int).Add(x, y).String(), nil
}

func budgetVal(s string) (*big.Int, error) {
	if s == "" {
		return nil, nil
	}
	return ParseCanonicalUint(s)
}

// Add returns b + o (field by field).
func (b Budget) Add(o Budget) (Budget, error) {
	var r Budget
	var err error
	if r.EVMTreasurySpendWei, err = addStr(b.EVMTreasurySpendWei, o.EVMTreasurySpendWei); err != nil {
		return r, err
	}
	if r.EVMBurnWei, err = addStr(b.EVMBurnWei, o.EVMBurnWei); err != nil {
		return r, err
	}
	if r.SolanaFeeLamports, err = addStr(b.SolanaFeeLamports, o.SolanaFeeLamports); err != nil {
		return r, err
	}
	if r.SolanaRentLamports, err = addStr(b.SolanaRentLamports, o.SolanaRentLamports); err != nil {
		return r, err
	}
	return r, nil
}

// CheckEVM enforces the treasury-spend cap on a (cumulative) budget.
func (b Budget) CheckEVM(c EVMCaps) error {
	v, err := budgetVal(b.EVMTreasurySpendWei)
	if err != nil {
		return err
	}
	if v != nil && v.Cmp(c.MaxTreasurySpend) > 0 {
		return fmt.Errorf("sweep: treasury max spend %s ETH exceeds the cap %s ETH", FormatUnits(v, weiPerETHDecimals), FormatUnits(c.MaxTreasurySpend, weiPerETHDecimals))
	}
	return nil
}

// CheckSolana enforces the fees+rent cap on a (cumulative) budget.
func (b Budget) CheckSolana() error {
	f, err := budgetVal(b.SolanaFeeLamports)
	if err != nil {
		return err
	}
	r, err := budgetVal(b.SolanaRentLamports)
	if err != nil {
		return err
	}
	t := new(big.Int)
	if f != nil {
		t.Add(t, f)
	}
	if r != nil {
		t.Add(t, r)
	}
	if t.Cmp(new(big.Int).SetUint64(SolanaMaxBudgetLamports)) > 0 {
		return fmt.Errorf("sweep: Solana fees+rent %s SOL exceed the cap %s SOL", FormatUnits(t, lamportsPerSOLDecimals), FormatUnits(new(big.Int).SetUint64(SolanaMaxBudgetLamports), lamportsPerSOLDecimals))
	}
	return nil
}

// SolanaTxFeeLamports = signatures*5000 + ceil(cuLimit*cuPrice/1e6).
func SolanaTxFeeLamports(numSignatures int, cuPriceMicros uint64) *big.Int {
	sig := new(big.Int).Mul(big.NewInt(int64(numSignatures)), new(big.Int).SetUint64(SolanaLamportsPerSignature))
	prio := new(big.Int).Mul(new(big.Int).SetUint64(uint64(SolanaComputeUnitLimit)), new(big.Int).SetUint64(cuPriceMicros))
	prio.Add(prio, big.NewInt(solanaMicroLamportsPerLamp-1))
	prio.Quo(prio, big.NewInt(solanaMicroLamportsPerLamp))
	return sig.Add(sig, prio)
}

// FormatETH renders wei as ETH.
func FormatETH(wei string) string {
	v, err := budgetVal(wei)
	if err != nil || v == nil {
		return "0"
	}
	return FormatUnits(v, weiPerETHDecimals)
}

// FormatSOL renders lamports as SOL.
func FormatSOL(lamports string) string {
	v, err := budgetVal(lamports)
	if err != nil || v == nil {
		return "0"
	}
	return FormatUnits(v, lamportsPerSOLDecimals)
}
