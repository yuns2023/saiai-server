//go:build integration

package repository

import (
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type AITPInstallCodeStoreSuite struct {
	IntegrationRedisSuite
	store service.AITPInstallCodeStore
}

func (s *AITPInstallCodeStoreSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.store = NewAITPInstallCodeStore(s.rdb)
}

func (s *AITPInstallCodeStoreSuite) rec() *service.AITPInstallCodeRecord {
	return &service.AITPInstallCodeRecord{
		UserID: 7, APIKeyID: 41, Shell: service.AITPInstallShellPowerShell,
		CreatedAt: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC),
	}
}

func (s *AITPInstallCodeStoreSuite) TestSaveConsumeOnceUnderHashedKey() {
	code := "Q7M2K9XW4TRB"
	saved, err := s.store.Save(s.ctx, code, s.rec(), service.AITPInstallCodeTTL)
	s.RequireNoError(err)
	require.True(s.T(), saved)

	// Only sha256(code) is a key; the value holds ids, never a key.
	key := aitpInstallCodeKey(code)
	require.NotContains(s.T(), key, code)
	keys, err := s.rdb.Keys(s.ctx, "*"+code+"*").Result()
	s.RequireNoError(err)
	require.Empty(s.T(), keys)
	ttl, err := s.rdb.TTL(s.ctx, key).Result()
	s.RequireNoError(err)
	s.AssertTTLWithin(ttl, service.AITPInstallCodeTTL-5*time.Second, service.AITPInstallCodeTTL)

	// SET NX: the same code cannot be stored twice.
	saved, err = s.store.Save(s.ctx, code, &service.AITPInstallCodeRecord{UserID: 99, APIKeyID: 99}, time.Minute)
	s.RequireNoError(err)
	require.False(s.T(), saved)

	got, err := s.store.Consume(s.ctx, code)
	s.RequireNoError(err)
	require.Equal(s.T(), *s.rec(), *got)

	again, err := s.store.Consume(s.ctx, code)
	s.RequireNoError(err)
	require.Nil(s.T(), again)
}

func (s *AITPInstallCodeStoreSuite) TestExpiredCodeIsGone() {
	code := "0123456789AB"
	saved, err := s.store.Save(s.ctx, code, s.rec(), 50*time.Millisecond)
	s.RequireNoError(err)
	require.True(s.T(), saved)
	time.Sleep(120 * time.Millisecond)
	got, err := s.store.Consume(s.ctx, code)
	s.RequireNoError(err)
	require.Nil(s.T(), got)
}

func (s *AITPInstallCodeStoreSuite) TestConcurrentConsumeSingleWinner() {
	code := "ZZZZ0000YYYY"
	saved, err := s.store.Save(s.ctx, code, s.rec(), time.Minute)
	s.RequireNoError(err)
	require.True(s.T(), saved)
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.store.Consume(s.ctx, code)
			if err == nil && got != nil {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(s.T(), 1, winners)
}

func (s *AITPInstallCodeStoreSuite) TestCountersAreFixedWindows() {
	for i := int64(1); i <= 3; i++ {
		n, err := s.store.CountIssued(s.ctx, 7, time.Minute)
		s.RequireNoError(err)
		require.Equal(s.T(), i, n)
	}
	n, err := s.store.CountIssued(s.ctx, 8, time.Minute)
	s.RequireNoError(err)
	require.Equal(s.T(), int64(1), n, "per user")
	ttl, err := s.rdb.PTTL(s.ctx, aitpInstallIssueKeyPrefix+"7").Result()
	s.RequireNoError(err)
	s.AssertTTLWithin(ttl, 50*time.Second, time.Minute)

	n, err = s.store.CountRedeemAttempt(s.ctx, "203.0.113.9", 100*time.Millisecond)
	s.RequireNoError(err)
	require.Equal(s.T(), int64(1), n)
	n, err = s.store.CountRedeemAttempt(s.ctx, "203.0.113.9", 100*time.Millisecond)
	s.RequireNoError(err)
	require.Equal(s.T(), int64(2), n)
	time.Sleep(200 * time.Millisecond)
	n, err = s.store.CountRedeemAttempt(s.ctx, "203.0.113.9", 100*time.Millisecond)
	s.RequireNoError(err)
	require.Equal(s.T(), int64(1), n, "the window expired")

	// A counter that lost its TTL is repaired instead of counting forever.
	s.RequireNoError(s.rdb.Set(s.ctx, aitpInstallRedeemIPKeyPrefix+"198.51.100.1", 5, 0).Err())
	n, err = s.store.CountRedeemAttempt(s.ctx, "198.51.100.1", time.Minute)
	s.RequireNoError(err)
	require.Equal(s.T(), int64(6), n)
	ttl, err = s.rdb.PTTL(s.ctx, aitpInstallRedeemIPKeyPrefix+"198.51.100.1").Result()
	s.RequireNoError(err)
	require.Greater(s.T(), ttl, time.Duration(0))
}

func TestAITPInstallCodeStoreSuite(t *testing.T) {
	suite.Run(t, new(AITPInstallCodeStoreSuite))
}
