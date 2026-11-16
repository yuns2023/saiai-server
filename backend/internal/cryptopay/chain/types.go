// Package chain holds chain-level logic for stablecoin deposits: verified asset
// constants, scanners and watch-only address derivation. It must not depend on
// ent, service or the database. See tmp-crypto/CONTRACT.md for the contract.
package chain

import (
	"context"
	"math/big"
	"time"
)

// Network identifies a chain deployment.
type Network string

const (
	ArbitrumOne     Network = "arbitrum-one"
	SolanaMainnet   Network = "solana-mainnet"
	ArbitrumSepolia Network = "arbitrum-sepolia" // tests/config only
	SolanaDevnet    Network = "solana-devnet"    // tests/config only
)

// Family groups networks that share address and scanning rules.
type Family string

const (
	FamilyEVM    Family = "evm"
	FamilySolana Family = "solana"
)

// Family returns the network family, or "" for an unknown network.
func (n Network) Family() Family {
	switch n {
	case ArbitrumOne, ArbitrumSepolia:
		return FamilyEVM
	case SolanaMainnet, SolanaDevnet:
		return FamilySolana
	default:
		return ""
	}
}

// SolanaTokenProgram is the classic SPL Token program id.
const SolanaTokenProgram = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"

// Asset is a whitelisted token. Identity is (Network, Contract[, TokenProgram]);
// symbols and names reported by RPCs are never trusted.
type Asset struct {
	Network      Network
	Symbol       string // "USDC" | "USDT"
	Contract     string // EVM: lowercase 0x hex; Solana: mint base58
	TokenProgram string // Solana only
	Decimals     int32
}

// MainnetAssets returns the four assets verified on chain on 2026-11-16
// (eth_call symbol/decimals on Arbitrum One, getAccountInfo on Solana).
// Never add an address from memory: verify on chain first.
func MainnetAssets() []Asset {
	return []Asset{
		{Network: ArbitrumOne, Symbol: "USDC", Contract: "0xaf88d065e77c8cc2239327c5edb3a432268e5831", Decimals: 6},
		{Network: ArbitrumOne, Symbol: "USDT", Contract: "0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9", Decimals: 6},
		{Network: SolanaMainnet, Symbol: "USDC", Contract: "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", TokenProgram: SolanaTokenProgram, Decimals: 6},
		{Network: SolanaMainnet, Symbol: "USDT", Contract: "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB", TokenProgram: SolanaTokenProgram, Decimals: 6},
	}
}

// Observation is one inflow candidate. Units are raw integer token units and
// always > 0; zero-value transfers are never emitted.
type Observation struct {
	Network      Network
	Asset        Asset
	TxHash       string // EVM: lowercase 0x tx hash; Solana: signature
	EventKey     string // EVM: "log:<logIndex>"; Solana: "acct:<tokenAccount>"; unique within the tx
	ToAddress    string // deposit address: EVM lowercase 0x; Solana: owner base58
	TokenAccount string // Solana only
	FromAddress  string // informational only, never an identity
	Units        *big.Int
	BlockNumber  uint64 // EVM block / Solana slot
	BlockHash    string
	BlockTime    time.Time
	Finalized    bool
}

// EVMScanner scans whitelisted ERC-20 Transfer logs.
type EVMScanner interface {
	Network() Network
	LatestBlock(ctx context.Context) (uint64, error)
	FinalizedBlock(ctx context.Context) (uint64, error)
	// ScanRange returns ALL matching logs in [from,to] or an error; never a partial result.
	ScanRange(ctx context.Context, from, to uint64, addresses []string, finalized bool) ([]Observation, error)
	// ObservationsForTx returns whitelisted inflows in one tx to any of addresses (manual re-scan).
	ObservationsForTx(ctx context.Context, txHash string, addresses []string, finalized bool) ([]Observation, error)
}

// SolanaScanner walks the signatures of a token account.
type SolanaScanner interface {
	Network() Network
	// ScanTokenAccount returns inflows for signatures newer than untilSig (exclusive; "" = full history).
	// A null getTransaction is an error (retry), never "no inflow".
	ScanTokenAccount(ctx context.Context, owner, tokenAccount string, asset Asset, untilSig string, commitment string) (obs []Observation, newestSig string, err error)
	ObservationsForTx(ctx context.Context, signature string, owners []string, commitment string) ([]Observation, error)
}

// SolanaPoolManifest is the offline-signed list of Solana deposit owners.
type SolanaPoolManifest struct {
	Version      int               `json:"version"`
	Network      Network           `json:"network"`
	PathTemplate string            `json:"path_template"`
	Start        uint32            `json:"start"`
	Entries      []SolanaPoolEntry `json:"entries"`
	SignerPubkey string            `json:"signer_pubkey"`
	Signature    string            `json:"signature,omitempty"`
}

// SolanaPoolEntry is one pre-generated owner address.
type SolanaPoolEntry struct {
	Index   uint32 `json:"index"`
	Address string `json:"address"`
}
