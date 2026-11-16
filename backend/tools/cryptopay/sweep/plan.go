// Package sweep is the pure (no RPC, no DB) core of the offline sweep tool:
// plan schema and strict decoding, offline transaction construction, budget
// computation, the offline signing ledger with its registries, and post-sign
// decode-and-verify. See cmd/cryptopay-offline/README.md.
//
// Trust model: everything produced on the online machine (plan.json, RPC
// data) is UNTRUSTED. The offline side derives every signer, the sweep target
// (the treasury) and every token/program address itself, rebuilds every
// transaction from semantic fields and never signs bytes taken from the plan.
// What it guarantees: sweep funds can only go to the derived treasury; send
// funds can only go to a destination registered offline; fee loss is bounded
// by the budget the operator confirmed offline (cumulative across
// replacements). What it does NOT guarantee: that balances, nonces or fee
// levels in the plan are true (a lying plan can make transactions fail or
// waste fees within the budget), or that the operator registered the right
// external address.
//
// This package lives in its own Go module (backend/tools/cryptopay) so the
// Server binary can never import it or inherit its dependencies.
package sweep

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
)

// PlanVersion is the only accepted plan.json version.
const PlanVersion = 1

// Kind is the operation kind.
type Kind string

const (
	KindSweep Kind = "sweep" // deposit sources -> treasury
	KindSend  Kind = "send"  // treasury -> registered external destination
)

// Plan is the unsigned, untrusted input produced by `cryptopay-sweep prepare`.
// One plan covers one network and one kind.
type Plan struct {
	Version   int           `json:"version"`
	Kind      Kind          `json:"kind"`
	Network   chain.Network `json:"network"`
	CreatedAt string        `json:"created_at"` // informational
	EVM       *EVMPlan      `json:"evm,omitempty"`
	Solana    *SolanaPlan   `json:"solana,omitempty"`
}

// EVMPlan carries chain state for an Arbitrum sweep or send.
type EVMPlan struct {
	ChainID              string `json:"chain_id"`
	Treasury             string `json:"treasury"` // must equal the offline-derived treasury
	TreasuryNonce        uint64 `json:"treasury_nonce"`
	MaxFeePerGas         string `json:"max_fee_per_gas"`          // wei
	MaxPriorityFeePerGas string `json:"max_priority_fee_per_gas"` // wei
	// Sweep only.
	TopUpGasLimit string      `json:"topup_gas_limit,omitempty"`
	Sources       []EVMSource `json:"sources,omitempty"`
	// Send only.
	Send *EVMSendPlan `json:"send,omitempty"`
}

// EVMSource is one deposit address to sweep.
type EVMSource struct {
	Index      uint32     `json:"index"`
	Address    string     `json:"address"`
	Nonce      uint64     `json:"nonce"`
	ETHBalance string     `json:"eth_balance"` // wei, declared by the online side
	Tokens     []EVMToken `json:"tokens"`
}

// EVMToken is one token balance to sweep from a source.
type EVMToken struct {
	Symbol   string `json:"symbol"`
	Contract string `json:"contract"`
	Amount   string `json:"amount"` // raw units
	GasLimit string `json:"gas_limit"`
}

// EVMSendPlan is the chain state for a treasury -> external transfer. The
// destination and amount are chosen offline; DestHint/TreasuryBalance are
// informational cross-checks only.
type EVMSendPlan struct {
	Symbol          string `json:"symbol"`
	Contract        string `json:"contract"`
	GasLimit        string `json:"gas_limit"`
	TreasuryBalance string `json:"treasury_balance"`
	DestHint        string `json:"dest_hint,omitempty"`
}

// SolanaPlan carries chain state for a Solana sweep or send.
type SolanaPlan struct {
	Treasury      string `json:"treasury"`                // must equal the offline-derived treasury
	CUPriceMicros string `json:"cu_price_micro_lamports"` // compute unit price
	// Sweep only: one transaction per durable nonce account.
	Txs []SolanaTxPlan `json:"txs,omitempty"`
	// Send only.
	Send *SolanaSendPlan `json:"send,omitempty"`
}

