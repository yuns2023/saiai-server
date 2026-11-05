//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Keys default to 5, the auth read carries the value, and the database
// rejects values outside 1..1000.
func TestMigration120APIKeyMaxConcurrency(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAPIKeyRepositoryWithSQL(tx.Client(), tx)
	u := mustCreateUser(t, tx.Client(), &service.User{Email: "keyconc-" + uuid.NewString() + "@example.com", Concurrency: 3})

	unset := &service.APIKey{UserID: u.ID, Key: "sk-keyconc-" + uuid.NewString(), Name: "unset", Status: service.StatusActive}
	require.NoError(t, repo.Create(ctx, unset))
	loaded, err := repo.GetByID(ctx, unset.ID)
	require.NoError(t, err)
	require.Equal(t, 5, loaded.MaxConcurrency, "column default")

	set := &service.APIKey{UserID: u.ID, Key: "sk-keyconc-" + uuid.NewString(), Name: "set", Status: service.StatusActive, MaxConcurrency: 12}
	require.NoError(t, repo.Create(ctx, set))
	auth, err := repo.GetByKeyForAuth(ctx, set.Key)
	require.NoError(t, err)
	require.Equal(t, 12, auth.MaxConcurrency, "auth query must select max_concurrency")

	full, err := repo.GetByID(ctx, set.ID)
	require.NoError(t, err)
	full.MaxConcurrency = 1000
	require.NoError(t, repo.Update(ctx, full))
	loaded, err = repo.GetByID(ctx, set.ID)
	require.NoError(t, err)
	require.Equal(t, 1000, loaded.MaxConcurrency)

	for _, v := range []int{0, -1, 1001} {
		_, err = tx.ExecContext(ctx, "SAVEPOINT p")
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, "UPDATE api_keys SET max_concurrency=$1 WHERE id=$2", v, set.ID)
		require.ErrorContains(t, err, "api_keys_max_concurrency_range", v)
		_, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT p")
		require.NoError(t, err)
	}
}
