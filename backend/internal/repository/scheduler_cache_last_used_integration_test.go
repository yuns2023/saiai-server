//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// UpdateLastUsed is a read-modify-write of the whole cached account: a
// concurrent SetAccount between its read and its write (here: the account
// got listed in a market tier) must survive, and the last-used time still
// lands on the newer copy.
func TestSchedulerCacheUpdateLastUsedKeepsConcurrentWrite(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := &schedulerCache{rdb: rdb}

	// int64 values beyond float64 integer precision (2^53 + 1).
	const id = int64(9007199254740993)
	big := int64(9007199254740995)
	owner := big
	rate := 1.1
	base := &service.Account{ID: id, Platform: service.PlatformAnthropic, Status: service.StatusActive, Schedulable: true,
		OwnerUserID: &owner, GroupIDs: []int64{3}, RateMultiplier: &rate}
	require.NoError(t, cache.SetAccount(ctx, base))

	listed := *base
	listed.MarketGroupIDs = []int64{7}
	listed.MarketListing = &service.AccountMarketListing{GroupID: 7, OwnerUserID: owner, Version: 4}
	writes := 0
	cache.afterLastUsedRead = func() {
		if writes == 0 {
			writes++
			require.NoError(t, cache.SetAccount(ctx, &listed))
		}
	}

	used := time.Unix(1_800_000_000, 0)
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{id: used, 424242: used}))

	got, err := cache.GetAccount(ctx, id)
	require.NoError(t, err)
	require.Equal(t, []int64{7}, got.MarketGroupIDs, "the concurrent listing survives")
	require.NotNil(t, got.MarketListing)
	require.Equal(t, int64(4), got.MarketListing.Version)
	require.Equal(t, big, *got.OwnerUserID, "int64 fields keep their exact value")
	require.Equal(t, id, got.ID)
	require.InDelta(t, 1.1, *got.RateMultiplier, 0)
	require.NotNil(t, got.LastUsedAt)
	require.True(t, used.Equal(*got.LastUsedAt), "last used lands on the newer copy")

	missing, err := cache.GetAccount(ctx, 424242)
	require.NoError(t, err)
	require.Nil(t, missing, "a missing account key is never recreated")

	// LastUsedAt never moves backwards.
	cache.afterLastUsedRead = nil
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{id: used.Add(-time.Hour)}))
	got, err = cache.GetAccount(ctx, id)
	require.NoError(t, err)
	require.True(t, used.Equal(*got.LastUsedAt))
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{id: used.Add(time.Hour)}))
	got, err = cache.GetAccount(ctx, id)
	require.NoError(t, err)
	require.True(t, used.Add(time.Hour).Equal(*got.LastUsedAt))
	require.Equal(t, []int64{7}, got.MarketGroupIDs)
}

// A key deleted between the read and the write stays deleted.
func TestSchedulerCacheUpdateLastUsedDoesNotResurrectDeleted(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := &schedulerCache{rdb: rdb}
	const id = int64(31)
	require.NoError(t, cache.SetAccount(ctx, &service.Account{ID: id, Platform: service.PlatformAnthropic}))
	cache.afterLastUsedRead = func() { require.NoError(t, cache.DeleteAccount(ctx, id)) }
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{id: time.Unix(1_800_000_000, 0)}))
	got, err := cache.GetAccount(ctx, id)
	require.NoError(t, err)
	require.Nil(t, got)
}
