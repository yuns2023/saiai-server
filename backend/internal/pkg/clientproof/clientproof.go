// Package clientproof implements the pure, I/O-free parts of SAIAI client
// proof v1 (docs/CLIENT_PROOF_V1.md): header parsing, the signed message,
// SEC1 public-key decoding and ECDSA P-256 verification.
//
// Device lookup, nonce replay protection and persistence live in the service
// layer; this package never performs I/O.
package clientproof

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const (
	// HeaderName carries the proof. The Gateway strips it before any upstream request.
	HeaderName = "X-Saiai-Client-Proof"
	// MaxHeaderLength caps the total header value length.
	MaxHeaderLength = 256
	// MaxClockSkew is the accepted |now - ts| window.
	MaxClockSkew = 300 * time.Second
	// NonceTTL is how long a seen nonce is remembered.
	NonceTTL = 900 * time.Second

	version        = "v1"
	messageDomain  = "saiai-client-proof-v1"
	deviceIDPrefix = "cd_"
	// 16 random bytes encode to 22 base64url characters without padding.
	randomTokenLength = 22
	publicKeyLength   = 65
	signatureLength   = 64
)

// Status is the attestation status stored in usage_logs.client_attestation.
// The numeric values are part of the persisted contract; never renumber.
type Status int16

const (
	StatusValid         Status = 1
	StatusMissing       Status = 2
	StatusInvalid       Status = 3
	StatusStale         Status = 4
	StatusReplay        Status = 5
	StatusUnknownDevice Status = 6
	StatusRevoked       Status = 7
	StatusUnverified    Status = 8
)

var statusNames = map[Status]string{
	StatusValid:         "valid",
	StatusMissing:       "missing",
	StatusInvalid:       "invalid",
	StatusStale:         "stale",
	StatusReplay:        "replay",
	StatusUnknownDevice: "unknown_device",
	StatusRevoked:       "revoked",
	StatusUnverified:    "unverified",
}

// String returns the stable snake_case name used by the admin API.
func (s Status) String() string {
	if name, ok := statusNames[s]; ok {
		return name
	}
	return "unknown"
}

// Known reports whether s is one of the defined statuses.
func (s Status) Known() bool {
	_, ok := statusNames[s]
	return ok
}

// AllStatuses lists the defined statuses in numeric order.
func AllStatuses() []Status {
	return []Status{StatusValid, StatusMissing, StatusInvalid, StatusStale, StatusReplay, StatusUnknownDevice, StatusRevoked, StatusUnverified}
}

// ErrMalformed is returned for any header or key that does not match the
// wire format exactly.
var ErrMalformed = errors.New("malformed client proof")

// Proof is a parsed X-Saiai-Client-Proof header.
type Proof struct {
	DeviceID  string
	Timestamp int64
	Nonce     string
	Signature []byte
}

// ParseHeader parses "v1.<device_id>.<ts>.<nonce>.<sig>". Any deviation from
// the format, including surrounding whitespace, is ErrMalformed.
func ParseHeader(value string) (Proof, error) {
	if value == "" || len(value) > MaxHeaderLength {
		return Proof{}, ErrMalformed
	}
	parts := strings.Split(value, ".")
	if len(parts) != 5 || parts[0] != version {
		return Proof{}, ErrMalformed
	}
	if !ValidDeviceID(parts[1]) || !validNonce(parts[3]) {
		return Proof{}, ErrMalformed
	}
	ts, ok := parseTimestamp(parts[2])
	if !ok {
		return Proof{}, ErrMalformed
	}
	sig, err := decodeB64URL(parts[4])
	if err != nil || len(sig) != signatureLength {
		return Proof{}, ErrMalformed
	}
	return Proof{DeviceID: parts[1], Timestamp: ts, Nonce: parts[3], Signature: sig}, nil
}

