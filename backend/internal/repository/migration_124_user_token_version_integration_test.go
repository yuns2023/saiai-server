//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Existing users start at version 0 (tokens issued before the migration stay
// valid); a password change and a moderation auto-disable increment it.
func TestMigration124UserTokenVersion(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)

	var nullable, def string
	rows, err := tx.QueryContext(ctx, `SELECT is_nullable, column_default FROM information_schema.columns
		WHERE table_name = 'users' AND column_name = 'token_version'`)
	require.NoError(t, err)
	require.True(t, rows.Next(), "users.token_version must exist")
	require.NoError(t, rows.Scan(&nullable, &def))
	require.NoError(t, rows.Close())
	require.Equal(t, "NO", nullable)
	require.Equal(t, "0", def)

	repo := newUserRepositoryWithSQL(tx.Client(), tx)
	u := &service.User{Email: "tokver-" + uuid.NewString() + "@example.com", Role: service.RoleUser, Status: service.StatusActive, Concurrency: 5}
	require.NoError(t, u.SetPassword("old-password"))
	require.NoError(t, repo.Create(ctx, u))
	loaded, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Zero(t, loaded.TokenVersion)

	require.NoError(t, loaded.SetPassword("new-password"))
	require.NoError(t, repo.Update(ctx, loaded))
	loaded, err = repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), loaded.TokenVersion)
	require.True(t, loaded.CheckPassword("new-password"))

	_, err = tx.ExecContext(ctx, `UPDATE users SET status = 'disabled', token_version = token_version + 1, updated_at = NOW()
		WHERE id = $1 AND status = 'active' AND role <> 'admin'`, u.ID)
	require.NoError(t, err)
	loaded, err = repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), loaded.TokenVersion)
}
