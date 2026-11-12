//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// rateLimitResetCacheFake records invalidations. Rate limits are no longer
// read from or written to Redis, so any other BillingCache call panics via the
// nil embedded interface.
type rateLimitResetCacheFake struct {
	BillingCache
	mu          sync.Mutex
	invalidated []int64
	// invalidateCtxErr is the context error seen by the first invalidation.
	invalidateCtxErr error
}

func (f *rateLimitResetCacheFake) InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.invalidated) == 0 {
		f.invalidateCtxErr = ctx.Err()
	}
	f.invalidated = append(f.invalidated, keyID)
	return nil
}

// rateLimitResetRepoStub is the database row of one key. Update honors the
// save intents like the real repository: billing counters are written only
// for an explicit reset. afterGet simulates billing that commits between the
// load and the save.
type rateLimitResetRepoStub struct {
	maxConcurrencyAPIKeyRepoStub
	afterGet     func(row *APIKey)
	windowResets atomic.Int32
	resetErr     error
}

func (s *rateLimitResetRepoStub) GetByID(ctx context.Context, id int64) (*APIKey, error) {
	key, err := s.maxConcurrencyAPIKeyRepoStub.GetByID(ctx, id)
	if s.afterGet != nil {
		s.afterGet(s.stored)
	}
	return key, err
}

func (s *rateLimitResetRepoStub) Update(ctx context.Context, key *APIKey) error {
	_ = s.maxConcurrencyAPIKeyRepoStub.Update(ctx, key)
	row := *s.stored
	row.Name = key.Name
	row.RateLimit5h = key.RateLimit5h
	row.RateLimit1d = key.RateLimit1d
	row.RateLimit7d = key.RateLimit7d
	if key.ResetQuotaUsedOnSave {
		row.QuotaUsed = 0
	}
	if key.ResetRateLimitUsageOnSave {
		row.Usage5h, row.Usage1d, row.Usage7d = 0, 0, 0
		row.Window5hStart, row.Window1dStart, row.Window7dStart = nil, nil, nil
	}
	s.stored = &row
	return nil
}

func (s *rateLimitResetRepoStub) ResetRateLimitWindows(context.Context, int64) error {
	s.windowResets.Add(1)
	return s.resetErr
}

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

func spentDailyKey(windowStart time.Time) *APIKey {
	return &APIKey{
		ID: 524, UserID: 9, Key: "sk-test-reset-rate-limit", Name: "k", Status: StatusActive,
		RateLimit1d: 1000, Usage5h: 900, Usage1d: 1001.5, Usage7d: 1001.5,
		Window5hStart: &windowStart, Window1dStart: &windowStart, Window7dStart: &windowStart,
	}
}

// Production 2026-11-12 (#229768): after the user reset a Key whose daily
// limit was spent, the database counters were 0 but a cached Redis copy still
// held the old usage, so the Key stayed blocked. The check now reads the
// database only, so a reset takes effect on the next request.
func TestAPIKeyResetRateLimitUsageUnblocksKeyImmediately(t *testing.T) {
	repo := &rateLimitResetRepoStub{maxConcurrencyAPIKeyRepoStub: maxConcurrencyAPIKeyRepoStub{
		stored: spentDailyKey(time.Now().Add(-2 * time.Hour)),
	}}
	cache := &rateLimitResetCacheFake{}
	billing := &BillingCacheService{cache: cache, apiKeyRateLimitLoader: repo, runAsync: func(f func()) { f() }}
	keys := ProvideAPIKeyService(repo, nil, nil, nil, nil, nil, cache, &config.Config{})

	require.ErrorIs(t, billing.checkAPIKeyRateLimits(context.Background(), repo.stored), ErrAPIKeyRateLimit1dExceeded)

	// Legacy Redis entries are still removed on reset, even when the caller
	// is gone once the database write commits.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reset := true
	updated, err := keys.Update(ctx, 524, 9, UpdateAPIKeyRequest{ResetRateLimitUsage: &reset})
	require.NoError(t, err)
	cache.mu.Lock()
	require.Equal(t, []int64{524}, cache.invalidated)
	require.NoError(t, cache.invalidateCtxErr)
	cache.mu.Unlock()

	require.NoError(t, billing.checkAPIKeyRateLimits(context.Background(), updated))
	require.Zero(t, repo.windowResets.Load(), "unset windows after a reset need no maintenance")
}

