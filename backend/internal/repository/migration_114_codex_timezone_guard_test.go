package repository

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// Migration 114 must stay an idempotent, transactional, default-off column
// add so existing groups keep their behavior until an administrator opts in.
func TestMigration114CodexTimezoneGuardIsIdempotentAndDefaultOff(t *testing.T) {
	const name = "114_group_codex_cn_timezone_guard.sql"
	data, err := migrations.FS.ReadFile(name)
	require.NoError(t, err)
	sql := string(data)
	require.Contains(t, sql, "ALTER TABLE groups ADD COLUMN IF NOT EXISTS codex_cn_timezone_guard BOOLEAN NOT NULL DEFAULT FALSE;")
	require.False(t, strings.HasSuffix(name, "_notx.sql"))
	require.NotContains(t, strings.ToUpper(sql), "CONCURRENTLY")
	require.Equal(t, 1, strings.Count(strings.ToUpper(sql), "ALTER TABLE"), "no other schema change")
	require.False(t, group.DefaultCodexCnTimezoneGuard)
}
