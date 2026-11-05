//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func (s *ConcurrencyCacheSuite) pairCache() service.UserAPIKeySlotCache {
	pair, ok := s.cache.(service.UserAPIKeySlotCache)
	require.True(s.T(), ok, "redis concurrency cache must implement UserAPIKeySlotCache")
	return pair
}

func (s *ConcurrencyCacheSuite) zcard(key string) int64 {
	n, err := s.rdb.ZCard(s.ctx, key).Result()
	require.NoError(s.T(), err)
	return n
}

func (s *ConcurrencyCacheSuite) TestUserAPIKeySlot_KeyLimit() {
	pair := s.pairCache()
	userID, keyID := int64(41), int64(901)

	for _, req := range []string{"r1", "r2"} {
		out, err := pair.AcquireUserAPIKeySlot(s.ctx, userID, 10, keyID, 2, req)
		require.NoError(s.T(), err)
		require.Equal(s.T(), service.SlotAcquired, out)
	}
	out, err := pair.AcquireUserAPIKeySlot(s.ctx, userID, 10, keyID, 2, "r3")
	require.NoError(s.T(), err)
	require.Equal(s.T(), service.SlotAPIKeyFull, out)
	// The rejected request holds neither slot.
	require.Equal(s.T(), int64(2), s.zcard(userSlotKey(userID)))
	require.Equal(s.T(), int64(2), s.zcard(apiKeySlotKey(keyID)))

	// User slots are shared with the plain user-slot path.
	cur, err := s.cache.GetUserConcurrency(s.ctx, userID)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 2, cur)

	require.NoError(s.T(), pair.ReleaseUserAPIKeySlot(s.ctx, userID, keyID, "r1"))
	require.Equal(s.T(), int64(1), s.zcard(userSlotKey(userID)))
	require.Equal(s.T(), int64(1), s.zcard(apiKeySlotKey(keyID)))
	// Releasing twice is harmless.
	require.NoError(s.T(), pair.ReleaseUserAPIKeySlot(s.ctx, userID, keyID, "r1"))

	out, err = pair.AcquireUserAPIKeySlot(s.ctx, userID, 10, keyID, 2, "r3")
	require.NoError(s.T(), err)
	require.Equal(s.T(), service.SlotAcquired, out)
}

func (s *ConcurrencyCacheSuite) TestUserAPIKeySlot_UserLimitAcrossKeys() {
	pair := s.pairCache()
	userID := int64(42)

	// Key caps add up to more than the user's 3: first come, first served.
	for i, keyID := range []int64{1, 1, 2} {
		out, err := pair.AcquireUserAPIKeySlot(s.ctx, userID, 3, keyID, 5, "u"+string(rune('a'+i)))
		require.NoError(s.T(), err)
		require.Equal(s.T(), service.SlotAcquired, out)
	}
	out, err := pair.AcquireUserAPIKeySlot(s.ctx, userID, 3, 2, 5, "ud")
	require.NoError(s.T(), err)
	require.Equal(s.T(), service.SlotUserFull, out)
	require.Equal(s.T(), int64(1), s.zcard(apiKeySlotKey(2)), "user-full request must not keep a key slot")

	// A plain user slot (e.g. a request without a key) counts toward the same total.
	ok, err := s.cache.AcquireUserSlot(s.ctx, userID, 3, "plain")
	require.NoError(s.T(), err)
	require.False(s.T(), ok)
}

func (s *ConcurrencyCacheSuite) TestUserAPIKeySlot_UnlimitedUser() {
	pair := s.pairCache()
	out, err := pair.AcquireUserAPIKeySlot(s.ctx, 43, 0, 903, 1, "a")
	require.NoError(s.T(), err)
	require.Equal(s.T(), service.SlotAcquired, out)
	require.Zero(s.T(), s.zcard(userSlotKey(43)), "unlimited user keeps no user slot, as AcquireUserSlot")

	out, err = pair.AcquireUserAPIKeySlot(s.ctx, 43, 0, 903, 1, "b")
	require.NoError(s.T(), err)
	require.Equal(s.T(), service.SlotAPIKeyFull, out)
}

