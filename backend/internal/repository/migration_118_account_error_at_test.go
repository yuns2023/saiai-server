package repository

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// Migration 118 must stay transactional and idempotent and must not backfill:
// accounts already in error keep error_at NULL ("time unknown").
func TestMigration118AccountErrorAtIsIdempotentWithoutBackfill(t *testing.T) {
	const name = "118_account_error_at.sql"
	data, err := migrations.FS.ReadFile(name)
	require.NoError(t, err)
	sql := string(data)
	require.Contains(t, sql, "SET LOCAL lock_timeout = '5s';")
	require.Contains(t, sql, "ALTER TABLE accounts ADD COLUMN IF NOT EXISTS error_at TIMESTAMPTZ;")
	require.Contains(t, sql, "CREATE OR REPLACE FUNCTION saiai_track_account_error_at()")
	require.Contains(t, sql, "DROP TRIGGER IF EXISTS accounts_track_error_at ON accounts;")
	require.False(t, strings.HasSuffix(name, "_notx.sql"))
	require.NotContains(t, strings.ToUpper(sql), "CONCURRENTLY")
	require.NotContains(t, sql, "UPDATE accounts")
}
