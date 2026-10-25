//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration114CodexTimezoneGuardAppliesTwice(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `CREATE SCHEMA codex_tz_guard_migration_test; SET LOCAL search_path=codex_tz_guard_migration_test;
 CREATE TABLE groups(id BIGINT PRIMARY KEY); INSERT INTO groups VALUES(1);`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("114_group_codex_cn_timezone_guard.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	var guard bool
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT codex_cn_timezone_guard FROM groups WHERE id=1`).Scan(&guard))
	require.False(t, guard, "existing groups default to off")
	_, err = tx.ExecContext(ctx, `INSERT INTO groups(id) VALUES(2)`)
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT codex_cn_timezone_guard FROM groups WHERE id=2`).Scan(&guard))
	require.False(t, guard)
}
