package sweep

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mr-tron/base58"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/chain"
	"github.com/Wei-Shaw/sub2api/internal/cryptopay/hdkey"
)

// Fixed role paths. The offline side derives every signer and the sweep
// target from the mnemonic itself; a plan can never name them.
const (
	// EVMTreasuryPath is the EVM treasury: pays gas top-ups, receives sweeps,
	// signs sends. Separate account (1') from deposit addresses (0').
	EVMTreasuryPath = "m/44'/60'/1'/0/0"
	// SolanaSignerIndex is the pool-manifest signing key (never a source).
	SolanaSignerIndex uint32 = 2147483000
	// SolanaTreasuryIndex is the Solana treasury: fee payer, nonce authority,
	// sweep target, send signer.
	SolanaTreasuryIndex uint32 = 2147483001
	// SolanaPoolIndexLimit: Solana pool sources must have index < this.
	SolanaPoolIndexLimit uint32 = 2147483000
)

// SolanaTreasuryPath is m/44'/501'/2147483001'/0'.
var SolanaTreasuryPath = SolanaPoolPath(SolanaTreasuryIndex)

// SolanaPoolPath returns m/44'/501'/i'/0'.
func SolanaPoolPath(i uint32) string {
	return strings.Replace(chain.SolanaPoolPathTemplate, "{i}", strconv.FormatUint(uint64(i), 10), 1)
}

// EVMDepositPath returns m/44'/60'/0'/0/i.
func EVMDepositPath(i uint32) string {
	return chain.EVMAccountPath + "/0/" + strconv.FormatUint(uint64(i), 10)
}

// Keyring derives role and source keys from a BIP39 seed. Offline only.
type Keyring struct {
	evmMaster *hdkey.ExtendedPrivateKey
	solMaster *hdkey.SLIP10Ed25519Key
}

// NewKeyring builds a keyring from a BIP39 seed.
func NewKeyring(seed []byte) (*Keyring, error) {
	m, err := hdkey.NewMasterKey(seed)
	if err != nil {
		return nil, err
	}
	return &Keyring{evmMaster: m, solMaster: hdkey.NewSLIP10Ed25519Master(seed)}, nil
}

// EVMKey is a derived secp256k1 key with its lowercase 0x address.
type EVMKey struct {
	Address string
	Private []byte // 32-byte scalar
}

func (k *Keyring) evmAt(path string) (*EVMKey, error) {
	x, err := k.evmMaster.DerivePath(path)
	if err != nil {
		return nil, err
	}
	b := x.Key.Bytes()
	return &EVMKey{Address: chain.EVMAddressFromPubKey(x.Neuter().PubKey), Private: b[:]}, nil
}

// EVMTreasury derives the EVM treasury key.
func (k *Keyring) EVMTreasury() (*EVMKey, error) { return k.evmAt(EVMTreasuryPath) }

// EVMDeposit derives deposit address i (m/44'/60'/0'/0/i).
func (k *Keyring) EVMDeposit(i uint32) (*EVMKey, error) {
	if i >= hdkey.HardenedOffset {
		return nil, errors.New("sweep: EVM index must be < 2^31")
	}
	return k.evmAt(EVMDepositPath(i))
}

// SolanaKey is a derived ed25519 key.
type SolanaKey struct {
	Address string
	Private ed25519.PrivateKey
}

func (k *Keyring) solAt(i uint32) (*SolanaKey, error) {
	if i >= hdkey.HardenedOffset {
		return nil, errors.New("sweep: Solana index must be < 2^31")
	}
	x, err := k.solMaster.DerivePath(SolanaPoolPath(i))
	if err != nil {
		return nil, err
	}
	return &SolanaKey{Address: base58.Encode(x.PublicKey()), Private: x.PrivateKey()}, nil
}

// SolanaTreasury derives the Solana treasury key.
func (k *Keyring) SolanaTreasury() (*SolanaKey, error) { return k.solAt(SolanaTreasuryIndex) }

// SolanaPool derives pool owner i; reserved and out-of-range indices are refused.
func (k *Keyring) SolanaPool(i uint32) (*SolanaKey, error) {
	if i >= SolanaPoolIndexLimit {
		return nil, fmt.Errorf("sweep: Solana pool index %d is reserved (pool indices must be < %d)", i, SolanaPoolIndexLimit)
	}
	return k.solAt(i)
}
