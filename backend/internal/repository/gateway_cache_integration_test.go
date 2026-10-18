//go:build integration

package repository

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type GatewayCacheSuite struct {
	IntegrationRedisSuite
	cache service.GatewayCache
}

func (s *GatewayCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.cache = NewGatewayCache(s.rdb)
}

func (s *GatewayCacheSuite) TestGetSessionAccountID_Missing() {
	_, err := s.cache.GetSessionAccountID(s.ctx, 1, "nonexistent")
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil for missing session")
}

func (s *GatewayCacheSuite) TestSetAndGetSessionAccountID() {
	sessionID := "s1"
	accountID := int64(99)
	groupID := int64(1)
	sessionTTL := 1 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, accountID, sessionTTL), "SetSessionAccountID")

	sid, err := s.cache.GetSessionAccountID(s.ctx, groupID, sessionID)
	require.NoError(s.T(), err, "GetSessionAccountID")
	require.Equal(s.T(), accountID, sid, "session id mismatch")
}

func (s *GatewayCacheSuite) TestSessionAccountID_TTL() {
	sessionID := "s2"
	accountID := int64(100)
	groupID := int64(1)
	sessionTTL := 1 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, accountID, sessionTTL), "SetSessionAccountID")

	sessionKey := buildSessionKey(groupID, sessionID)
	ttl, err := s.rdb.TTL(s.ctx, sessionKey).Result()
	require.NoError(s.T(), err, "TTL sessionKey after Set")
	s.AssertTTLWithin(ttl, 1*time.Second, sessionTTL)
}

func (s *GatewayCacheSuite) TestRefreshSessionTTL() {
	sessionID := "s3"
	accountID := int64(101)
	groupID := int64(1)
	initialTTL := 1 * time.Minute
	refreshTTL := 3 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, accountID, initialTTL), "SetSessionAccountID")

	require.NoError(s.T(), s.cache.RefreshSessionTTL(s.ctx, groupID, sessionID, refreshTTL), "RefreshSessionTTL")

	sessionKey := buildSessionKey(groupID, sessionID)
	ttl, err := s.rdb.TTL(s.ctx, sessionKey).Result()
	require.NoError(s.T(), err, "TTL after Refresh")
	s.AssertTTLWithin(ttl, 1*time.Second, refreshTTL)
}

func (s *GatewayCacheSuite) TestRefreshSessionTTL_MissingKey() {
	// RefreshSessionTTL on a missing key should not error (no-op)
	err := s.cache.RefreshSessionTTL(s.ctx, 1, "missing-session", 1*time.Minute)
	require.NoError(s.T(), err, "RefreshSessionTTL on missing key should not error")
}

func (s *GatewayCacheSuite) TestDeleteSessionAccountID() {
	sessionID := "openai:s4"
	accountID := int64(102)
	groupID := int64(1)
	sessionTTL := 1 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, accountID, sessionTTL), "SetSessionAccountID")
	require.NoError(s.T(), s.cache.DeleteSessionAccountID(s.ctx, groupID, sessionID), "DeleteSessionAccountID")

	_, err := s.cache.GetSessionAccountID(s.ctx, groupID, sessionID)
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil after delete")
}

func (s *GatewayCacheSuite) TestGetSessionAccountID_CorruptedValue() {
	sessionID := "corrupted"
	groupID := int64(1)
	sessionKey := buildSessionKey(groupID, sessionID)

	// Set a non-integer value
	require.NoError(s.T(), s.rdb.Set(s.ctx, sessionKey, "not-a-number", 1*time.Minute).Err(), "Set invalid value")

	_, err := s.cache.GetSessionAccountID(s.ctx, groupID, sessionID)
	require.Error(s.T(), err, "expected error for corrupted value")
	require.False(s.T(), errors.Is(err, redis.Nil), "expected parsing error, not redis.Nil")
}

func TestGatewayCacheSuite(t *testing.T) {
	suite.Run(t, new(GatewayCacheSuite))
}

