//go:build unit

package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/clientproof"
	"github.com/stretchr/testify/require"
)

type clientDeviceRepoFake struct {
	mu        sync.Mutex
	devices   map[string]*ClientDevice
	lookupErr error
	lookups   int
	touches   []ClientDeviceTouch
	enrollOut ClientDeviceEnrollOutcome
	enrollIn  []ClientDeviceEnrollInput
	onLookup  func()
}

func (f *clientDeviceRepoFake) Enroll(_ context.Context, in ClientDeviceEnrollInput, _ int) (*ClientDevice, ClientDeviceEnrollOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enrollIn = append(f.enrollIn, in)
	if f.enrollOut == ClientDeviceEnrollRevoked || f.enrollOut == ClientDeviceEnrollLimit {
		return nil, f.enrollOut, nil
	}
	return &ClientDevice{ID: 1, DeviceID: in.DeviceID, UserID: in.UserID, PublicKey: in.PublicKey}, ClientDeviceEnrollCreated, nil
}

func (f *clientDeviceRepoFake) GetByDeviceID(_ context.Context, id string) (*ClientDevice, error) {
	if f.onLookup != nil {
		hook := f.onLookup
		f.onLookup = nil
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	device := f.devices[id]
	if device == nil {
		return nil, nil
	}
	copied := *device
	return &copied, nil
}

func (f *clientDeviceRepoFake) ListByUser(context.Context, int64) ([]ClientDevice, error) {
	return nil, nil
}

func (f *clientDeviceRepoFake) Revoke(_ context.Context, userID, id, _ int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, device := range f.devices {
		if device.ID == id && device.UserID == userID {
			now := time.Now()
			device.RevokedAt = &now
			return device.DeviceID, nil
		}
	}
	return "", ErrClientDeviceNotFound
}

func (f *clientDeviceRepoFake) Touch(_ context.Context, _ int64, touch ClientDeviceTouch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touches = append(f.touches, touch)
	return nil
}

func (f *clientDeviceRepoFake) CountActiveCodexDevices(_ context.Context, ids []int64, _ time.Time) (map[int64]int, error) {
	return map[int64]int{ids[0]: 2}, nil
}

func (f *clientDeviceRepoFake) CountClientAttestations(_ context.Context, ids []int64, _ time.Time) (map[int64]map[int16]int64, error) {
	return map[int64]map[int16]int64{ids[0]: {1: 10, 2: 3, 99: 4}}, nil
}

type nonceStoreFake struct {
	mu   sync.Mutex
	seen map[string]bool
	err  error
}

func (n *nonceStoreFake) Claim(_ context.Context, deviceID, nonce string, ttl time.Duration) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.err != nil {
		return false, n.err
	}
	if ttl != 900*time.Second {
		return false, errors.New("unexpected ttl")
	}
	if n.seen == nil {
		n.seen = make(map[string]bool)
	}
	key := deviceID + ":" + nonce
	if n.seen[key] {
		return false, nil
	}
	n.seen[key] = true
	return true, nil
}

type proofFixture struct {
	svc    *ClientDeviceService
	repo   *clientDeviceRepoFake
	nonces *nonceStoreFake
	priv   *ecdsa.PrivateKey
	device *ClientDevice
	now    time.Time
}

const proofTestAPIKey = "sk-proof-test"

func newProofFixture(t *testing.T) *proofFixture {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	raw, err := priv.PublicKey.Bytes()
	require.NoError(t, err)
	deviceID, err := clientproof.NewDeviceID()
	require.NoError(t, err)
	device := &ClientDevice{ID: 42, DeviceID: deviceID, UserID: 7, PublicKey: raw}
	repo := &clientDeviceRepoFake{devices: map[string]*ClientDevice{deviceID: device}}
	nonces := &nonceStoreFake{}
	svc := NewClientDeviceService(repo, nonces)
	now := time.Unix(1_800_000_000, 0)
	svc.now = func() time.Time { return now }
	svc.touchAsync = func(fn func()) { fn() }
	return &proofFixture{svc: svc, repo: repo, nonces: nonces, priv: priv, device: device, now: now}
}

func (f *proofFixture) header(t *testing.T, target string, ts int64) string {
	t.Helper()
	nonce, err := clientproof.NewNonce()
	require.NoError(t, err)
	header, err := clientproof.Sign(f.priv, f.device.DeviceID, "POST", target, ts, nonce, proofTestAPIKey)
	require.NoError(t, err)
	return header
}

func (f *proofFixture) request(header string) ClientProofRequest {
	return ClientProofRequest{Header: header, Method: "POST", Target: "/v1/responses?x=1", APIKey: proofTestAPIKey, UserID: 7, Platform: PlatformOpenAI, ClientIP: "203.0.113.5"}
}

