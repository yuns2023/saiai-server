//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/clientproof"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func clientDeviceInput(t *testing.T, userID int64, keyByte byte) service.ClientDeviceEnrollInput {
	t.Helper()
	deviceID, err := clientproof.NewDeviceID()
	require.NoError(t, err)
	raw := make([]byte, 65)
	raw[0], raw[1] = 0x04, keyByte
	return service.ClientDeviceEnrollInput{UserID: userID, DeviceID: deviceID, PublicKey: raw,
		PublicKeySHA256: clientproof.PublicKeySHA256(raw), ClientVersion: "1.1.54", OS: "linux", Arch: "x86_64"}
}

func TestClientDeviceRepositoryEnrollRevokeTouchAndSummaries(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := time.Now().UnixNano()
	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("client-device-%d@example.com", suffix)})
	other := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("client-device-other-%d@example.com", suffix)})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM client_devices WHERE user_id IN ($1,$2)`, user.ID, other.ID)
		_ = client.User.DeleteOneID(user.ID).Exec(ctx)
		_ = client.User.DeleteOneID(other.ID).Exec(ctx)
	})
	repo := NewClientDeviceRepository(integrationDB)

	in := clientDeviceInput(t, user.ID, 1)
	created, outcome, err := repo.Enroll(ctx, in, 2)
	require.NoError(t, err)
	require.Equal(t, service.ClientDeviceEnrollCreated, outcome)
	require.Equal(t, in.DeviceID, created.DeviceID)

	again := in
	again.DeviceID, again.ClientVersion = "cd_BBBBBBBBBBBBBBBBBBBBBB", "1.1.55"
	existing, outcome, err := repo.Enroll(ctx, again, 2)
	require.NoError(t, err)
	require.Equal(t, service.ClientDeviceEnrollExisting, outcome)
	require.Equal(t, created.DeviceID, existing.DeviceID, "idempotent on (user, key hash)")
	require.Equal(t, "1.1.55", existing.ClientVersion)

	// Same key, other user → a separate device.
	otherIn := in
	otherIn.UserID, otherIn.DeviceID = other.ID, "cd_CCCCCCCCCCCCCCCCCCCCCC"
	_, outcome, err = repo.Enroll(ctx, otherIn, 2)
	require.NoError(t, err)
	require.Equal(t, service.ClientDeviceEnrollCreated, outcome)

	// Limit is per user and counts non-revoked devices; concurrent enrollments
	// are serialized so the limit holds.
	var wg sync.WaitGroup
	outcomes := make([]service.ClientDeviceEnrollOutcome, 4)
	for i := range outcomes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, outcomes[i], _ = repo.Enroll(ctx, clientDeviceInput(t, user.ID, byte(10+i)), 2)
		}(i)
	}
	wg.Wait()
	createdCount := 0
	for _, o := range outcomes {
		if o == service.ClientDeviceEnrollCreated {
			createdCount++
		} else {
			require.Equal(t, service.ClientDeviceEnrollLimit, o)
		}
	}
	require.Equal(t, 1, createdCount)

	revokedDeviceID, err := repo.Revoke(ctx, user.ID, created.ID, 99)
	require.NoError(t, err)
	require.Equal(t, created.DeviceID, revokedDeviceID)
	_, err = repo.Revoke(ctx, user.ID, created.ID, 99)
	require.ErrorIs(t, err, service.ErrClientDeviceNotFound, "already revoked")
	_, err = repo.Revoke(ctx, other.ID, created.ID, 99)
	require.ErrorIs(t, err, service.ErrClientDeviceNotFound, "owned by another user")

	_, outcome, err = repo.Enroll(ctx, in, 2)
	require.NoError(t, err)
	require.Equal(t, service.ClientDeviceEnrollRevoked, outcome, "revoked keys stay revoked")
	_, outcome, err = repo.Enroll(ctx, clientDeviceInput(t, user.ID, 50), 2)
	require.NoError(t, err)
	require.Equal(t, service.ClientDeviceEnrollCreated, outcome, "revoked devices free a slot")

	got, err := repo.GetByDeviceID(ctx, created.DeviceID)
	require.NoError(t, err)
	require.NotNil(t, got.RevokedAt)
	require.Equal(t, int64(99), *got.RevokedBy)
	missing, err := repo.GetByDeviceID(ctx, "cd_DDDDDDDDDDDDDDDDDDDDDD")
	require.NoError(t, err)
	require.Nil(t, missing)

	devices, err := repo.ListByUser(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, devices, 3)
	require.Nil(t, devices[0].RevokedAt, "active devices first")
	active := devices[0]

	require.NoError(t, repo.Touch(ctx, active.ID, service.ClientDeviceTouch{IP: "203.0.113.9", Seen: true, Codex: true}))
	require.NoError(t, repo.Touch(ctx, active.ID, service.ClientDeviceTouch{Claude: true}))
	touched, err := repo.GetByDeviceID(ctx, active.DeviceID)
	require.NoError(t, err)
	require.NotNil(t, touched.LastSeenAt)
	require.NotNil(t, touched.LastCodexAt)
	require.NotNil(t, touched.LastClaudeAt)
	require.Equal(t, "203.0.113.9", touched.LastIP)

	counts, err := repo.CountActiveCodexDevices(ctx, []int64{user.ID, other.ID}, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, map[int64]int{user.ID: 1}, counts)

	account := mustCreateAccount(t, client, &service.Account{Name: fmt.Sprintf("client-attestation-%d", suffix)})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: fmt.Sprintf("sk-client-attestation-%d", suffix), Name: "k"})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM usage_logs WHERE api_key_id=$1`, apiKey.ID)
	})
	for i, status := range []any{int16(1), int16(1), int16(2), nil} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs (user_id, api_key_id, account_id, request_id, model, client_attestation)
			VALUES ($1, $2, $3, $4, 'm', $5)`, user.ID, apiKey.ID, account.ID, fmt.Sprintf("client-attestation-%d-%d", suffix, i), status)
		require.NoError(t, err)
	}
	attestations, err := repo.CountClientAttestations(ctx, []int64{user.ID}, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, map[int16]int64{1: 2, 2: 1}, attestations[user.ID], "NULL rows are not counted")
}

func TestClientProofNonceStoreSetNX(t *testing.T) {
	ctx := context.Background()
	store := NewClientProofNonceStore(integrationRedis)
	nonce := fmt.Sprintf("n%d", time.Now().UnixNano())
	fresh, err := store.Claim(ctx, "cd_test", nonce, time.Minute)
	require.NoError(t, err)
	require.True(t, fresh)
	fresh, err = store.Claim(ctx, "cd_test", nonce, time.Minute)
	require.NoError(t, err)
	require.False(t, fresh)
	ttl, err := integrationRedis.TTL(ctx, clientProofNonceKeyPrefix+"cd_test:"+nonce).Result()
	require.NoError(t, err)
	require.Greater(t, ttl, 50*time.Second)
}
