// Package hdkey implements the minimum of BIP32 (secp256k1) and SLIP-10
// (ed25519) needed by the stablecoin deposit feature.
//
// The server only ever uses the public half (ParseExtendedPublicKey +
// PublicChild). Private derivation exists for the air-gapped
// cmd/cryptopay-offline tool and its tests.
package hdkey

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/mr-tron/base58"
)

// HardenedOffset is the first hardened child index (2^31).
const HardenedOffset uint32 = 0x80000000

// Mainnet BIP32 version bytes.
var (
	VersionXpub = [4]byte{0x04, 0x88, 0xB2, 0x1E}
	VersionXprv = [4]byte{0x04, 0x88, 0xAD, 0xE4}
)

var (
	ErrPrivateKeyGiven = errors.New("hdkey: extended PRIVATE key (xprv) given; only an xpub may be imported")
	ErrHardenedPublic  = errors.New("hdkey: cannot derive a hardened child from a public key")
	ErrInvalidChild    = errors.New("hdkey: derived child is invalid (IL >= n or point at infinity); use the next index")
)

// ExtendedPublicKey is a BIP32 extended public key.
type ExtendedPublicKey struct {
	Depth       byte
	ParentFP    [4]byte
	ChildNumber uint32
	ChainCode   [32]byte
	PubKey      *secp256k1.PublicKey
}

// ExtendedPrivateKey is a BIP32 extended private key (offline tool only).
type ExtendedPrivateKey struct {
	Depth       byte
	ParentFP    [4]byte
	ChildNumber uint32
	ChainCode   [32]byte
	Key         secp256k1.ModNScalar
}

func hash160Fingerprint(compressed []byte) [4]byte {
	// BIP32 fingerprint = first 4 bytes of RIPEMD160(SHA256(pubkey)); only used
	// when serializing child keys.
	s := sha256.Sum256(compressed)
	r := ripemd160Sum(s[:])
	var fp [4]byte
	copy(fp[:], r[:4])
	return fp
}

// ParseExtendedPublicKey parses a Base58Check mainnet xpub. Private keys and
// any other version bytes are rejected, as are invalid public keys.
func ParseExtendedPublicKey(s string) (*ExtendedPublicKey, error) {
	payload, err := decodeBase58Check(strings.TrimSpace(s))
	if err != nil {
		return nil, err
	}
	if len(payload) != 78 {
		return nil, fmt.Errorf("hdkey: extended key payload is %d bytes, want 78", len(payload))
	}
	var version [4]byte
	copy(version[:], payload[0:4])
	switch version {
	case VersionXpub:
	case VersionXprv:
		return nil, ErrPrivateKeyGiven
	default:
		return nil, fmt.Errorf("hdkey: unsupported extended key version %x (want mainnet xpub)", version)
	}
	k := &ExtendedPublicKey{Depth: payload[4], ChildNumber: binary.BigEndian.Uint32(payload[9:13])}
	copy(k.ParentFP[:], payload[5:9])
	copy(k.ChainCode[:], payload[13:45])
	if k.Depth == 0 && (k.ParentFP != [4]byte{} || k.ChildNumber != 0) {
		return nil, errors.New("hdkey: depth 0 key with non-zero parent fingerprint or child number")
	}
	keyData := payload[45:78]
	if keyData[0] != 0x02 && keyData[0] != 0x03 {
		return nil, fmt.Errorf("hdkey: invalid compressed public key prefix %#x", keyData[0])
	}
	pub, err := secp256k1.ParsePubKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("hdkey: invalid public key: %w", err)
	}
	k.PubKey = pub
	return k, nil
}

// String serializes the key as a Base58Check mainnet xpub.
func (k *ExtendedPublicKey) String() string {
	buf := make([]byte, 0, 78)
	buf = append(buf, VersionXpub[:]...)
	buf = append(buf, k.Depth)
	buf = append(buf, k.ParentFP[:]...)
	buf = binary.BigEndian.AppendUint32(buf, k.ChildNumber)
	buf = append(buf, k.ChainCode[:]...)
	buf = append(buf, k.PubKey.SerializeCompressed()...)
	return encodeBase58Check(buf)
}