func TestClientProofVerifyStatuses(t *testing.T) {
	ctx := context.Background()

	t.Run("valid then replay", func(t *testing.T) {
		f := newProofFixture(t)
		header := f.header(t, "/v1/responses?x=1", f.now.Unix())
		require.Equal(t, clientproof.StatusValid, f.svc.VerifyProof(ctx, f.request(header)))
		require.Equal(t, clientproof.StatusReplay, f.svc.VerifyProof(ctx, f.request(header)))
	})
	t.Run("missing", func(t *testing.T) {
		f := newProofFixture(t)
		require.Equal(t, clientproof.StatusMissing, f.svc.VerifyProof(ctx, f.request("")))
	})
	t.Run("invalid format", func(t *testing.T) {
		f := newProofFixture(t)
		require.Equal(t, clientproof.StatusInvalid, f.svc.VerifyProof(ctx, f.request("v1.garbage")))
		require.Zero(t, f.repo.lookups, "format errors never reach the database")
	})
	t.Run("invalid signature for another target", func(t *testing.T) {
		f := newProofFixture(t)
		header := f.header(t, "/v1/responses?x=2", f.now.Unix())
		require.Equal(t, clientproof.StatusInvalid, f.svc.VerifyProof(ctx, f.request(header)))
	})
	t.Run("invalid signature for another api key", func(t *testing.T) {
		f := newProofFixture(t)
		header := f.header(t, "/v1/responses?x=1", f.now.Unix())
		req := f.request(header)
		req.APIKey = "sk-other"
		require.Equal(t, clientproof.StatusInvalid, f.svc.VerifyProof(ctx, req))
	})
	t.Run("stale", func(t *testing.T) {
		f := newProofFixture(t)
		header := f.header(t, "/v1/responses?x=1", f.now.Unix()-301)
		require.Equal(t, clientproof.StatusStale, f.svc.VerifyProof(ctx, f.request(header)))
		require.Empty(t, f.nonces.seen, "stale proofs do not consume nonces")
	})
	t.Run("unknown device", func(t *testing.T) {
		f := newProofFixture(t)
		delete(f.repo.devices, f.device.DeviceID)
		header := f.header(t, "/v1/responses?x=1", f.now.Unix())
		require.Equal(t, clientproof.StatusUnknownDevice, f.svc.VerifyProof(ctx, f.request(header)))
	})
	t.Run("device of another user", func(t *testing.T) {
		f := newProofFixture(t)
		header := f.header(t, "/v1/responses?x=1", f.now.Unix())
		req := f.request(header)
		req.UserID = 8
		require.Equal(t, clientproof.StatusUnknownDevice, f.svc.VerifyProof(ctx, req))
	})
	t.Run("revoked", func(t *testing.T) {
		f := newProofFixture(t)
		revoked := f.now.Add(-time.Hour)
		f.device.RevokedAt = &revoked
		header := f.header(t, "/v1/responses?x=1", f.now.Unix())
		require.Equal(t, clientproof.StatusRevoked, f.svc.VerifyProof(ctx, f.request(header)))
	})
	t.Run("unverified on db error", func(t *testing.T) {
		f := newProofFixture(t)
		f.repo.lookupErr = errors.New("db down")
		header := f.header(t, "/v1/responses?x=1", f.now.Unix())
		require.Equal(t, clientproof.StatusUnverified, f.svc.VerifyProof(ctx, f.request(header)))
	})
	t.Run("unverified on redis error", func(t *testing.T) {
		f := newProofFixture(t)
		f.nonces.err = errors.New("redis down")
		header := f.header(t, "/v1/responses?x=1", f.now.Unix())
		require.Equal(t, clientproof.StatusUnverified, f.svc.VerifyProof(ctx, f.request(header)))
	})
	t.Run("unverified without nonce store", func(t *testing.T) {
		f := newProofFixture(t)
		f.svc.nonces = nil
		header := f.header(t, "/v1/responses?x=1", f.now.Unix())
		require.Equal(t, clientproof.StatusUnverified, f.svc.VerifyProof(ctx, f.request(header)))
	})
	t.Run("nil service", func(t *testing.T) {
		var svc *ClientDeviceService
		f := newProofFixture(t)
		header := f.header(t, "/v1/responses?x=1", f.now.Unix())
		require.Equal(t, clientproof.StatusUnverified, svc.VerifyProof(ctx, f.request(header)))
	})
}

