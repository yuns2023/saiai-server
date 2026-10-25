package repository

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// Migration 115 must stay an idempotent, transactional column add whose
// default keeps the batch-114 reject behavior for every existing group.
func TestMigration115CodexTimezoneActionIsIdempotentAndDefaultsToReject(t *testing.T) {
	const name = "115_group_codex_timezone_action.sql"
	data, err := migrations.FS.ReadFile(name)
	require.NoError(t, err)
	sql := string(data)
	require.Contains(t, sql, "SET LOCAL lock_timeout = '5s';")
	require.Contains(t, sql, "SET LOCAL statement_timeout = '60s';")
	require.Contains(t, sql, "ALTER TABLE groups ADD COLUMN IF NOT EXISTS codex_timezone_action VARCHAR(16) NOT NULL DEFAULT 'reject';")
	require.Contains(t, sql, "CHECK (codex_timezone_action IN ('reject', 'rewrite'))")
	require.Contains(t, sql, "IF NOT EXISTS (")
	require.False(t, strings.HasSuffix(name, "_notx.sql"))
	require.NotContains(t, strings.ToUpper(sql), "CONCURRENTLY")
	require.Equal(t, "reject", group.DefaultCodexTimezoneAction)
}