func (s *ConcurrencyCacheSuite) TestUserAPIKeySlot_TTLAndExpiredMembers() {
	pair := s.pairCache()
	keyID := int64(904)
	out, err := pair.AcquireUserAPIKeySlot(s.ctx, 44, 5, keyID, 1, "fresh")
	require.NoError(s.T(), err)
	require.Equal(s.T(), service.SlotAcquired, out)

	ttl, err := s.rdb.TTL(s.ctx, apiKeySlotKey(keyID)).Result()
	require.NoError(s.T(), err)
	s.AssertTTLWithin(ttl, 1*time.Second, testSlotTTL)

	// A member older than the slot TTL no longer counts.
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, apiKeySlotKey(keyID), redis.Z{Score: 1, Member: "fresh"}).Err())
	out, err = pair.AcquireUserAPIKeySlot(s.ctx, 44, 5, keyID, 1, "next")
	require.NoError(s.T(), err)
	require.Equal(s.T(), service.SlotAcquired, out)
}

func (s *ConcurrencyCacheSuite) TestCleanupStaleProcessSlots_APIKeySlots() {
	pair := s.pairCache()
	_, err := pair.AcquireUserAPIKeySlot(s.ctx, 45, 5, 905, 5, "oldproc-1")
	require.NoError(s.T(), err)
	_, err = pair.AcquireUserAPIKeySlot(s.ctx, 45, 5, 905, 5, "newproc-1")
	require.NoError(s.T(), err)

	require.NoError(s.T(), s.cache.CleanupStaleProcessSlots(s.ctx, "newproc-"))

	members, err := s.rdb.ZRange(s.ctx, apiKeySlotKey(905), 0, -1).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), []string{"newproc-1"}, members)
}

// A retried acquire with the same request ID (e.g. a resent command whose
// first reply was lost) must find its own slots, not report a full limit.
func (s *ConcurrencyCacheSuite) TestUserAPIKeySlot_SameRequestIDRetry() {
	pair := s.pairCache()
	cases := []struct {
		name            string
		userMax, keyMax int
	}{
		{"key full", 10, 1},
		{"user full", 1, 10},
		{"both full", 1, 1},
		{"unlimited user", 0, 1},
	}
	for i, tc := range cases {
		userID, keyID := int64(60+i), int64(960+i)
		for attempt := 0; attempt < 2; attempt++ {
			out, err := pair.AcquireUserAPIKeySlot(s.ctx, userID, tc.userMax, keyID, tc.keyMax, "same")
			require.NoError(s.T(), err, tc.name)
			require.Equal(s.T(), service.SlotAcquired, out, tc.name)
		}
		require.Equal(s.T(), int64(1), s.zcard(apiKeySlotKey(keyID)), tc.name)
		wantUser := int64(1)
		if tc.userMax <= 0 {
			wantUser = 0
		}
		require.Equal(s.T(), wantUser, s.zcard(userSlotKey(userID)), tc.name)

		out, err := pair.AcquireUserAPIKeySlot(s.ctx, userID, tc.userMax, keyID, tc.keyMax, "other")
		require.NoError(s.T(), err, tc.name)
		require.NotEqual(s.T(), service.SlotAcquired, out, tc.name)
	}
}

// A retry that finds its key slot but not its user slot (e.g. half a release
// went through) refreshes the key slot without re-adding the user slot.
func (s *ConcurrencyCacheSuite) TestUserAPIKeySlot_RetryDoesNotReAddUserSlot() {
	pair := s.pairCache()
	userID, keyID := int64(70), int64(970)
	out, err := pair.AcquireUserAPIKeySlot(s.ctx, userID, 1, keyID, 5, "half")
	require.NoError(s.T(), err)
	require.Equal(s.T(), service.SlotAcquired, out)
	require.NoError(s.T(), s.rdb.ZRem(s.ctx, userSlotKey(userID), "half").Err())
	ok, err := s.cache.AcquireUserSlot(s.ctx, userID, 1, "plain")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	out, err = pair.AcquireUserAPIKeySlot(s.ctx, userID, 1, keyID, 5, "half")
	require.NoError(s.T(), err)
	require.Equal(s.T(), service.SlotAcquired, out)
	members, err := s.rdb.ZRange(s.ctx, userSlotKey(userID), 0, -1).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), []string{"plain"}, members, "user total must stay at its limit")
}
