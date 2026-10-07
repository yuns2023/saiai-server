//go:build integration

package repository

import (
	"errors"
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
