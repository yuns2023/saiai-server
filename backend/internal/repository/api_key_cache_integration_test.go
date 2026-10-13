//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type ApiKeyCacheSuite struct {
	IntegrationRedisSuite
}

func (s *ApiKeyCacheSuite) TestCreateAttemptCount() {
	tests := []struct {
		name string
		fn   func(ctx context.Context, rdb *redis.Client, cache *apiKeyCache)
	}{
		{
			name: "missing_key_returns_zero_nil",
			fn: func(ctx context.Context, rdb *redis.Client, cache *apiKeyCache) {
				userID := int64(1)

				count, err := cache.GetCreateAttemptCount(ctx, userID)

				require.NoError(s.T(), err, "expected nil error for missing key")
				require.Equal(s.T(), 0, count, "expected zero count for missing key")
			},
		},
		{
			name: "increment_increases_count_and_sets_ttl",
			fn: func(ctx context.Context, rdb *redis.Client, cache *apiKeyCache) {
				userID := int64(1)
				key := fmt.Sprintf("%s%d", apiKeyRateLimitKeyPrefix, userID)

				require.NoError(s.T(), cache.IncrementCreateAttemptCount(ctx, userID), "IncrementCreateAttemptCount")
				require.NoError(s.T(), cache.IncrementCreateAttemptCount(ctx, userID), "IncrementCreateAttemptCount 2")

				count, err := cache.GetCreateAttemptCount(ctx, userID)
				require.NoError(s.T(), err, "GetCreateAttemptCount")
				require.Equal(s.T(), 2, count, "count mismatch")

				ttl, err := rdb.TTL(ctx, key).Result()
				require.NoError(s.T(), err, "TTL")
				s.AssertTTLWithin(ttl, 1*time.Second, apiKeyRateLimitDuration)
			},
		},
		{
			name: "delete_removes_key",
			fn: func(ctx context.Context, rdb *redis.Client, cache *apiKeyCache) {
				userID := int64(1)

				require.NoError(s.T(), cache.IncrementCreateAttemptCount(ctx, userID))
				require.NoError(s.T(), cache.DeleteCreateAttemptCount(ctx, userID), "DeleteCreateAttemptCount")

				count, err := cache.GetCreateAttemptCount(ctx, userID)
				require.NoError(s.T(), err, "expected nil error after delete")
				require.Equal(s.T(), 0, count, "expected zero count after delete")
			},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			// 每个 case 重新获取隔离资源
			rdb := testRedis(s.T())
			cache := &apiKeyCache{rdb: rdb}
			ctx := context.Background()

			tt.fn(ctx, rdb, cache)
		})
	}
}

func (s *ApiKeyCacheSuite) TestDailyUsage() {
	tests := []struct {
		name string
		fn   func(ctx context.Context, rdb *redis.Client, cache *apiKeyCache)
	}{
		{
			name: "increment_increases_count",
			fn: func(ctx context.Context, rdb *redis.Client, cache *apiKeyCache) {
				dailyKey := "daily:sk-test"

				require.NoError(s.T(), cache.IncrementDailyUsage(ctx, dailyKey), "IncrementDailyUsage")
				require.NoError(s.T(), cache.IncrementDailyUsage(ctx, dailyKey), "IncrementDailyUsage 2")

				n, err := rdb.Get(ctx, dailyKey).Int()
				require.NoError(s.T(), err, "Get dailyKey")
				require.Equal(s.T(), 2, n, "expected daily usage=2")
			},
		},
		{
			name: "set_expiry_sets_ttl",
			fn: func(ctx context.Context, rdb *redis.Client, cache *apiKeyCache) {
				dailyKey := "daily:sk-test-expiry"

				require.NoError(s.T(), cache.IncrementDailyUsage(ctx, dailyKey))
				require.NoError(s.T(), cache.SetDailyUsageExpiry(ctx, dailyKey, 1*time.Hour), "SetDailyUsageExpiry")

				ttl, err := rdb.TTL(ctx, dailyKey).Result()
				require.NoError(s.T(), err, "TTL dailyKey")
				require.Greater(s.T(), ttl, time.Duration(0), "expected ttl > 0")
			},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			rdb := testRedis(s.T())
			cache := &apiKeyCache{rdb: rdb}
			ctx := context.Background()

			tt.fn(ctx, rdb, cache)
		})
	}
}

func TestApiKeyCacheSuite(t *testing.T) {
	suite.Run(t, new(ApiKeyCacheSuite))
}

func (s *ApiKeyCacheSuite) TestAuthCacheFenceRejectsObsoleteSnapshot() {
	rdb := testRedis(s.T())
	cache := &apiKeyCache{rdb: rdb}
	ctx := context.Background()
	key := "TEST_ONLY_HASH"
	version, err := cache.GetAuthCacheVersion(ctx, key)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "0", version)
	entry := &service.APIKeyAuthCacheEntry{Snapshot: &service.APIKeyAuthSnapshot{APIKeyID: 99}}
	accepted, err := cache.SetAuthCacheIfVersion(ctx, key, entry, time.Minute, version)
	require.NoError(s.T(), err)
	require.True(s.T(), accepted)
	require.NoError(s.T(), cache.DeleteAuthCache(ctx, key))
	_, err = cache.GetAuthCache(ctx, key)
	require.ErrorIs(s.T(), err, redis.Nil)
	accepted, err = cache.SetAuthCacheIfVersion(ctx, key, entry, time.Minute, version)
	require.NoError(s.T(), err)
	require.False(s.T(), accepted, "a read started before revocation must not resurrect the Key")
	_, err = cache.GetAuthCache(ctx, key)
	require.ErrorIs(s.T(), err, redis.Nil)
	version, err = cache.GetAuthCacheVersion(ctx, key)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "1", version)
	negative := &service.APIKeyAuthCacheEntry{NotFound: true}
	accepted, err = cache.SetAuthCacheIfVersion(ctx, key, negative, time.Minute, version)
	require.NoError(s.T(), err)
	require.True(s.T(), accepted)
	stored, err := cache.GetAuthCache(ctx, key)
	require.NoError(s.T(), err)
	require.True(s.T(), stored.NotFound)
	ttl, err := rdb.TTL(ctx, apiKeyAuthCacheKey(key)).Result()
	require.NoError(s.T(), err)
	require.Positive(s.T(), ttl)
	versionTTL, err := rdb.TTL(ctx, apiKeyAuthVersionPrefix+key).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), time.Duration(-1), versionTTL, "fence must not disappear while a reader is delayed")
}
