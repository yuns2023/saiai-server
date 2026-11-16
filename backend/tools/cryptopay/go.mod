// Offline sweep tooling for the stablecoin deposit feature. A separate module
// so that go-ethereum / solana-go (and the dependency upgrades they force)
// never enter the Server binary's module graph. See sweep/plan.go.
module github.com/Wei-Shaw/sub2api/tools/cryptopay

go 1.26.11

require (
	github.com/Wei-Shaw/sub2api v0.0.0-00010101000000-000000000000
	github.com/ethereum/go-ethereum v1.17.10
	github.com/gagliardetto/solana-go v1.26.0
	github.com/mr-tron/base58 v1.3.0
	github.com/tyler-smith/go-bip39 v1.1.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/ProjectZKM/Ziren/crates/go-runtime/zkvm_runtime v0.0.0-20251001021608-1fe7b43fc4d6 // indirect
	github.com/benbjohnson/clock v1.3.5 // indirect
	github.com/bits-and-blooms/bitset v1.20.0 // indirect
	github.com/blendle/zapdriver v1.3.1 // indirect
	github.com/consensys/gnark-crypto v0.18.1 // indirect
	github.com/crate-crypto/go-eth-kzg v1.5.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1 // indirect
	github.com/ethereum/c-kzg-4844/v2 v2.1.8 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/gagliardetto/binary v0.8.0 // indirect
	github.com/gagliardetto/treeout v0.1.4 // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/holiman/uint256 v1.3.2 // indirect
	github.com/klauspost/compress v1.20.2 // indirect
	github.com/logrusorgru/aurora v2.0.3+incompatible // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mitchellh/go-testing-interface v1.14.1 // indirect
	github.com/mostynb/zstdpool-freelist v0.0.0-20201229113212-927304c0c3b1 // indirect
	github.com/oasisprotocol/curve25519-voi v0.0.0-20251114093237-2ab5a27a1729 // indirect
	github.com/streamingfast/logging v0.0.0-20250404134358-92b15d2fbd2e // indirect
	github.com/supranational/blst v0.3.16 // indirect
	go.mongodb.org/mongo-driver/v2 v2.5.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/ratelimit v0.3.1 // indirect
	go.uber.org/zap v1.27.0 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/term v0.46.0 // indirect
	golang.org/x/time v0.14.0 // indirect
)

replace github.com/Wei-Shaw/sub2api => ../..