// PublicChild implements BIP32 CKDpub for a non-hardened index.
func (k *ExtendedPublicKey) PublicChild(index uint32) (*ExtendedPublicKey, error) {
	if index >= HardenedOffset {
		return nil, ErrHardenedPublic
	}
	if k.Depth == 255 {
		return nil, errors.New("hdkey: maximum depth reached")
	}
	parentPub := k.PubKey.SerializeCompressed()
	data := make([]byte, 0, 37)
	data = append(data, parentPub...)
	data = binary.BigEndian.AppendUint32(data, index)
	il, ir := hmacSHA512(k.ChainCode[:], data)

	var tweak secp256k1.ModNScalar
	if overflow := tweak.SetByteSlice(il); overflow {
		return nil, ErrInvalidChild
	}
	var tweakPoint, parentPoint, sum secp256k1.JacobianPoint
	secp256k1.ScalarBaseMultNonConst(&tweak, &tweakPoint)
	k.PubKey.AsJacobian(&parentPoint)
	secp256k1.AddNonConst(&tweakPoint, &parentPoint, &sum)
	if (sum.X.IsZero() && sum.Y.IsZero()) || sum.Z.IsZero() {
		return nil, ErrInvalidChild
	}
	sum.ToAffine()
	child := &ExtendedPublicKey{
		Depth:       k.Depth + 1,
		ParentFP:    hash160Fingerprint(parentPub),
		ChildNumber: index,
		PubKey:      secp256k1.NewPublicKey(&sum.X, &sum.Y),
	}
	copy(child.ChainCode[:], ir)
	return child, nil
}

// NewMasterKey derives the BIP32 master key from a seed (16..64 bytes).
func NewMasterKey(seed []byte) (*ExtendedPrivateKey, error) {
	if len(seed) < 16 || len(seed) > 64 {
		return nil, fmt.Errorf("hdkey: seed length %d out of range [16,64]", len(seed))
	}
	il, ir := hmacSHA512([]byte("Bitcoin seed"), seed)
	k := &ExtendedPrivateKey{}
	if overflow := k.Key.SetByteSlice(il); overflow || k.Key.IsZero() {
		return nil, ErrInvalidChild
	}
	copy(k.ChainCode[:], ir)
	return k, nil
}

func (k *ExtendedPrivateKey) publicKey() *secp256k1.PublicKey {
	return secp256k1.NewPrivateKey(&k.Key).PubKey()
}

// Child implements BIP32 CKDpriv.
func (k *ExtendedPrivateKey) Child(index uint32) (*ExtendedPrivateKey, error) {
	if k.Depth == 255 {
		return nil, errors.New("hdkey: maximum depth reached")
	}
	parentPub := k.publicKey().SerializeCompressed()
	data := make([]byte, 0, 37)
	if index >= HardenedOffset {
		keyBytes := k.Key.Bytes()
		data = append(data, 0x00)
		data = append(data, keyBytes[:]...)
	} else {
		data = append(data, parentPub...)
	}
	data = binary.BigEndian.AppendUint32(data, index)
	il, ir := hmacSHA512(k.ChainCode[:], data)
	var tweak secp256k1.ModNScalar
	if overflow := tweak.SetByteSlice(il); overflow {
		return nil, ErrInvalidChild
	}
	child := &ExtendedPrivateKey{
		Depth:       k.Depth + 1,
		ParentFP:    hash160Fingerprint(parentPub),
		ChildNumber: index,
	}
	child.Key.Set(&tweak).Add(&k.Key)
	if child.Key.IsZero() {
		return nil, ErrInvalidChild
	}
	copy(child.ChainCode[:], ir)
	return child, nil
}

// Neuter returns the extended public key.
func (k *ExtendedPrivateKey) Neuter() *ExtendedPublicKey {
	return &ExtendedPublicKey{
		Depth:       k.Depth,
		ParentFP:    k.ParentFP,
		ChildNumber: k.ChildNumber,
		ChainCode:   k.ChainCode,
		PubKey:      k.publicKey(),
	}
}

// String serializes the key as a Base58Check mainnet xprv (tests only; the
// offline tool never prints it).
func (k *ExtendedPrivateKey) String() string {
	buf := make([]byte, 0, 78)
	buf = append(buf, VersionXprv[:]...)
	buf = append(buf, k.Depth)
	buf = append(buf, k.ParentFP[:]...)
	buf = binary.BigEndian.AppendUint32(buf, k.ChildNumber)
	buf = append(buf, k.ChainCode[:]...)
	keyBytes := k.Key.Bytes()
	buf = append(buf, 0x00)
	buf = append(buf, keyBytes[:]...)
	return encodeBase58Check(buf)
}

