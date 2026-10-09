package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatGPTAttestationMetadataIsStrictAndScoped(t *testing.T) {
	challenge, err := ChatGPTAttestationResponse([]byte(`{ "attestation_challenge":"TEST_ONLY_CHALLENGE", "future": [1,2] }`))
	require.NoError(t, err)
	require.Equal(t, "TEST_ONLY_CHALLENGE", challenge)
	scope := ChatGPTTurnScope{UserID: 1, APIKeyID: 2, GroupID: 3}
	key := scope.AttestationKey(challenge)
	require.NotContains(t, key, challenge)
	require.NotEqual(t, scope.UploadedFileKey(challenge), key)
	for _, other := range []ChatGPTTurnScope{{UserID: 4, APIKeyID: 2, GroupID: 3}, {UserID: 1, APIKeyID: 4, GroupID: 3}, {UserID: 1, APIKeyID: 2, GroupID: 4}} {
		require.NotEqual(t, key, other.AttestationKey(challenge))
	}
	for _, raw := range []string{`{}`, `null`, `[]`, `{"attestation_challenge":null}`, `{"attestation_challenge":1}`, `{"attestation_challenge":""}`, `{"attestation_challenge":"one","attestation_challenge":"two"}`, `{"attestation_challenge":"one"} {}`, `{"attestation_challenge":"` + strings.Repeat("x", 32*1024+1) + `"}`} {
		_, err := ChatGPTAttestationResponse([]byte(raw))
		require.Error(t, err)
	}
	for _, raw := range []string{`{}`, `{"model":"auto","future":null}`, `{"app_attest_challenge":null}`} {
		challenge, err := ChatGPTRequestAttestation([]byte(raw))
		require.NoError(t, err)
		require.Empty(t, challenge)
	}
	for _, raw := range []string{`{"app_attest_challenge":""}`, `{"app_attest_challenge":"one","app_attest_challenge":"two"}`} {
		_, err := ChatGPTRequestAttestation([]byte(raw))
		require.Error(t, err)
	}
}