func TestClientProofDeviceCacheAndRevokeInvalidation(t *testing.T) {
	ctx := context.Background()
	f := newProofFixture(t)
	require.Equal(t, clientproof.StatusValid, f.svc.VerifyProof(ctx, f.request(f.header(t, "/v1/responses?x=1", f.now.Unix()))))
	require.Equal(t, clientproof.StatusValid, f.svc.VerifyProof(ctx, f.request(f.header(t, "/v1/responses?x=1", f.now.Unix()))))
	require.Equal(t, 1, f.repo.lookups, "device lookups are cached")

	require.NoError(t, f.svc.RevokeUserDevice(ctx, 7, f.device.ID, 1))
	require.Equal(t, clientproof.StatusRevoked, f.svc.VerifyProof(ctx, f.request(f.header(t, "/v1/responses?x=1", f.now.Unix()))),
		"revocation drops the local cache entry immediately")
	require.Equal(t, 2, f.repo.lookups)
}

func TestClientProofNegativeLookupIsCachedBriefly(t *testing.T) {
	ctx := context.Background()
	f := newProofFixture(t)
	delete(f.repo.devices, f.device.DeviceID)
	header := f.header(t, "/v1/responses?x=1", f.now.Unix())
	require.Equal(t, clientproof.StatusUnknownDevice, f.svc.VerifyProof(ctx, f.request(header)))
	require.Equal(t, clientproof.StatusUnknownDevice, f.svc.VerifyProof(ctx, f.request(header)))
	require.Equal(t, 1, f.repo.lookups)
}

func TestClientProofTouchIsThrottledPerField(t *testing.T) {
	ctx := context.Background()
	f := newProofFixture(t)
	for i := 0; i < 3; i++ {
		require.Equal(t, clientproof.StatusValid, f.svc.VerifyProof(ctx, f.request(f.header(t, "/v1/responses?x=1", f.now.Unix()))))
	}
	require.Equal(t, []ClientDeviceTouch{{IP: "203.0.113.5", Seen: true, Codex: true}}, f.repo.touches)

	claude := f.request(f.header(t, "/v1/messages", f.now.Unix()))
	claude.Target, claude.Platform = "/v1/messages", PlatformAnthropic
	require.Equal(t, clientproof.StatusValid, f.svc.VerifyProof(ctx, claude))
	require.Equal(t, ClientDeviceTouch{IP: "203.0.113.5", Claude: true}, f.repo.touches[1], "only the not-yet-touched field is updated")

	bad := f.request(f.header(t, "/v1/responses?x=1", f.now.Unix()-1000))
	f.svc.touches.Flush()
	require.Equal(t, clientproof.StatusStale, f.svc.VerifyProof(ctx, bad))
	require.Len(t, f.repo.touches, 2, "non-valid proofs never touch last_*")
}

func TestClientDeviceEnroll(t *testing.T) {
	ctx := context.Background()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	raw, err := priv.PublicKey.Bytes()
	require.NoError(t, err)
	encoded := base64.RawURLEncoding.EncodeToString(raw)

	repo := &clientDeviceRepoFake{}
	svc := NewClientDeviceService(repo, nil)
	device, err := svc.Enroll(ctx, 7, 3, encoded, "1.1.54", "windows", "x86_64")
	require.NoError(t, err)
	require.True(t, clientproof.ValidDeviceID(device.DeviceID))
	require.Equal(t, clientproof.PublicKeySHA256(raw), repo.enrollIn[0].PublicKeySHA256)
	require.Equal(t, raw, repo.enrollIn[0].PublicKey)

	_, err = svc.Enroll(ctx, 7, 3, "not-a-key", "", "", "")
	require.ErrorIs(t, err, ErrClientDeviceInvalid)
	compressed := append([]byte{0x02 | (raw[64] & 1)}, raw[1:33]...)
	_, err = svc.Enroll(ctx, 7, 3, base64.RawURLEncoding.EncodeToString(compressed), "", "", "")
	require.ErrorIs(t, err, ErrClientDeviceInvalid)

	repo.enrollOut = ClientDeviceEnrollRevoked
	_, err = svc.Enroll(ctx, 7, 3, encoded, "", "", "")
	require.ErrorIs(t, err, ErrClientDeviceRevoked)

	repo.enrollOut = ClientDeviceEnrollLimit
	_, err = svc.Enroll(ctx, 7, 3, encoded, "", "", "")
	require.ErrorIs(t, err, ErrClientDeviceLimit)
}

func TestClientDeviceUserSummaries(t *testing.T) {
	svc := NewClientDeviceService(&clientDeviceRepoFake{}, nil)
	out := svc.ListUserSummaries(context.Background(), []int64{5, 6})
	require.Equal(t, 2, *out[5].CodexDeviceCount)
	require.Equal(t, map[string]int64{"valid": 10, "missing": 3}, out[5].ClientAttestation7d, "unknown status codes are dropped")
	require.Equal(t, 0, *out[6].CodexDeviceCount)
	require.Equal(t, map[string]int64{}, out[6].ClientAttestation7d)
}

