//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration125UserInvitationOwner(t *testing.T) {
	ctx := context.Background()

	// Re-running both files is a no-op (disaster-recovery replay).
	tx := testEntTx(t)
	columns, err := migrations.FS.ReadFile("125_user_invitation_owner.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(columns))
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())

	indexes, err := migrations.FS.ReadFile("126_user_invitation_owner_indexes_notx.sql")
	require.NoError(t, err)
	for _, stmt := range strings.Split(string(indexes), ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		_, err := integrationDB.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}

	for _, col := range []struct{ table, column string }{
		{"redeem_codes", "owner_user_id"},
		{"users", "invited_by_user_id"},
	} {
		var nullable string
		var def *string
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT is_nullable, column_default FROM information_schema.columns
			WHERE table_name = $1 AND column_name = $2`, col.table, col.column).Scan(&nullable, &def))
		require.Equal(t, "YES", nullable, col.column)
		require.Nil(t, def, "no default: metadata-only add")
	}

	for _, name := range []string{"idx_redeem_codes_owner_user_id", "idx_users_invited_by_user_id"} {
		var valid bool
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT i.indisvalid FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = $1`, name).Scan(&valid), name)
		require.True(t, valid, name)
	}
}
