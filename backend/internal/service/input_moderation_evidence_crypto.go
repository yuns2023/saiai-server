package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Evidence keys are derived from totp.encryption_key with HKDF so the
// SecretEncryptor format and its key usage stay untouched.
const (
	moderationEvidenceEncInfo = "saiai/moderation-evidence/enc/v1"
	moderationEvidenceMACInfo = "saiai/moderation-evidence/mac/v1"
)

var errModerationEvidenceKeyUnknown = errors.New("moderation evidence key is not configured")

// ModerationEvidenceAAD is the authenticated context of one evidence row. It is
// stored immutably next to the ciphertext and re-encoded with this struct's
// field order to rebuild the GCM additional data.
type ModerationEvidenceAAD struct {
	JobID      string   `json:"job_id"`
	UserID     int64    `json:"user_id"`
	GroupID    int64    `json:"group_id"`
	CheckIndex int      `json:"check_index"`
	Kinds      []string `json:"kinds"`
}

// moderationEvidenceSealer encrypts evidence and keys content MACs. It is an
// interface so tests can inject encryption failures.
type moderationEvidenceSealer interface {
	KeyID() string
	Seal(aad ModerationEvidenceAAD, plaintext []byte) (string, error)
	MAC(value any) string
}

type moderationEvidenceCrypto struct {
	keyID   string
	enc     []byte
	mac     []byte
	decrypt map[string][]byte
}

func deriveModerationEvidenceKeys(root string) (enc, mac []byte, keyID string, err error) {
	secret, err := hex.DecodeString(strings.TrimSpace(root))
	if err != nil || len(secret) != 32 {
		return nil, nil, "", fmt.Errorf("moderation evidence root key must be 32 bytes hex")
	}
	if enc, err = hkdf.Key(sha256.New, secret, nil, moderationEvidenceEncInfo, 32); err != nil {
		return nil, nil, "", err
	}
	if mac, err = hkdf.Key(sha256.New, secret, nil, moderationEvidenceMACInfo, 32); err != nil {
		return nil, nil, "", err
	}
	sum := sha256.Sum256(enc)
	return enc, mac, hex.EncodeToString(sum[:])[:16], nil
}

// newModerationEvidenceCrypto uses root for new evidence and MACs; previous
// roots are kept for decryption only.
func newModerationEvidenceCrypto(root string, previous []string) (*moderationEvidenceCrypto, error) {
	enc, mac, keyID, err := deriveModerationEvidenceKeys(root)
	if err != nil {
		return nil, err
	}
	c := &moderationEvidenceCrypto{keyID: keyID, enc: enc, mac: mac, decrypt: map[string][]byte{keyID: enc}}
	for _, old := range previous {
		oldEnc, _, oldID, err := deriveModerationEvidenceKeys(old)
		if err != nil {
			return nil, fmt.Errorf("previous moderation evidence key: %w", err)
		}
		if _, exists := c.decrypt[oldID]; !exists {
			c.decrypt[oldID] = oldEnc
		}
	}
	return c, nil
}

func (c *moderationEvidenceCrypto) KeyID() string { return c.keyID }

func moderationEvidenceAADBytes(aad ModerationEvidenceAAD) []byte {
	if aad.Kinds == nil {
		aad.Kinds = []string{}
	}
	b, _ := json.Marshal(aad)
	return b
}

func (c *moderationEvidenceCrypto) Seal(aad ModerationEvidenceAAD, plaintext []byte) (string, error) {
	gcm, err := moderationEvidenceGCM(c.enc)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plaintext, moderationEvidenceAADBytes(aad))), nil
}

func (c *moderationEvidenceCrypto) Open(keyID string, aad ModerationEvidenceAAD, ciphertext string) ([]byte, error) {
	key, ok := c.decrypt[keyID]
	if !ok {
		return nil, errModerationEvidenceKeyUnknown
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode evidence: %w", err)
	}
	gcm, err := moderationEvidenceGCM(key)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, fmt.Errorf("evidence ciphertext too short")
	}
	plaintext, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], moderationEvidenceAADBytes(aad))
	if err != nil {
		return nil, fmt.Errorf("evidence authentication failed")
	}
	return plaintext, nil
}

// MAC returns hex HMAC-SHA256 over the canonical JSON encoding of value.
func (c *moderationEvidenceCrypto) MAC(value any) string {
	b, _ := json.Marshal(value)
	h := hmac.New(sha256.New, c.mac)
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func moderationEvidenceGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	return cipher.NewGCM(block)
}
