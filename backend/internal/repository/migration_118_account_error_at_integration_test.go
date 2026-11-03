//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration118AccountErrorAtTrigger(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `CREATE SCHEMA account_error_at_migration_test; SET LOCAL search_path=account_error_at_migration_test;
 CREATE TABLE accounts(id BIGINT PRIMARY KEY, status VARCHAR(20) NOT NULL, error_message TEXT, priority INT NOT NULL DEFAULT 1);
 INSERT INTO accounts(id, status, error_message) VALUES (1, 'error', 'old'), (2, 'active', NULL);`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("118_account_error_at.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}

	errorAt := func(id int64) sql.NullTime {
		var v sql.NullTime
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT error_at FROM accounts WHERE id=$1`, id).Scan(&v))
		return v
	}

	require.False(t, errorAt(1).Valid, "no backfill for accounts already in error")
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET status='error', error_message='again' WHERE id=1`)
	require.NoError(t, err)
	require.False(t, errorAt(1).Valid, "repeated error on a pre-migration account stays unknown")

	// active -> error stamps the time.
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET status='error', error_message='401' WHERE id=2`)
	require.NoError(t, err)
	first := errorAt(2)
	require.True(t, first.Valid)

	// Repeated error and full-row writes (admin update re-sends status + a stale error_at) keep the first time.
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET status='error', error_message='401 again', error_at=$1 WHERE id=2`, time.Unix(0, 0))
	require.NoError(t, err)
	require.Equal(t, first.Time, errorAt(2).Time)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET priority=5 WHERE id=2`)
	require.NoError(t, err)
	require.Equal(t, first.Time, errorAt(2).Time)

	// Leaving error clears it; a direct write while active is ignored.
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET status='active', error_message='' WHERE id=2`)
	require.NoError(t, err)
	require.False(t, errorAt(2).Valid)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET error_at=NOW() WHERE id=2`)
	require.NoError(t, err)
	require.False(t, errorAt(2).Valid)

	// Inserting an account directly in error stamps the time.
	_, err = tx.ExecContext(ctx, `INSERT INTO accounts(id, status) VALUES (3, 'error')`)
	require.NoError(t, err)
	require.True(t, errorAt(3).Valid)
}
