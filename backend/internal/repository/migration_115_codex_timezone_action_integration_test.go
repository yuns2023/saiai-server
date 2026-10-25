//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration115CodexTimezoneActionAppliesTwice(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `CREATE SCHEMA codex_tz_action_migration_test; SET LOCAL search_path=codex_tz_action_migration_test;
 CREATE TABLE groups(id BIGINT PRIMARY KEY); INSERT INTO groups VALUES(1);`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("115_group_codex_timezone_action.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	var action string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT codex_timezone_action FROM groups WHERE id=1`).Scan(&action))
	require.Equal(t, "reject", action, "existing groups keep rejecting")
	_, err = tx.ExecContext(ctx, `UPDATE groups SET codex_timezone_action='rewrite' WHERE id=1`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SAVEPOINT invalid_action`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE groups SET codex_timezone_action='allow' WHERE id=1`)
	require.Error(t, err, "CHECK constraint rejects unknown actions")
	_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT invalid_action`)
	require.NoError(t, err)
}
