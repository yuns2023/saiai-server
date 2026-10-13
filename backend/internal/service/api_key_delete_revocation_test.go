//go:build unit

package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type revocationRepo struct {
	APIKeyRepository
	key          *APIKey
	deleted      atomic.Bool
	beforeCommit func()
	readHook     func()
	reads        atomic.Int32
}

func (r *revocationRepo) GetKeyAndOwnerID(context.Context, int64) (string, int64, error) {
	return r.key.Key, r.key.UserID, nil
}

func (r *revocationRepo) GetByKeyForAuth(context.Context, string) (*APIKey, error) {
	reads := r.reads.Add(1)
	if r.deleted.Load() {
		return nil, ErrAPIKeyNotFound
	}
	clone := *r.key
	if reads == 1 && r.readHook != nil {
		r.readHook()
	}
	return &clone, nil
}

func (r *revocationRepo) Delete(context.Context, int64) error {
	if r.beforeCommit != nil {
		r.beforeCommit()
	}
	r.deleted.Store(true)
	return nil
}

type revocationCache struct {
	APIKeyCache
	mu                sync.Mutex
	entries           map[string]*APIKeyAuthCacheEntry
	versions          map[string]int
	subscriber        func(string)
	versionErr        error
	rejected          int
	invalidationError error
}

func newRevocationCache() *revocationCache {
	return &revocationCache{entries: map[string]*APIKeyAuthCacheEntry{}, versions: map[string]int{}}
}

func (c *revocationCache) DeleteCreateAttemptCount(context.Context, int64) error { return nil }
func (c *revocationCache) GetAuthCache(_ context.Context, key string) (*APIKeyAuthCacheEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[key], nil
}

func (c *revocationCache) GetAuthCacheVersion(_ context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strconv.Itoa(c.versions[key]), c.versionErr
}

func (c *revocationCache) SetAuthCacheIfVersion(_ context.Context, key string, entry *APIKeyAuthCacheEntry, _ time.Duration, version string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strconv.Itoa(c.versions[key]) != version {
		c.rejected++
		return false, nil
	}
	c.entries[key] = entry
	return true, nil
}

func (c *revocationCache) SetAuthCache(_ context.Context, key string, entry *APIKeyAuthCacheEntry, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = entry
	return nil
}

func (c *revocationCache) DeleteAuthCache(ctx context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidationError = ctx.Err()
	c.versions[key]++
	delete(c.entries, key)
	return ctx.Err()
}

func (c *revocationCache) PublishAuthCacheInvalidation(context.Context, string) error { return nil }
func (c *revocationCache) SubscribeAuthCacheInvalidation(_ context.Context, handler func(string)) error {
	c.subscriber = handler
	return nil
}

func revocationTestKey() *APIKey {
	return &APIKey{ID: 99, Key: "TEST_ONLY", UserID: 7, Status: StatusActive,
		User:  &User{ID: 7, Status: StatusActive, Balance: 10},
		Group: &Group{ID: 42, Platform: PlatformOpenAI, Status: StatusActive, CodexClientPolicy: "local_proxy_only"}}
}

func revocationTestConfig(singleflight bool) *config.Config {
	return &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{
		L1Size: 1000, L1TTLSeconds: 60, L2TTLSeconds: 300, NegativeTTLSeconds: 30, Singleflight: singleflight}}
}

func TestAPIKeyDeleteInvalidatesOnlyAfterDatabaseCommit(t *testing.T) {
	repo := &revocationRepo{key: revocationTestKey()}
	cache := newRevocationCache()
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, cache, revocationTestConfig(true))
	t.Cleanup(svc.authCacheL1.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo.beforeCommit = func() {
		// A request between invalidation and deletion used to repopulate the
		// active Key snapshot. The corrected order clears it after commit.
		_, err := svc.GetByKey(context.Background(), "TEST_ONLY")
		require.NoError(t, err)
		cancel() // client disconnect after a successful database mutation
	}
	require.NoError(t, svc.Delete(ctx, 99, 7))
	require.NoError(t, cache.invalidationError, "revocation must finish despite client cancellation")
	_, err := svc.GetByKey(context.Background(), "TEST_ONLY")
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
}

