package chain

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/mr-tron/base58"
)

// SolanaPoolManifestVersion is the only supported manifest version.
const SolanaPoolManifestVersion = 1

// MaxSolanaPoolEntries bounds one manifest (sanity limit, not a product limit).
const MaxSolanaPoolEntries = 1_000_000

// SolanaPoolManifestSigningBytes returns the exact bytes that are signed.
//
// Canonical form: encoding/json.Marshal of the SolanaPoolManifest struct with
// Signature set to "" (the field is tagged omitempty, so it is omitted). That
// yields compact JSON with keys in struct order:
//
//	{"version":1,"network":"solana-mainnet","path_template":"m/44'/501'/{i}'/0'",
//	 "start":0,"entries":[{"index":0,"address":"..."},...],"signer_pubkey":"..."}
//
// (no whitespace; shown wrapped here). Verifiers never sign/verify the raw file
// bytes: they decode the file and re-marshal, so formatting of the file on disk
// is irrelevant, but unknown fields are rejected so nothing unsigned can ride
// along.
func SolanaPoolManifestSigningBytes(m *SolanaPoolManifest) ([]byte, error) {
	c := *m
	c.Signature = ""
	return json.Marshal(&c)
}

// VerifySolanaPoolManifest decodes, signature-checks and validates an offline
// pool manifest. trustedSignerPubkey (base58) comes from server configuration,
// never from the manifest itself.
func VerifySolanaPoolManifest(raw []byte, trustedSignerPubkey string) (*SolanaPoolManifest, error) {
	trusted, err := DecodeSolanaPubkey(trustedSignerPubkey)
	if err != nil {
		return nil, fmt.Errorf("chain: trusted signer pubkey: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m SolanaPoolManifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("chain: decode manifest: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("chain: trailing data after manifest JSON")
	}

	signer, err := DecodeSolanaPubkey(m.SignerPubkey)
	if err != nil {
		return nil, fmt.Errorf("chain: manifest signer_pubkey: %w", err)
	}
	if !bytes.Equal(signer, trusted) {
		return nil, errors.New("chain: manifest signer_pubkey is not the trusted signer")
	}
	sig, err := base58.Decode(m.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, errors.New("chain: manifest signature missing or malformed")
	}
	msg, err := SolanaPoolManifestSigningBytes(&m)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(ed25519.PublicKey(trusted), msg, sig) {
		return nil, errors.New("chain: manifest signature does not verify")
	}

	if err := validateSolanaPoolManifest(&m, signer); err != nil {
		return nil, err
	}
	return &m, nil
}

func validateSolanaPoolManifest(m *SolanaPoolManifest, signer []byte) error {
	if m.Version != SolanaPoolManifestVersion {
		return fmt.Errorf("chain: unsupported manifest version %d", m.Version)
	}
	if m.Network.Family() != FamilySolana {
		return fmt.Errorf("chain: manifest network %s is not a known Solana network", clip(string(m.Network)))
	}
	if m.PathTemplate != SolanaPoolPathTemplate {
		return fmt.Errorf("chain: manifest path_template %s, want %q", clip(m.PathTemplate), SolanaPoolPathTemplate)
	}
	if len(m.Entries) == 0 {
		return errors.New("chain: manifest has no entries")
	}
	if len(m.Entries) > MaxSolanaPoolEntries {
		return fmt.Errorf("chain: manifest has %d entries (max %d)", len(m.Entries), MaxSolanaPoolEntries)
	}
	if m.Entries[0].Index != m.Start {
		return fmt.Errorf("chain: first entry index %d != start %d", m.Entries[0].Index, m.Start)
	}
	signerAddr := base58.Encode(signer)
	seen := make(map[string]struct{}, len(m.Entries))
	for i, e := range m.Entries {
		if e.Index >= 0x80000000 {
			return fmt.Errorf("chain: entry %d index %d must be < 2^31 (hardened path component)", i, e.Index)
		}
		if i > 0 && e.Index <= m.Entries[i-1].Index {
			return fmt.Errorf("chain: entry indexes must be strictly increasing (entry %d: %d after %d)", i, e.Index, m.Entries[i-1].Index)
		}
		if _, err := DecodeSolanaPubkey(e.Address); err != nil {
			return fmt.Errorf("chain: entry %d: %w", i, err)
		}
		if e.Address == signerAddr {
			return fmt.Errorf("chain: entry %d address equals the manifest signer", i)
		}
		if _, dup := seen[e.Address]; dup {
			return fmt.Errorf("chain: duplicate address %s in manifest", e.Address)
		}
		seen[e.Address] = struct{}{}
	}
	return nil
}
