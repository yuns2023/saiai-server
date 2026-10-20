//go:build integration

package repository

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type StickyOwnershipSuite struct{ IntegrationRedisSuite }

func TestStickyOwnershipSuite(t *testing.T) { suite.Run(t, new(StickyOwnershipSuite)) }

func (s *StickyOwnershipSuite) TestMigrationFencesLateSuccessIncludingABA() {
	c := &gatewayCache{rdb: s.rdb}
	t := s.T()
	claim := func(previous, token string, account int64) {
		ok, err := c.ClaimStickyOwnership(s.ctx, 1, "session", previous, token, account, 2*time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
	}
	confirm := func(token string, account int64, want bool) {
		ok, err := c.ConfirmStickyOwnership(s.ctx, 1, "session", token, account, time.Hour)
		require.NoError(t, err)
		require.Equal(t, want, ok)
	}
	claim("", "1:first", 1)
	_, err := c.GetSessionAccountID(s.ctx, 1, "session")
	require.Error(t, err, "selection must not confirm success")
	confirm("1:first", 1, true)
	claim("1:first", "2:second", 2)
	confirm("1:first", 1, false)
	confirm("2:second", 2, true)
	claim("2:second", "1:third", 1)
	confirm("1:first", 1, false)
	confirm("1:third", 1, true)
	account, err := c.GetSessionAccountID(s.ctx, 1, "session")
	require.NoError(t, err)
	require.Equal(t, int64(1), account)
	_, err = c.GetPendingSessionAccountID(s.ctx, 1, "session")
	require.Error(t, err)
	ttl, err := s.rdb.TTL(s.ctx, stickyOwnershipKey(1, "session")).Result()
	require.NoError(t, err)
	require.InDelta(t, time.Hour.Seconds(), ttl.Seconds(), 2)
}

func (s *StickyOwnershipSuite) TestConcurrentFirstSelectionsHaveOneWinner() {
	c := &gatewayCache{rdb: s.rdb}
	var wg sync.WaitGroup
	winners := make(chan string, 12)
	errs := make(chan error, 12)
	for i := 1; i <= 12; i++ {
		wg.Add(1)
		go func(account int64) {
			defer wg.Done()
			token := fmt.Sprintf("%d:generation", account)
			ok, err := c.ClaimStickyOwnership(s.ctx, 1, "parallel", "", token, account, 2*time.Minute)
			if err != nil {
				errs <- err
			}
			if ok {
				winners <- token
			}
		}(int64(i))
	}
	wg.Wait()
	close(winners)
	close(errs)
	for err := range errs {
		require.NoError(s.T(), err)
	}
	require.Len(s.T(), winners, 1)
	owner, err := c.GetStickyOwnership(s.ctx, 1, "parallel")
	require.NoError(s.T(), err)
	require.Equal(s.T(), <-winners, owner)
	other, err := c.GetStickyOwnership(s.ctx, 2, "parallel")
	require.NoError(s.T(), err)
	require.Empty(s.T(), other, "groups must remain isolated")
}

func (s *StickyOwnershipSuite) TestRenewCannotReviveOrReassignOwner() {
	c := &gatewayCache{rdb: s.rdb}
	t := s.T()
	ok, err := c.ClaimStickyOwnership(s.ctx, 1, "lease", "", "1:a", 1, 2*time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, s.rdb.PExpire(s.ctx, stickyOwnershipKey(1, "lease"), time.Second).Err())
	ok, err = c.RenewStickyOwnership(s.ctx, 1, "lease", "1:a", 2*time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	ttl, err := s.rdb.TTL(s.ctx, stickyOwnershipKey(1, "lease")).Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Minute)
	ok, err = c.RenewStickyOwnership(s.ctx, 1, "lease", "2:stale", 2*time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, s.rdb.Del(s.ctx, stickyOwnershipKey(1, "lease")).Err())
	ok, err = c.RenewStickyOwnership(s.ctx, 1, "lease", "1:a", 2*time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = c.ConfirmStickyOwnership(s.ctx, 1, "lease", "1:a", 1, time.Hour)
	require.NoError(t, err)
	require.False(t, ok)
}