func TestAPIKeyDeleteRejectsDelayedSnapshotOnSameAndOtherInstances(t *testing.T) {
	for _, otherInstance := range []bool{false, true} {
		for _, singleflight := range []bool{false, true} {
			t.Run(strconv.FormatBool(otherInstance)+"/singleflight="+strconv.FormatBool(singleflight), func(t *testing.T) {
				started, release := make(chan struct{}), make(chan struct{})
				repo := &revocationRepo{key: revocationTestKey(), readHook: func() { close(started); <-release }}
				cache := newRevocationCache()
				reader := NewAPIKeyService(repo, nil, nil, nil, nil, cache, revocationTestConfig(singleflight))
				t.Cleanup(reader.authCacheL1.Close)
				deleter := reader
				if otherInstance {
					deleter = NewAPIKeyService(repo, nil, nil, nil, nil, cache, revocationTestConfig(singleflight))
					t.Cleanup(deleter.authCacheL1.Close)
				}
				result := make(chan error, 1)
				go func() { _, err := reader.GetByKey(context.Background(), "TEST_ONLY"); result <- err }()
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("snapshot read did not start")
				}
				require.NoError(t, deleter.Delete(context.Background(), 99, 7))
				close(release)
				select {
				case err := <-result:
					require.ErrorIs(t, err, ErrAPIKeyNotFound)
				case <-time.After(time.Second):
					t.Fatal("snapshot load did not finish")
				}
				if otherInstance {
					require.Equal(t, 1, cache.rejected, "Redis fence works even before Pub/Sub arrives")
				}
				for _, svc := range []*APIKeyService{reader, deleter} {
					_, err := svc.GetByKey(context.Background(), "TEST_ONLY")
					require.ErrorIs(t, err, ErrAPIKeyNotFound)
				}
			})
		}
	}
}

func TestAPIKeyAuthFenceUnavailableDoesNotCacheDatabaseResult(t *testing.T) {
	repo := &revocationRepo{key: revocationTestKey()}
	cache := newRevocationCache()
	cache.versionErr = errors.New("TEST_ONLY_REDIS_UNAVAILABLE")
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, cache, revocationTestConfig(true))
	t.Cleanup(svc.authCacheL1.Close)
	for i := 0; i < 2; i++ {
		_, err := svc.GetByKey(context.Background(), "TEST_ONLY")
		require.NoError(t, err)
	}
	require.Equal(t, int32(2), repo.reads.Load())
	require.Empty(t, cache.entries)
}

func TestAPIKeyAuthSubscriberPreventsDelayedLocalSnapshot(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	repo := &revocationRepo{key: revocationTestKey(), readHook: func() { close(started); <-release }}
	cache := newRevocationCache()
	cfg := revocationTestConfig(true)
	cfg.APIKeyAuth.L2TTLSeconds = 0 // exercise local fence independently
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, cache, cfg)
	t.Cleanup(svc.authCacheL1.Close)
	svc.StartAuthCacheInvalidationSubscriber(context.Background())
	result := make(chan error, 1)
	go func() { _, err := svc.GetByKey(context.Background(), "TEST_ONLY"); result <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("snapshot read did not start")
	}
	repo.deleted.Store(true)
	cache.subscriber(svc.authCacheKey("TEST_ONLY"))
	close(release)
	select {
	case err := <-result:
		require.ErrorIs(t, err, ErrAPIKeyNotFound)
	case <-time.After(time.Second):
		t.Fatal("snapshot load did not finish")
	}
	_, err := svc.GetByKey(context.Background(), "TEST_ONLY")
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
}
