//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// rateLimitResetCacheFake keeps API Key rate-limit entries in memory. Other
// BillingCache methods are not used by these tests and panic via the nil
// embedded interface.
type rateLimitResetCacheFake struct {
	BillingCache
	mu          sync.Mutex
	entries     map[int64]*APIKeyRateLimitCacheData
	invalidated []int64
	// invalidateCtxErr is the context error seen by the first invalidation.
	invalidateCtxErr error
}

func (f *rateLimitResetCacheFake) GetAPIKeyRateLimit(_ context.Context, keyID int64) (*APIKeyRateLimitCacheData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.entries[keyID]; ok {
		clone := *e
		return &clone, nil
	}
	return nil, errors.New("cache miss")
}

func (f *rateLimitResetCacheFake) SetAPIKeyRateLimit(_ context.Context, keyID int64, data *APIKeyRateLimitCacheData) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	clone := *data
	f.entries[keyID] = &clone
	return nil
}

func (f *rateLimitResetCacheFake) InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.invalidated) == 0 {
		f.invalidateCtxErr = ctx.Err()
	}
	f.invalidated = append(f.invalidated, keyID)
	delete(f.entries, keyID)
	return nil
}

// rateLimitResetRepoStub serves the stored key's rate-limit counters, like the
// database after the reset was saved.
type rateLimitResetRepoStub struct {
	maxConcurrencyAPIKeyRepoStub
}

func (s *rateLimitResetRepoStub) Update(ctx context.Context, key *APIKey) error {
	_ = s.maxConcurrencyAPIKeyRepoStub.Update(ctx, key)
	clone := *key
	s.stored = &clone
	return nil
}

// ResetRateLimitWindows is called asynchronously when a window is unset or
// expired; the stored counters are already reset, so there is nothing to do.
func (s *rateLimitResetRepoStub) ResetRateLimitWindows(context.Context, int64) error { return nil }

func (s *rateLimitResetRepoStub) GetRateLimitData(context.Context, int64) (*APIKeyRateLimitData, error) {
	return &APIKeyRateLimitData{
		Usage5h:       s.stored.Usage5h,
		Usage1d:       s.stored.Usage1d,
		Usage7d:       s.stored.Usage7d,
		Window5hStart: s.stored.Window5hStart,
		Window1dStart: s.stored.Window1dStart,
		Window7dStart: s.stored.Window7dStart,
	}, nil
}

// Production 2026-11-12 (#229768): the user reset a Key whose daily limit was
// spent, the database counters went to 0, but the Redis entry kept the old
// usage because the API Key service was never given the invalidator, so the
// Key stayed blocked until the cached window ended.
func TestAPIKeyResetRateLimitUsageUnblocksKeyImmediately(t *testing.T) {
	// All windows are live, so the first check starts no async window reset
	// that could clear the cache on its own.
	windowStart := time.Now().Add(-2 * time.Hour)
	stored := &APIKey{
		ID: 524, UserID: 9, Key: "sk-test-reset-rate-limit", Name: "k", Status: StatusActive,
		RateLimit1d: 1000, Usage5h: 900, Usage1d: 1001.5, Usage7d: 1001.5,
		Window5hStart: &windowStart, Window1dStart: &windowStart, Window7dStart: &windowStart,
	}
	repo := &rateLimitResetRepoStub{maxConcurrencyAPIKeyRepoStub{stored: stored}}
	w := windowStart.Unix()
	cache := &rateLimitResetCacheFake{entries: map[int64]*APIKeyRateLimitCacheData{
		524: {Usage5h: 900, Usage1d: 1001.5, Usage7d: 1001.5, Window5h: w, Window1d: w, Window7d: w},
	}}
	billing := &BillingCacheService{cache: cache, apiKeyRateLimitLoader: repo}
	keys := ProvideAPIKeyService(repo, nil, nil, nil, nil, nil, cache, &config.Config{})

	require.ErrorIs(t, billing.checkAPIKeyRateLimits(context.Background(), stored), ErrAPIKeyRateLimit1dExceeded)

	cache.mu.Lock()
	require.Empty(t, cache.invalidated, "a live window must not trigger the async reset")
	cache.mu.Unlock()

	// The caller may already be gone once the database write commits; the
	// cache must still be cleared.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reset := true
	updated, err := keys.Update(ctx, 524, 9, UpdateAPIKeyRequest{ResetRateLimitUsage: &reset})
	require.NoError(t, err)
	cache.mu.Lock()
	require.Equal(t, []int64{524}, cache.invalidated)
	require.NoError(t, cache.invalidateCtxErr)
	cache.mu.Unlock()

	// The next request reloads the reset counters from the database.
	require.NoError(t, billing.checkAPIKeyRateLimits(context.Background(), updated))
}

func TestAPIKeyUpdateWithoutResetKeepsRateLimitCache(t *testing.T) {
	stored := &APIKey{ID: 7, UserID: 9, Key: "sk-test-no-reset", Name: "k", Status: StatusActive, RateLimit1d: 10}
	repo := &rateLimitResetRepoStub{maxConcurrencyAPIKeyRepoStub{stored: stored}}
	cache := &rateLimitResetCacheFake{entries: map[int64]*APIKeyRateLimitCacheData{7: {Usage1d: 3}}}
	keys := ProvideAPIKeyService(repo, nil, nil, nil, nil, nil, cache, &config.Config{})

	_, err := keys.Update(context.Background(), 7, 9, UpdateAPIKeyRequest{RateLimit1d: floatPtrForResetTest(20)})
	require.NoError(t, err)
	require.Empty(t, cache.invalidated)
	require.Equal(t, 3.0, cache.entries[7].Usage1d)
}

func floatPtrForResetTest(v float64) *float64 { return &v }