// parseTimestamp accepts only canonical non-negative decimal unix seconds.
func parseTimestamp(raw string) (int64, bool) {
	if raw == "" || len(raw) > 19 || (len(raw) > 1 && raw[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
	}
	ts, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return ts, true
}

// Message builds the exact signed byte string (no trailing newline).
func Message(method, target string, ts int64, nonce, apiKey string) []byte {
	keyDigest := sha256.Sum256([]byte(apiKey))
	return []byte(strings.Join([]string{
		messageDomain,
		strings.ToUpper(method),
		target,
		strconv.FormatInt(ts, 10),
		nonce,
		hex.EncodeToString(keyDigest[:]),
	}, "\n"))
}

// DecodePublicKey decodes the wire encoding: base64url (no padding) of the
// 65-byte SEC1 uncompressed P-256 point. It returns the parsed key and the
// raw point bytes.
func DecodePublicKey(encoded string) (*ecdsa.PublicKey, []byte, error) {
	raw, err := decodeB64URL(encoded)
	if err != nil {
		return nil, nil, ErrMalformed
	}
	key, err := ParsePublicKey(raw)
	if err != nil {
		return nil, nil, err
	}
	return key, raw, nil
}

// ParsePublicKey parses a raw 65-byte SEC1 uncompressed P-256 point and
// rejects points that are not on the curve.
func ParsePublicKey(raw []byte) (*ecdsa.PublicKey, error) {
	if len(raw) != publicKeyLength || raw[0] != 0x04 {
		return nil, ErrMalformed
	}
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		return nil, ErrMalformed
	}
	return key, nil
}

// PublicKeySHA256 returns the lowercase hex SHA-256 of the raw point bytes.
func PublicKeySHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Verify checks a parsed proof against the device public key and request.
// It returns StatusInvalid when the signature does not match, StatusStale
// when the signature matches but |now-ts| exceeds MaxClockSkew, otherwise
// StatusValid. Replay and device state are checked by the caller.
func Verify(proof Proof, key *ecdsa.PublicKey, method, target, apiKey string, now time.Time) Status {
	if key == nil || len(proof.Signature) != signatureLength {
		return StatusInvalid
	}
	digest := sha256.Sum256(Message(method, target, proof.Timestamp, proof.Nonce, apiKey))
	r := new(big.Int).SetBytes(proof.Signature[:32])
	s := new(big.Int).SetBytes(proof.Signature[32:])
	if !ecdsa.Verify(key, digest[:], r, s) {
		return StatusInvalid
	}
	if !Fresh(proof.Timestamp, now) {
		return StatusStale
	}
	return StatusValid
}

// Fresh reports whether ts is within MaxClockSkew of now (inclusive).
func Fresh(ts int64, now time.Time) bool {
	skew := now.Unix() - ts
	if skew < 0 {
		skew = -skew
	}
	return skew <= int64(MaxClockSkew/time.Second)
}

// Sign produces a v1 header value the way the SAIAI client does. The Gateway
// itself never signs; this exists for tests and Go tooling.
func Sign(priv *ecdsa.PrivateKey, deviceID, method, target string, ts int64, nonce, apiKey string) (string, error) {
	if priv == nil {
		return "", errors.New("client proof: nil private key")
	}
	digest := sha256.Sum256(Message(method, target, ts, nonce, apiKey))
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, signatureLength)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return strings.Join([]string{version, deviceID, strconv.FormatInt(ts, 10), nonce, base64.RawURLEncoding.EncodeToString(sig)}, "."), nil
}

// NewNonce returns base64url(16 random bytes).
func NewNonce() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// NewDeviceID returns "cd_" + base64url(16 random bytes).
func NewDeviceID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return deviceIDPrefix + base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// ValidDeviceID reports whether id has the issued device-id shape.
func ValidDeviceID(id string) bool {
	if !strings.HasPrefix(id, deviceIDPrefix) {
		return false
	}
	return validRandomToken(id[len(deviceIDPrefix):])
}

func validNonce(nonce string) bool {
	return validRandomToken(nonce)
}

func validRandomToken(token string) bool {
	if len(token) != randomTokenLength {
		return false
	}
	decoded, err := decodeB64URL(token)
	return err == nil && len(decoded) == 16
}

// decodeB64URL decodes unpadded base64url strictly (no padding, no
// non-canonical trailing bits).
func decodeB64URL(value string) ([]byte, error) {
	return base64.RawURLEncoding.Strict().DecodeString(value)
}