// Billing that commits while the user edits the key must survive the save.
func TestAPIKeyUpdateWithoutResetKeepsConcurrentUsage(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	repo := &rateLimitResetRepoStub{maxConcurrencyAPIKeyRepoStub: maxConcurrencyAPIKeyRepoStub{
		stored: &APIKey{ID: 7, UserID: 9, Key: "sk-test-no-reset", Name: "k", Status: StatusActive,
			RateLimit1d: 10, Quota: 50, QuotaUsed: 3, Usage1d: 3, Window1dStart: &start},
	}}
	repo.afterGet = func(row *APIKey) { row.QuotaUsed, row.Usage1d = 4, 4 }
	cache := &rateLimitResetCacheFake{}
	keys := ProvideAPIKeyService(repo, nil, nil, nil, nil, nil, cache, &config.Config{})

	_, err := keys.Update(context.Background(), 7, 9, UpdateAPIKeyRequest{RateLimit1d: floatPtrForResetTest(20)})
	require.NoError(t, err)
	require.False(t, repo.updated.ResetQuotaUsedOnSave)
	require.False(t, repo.updated.ResetRateLimitUsageOnSave)
	require.Equal(t, 20.0, repo.stored.RateLimit1d)
	require.Equal(t, 3.0, repo.updated.QuotaUsed, "the save carried the stale loaded values")
	require.Equal(t, 3.0, repo.updated.Usage1d)
	require.Equal(t, 4.0, repo.stored.QuotaUsed, "and the billed values survived it")
	require.Equal(t, 4.0, repo.stored.Usage1d)
	require.Empty(t, cache.invalidated)

	reset := true
	_, err = keys.Update(context.Background(), 7, 9, UpdateAPIKeyRequest{ResetQuota: &reset})
	require.NoError(t, err)
	require.True(t, repo.updated.ResetQuotaUsedOnSave)
	require.False(t, repo.updated.ResetRateLimitUsageOnSave)
}

func TestAPIKeyRateLimitCheckWindows(t *testing.T) {
	ended := time.Now().Add(-25 * time.Hour)
	cases := []struct {
		name       string
		key        *APIKey
		wantErr    error
		wantResets int32
	}{
		{name: "live window over the limit", key: spentDailyKey(time.Now().Add(-time.Hour)), wantErr: ErrAPIKeyRateLimit1dExceeded},
		{name: "ended window", key: func() *APIKey {
			k := spentDailyKey(ended)
			live := time.Now().Add(-time.Hour)
			k.Window5hStart = &live
			return k
		}(), wantResets: 1},
		{name: "unset windows", key: &APIKey{ID: 1, RateLimit1d: 10, Usage1d: 99}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &rateLimitResetRepoStub{maxConcurrencyAPIKeyRepoStub: maxConcurrencyAPIKeyRepoStub{stored: tc.key}}
			billing := &BillingCacheService{cache: &rateLimitResetCacheFake{}, apiKeyRateLimitLoader: repo,
				runAsync: func(f func()) { f() }}
			err := billing.checkAPIKeyRateLimits(context.Background(), tc.key)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantResets, repo.windowResets.Load())
		})
	}
}

// Requests that see the same ended window schedule one reset at a time.
func TestAPIKeyRateLimitWindowResetRunsOncePerKey(t *testing.T) {
	ended := time.Now().Add(-25 * time.Hour)
	key := spentDailyKey(ended)
	repo := &rateLimitResetRepoStub{maxConcurrencyAPIKeyRepoStub: maxConcurrencyAPIKeyRepoStub{stored: key}}
	var pending []func()
	billing := &BillingCacheService{cache: &rateLimitResetCacheFake{}, apiKeyRateLimitLoader: repo,
		runAsync: func(f func()) { pending = append(pending, f) }}

	for i := 0; i < 3; i++ {
		require.NoError(t, billing.checkAPIKeyRateLimits(context.Background(), key))
	}
	require.Len(t, pending, 1, "an in-flight reset absorbs repeats")

	other := spentDailyKey(ended)
	other.ID = 525
	billing.scheduleRateLimitWindowReset(other.ID)
	require.Len(t, pending, 2, "another key is not blocked by the first one")
	pending = pending[:1]

	pending[0]()
	require.Equal(t, int32(1), repo.windowResets.Load())
	require.NoError(t, billing.checkAPIKeyRateLimits(context.Background(), key))
	require.Len(t, pending, 2, "a finished reset can be scheduled again")
}

// A failed reset releases the key so a later request can try again.
func TestAPIKeyRateLimitWindowResetRetriesAfterFailure(t *testing.T) {
	key := spentDailyKey(time.Now().Add(-25 * time.Hour))
	repo := &rateLimitResetRepoStub{maxConcurrencyAPIKeyRepoStub: maxConcurrencyAPIKeyRepoStub{stored: key},
		resetErr: errors.New("db down")}
	billing := &BillingCacheService{cache: &rateLimitResetCacheFake{}, apiKeyRateLimitLoader: repo,
		runAsync: func(f func()) { f() }}

	require.NoError(t, billing.checkAPIKeyRateLimits(context.Background(), key))
	require.NoError(t, billing.checkAPIKeyRateLimits(context.Background(), key))
	require.Equal(t, int32(2), repo.windowResets.Load())
}

func floatPtrForResetTest(v float64) *float64 { return &v }
