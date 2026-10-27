//go:build unit

package middleware

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/clientproof"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type recordingProofVerifier struct {
	calls  []service.ClientProofRequest
	status clientproof.Status
}

func (r *recordingProofVerifier) VerifyProof(_ context.Context, req service.ClientProofRequest) clientproof.Status {
	r.calls = append(r.calls, req)
	return r.status
}

type clientProofObservation struct {
	status      *int16
	proofHeader string
	reached     bool
}

func runClientProof(t *testing.T, verifier ClientProofVerifier, key *service.APIKey, req *http.Request) clientProofObservation {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var seen clientProofObservation
	handler := func(c *gin.Context) {
		seen.reached = true
		seen.status = GetClientAttestation(c)
		seen.proofHeader = c.Request.Header.Get(clientproof.HeaderName)
		c.Status(http.StatusNoContent)
	}
	auth := func(c *gin.Context) {
		if key != nil {
			c.Set(string(ContextKeyAPIKey), key)
		}
		c.Next()
	}
	router.Any("/*path", auth, ClientProof(verifier), handler)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code, "client proof never rejects")
	return seen
}

func observeKey(policy, platform string) *service.APIKey {
	return &service.APIKey{ID: 3, UserID: 7, Key: "sk-stored", Group: &service.Group{ID: 9, Platform: platform, ClientAttestationPolicy: policy}}
}

func TestClientProofMiddlewareObserveRecordsStatusAndStripsHeader(t *testing.T) {
	verifier := &recordingProofVerifier{status: clientproof.StatusReplay}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true&q=%2Fa%20b", nil)
	req.Header.Set("Authorization", "Bearer  sk-presented ")
	req.Header.Set(clientproof.HeaderName, "v1.proof")
	req.RemoteAddr = "198.51.100.7:1234"

	seen := runClientProof(t, verifier, observeKey(service.ClientAttestationPolicyObserve, service.PlatformAnthropic), req)

	require.True(t, seen.reached)
	require.Empty(t, seen.proofHeader, "header is removed before any handler/upstream code")
	require.NotNil(t, seen.status)
	require.Equal(t, int16(clientproof.StatusReplay), *seen.status)
	require.Len(t, verifier.calls, 1)
	call := verifier.calls[0]
	require.Equal(t, "v1.proof", call.Header)
	require.Equal(t, http.MethodPost, call.Method)
	require.Equal(t, "/v1/messages?beta=true&q=%2Fa%20b", call.Target, "raw request-line target, escapes preserved")
	require.Equal(t, "sk-presented", call.APIKey, "the presented bearer key, trimmed like API-key auth")
	require.Equal(t, int64(7), call.UserID)
	require.Equal(t, service.ClientSurfaceClaude, call.Surface, "surface by route")
	require.Equal(t, "198.51.100.7", call.ClientIP)
}

func TestClientProofMiddlewareMissingHeaderDoesNoIO(t *testing.T) {
	verifier := &recordingProofVerifier{status: clientproof.StatusValid}
	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	req.Header.Set("x-api-key", "sk-x")
	seen := runClientProof(t, verifier, observeKey(service.ClientAttestationPolicyOff, service.PlatformOpenAI), req)
	require.Equal(t, int16(clientproof.StatusMissing), *seen.status)
	require.Empty(t, verifier.calls, "no header → no verifier call")
}

func TestClientProofMiddlewareDuplicateHeadersAreInvalid(t *testing.T) {
	verifier := &recordingProofVerifier{status: clientproof.StatusValid}
	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	req.Header.Add(clientproof.HeaderName, "v1.a")
	req.Header.Add(clientproof.HeaderName, "v1.b")
	seen := runClientProof(t, verifier, observeKey(service.ClientAttestationPolicyObserve, service.PlatformOpenAI), req)
	require.Equal(t, int16(clientproof.StatusInvalid), *seen.status)
	require.Empty(t, verifier.calls)
	require.Empty(t, seen.proofHeader)
}

// A present header is verified whatever the initial group's policy (smart
// switch or fallbacks may move the request to an observe group); usage rows
// keep it only when the final group collects attestation.
func TestClientProofMiddlewareVerifiesRegardlessOfInitialPolicy(t *testing.T) {
	for _, key := range []*service.APIKey{
		observeKey(service.ClientAttestationPolicyOff, service.PlatformOpenAI),
		observeKey("", service.PlatformOpenAI),
		{ID: 3, UserID: 7}, // no group: must not panic
	} {
		verifier := &recordingProofVerifier{status: clientproof.StatusValid}
		req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
		req.Header.Set(clientproof.HeaderName, "v1.proof")
		seen := runClientProof(t, verifier, key, req)
		require.Equal(t, int16(clientproof.StatusValid), *seen.status)
		require.Len(t, verifier.calls, 1)
		require.Equal(t, service.ClientSurfaceCodex, verifier.calls[0].Surface)
		require.Empty(t, seen.proofHeader, "the header is stripped regardless of policy")
		require.Nil(t, service.ClientAttestationForUsage(seen.status, key), "final group off → NULL")
		observe := *key
		observe.Group = &service.Group{ClientAttestationPolicy: service.ClientAttestationPolicyObserve}
		require.NotNil(t, service.ClientAttestationForUsage(seen.status, &observe), "final group observe → stored")
	}

	// Without an authenticated key nothing is recorded, but the header is still stripped.
	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	req.Header.Set(clientproof.HeaderName, "v1.proof")
	seen := runClientProof(t, &recordingProofVerifier{}, nil, req)
	require.Nil(t, seen.status)
	require.Empty(t, seen.proofHeader)
}

