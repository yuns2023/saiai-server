package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// Keep the real provider challenge only in transit. The cache contains its
// scoped digest and original OAuth owner, and expires conservatively.
const ChatGPTAttestationTTL = 5 * time.Minute

func (s ChatGPTTurnScope) AttestationKey(challenge string) string {
	return s.uploadKey("attestation", challenge)
}

func validChatGPTAttestation(challenge string) bool {
	return len(challenge) > 0 && len(challenge) <= 32*1024
}

func ChatGPTAttestationResponse(raw []byte) (string, error) {
	return chatGPTAttestationField(raw, "attestation_challenge", true)
}

// Inspect the observed Mac native field without reserializing the request.
// Reject duplicate integrity fields rather than relying on parser-specific
// first/last-key behavior at the two ends of the proxy.
func ChatGPTRequestAttestation(raw []byte) (string, error) {
	return chatGPTAttestationField(raw, "app_attest_challenge", false)
}

func chatGPTAttestationField(raw []byte, field string, required bool) (string, error) {
	invalid := errors.New("invalid native Chat attestation metadata")
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return "", invalid
	}
	challenge, seen := "", false
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return "", invalid
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return "", invalid
		}
		if key == field {
			if seen {
				return "", invalid
			}
			seen = true
			// Other native clients may represent the optional field as null.
			// Preserve that request without requiring Mac-specific ownership.
			if !required && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				continue
			}
			if json.Unmarshal(value, &challenge) != nil || !validChatGPTAttestation(challenge) {
				return "", invalid
			}
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return "", invalid
	}
	if _, err := decoder.Token(); err != io.EOF || required && !seen {
		return "", invalid
	}
	return challenge, nil
}