func (s *GatewayCacheSuite) TestChatGPTTurnContextAtomicSnapshotAndScope() {
	firstCache := s.cache.(service.ChatGPTTurnCache)
	secondCache := NewGatewayCache(s.rdb).(service.ChatGPTTurnCache)
	scope := service.ChatGPTTurnScope{UserID: 1, APIKeyID: 2, GroupID: 3}
	first := &service.ChatGPTTurnSnapshot{Identity: service.ChatGPTTurnBillingIdentity{RequestID: "first", PayloadHash: "digest"},
		AccountID: 19, BasePriceUSD: 0.05, RequestedModel: "gpt-6-pro", StartedAt: time.Now().UTC()}
	key := scope.TurnKey(first.Identity)
	alias := scope.ResumeKey("TEST_ONLY_PRIVATE_CONVERSATION")
	copy, err := firstCache.PutChatGPTTurnIfAbsent(s.ctx, key, first, time.Minute)
	require.NoError(s.T(), err)
	require.Equal(s.T(), first, copy)
	changed := *first
	changed.BasePriceUSD = 0.09
	changed.AccountID = 20
	copy, err = secondCache.PutChatGPTTurnIfAbsent(s.ctx, key, &changed, time.Hour)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 0.05, copy.BasePriceUSD)
	require.Equal(s.T(), int64(19), copy.AccountID)
	require.NoError(s.T(), firstCache.BindChatGPTResume(s.ctx, alias, key))
	copy, err = secondCache.GetChatGPTResume(s.ctx, alias)
	require.NoError(s.T(), err)
	require.Equal(s.T(), first, copy)
	ttl, err := s.rdb.PTTL(s.ctx, key).Result()
	require.NoError(s.T(), err)
	require.LessOrEqual(s.T(), ttl, time.Minute)
	firstHash, secondHash := strings.Repeat("a", 64), strings.Repeat("b", 64)
	copy, err = firstCache.MergeChatGPTTurnImages(s.ctx, key, service.ChatGPTImageEvidence{GenerationSeen: true})
	require.NoError(s.T(), err, "zero observed images must decode as an empty array, not a Lua object")
	require.True(s.T(), copy.Images.GenerationSeen)
	_, err = firstCache.MergeChatGPTTurnImages(s.ctx, key, service.ChatGPTImageEvidence{AssetHashes: []string{firstHash}})
	require.NoError(s.T(), err)
	copy, err = secondCache.MergeChatGPTTurnImages(s.ctx, key, service.ChatGPTImageEvidence{AssetHashes: []string{firstHash, secondHash, "PRIVATE_INVALID_POINTER"}})
	require.NoError(s.T(), err)
	require.ElementsMatch(s.T(), []string{firstHash, secondHash}, copy.Images.AssetHashes)
	updatedTTL, err := s.rdb.PTTL(s.ctx, key).Result()
	require.NoError(s.T(), err)
	require.LessOrEqual(s.T(), updatedTTL, ttl)
	for _, other := range []service.ChatGPTTurnScope{{2, 2, 3}, {1, 3, 3}, {1, 2, 4}} {
		copy, err = secondCache.GetChatGPTResume(s.ctx, other.ResumeKey("TEST_ONLY_PRIVATE_CONVERSATION"))
		require.NoError(s.T(), err)
		require.Nil(s.T(), copy)
	}
	changed.Identity.RequestID = "second"
	_, err = secondCache.PutChatGPTTurnIfAbsent(s.ctx, scope.TurnKey(changed.Identity), &changed, time.Minute)
	require.NoError(s.T(), err)
	require.Error(s.T(), secondCache.BindChatGPTResume(s.ctx, alias, scope.TurnKey(changed.Identity)))
	require.NoError(s.T(), firstCache.MarkChatGPTTurnTerminal(s.ctx, key))
	copy, err = secondCache.GetChatGPTTurn(s.ctx, key)
	require.NoError(s.T(), err)
	require.True(s.T(), copy.TerminalSeen)
	require.False(s.T(), copy.Completed)
	require.NoError(s.T(), secondCache.BindChatGPTResume(s.ctx, alias, scope.TurnKey(changed.Identity)))
	require.NoError(s.T(), firstCache.CompleteChatGPTTurn(s.ctx, key))
	require.NoError(s.T(), secondCache.BindChatGPTResume(s.ctx, alias, scope.TurnKey(changed.Identity)))
	copy, err = firstCache.GetChatGPTResume(s.ctx, alias)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "second", copy.Identity.RequestID)
	raw, err := s.rdb.Get(s.ctx, key).Result()
	require.NoError(s.T(), err)
	require.NotContains(s.T(), raw, "TEST_ONLY_PRIVATE_CONVERSATION")
	require.NoError(s.T(), s.rdb.Del(s.ctx, scope.TurnKey(changed.Identity)).Err())
	copy, err = firstCache.GetChatGPTResume(s.ctx, alias)
	require.NoError(s.T(), err)
	require.Nil(s.T(), copy)
	require.Error(s.T(), firstCache.BindChatGPTResume(s.ctx, alias, scope.TurnKey(changed.Identity)))
}

