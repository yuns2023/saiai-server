//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type maxConcurrencyAPIKeyRepoStub struct {
	quotaBaseAPIKeyRepoStub
	created *APIKey
	stored  *APIKey
	updated *APIKey
}

func (s *maxConcurrencyAPIKeyRepoStub) Create(_ context.Context, key *APIKey) error {
	clone := *key
	s.created = &clone
	return nil
}

func (s *maxConcurrencyAPIKeyRepoStub) GetByID(context.Context, int64) (*APIKey, error) {
	clone := *s.stored
	return &clone, nil
}

func (s *maxConcurrencyAPIKeyRepoStub) Update(_ context.Context, key *APIKey) error {
	clone := *key
	s.updated = &clone
	return nil
}

func TestValidAPIKeyMaxConcurrency(t *testing.T) {
	for _, v := range []int{1, 5, MaxAPIKeyMaxConcurrency} {
		require.True(t, validAPIKeyMaxConcurrency(intPtr(v)), v)
	}
	for _, v := range []int{0, -1, MaxAPIKeyMaxConcurrency + 1} {
		require.False(t, validAPIKeyMaxConcurrency(intPtr(v)), v)
	}
	require.True(t, validAPIKeyMaxConcurrency(nil), "omitted = default / unchanged")
}

func TestAPIKeyService_CreateMaxConcurrency(t *testing.T) {
	// The user's own concurrency (3) does not cap a key's setting.
	users := &userRepoStub{user: &User{ID: 9, Concurrency: 3, Status: StatusActive}}

	repo := &maxConcurrencyAPIKeyRepoStub{}
	svc := &APIKeyService{apiKeyRepo: repo, userRepo: users, cfg: &config.Config{}}
	_, err := svc.Create(context.Background(), 9, CreateAPIKeyRequest{Name: "default"})
	require.NoError(t, err)
	require.Equal(t, DefaultAPIKeyMaxConcurrency, repo.created.MaxConcurrency)

	repo = &maxConcurrencyAPIKeyRepoStub{}
	svc = &APIKeyService{apiKeyRepo: repo, userRepo: users, cfg: &config.Config{}}
	_, err = svc.Create(context.Background(), 9, CreateAPIKeyRequest{Name: "wide", MaxConcurrency: intPtr(20)})
	require.NoError(t, err)
	require.Equal(t, 20, repo.created.MaxConcurrency)

	for _, v := range []int{0, -3, 1001} {
		repo = &maxConcurrencyAPIKeyRepoStub{}
		svc = &APIKeyService{apiKeyRepo: repo, userRepo: users, cfg: &config.Config{}}
		_, err = svc.Create(context.Background(), 9, CreateAPIKeyRequest{Name: "bad", MaxConcurrency: intPtr(v)})
		require.ErrorIs(t, err, ErrAPIKeyMaxConcurrency, v)
		require.Nil(t, repo.created)
	}
}

func TestAPIKeyService_UpdateMaxConcurrency(t *testing.T) {
	stored := &APIKey{ID: 4, UserID: 9, Key: "sk-test-update-conc", Name: "k", Status: StatusActive, MaxConcurrency: 5}
	repo := &maxConcurrencyAPIKeyRepoStub{stored: stored}
	svc := &APIKeyService{apiKeyRepo: repo, cfg: &config.Config{}}

	_, err := svc.Update(context.Background(), 4, 9, UpdateAPIKeyRequest{MaxConcurrency: intPtr(12)})
	require.NoError(t, err)
	require.Equal(t, 12, repo.updated.MaxConcurrency)

	repo.updated = nil
	_, err = svc.Update(context.Background(), 4, 9, UpdateAPIKeyRequest{})
	require.NoError(t, err)
	require.Equal(t, 5, repo.updated.MaxConcurrency, "omitted keeps the stored value")

	repo.updated = nil
	_, err = svc.Update(context.Background(), 4, 9, UpdateAPIKeyRequest{MaxConcurrency: intPtr(0)})
	require.ErrorIs(t, err, ErrAPIKeyMaxConcurrency)
	require.Nil(t, repo.updated)
}

func TestAPIKeyService_SnapshotRoundTripsMaxConcurrency(t *testing.T) {
	svc := &APIKeyService{}
	key := &APIKey{ID: 1, UserID: 2, User: &User{ID: 2, Concurrency: 3}, Status: StatusActive, MaxConcurrency: 7}
	snapshot := svc.snapshotFromAPIKey(key)
	encoded, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"max_concurrency":7`)

	var decoded APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	restored, ok, err := svc.applyAuthCacheEntry("k", &decoded)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 7, restored.MaxConcurrency)

	// v5 snapshots predate the field and would decode it as 0.
	decoded.Snapshot.SchemaVersion = 5
	_, ok, err = svc.applyAuthCacheEntry("k", &decoded)
	require.NoError(t, err)
	require.False(t, ok)
}