func TestClientProofMiddlewareNilVerifier(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	req.Header.Set(clientproof.HeaderName, "v1.proof")
	seen := runClientProof(t, nil, observeKey(service.ClientAttestationPolicyObserve, service.PlatformOpenAI), req)
	require.Equal(t, int16(clientproof.StatusUnverified), *seen.status)
	require.Empty(t, seen.proofHeader)

	seen = runClientProof(t, nil, observeKey(service.ClientAttestationPolicyObserve, service.PlatformOpenAI), httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
	require.Equal(t, int16(clientproof.StatusMissing), *seen.status)
}

func TestGetClientAttestationIgnoresUnknownValues(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Nil(t, GetClientAttestation(c))
	SetClientAttestation(c, clientproof.Status(42))
	require.Nil(t, GetClientAttestation(c))
	SetClientAttestation(c, clientproof.StatusUnverified)
	require.Equal(t, int16(8), *GetClientAttestation(c))
	require.Nil(t, GetClientAttestation(nil))
}

type proofDeviceRepo struct {
	service.ClientDeviceRepository
	device *service.ClientDevice
}

func (r *proofDeviceRepo) GetByDeviceID(_ context.Context, id string) (*service.ClientDevice, error) {
	if r.device != nil && r.device.DeviceID == id {
		copied := *r.device
		return &copied, nil
	}
	return nil, nil
}

func (r *proofDeviceRepo) Touch(context.Context, int64, service.ClientDeviceTouch) error { return nil }

type proofNonces struct{ seen map[string]bool }

func (n *proofNonces) Claim(_ context.Context, deviceID, nonce string, _ time.Duration) (bool, error) {
	if n.seen[deviceID+nonce] {
		return false, nil
	}
	n.seen[deviceID+nonce] = true
	return true, nil
}

// End to end with the real verifier: the signed key is the raw Bearer value
// (Claude Code's own Authorization header forwarded by the local proxy).
func TestClientProofMiddlewareSignsRawBearerValue(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	raw, err := priv.PublicKey.Bytes()
	require.NoError(t, err)
	deviceID, err := clientproof.NewDeviceID()
	require.NoError(t, err)
	svc := service.NewClientDeviceService(&proofDeviceRepo{device: &service.ClientDevice{ID: 1, DeviceID: deviceID, UserID: 7, PublicKey: raw}}, &proofNonces{seen: map[string]bool{}})
	key := observeKey(service.ClientAttestationPolicyObserve, service.PlatformAnthropic)
	sign := func(target, apiKey string) string {
		nonce, err := clientproof.NewNonce()
		require.NoError(t, err)
		header, err := clientproof.Sign(priv, deviceID, http.MethodPost, target, time.Now().Unix(), nonce, apiKey)
		require.NoError(t, err)
		return header
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil)
	req.Header.Set("Authorization", "bearer sk-claude-code")
	req.Header.Set(clientproof.HeaderName, sign("/v1/messages?beta=true", "sk-claude-code"))
	require.Equal(t, int16(clientproof.StatusValid), *runClientProof(t, svc, key, req).status)

	// x-api-key only: the client sends no proof → missing.
	req = httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil)
	req.Header.Set("x-api-key", "sk-claude-code")
	require.Equal(t, int16(clientproof.StatusMissing), *runClientProof(t, svc, key, req).status)

	// A proof over the x-api-key value is not accepted without a Bearer token.
	req = httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil)
	req.Header.Set("x-api-key", "sk-claude-code")
	req.Header.Set(clientproof.HeaderName, sign("/v1/messages?beta=true", "sk-claude-code"))
	require.Equal(t, int16(clientproof.StatusInvalid), *runClientProof(t, svc, key, req).status)

	// The authenticated key resolved by API-key auth is not the hash input.
	req = httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil)
	req.Header.Set("Authorization", "Bearer sk-claude-code")
	req.Header.Set(clientproof.HeaderName, sign("/v1/messages?beta=true", key.Key))
	require.Equal(t, int16(clientproof.StatusInvalid), *runClientProof(t, svc, key, req).status)
}