func (s *GatewayCacheSuite) TestRefreshSessionTTLForAccount() {
	cache := s.cache.(*gatewayCache)
	for _, tc := range []struct {
		name              string
		owner             int64
		ttl               time.Duration
		exists, wantRenew bool
	}{
		{"same_owner", 17, time.Minute, true, true},
		{"wrong_owner", 18, time.Minute, true, false},
		{"missing", 17, 0, false, false},
		{"persistent", 17, 0, true, false},
	} {
		s.Run(tc.name, func() {
			key := "turn-renew-" + tc.name
			if tc.exists {
				require.NoError(s.T(), cache.SetSessionAccountID(s.ctx, 0, key, tc.owner, tc.ttl))
			}
			remaining, err := cache.RefreshSessionTTLForAccount(s.ctx, 0, key, 17, time.Hour, time.Minute)
			require.NoError(s.T(), err)
			if tc.wantRenew {
				require.Equal(s.T(), time.Hour, remaining)
				// A second success within a minute must not reset the deadline again.
				require.NoError(s.T(), s.rdb.PExpire(s.ctx, buildSessionKey(0, key), time.Hour-30*time.Second).Err())
				remaining, err = cache.RefreshSessionTTLForAccount(s.ctx, 0, key, 17, time.Hour, time.Minute)
				require.NoError(s.T(), err)
				require.LessOrEqual(s.T(), remaining, time.Hour-30*time.Second)
				require.Greater(s.T(), remaining, 59*time.Minute)
			} else {
				require.Zero(s.T(), remaining)
			}
			if tc.exists {
				owner, err := cache.GetSessionAccountID(s.ctx, 0, key)
				require.NoError(s.T(), err)
				require.Equal(s.T(), tc.owner, owner)
			} else {
				require.Zero(s.T(), s.rdb.Exists(s.ctx, buildSessionKey(0, key)).Val())
			}
		})
	}
	// Expire while a request is in flight; successful completion must not SET it.
	require.NoError(s.T(), cache.SetSessionAccountID(s.ctx, 0, "late", 17, time.Minute))
	require.NoError(s.T(), s.rdb.PExpire(s.ctx, buildSessionKey(0, "late"), -time.Second).Err())
	remaining, err := cache.RefreshSessionTTLForAccount(s.ctx, 0, "late", 17, time.Hour, time.Minute)
	require.NoError(s.T(), err)
	require.Zero(s.T(), remaining)
	require.Zero(s.T(), s.rdb.Exists(s.ctx, buildSessionKey(0, "late")).Val())
	// Concurrent stale completions cannot overwrite the current account.
	require.NoError(s.T(), cache.SetSessionAccountID(s.ctx, 0, "concurrent", 18, time.Minute))
	var wg sync.WaitGroup
	outcomes := make(chan time.Duration, 16)
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ttl, err := cache.RefreshSessionTTLForAccount(s.ctx, 0, "concurrent", 17, time.Hour, time.Minute)
			outcomes <- ttl
			errs <- err
		}()
	}
	wg.Wait()
	close(outcomes)
	close(errs)
	for err := range errs {
		require.NoError(s.T(), err)
	}
	for ttl := range outcomes {
		require.Zero(s.T(), ttl)
	}
	owner, err := cache.GetSessionAccountID(s.ctx, 0, "concurrent")
	require.NoError(s.T(), err)
	require.EqualValues(s.T(), 18, owner)
}
