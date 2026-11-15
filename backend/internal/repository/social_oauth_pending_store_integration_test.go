//go:build integration

package repository

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type SocialOAuthPendingStoreSuite struct {
	IntegrationRedisSuite
	store service.SocialOAuthPendingStore
}

func (s *SocialOAuthPendingStoreSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.store = NewSocialOAuthPendingStore(s.rdb)
}

func (s *SocialOAuthPendingStoreSuite) pending() *service.SocialOAuthPendingSignup {
	return &service.SocialOAuthPendingSignup{Provider: service.SocialOAuthProviderGitHub, Subject: "583231", Username: "octocat", VerifiedEmail: "octo@example.com"}
}

func (s *SocialOAuthPendingStoreSuite) TestSaveConsumeOnce() {
	token := strings.Repeat("a", 64)
	s.RequireNoError(s.store.Save(s.ctx, token, s.pending(), time.Minute))

	// Only a hash of the handle is used as the key.
	key := socialOAuthPendingKey(token)
	require.NotContains(s.T(), key, token)
	n, err := s.rdb.Exists(s.ctx, key).Result()
	s.RequireNoError(err)
	require.Equal(s.T(), int64(1), n)
	ttl, err := s.rdb.TTL(s.ctx, key).Result()
	s.RequireNoError(err)
	s.AssertTTLWithin(ttl, time.Second, time.Minute)

	peeked, err := s.store.Peek(s.ctx, token)
	s.RequireNoError(err)
	require.Equal(s.T(), "583231", peeked.Subject)

	got, err := s.store.Consume(s.ctx, token)
	s.RequireNoError(err)
	require.Equal(s.T(), *s.pending(), *got)

	again, err := s.store.Consume(s.ctx, token)
	s.RequireNoError(err)
	require.Nil(s.T(), again)
	peeked, err = s.store.Peek(s.ctx, token)
	s.RequireNoError(err)
	require.Nil(s.T(), peeked)
}

func (s *SocialOAuthPendingStoreSuite) TestConcurrentConsumeSingleWinner() {
	token := strings.Repeat("c", 64)
	s.RequireNoError(s.store.Save(s.ctx, token, s.pending(), time.Minute))
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.store.Consume(s.ctx, token)
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

func (s *SocialOAuthPendingStoreSuite) TestSaveRefusesOverwrite() {
	token := strings.Repeat("d", 64)
	s.RequireNoError(s.store.Save(s.ctx, token, s.pending(), time.Minute))
	require.Error(s.T(), s.store.Save(s.ctx, token, s.pending(), time.Minute))
}

func (s *SocialOAuthPendingStoreSuite) TestMissing() {
	got, err := s.store.Consume(s.ctx, strings.Repeat("e", 64))
	s.RequireNoError(err)
	require.Nil(s.T(), got)
}

func (s *SocialOAuthPendingStoreSuite) TestFailureBudgetDeletesPending() {
	token := strings.Repeat("f", 64)
	s.RequireNoError(s.store.Save(s.ctx, token, s.pending(), time.Minute))
	for i := 1; i < 3; i++ {
		exhausted, err := s.store.RecordFailure(s.ctx, token, 3, time.Minute)
		s.RequireNoError(err)
		require.False(s.T(), exhausted)
	}
	exhausted, err := s.store.RecordFailure(s.ctx, token, 3, time.Minute)
	s.RequireNoError(err)
	require.True(s.T(), exhausted)
	got, err := s.store.Peek(s.ctx, token)
	s.RequireNoError(err)
	require.Nil(s.T(), got)
	ttl, err := s.rdb.TTL(s.ctx, hashedKey(socialOAuthPendingFailKeyPrefix, token)).Result()
	s.RequireNoError(err)
	require.True(s.T(), ttl < 0, "counter removed with the entry")
}

func (s *SocialOAuthPendingStoreSuite) TestHandoffConsumeOnce() {
	store := NewSocialOAuthHandoffStore(s.rdb)
	code := strings.Repeat("9", 64)
	h := &service.SocialOAuthHandoff{Provider: service.SocialOAuthProviderGoogle, BinderHash: "bh", Kind: service.SocialOAuthHandoffTokens, AccessToken: "at", RefreshToken: "rt", ExpiresIn: 900, Redirect: "/keys"}
	s.RequireNoError(store.Save(s.ctx, code, h, 90*time.Second))
	ttl, err := s.rdb.TTL(s.ctx, hashedKey(socialOAuthHandoffKeyPrefix, code)).Result()
	s.RequireNoError(err)
	s.AssertTTLWithin(ttl, time.Second, 90*time.Second)
	got, err := store.Consume(s.ctx, code)
	s.RequireNoError(err)
	require.Equal(s.T(), *h, *got)
	again, err := store.Consume(s.ctx, code)
	s.RequireNoError(err)
	require.Nil(s.T(), again)
}

func TestSocialOAuthPendingStoreSuite(t *testing.T) {
	suite.Run(t, new(SocialOAuthPendingStoreSuite))
}
