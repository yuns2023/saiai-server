//go:build integration

package repository

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type SessionInitialSerialSuite struct {
	IntegrationRedisSuite
	cache *gatewayCache
}

func (s *SessionInitialSerialSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.cache = &gatewayCache{rdb: s.rdb}
}

func (s *SessionInitialSerialSuite) acquire(group int64, session string, account int64, generation, owner string) *service.SessionInitialSerialResult {
	s.T().Helper()
	result, err := s.cache.TryAcquireSessionInitialSerial(s.ctx, group, session, account, generation, owner, time.Minute)
	require.NoError(s.T(), err)
	return result
}

func (s *SessionInitialSerialSuite) release(account int64, result *service.SessionInitialSerialResult, owner string, forwarded bool) {
	s.T().Helper()
	require.NoError(s.T(), s.cache.ReleaseSessionInitialSerial(s.ctx, 1, "session", account, result.Generation, owner, forwarded))
}

func (s *SessionInitialSerialSuite) TestFirstThreeThenParallel() {
	for i := 1; i <= 3; i++ {
		owner := fmt.Sprintf("owner-%d", i)
		result := s.acquire(1, "session", 11, "", owner)
		require.Equal(s.T(), service.SessionInitialSerialAcquired, result.Decision)
		require.Equal(s.T(), i, result.Ordinal)
		// A retried Redis operation must not wait on itself or count twice.
		retry := s.acquire(1, "session", 11, result.Generation, owner)
		require.Equal(s.T(), service.SessionInitialSerialAcquired, retry.Decision)
		require.Equal(s.T(), i, retry.Ordinal)
		waiting := s.acquire(1, "session", 11, "", "follower")
		require.Equal(s.T(), service.SessionInitialSerialWait, waiting.Decision, "four must wait for three too")
		s.release(11, result, owner, true)
	}
	for _, owner := range []string{"four", "five", "six"} {
		require.Equal(s.T(), service.SessionInitialSerialBypass, s.acquire(1, "session", 11, "", owner).Decision)
	}
	ttl, err := s.rdb.PTTL(s.ctx, sessionInitialSerialKey(1, "session")).Result()
	require.NoError(s.T(), err)
	require.Greater(s.T(), ttl, 59*time.Minute)
}

func (s *SessionInitialSerialSuite) TestSwitchAndOldCallbacksAreFenced() {
	a := s.acquire(1, "session", 11, "", "a")
	b := s.acquire(1, "session", 22, "", "b")
	require.Equal(s.T(), 1, b.Ordinal)
	require.NotEqual(s.T(), a.Generation, b.Generation)
	s.release(11, a, "a", true)
	require.Equal(s.T(), service.SessionInitialSerialWait, s.acquire(1, "session", 22, "", "b-follower").Decision)
	require.Equal(s.T(), service.SessionInitialSerialStale, s.acquire(1, "session", 11, a.Generation, "old-a-follower").Decision)
	s.release(22, b, "b", true)
	// Returning to the old account starts a fresh three, not its old counter.
	a2 := s.acquire(1, "session", 11, "", "a2")
	require.Equal(s.T(), 1, a2.Ordinal)
	require.NotEqual(s.T(), a.Generation, a2.Generation)
	s.release(11, a, "a", false)
	require.Equal(s.T(), service.SessionInitialSerialWait, s.acquire(1, "session", 11, "", "a2-follower").Decision)
}

func (s *SessionInitialSerialSuite) TestCanceledReservationAndExpiredOwner() {
	a := s.acquire(1, "session", 11, "", "canceled")
	s.release(11, a, "canceled", false)
	b := s.acquire(1, "session", 11, "", "b")
	require.Equal(s.T(), 1, b.Ordinal, "no forwarding attempt must be refunded")
	key := sessionInitialSerialKey(1, "session")
	require.NoError(s.T(), s.rdb.HSet(s.ctx, key, "lease_until", 0).Err())
	c := s.acquire(1, "session", 11, "", "c")
	require.Equal(s.T(), 2, c.Ordinal, "expired attempt stays counted")
	s.release(11, b, "b", false)
	require.Equal(s.T(), service.SessionInitialSerialWait, s.acquire(1, "session", 11, "", "d").Decision)
	s.release(11, c, "c", true)
	require.NoError(s.T(), s.rdb.Del(s.ctx, key).Err())
	require.Equal(s.T(), 1, s.acquire(1, "session", 11, "", "new").Ordinal)
}

func (s *SessionInitialSerialSuite) TestIndependentSessionsAndGroups() {
	s.acquire(1, "session", 11, "", "a")
	require.Equal(s.T(), service.SessionInitialSerialAcquired, s.acquire(1, "other-session", 11, "", "b").Decision)
	require.Equal(s.T(), service.SessionInitialSerialAcquired, s.acquire(2, "session", 11, "", "c").Decision)
}

func (s *SessionInitialSerialSuite) TestConcurrentInstancesElectOneOwner() {
	const workers = 8
	results := make(chan *service.SessionInitialSerialResult, workers)
	errors := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Separate cache instances coordinate through the same Redis state.
			cache := &gatewayCache{rdb: s.rdb}
			r, err := cache.TryAcquireSessionInitialSerial(s.ctx, 1, "session", 11, "", fmt.Sprint(i), time.Minute)
			results <- r
			errors <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(s.T(), err)
	}
	acquired, waiting := 0, 0
	for r := range results {
		switch r.Decision {
		case service.SessionInitialSerialAcquired:
			acquired++
		case service.SessionInitialSerialWait:
			waiting++
		}
	}
	require.Equal(s.T(), 1, acquired)
	require.Equal(s.T(), workers-1, waiting)
}

func TestSessionInitialSerialSuite(t *testing.T) {
	suite.Run(t, new(SessionInitialSerialSuite))
}
