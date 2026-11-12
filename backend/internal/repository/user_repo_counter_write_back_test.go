package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Production: profile/password/admin saves wrote back the balance read before
// the save, so a charge or top-up landing in between was lost.
func TestUserRepositoryUpdate_DoesNotWriteBackCounters(t *testing.T) {
	ctx := context.Background()
	repo, _ := newUserRepoSQLite(t)
	u := createTokenVersionUser(t, repo)

	stale, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)

	// Concurrent atomic changes after the stale read.
	require.NoError(t, repo.UpdateBalance(ctx, u.ID, 12.5))
	require.NoError(t, repo.DeductBalance(ctx, u.ID, 2.5))
	require.NoError(t, repo.UpdateConcurrency(ctx, u.ID, 3))
	_, err = repo.client.User.UpdateOneID(u.ID).SetSoraStorageUsedBytes(4096).Save(ctx)
	require.NoError(t, err)

	stale.Username = "renamed"
	stale.Balance = 999            // ignored: balance has no save path
	stale.SoraStorageUsedBytes = 1 // ignored
	stale.Concurrency = 1          // ignored without the intent
	require.NoError(t, repo.Update(ctx, stale))

	got, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "renamed", got.Username)
	require.Equal(t, 10.0, got.Balance)
	require.Equal(t, 8, got.Concurrency)
	require.Equal(t, int64(4096), got.SoraStorageUsedBytes)

	// The saved object reports the stored counters, not the stale ones.
	require.Equal(t, 10.0, stale.Balance)
	require.Equal(t, 8, stale.Concurrency)
	require.Equal(t, int64(4096), stale.SoraStorageUsedBytes)
}

func TestUserRepositoryUpdate_ConcurrencyIntentWrites(t *testing.T) {
	ctx := context.Background()
	repo, _ := newUserRepoSQLite(t)
	u := createTokenVersionUser(t, repo)

	loaded, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	loaded.Concurrency = 9
	loaded.ConcurrencyChangedOnSave = true
	require.NoError(t, repo.Update(ctx, loaded))
	require.False(t, loaded.ConcurrencyChangedOnSave, "intent is spent by a successful save")

	got, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, 9, got.Concurrency)
}