// SolanaTxPlan is one sweep transaction.
type SolanaTxPlan struct {
	NonceAccount      string           `json:"nonce_account"`
	NonceValue        string           `json:"nonce_value"`
	CreateTreasuryATA []string         `json:"create_treasury_ata,omitempty"` // symbols whose treasury ATA must be created
	Transfers         []SolanaTransfer `json:"transfers"`
}

// SolanaTransfer is one source token account to empty.
type SolanaTransfer struct {
	Index  uint32 `json:"index"`
	Owner  string `json:"owner"`
	Symbol string `json:"symbol"`
	Mint   string `json:"mint"`
	Amount string `json:"amount"` // raw units
}

// SolanaSendPlan is the chain state for a treasury -> external transfer.
type SolanaSendPlan struct {
	NonceAccount    string `json:"nonce_account"`
	NonceValue      string `json:"nonce_value"`
	Symbol          string `json:"symbol"`
	Mint            string `json:"mint"`
	TreasuryBalance string `json:"treasury_balance"`
	DestHint        string `json:"dest_hint,omitempty"`
}

// DecodePlan strictly decodes plan.json and checks its shape. Semantic
// checks against the derived keys happen in the builders.
func DecodePlan(data []byte) (*Plan, error) {
	var p Plan
	if err := DecodeStrict(data, &p); err != nil {
		return nil, err
	}
	if p.Version != PlanVersion {
		return nil, fmt.Errorf("sweep: plan version %d, want %d", p.Version, PlanVersion)
	}
	if p.Kind != KindSweep && p.Kind != KindSend {
		return nil, fmt.Errorf("sweep: unknown plan kind %s", clip(string(p.Kind)))
	}
	if p.Network != chain.ArbitrumOne && p.Network != chain.SolanaMainnet {
		return nil, fmt.Errorf("sweep: unsupported network %s (only arbitrum-one and solana-mainnet)", clip(string(p.Network)))
	}
	switch p.Network.Family() {
	case chain.FamilyEVM:
		if p.EVM == nil || p.Solana != nil {
			return nil, errors.New("sweep: an arbitrum-one plan needs exactly the evm section")
		}
		e := p.EVM
		if p.Kind == KindSweep && (e.Send != nil || len(e.Sources) == 0 || e.TopUpGasLimit == "") {
			return nil, errors.New("sweep: an EVM sweep plan needs sources and topup_gas_limit and no send section")
		}
		if p.Kind == KindSend && (e.Send == nil || len(e.Sources) != 0 || e.TopUpGasLimit != "") {
			return nil, errors.New("sweep: an EVM send plan needs exactly the send section")
		}
	case chain.FamilySolana:
		if p.Solana == nil || p.EVM != nil {
			return nil, errors.New("sweep: a solana-mainnet plan needs exactly the solana section")
		}
		s := p.Solana
		if p.Kind == KindSweep && (s.Send != nil || len(s.Txs) == 0) {
			return nil, errors.New("sweep: a Solana sweep plan needs txs and no send section")
		}
		if p.Kind == KindSend && (s.Send == nil || len(s.Txs) != 0) {
			return nil, errors.New("sweep: a Solana send plan needs exactly the send section")
		}
	}
	return &p, nil
}

// assetBySymbol returns the hard-coded mainnet asset of network+symbol and
// requires the plan's contract/mint to match it exactly.
func assetBySymbol(network chain.Network, symbol, contract string) (chain.Asset, error) {
	for _, a := range chain.MainnetAssets() {
		if a.Network == network && a.Symbol == symbol {
			if contract != a.Contract {
				return chain.Asset{}, fmt.Errorf("sweep: %s %s contract/mint %s does not match the built-in %s", network, symbol, clip(contract), a.Contract)
			}
			return a, nil
		}
	}
	return chain.Asset{}, fmt.Errorf("sweep: asset %s is not whitelisted on %s", clip(symbol), network)
}

// AssetBySymbol returns the hard-coded mainnet asset for network+symbol.
func AssetBySymbol(network chain.Network, symbol string) (chain.Asset, error) {
	for _, a := range chain.MainnetAssets() {
		if a.Network == network && a.Symbol == symbol {
			return a, nil
		}
	}
	return chain.Asset{}, fmt.Errorf("sweep: asset %s is not whitelisted on %s", clip(symbol), network)
}

func mustBig(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("sweep: bad constant " + s)
	}
	return v
}