// DerivePath derives a BIP32 path like "m/44'/60'/0'/0/1".
func (k *ExtendedPrivateKey) DerivePath(path string) (*ExtendedPrivateKey, error) {
	indexes, err := ParsePath(path)
	if err != nil {
		return nil, err
	}
	cur := k
	for _, idx := range indexes {
		if cur, err = cur.Child(idx); err != nil {
			return nil, err
		}
	}
	return cur, nil
}

// ParsePath parses "m/44'/60'/0'/0/1" (also accepts h/H as hardened markers).
func ParsePath(path string) ([]uint32, error) {
	parts := strings.Split(strings.TrimSpace(path), "/")
	if len(parts) == 0 || parts[0] != "m" {
		return nil, fmt.Errorf("hdkey: path %q must start with m", path)
	}
	out := make([]uint32, 0, len(parts)-1)
	for _, p := range parts[1:] {
		hardened := false
		if strings.HasSuffix(p, "'") || strings.HasSuffix(p, "h") || strings.HasSuffix(p, "H") {
			hardened = true
			p = p[:len(p)-1]
		}
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil || n >= uint64(HardenedOffset) {
			return nil, fmt.Errorf("hdkey: invalid path component %q", p)
		}
		idx := uint32(n)
		if hardened {
			idx += HardenedOffset
		}
		out = append(out, idx)
	}
	return out, nil
}

// SLIP10Ed25519Key is a SLIP-10 ed25519 node (hardened derivation only).
type SLIP10Ed25519Key struct {
	Key       [32]byte // ed25519 private seed
	ChainCode [32]byte
}

// NewSLIP10Ed25519Master derives the SLIP-10 ed25519 master node.
func NewSLIP10Ed25519Master(seed []byte) *SLIP10Ed25519Key {
	il, ir := hmacSHA512([]byte("ed25519 seed"), seed)
	k := &SLIP10Ed25519Key{}
	copy(k.Key[:], il)
	copy(k.ChainCode[:], ir)
	return k
}

// Child derives a hardened child; index is the hardened index without the
// 2^31 offset or with it (both accepted, ed25519 is hardened-only).
func (k *SLIP10Ed25519Key) Child(index uint32) *SLIP10Ed25519Key {
	index |= HardenedOffset
	data := make([]byte, 0, 37)
	data = append(data, 0x00)
	data = append(data, k.Key[:]...)
	data = binary.BigEndian.AppendUint32(data, index)
	il, ir := hmacSHA512(k.ChainCode[:], data)
	c := &SLIP10Ed25519Key{}
	copy(c.Key[:], il)
	copy(c.ChainCode[:], ir)
	return c
}

// DerivePath derives a SLIP-10 ed25519 path; every component must be hardened.
func (k *SLIP10Ed25519Key) DerivePath(path string) (*SLIP10Ed25519Key, error) {
	indexes, err := ParsePath(path)
	if err != nil {
		return nil, err
	}
	cur := k
	for _, idx := range indexes {
		if idx < HardenedOffset {
			return nil, fmt.Errorf("hdkey: ed25519 path %q has a non-hardened component", path)
		}
		cur = cur.Child(idx)
	}
	return cur, nil
}

// PrivateKey returns the ed25519 private key for this node.
func (k *SLIP10Ed25519Key) PrivateKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(k.Key[:])
}

// PublicKey returns the 32-byte ed25519 public key.
func (k *SLIP10Ed25519Key) PublicKey() ed25519.PublicKey {
	priv := k.PrivateKey()
	pub := make(ed25519.PublicKey, ed25519.PublicKeySize)
	copy(pub, priv[ed25519.SeedSize:]) // ed25519.PrivateKey = seed || public key
	return pub
}

func hmacSHA512(key, data []byte) (il, ir []byte) {
	mac := hmac.New(sha512.New, key)
	_, _ = mac.Write(data) // hash.Hash.Write never fails
	sum := mac.Sum(nil)
	return sum[:32], sum[32:]
}

func encodeBase58Check(payload []byte) string {
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	full := make([]byte, 0, len(payload)+4)
	full = append(full, payload...)
	full = append(full, second[:4]...)
	return base58.Encode(full)
}

func decodeBase58Check(s string) ([]byte, error) {
	raw, err := base58.Decode(s)
	if err != nil {
		return nil, fmt.Errorf("hdkey: invalid base58: %w", err)
	}
	if len(raw) < 5 {
		return nil, errors.New("hdkey: base58check payload too short")
	}
	payload, checksum := raw[:len(raw)-4], raw[len(raw)-4:]
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	if !bytes.Equal(second[:4], checksum) {
		return nil, errors.New("hdkey: base58check checksum mismatch")
	}
	return payload, nil
}
