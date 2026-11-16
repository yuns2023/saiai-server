package chain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"filippo.io/edwards25519"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/mr-tron/base58"
	"golang.org/x/crypto/sha3"

	"github.com/Wei-Shaw/sub2api/internal/cryptopay/hdkey"
)

// EVMAccountPath is the BIP32 path of the account-level xpub the server holds.
const EVMAccountPath = "m/44'/60'/0'"

// SolanaPoolPathTemplate is the Phantom-compatible SLIP-10 path of pool owners.
const SolanaPoolPathTemplate = "m/44'/501'/{i}'/0'"

// AssociatedTokenProgram is the SPL Associated Token Account program id.
const AssociatedTokenProgram = "ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL"

// TransferTopic0 = keccak256("Transfer(address,address,uint256)"); asserted in tests.
const TransferTopic0 = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// Keccak256 is the legacy Keccak-256 used by Ethereum.
func Keccak256(data ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, d := range data {
		_, _ = h.Write(d) // hash.Hash.Write never fails
	}
	return h.Sum(nil)
}

// EVMAddressFromPubKey returns the lowercase 0x address of a secp256k1 key.
func EVMAddressFromPubKey(pub *secp256k1.PublicKey) string {
	unc := pub.SerializeUncompressed() // 0x04 || X || Y
	return "0x" + hex.EncodeToString(Keccak256(unc[1:])[12:])
}

// EVMAddressFromXpub derives m/44'/60'/0'/0/index from the account xpub at
// m/44'/60'/0' and returns the lowercase 0x address. The xpub must be a mainnet
// xpub at depth 3 with child number 0' (rejects xprv and wrong-level keys).
// An xpub cannot prove its ancestors were 44'/60': a depth-3 account-0 key from
// another path would pass, so imports must still be cross-checked against the
// first addresses printed by cmd/cryptopay-offline evm-xpub.
func EVMAddressFromXpub(xpub string, index uint32) (string, error) {
	acct, err := ParseEVMAccountXpub(xpub)
	if err != nil {
		return "", err
	}
	return evmAddressFromAccount(acct, index)
}

// ParseEVMAccountXpub parses and validates the account-level xpub once, for
// callers that derive many addresses.
func ParseEVMAccountXpub(xpub string) (*hdkey.ExtendedPublicKey, error) {
	acct, err := hdkey.ParseExtendedPublicKey(xpub)
	if err != nil {
		return nil, err
	}
	if acct.Depth != 3 || acct.ChildNumber != hdkey.HardenedOffset {
		return nil, fmt.Errorf("chain: xpub must be the account key at %s (depth 3, child 0'); got depth %d child %#x",
			EVMAccountPath, acct.Depth, acct.ChildNumber)
	}
	return acct, nil
}

// EVMAddressFromAccount derives /0/index from an account xpub that MUST come
// from ParseEVMAccountXpub (the level checks are not repeated here).
func EVMAddressFromAccount(acct *hdkey.ExtendedPublicKey, index uint32) (string, error) {
	return evmAddressFromAccount(acct, index)
}

func evmAddressFromAccount(acct *hdkey.ExtendedPublicKey, index uint32) (string, error) {
	if index >= hdkey.HardenedOffset {
		return "", fmt.Errorf("chain: EVM address index %d must be < 2^31", index)
	}
	external, err := acct.PublicChild(0)
	if err != nil {
		return "", err
	}
	child, err := external.PublicChild(index)
	if err != nil {
		return "", err
	}
	return EVMAddressFromPubKey(child.PubKey), nil
}

// NormalizeEVMAddress validates a 0x + 40 hex address and lowercases it.
func NormalizeEVMAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) != 42 || (s[:2] != "0x" && s[:2] != "0X") {
		return "", fmt.Errorf("chain: invalid EVM address %q", s)
	}
	if _, err := hex.DecodeString(s[2:]); err != nil {
		return "", fmt.Errorf("chain: invalid EVM address %q", s)
	}
	return "0x" + strings.ToLower(s[2:]), nil
}

// DecodeSolanaPubkey decodes a base58 Solana address and requires 32 bytes.
func DecodeSolanaPubkey(s string) ([]byte, error) {
	if s == "" || len(s) > 44 {
		return nil, fmt.Errorf("chain: invalid Solana address %q", s)
	}
	b, err := base58.Decode(s)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("chain: invalid Solana address %q", s)
	}
	// Reject non-canonical encodings (e.g. extra leading '1's are length-checked
	// above, but make the round trip explicit).
	if base58.Encode(b) != s {
		return nil, fmt.Errorf("chain: non-canonical Solana address %q", s)
	}
	return b, nil
}

// IsOnEd25519Curve reports whether 32 bytes decode to an ed25519 point, with the
// same (non-canonical-accepting) rules as curve25519-dalek used by Solana.
func IsOnEd25519Curve(b []byte) bool {
	if len(b) != 32 {
		return false
	}
	_, err := new(edwards25519.Point).SetBytes(b)
	return err == nil
}

var errNoPDA = errors.New("chain: unable to find a viable program address bump")

// FindProgramAddress implements Solana's find_program_address.
func FindProgramAddress(seeds [][]byte, programID []byte) ([]byte, uint8, error) {
	if len(seeds) > 15 { // MAX_SEEDS 16 including the bump
		return nil, 0, errors.New("chain: too many PDA seeds")
	}
	for _, s := range seeds {
		if len(s) > 32 {
			return nil, 0, errors.New("chain: PDA seed longer than 32 bytes")
		}
	}
	for bump := 255; bump >= 0; bump-- {
		buf := make([]byte, 0, 32*len(seeds)+1+len(programID)+21)
		for _, s := range seeds {
			buf = append(buf, s...)
		}
		buf = append(buf, byte(bump))
		buf = append(buf, programID...)
		buf = append(buf, "ProgramDerivedAddress"...)
		sum := sha256.Sum256(buf)
		if !IsOnEd25519Curve(sum[:]) {
			return sum[:], uint8(bump), nil
		}
	}
	return nil, 0, errNoPDA
}

// SolanaATA returns the associated token account of owner for mint under the
// given token program (seeds = [owner, tokenProgram, mint]).
func SolanaATA(owner, mint, tokenProgram string) (string, error) {
	o, err := DecodeSolanaPubkey(owner)
	if err != nil {
		return "", err
	}
	m, err := DecodeSolanaPubkey(mint)
	if err != nil {
		return "", err
	}
	tp, err := DecodeSolanaPubkey(tokenProgram)
	if err != nil {
		return "", err
	}
	ataProgram, err := DecodeSolanaPubkey(AssociatedTokenProgram)
	if err != nil {
		return "", err
	}
	addr, _, err := FindProgramAddress([][]byte{o, tp, m}, ataProgram)
	if err != nil {
		return "", err
	}
	return base58.Encode(addr), nil
}