func TestClientAttestationForUsageFollowsFinalGroupPolicy(t *testing.T) {
	status := int16(clientproof.StatusMissing)
	require.Nil(t, ClientAttestationForUsage(&status, &APIKey{Group: &Group{ClientAttestationPolicy: ClientAttestationPolicyOff}}))
	require.Nil(t, ClientAttestationForUsage(&status, &APIKey{Group: &Group{}}))
	require.Nil(t, ClientAttestationForUsage(&status, &APIKey{}))
	require.Nil(t, ClientAttestationForUsage(nil, &APIKey{Group: &Group{ClientAttestationPolicy: ClientAttestationPolicyObserve}}))
	got := ClientAttestationForUsage(&status, &APIKey{Group: &Group{ClientAttestationPolicy: ClientAttestationPolicyObserve}})
	require.NotNil(t, got)
	require.Equal(t, status, *got)
	require.NotNil(t, ClientAttestationForUsage(&status, &APIKey{Group: &Group{ClientAttestationPolicy: ClientAttestationPolicyEnforce}}))

	require.Equal(t, ClientAttestationPolicyOff, NormalizeClientAttestationPolicy(" bogus "))
	require.Equal(t, ClientAttestationPolicyObserve, NormalizeClientAttestationPolicy("OBSERVE"))
}

func TestGatewayServiceRecordUsage_ClientAttestationPersistedOnlyForObserveGroups(t *testing.T) {
	record := func(policy string) *UsageLog {
		usageRepo := &openAIRecordUsageLogRepoStub{}
		billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
		svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
		status := int16(clientproof.StatusValid)
		groupID := int64(11)
		err := svc.RecordUsage(context.Background(), &RecordUsageInput{
			Result: &ForwardResult{RequestID: "attestation-" + policy, Model: "claude-sonnet-4", Duration: time.Second,
				Usage: ClaudeUsage{InputTokens: 3, OutputTokens: 2}},
			APIKey:            &APIKey{ID: 501, Quota: 100, GroupID: &groupID, Group: &Group{ID: groupID, ClientAttestationPolicy: policy, RateMultiplier: 1}},
			User:              &User{ID: 601},
			Account:           &Account{ID: 701},
			APIKeyService:     &openAIRecordUsageAPIKeyQuotaStub{},
			ClientAttestation: &status,
		})
		require.NoError(t, err)
		require.NotNil(t, usageRepo.lastLog)
		return usageRepo.lastLog
	}
	require.Nil(t, record(ClientAttestationPolicyOff).ClientAttestation)
	observed := record(ClientAttestationPolicyObserve).ClientAttestation
	require.NotNil(t, observed)
	require.Equal(t, int16(clientproof.StatusValid), *observed)
}

func TestClientProofWithoutBearerIsInvalid(t *testing.T) {
	f := newProofFixture(t)
	nonce, err := clientproof.NewNonce()
	require.NoError(t, err)
	// A real device signing over sha256("") must not pass when no Bearer was sent.
	header, err := clientproof.Sign(f.priv, f.device.DeviceID, "POST", "/v1/responses?x=1", f.now.Unix(), nonce, "")
	require.NoError(t, err)
	req := f.request(header)
	req.APIKey = ""
	require.Equal(t, clientproof.StatusInvalid, f.svc.VerifyProof(context.Background(), req))
	require.Empty(t, f.nonces.seen)
}

func TestClientProofRevokeDuringLookupDoesNotRepopulateCache(t *testing.T) {
	ctx := context.Background()
	f := newProofFixture(t)
	// The lookup reads the active row, then a revoke lands before the cache
	// write (simulated by revoking from inside the repository call).
	f.repo.onLookup = func() {
		copied := *f.device
		f.repo.mu.Lock()
		f.repo.devices[f.device.DeviceID] = &copied
		f.repo.mu.Unlock()
		f.svc.revocations.Add(1)
	}
	require.Equal(t, clientproof.StatusValid, f.svc.VerifyProof(ctx, f.request(f.header(t, "/v1/responses?x=1", f.now.Unix()))))
	_, cached := f.svc.devices.Get(f.device.DeviceID)
	require.False(t, cached, "a lookup that raced a revoke is not cached")

	require.NoError(t, f.svc.RevokeUserDevice(ctx, 7, f.device.ID, 1))
	require.Equal(t, clientproof.StatusRevoked, f.svc.VerifyProof(ctx, f.request(f.header(t, "/v1/responses?x=1", f.now.Unix()))))
}
