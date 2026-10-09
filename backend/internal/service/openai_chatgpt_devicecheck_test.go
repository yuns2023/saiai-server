package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChatGPTDeviceProofIdentityLifetimeAndRedaction(t *testing.T) {
	for _, headers := range []http.Header{{"Oai-Did": {"TEST_ONLY_DEVICE"}}, {"Oai-Device-Id": {"TEST_ONLY_DEVICE"}}, {"Oai-Did": {"TEST_ONLY_DEVICE"}, "Oai-Device-Id": {"TEST_ONLY_DEVICE"}}} {
		device, err := ChatGPTDeviceID(headers)
		require.NoError(t, err)
		require.Equal(t, "TEST_ONLY_DEVICE", device)
	}
	_, err := ChatGPTDeviceID(http.Header{"Oai-Did": {"TEST_ONLY_ONE", "TEST_ONLY_TWO"}})
	require.Error(t, err)
	device, err := ChatGPTDeviceID(http.Header{"Oai-Did": {"TEST_ONLY_ONE"}, "Oai-Device-Id": {"TEST_ONLY_TWO"}})
	require.NoError(t, err)
	require.Equal(t, "TEST_ONLY_ONE", device, "the native cookie manager uses oai-did")
	scope := ChatGPTTurnScope{UserID: 1, APIKeyID: 2, GroupID: 3}
	key := scope.DeviceCookieKey("TEST_ONLY_DEVICE", "TEST_ONLY_PROOF")
	require.NotContains(t, key, "TEST_ONLY")
	cache := NewChatGPTMemoryTurnCache().(ChatGPTUploadCache)
	_, err = cache.ClaimChatGPTUploadOwner(context.Background(), key, 22, 24*time.Hour)
	require.NoError(t, err, "session device proofs need their own longer ownership lease")
	_, err = cache.ClaimChatGPTUploadOwner(context.Background(), scope.UploadSessionKey("TEST_ONLY_DEVICE"), 22, 24*time.Hour)
	require.Error(t, err, "attachment leases remain bounded")
	require.False(t, ValidChatGPTUploadClaimTTL(key, 31*24*time.Hour))
	require.NotEqual(t, key, scope.DeviceCookieKey("TEST_ONLY_OTHER", "TEST_ONLY_PROOF"))
	require.NotEqual(t, key, (ChatGPTTurnScope{UserID: 2, APIKeyID: 2, GroupID: 3}).DeviceCookieKey("TEST_ONLY_DEVICE", "TEST_ONLY_PROOF"))
	valid := http.Cookie{Name: "_devicecheck", Value: "TEST_ONLY_PROOF", Domain: ".chatgpt.com", Path: "/", Secure: true, HttpOnly: true, MaxAge: 600}
	now := time.Now()
	ttl, err := ChatGPTDeviceCookieTTL(&valid, now)
	require.NoError(t, err)
	require.Equal(t, 10*time.Minute, ttl)
	for _, mutate := range []func(*http.Cookie){func(c *http.Cookie) { c.Domain = "foreign.invalid" }, func(c *http.Cookie) { c.Secure = false }, func(c *http.Cookie) { c.HttpOnly = false }, func(c *http.Cookie) { c.Name = "session" }, func(c *http.Cookie) { c.Value = "saiai-local-proxy" }, func(c *http.Cookie) { c.MaxAge = -1 }} {
		bad := valid
		mutate(&bad)
		_, err = ChatGPTDeviceCookieTTL(&bad, now)
		require.Error(t, err)
	}
	valid.MaxAge = 0
	valid.Expires = now.Add(-time.Second)
	_, err = ChatGPTDeviceCookieTTL(&valid, now)
	require.Error(t, err)
	redacted := redactSensitiveJSON(map[string]any{"app_attest_challenge": "TEST_ONLY_SECRET", "attestation_challenge": "TEST_ONLY_SECRET", "device_token": "TEST_ONLY_SECRET", "x-sentinel-dc": "TEST_ONLY_SECRET", "Cookie": "TEST_ONLY_SECRET", "input_tokens": 12}).(map[string]any)
	for _, name := range []string{"app_attest_challenge", "attestation_challenge", "device_token", "x-sentinel-dc", "Cookie"} {
		require.Equal(t, "[REDACTED]", redacted[name])
	}
	require.Equal(t, 12, redacted["input_tokens"])
	for _, name := range []string{"x-sentinel-dc", "OpenAI-Sentinel-Chat-Requirements-Token", "OpenAI-Sentinel-Proof-Token", "Cookie"} {
		require.True(t, isSensitiveOpsUpstreamHeader(name))
	}
}
