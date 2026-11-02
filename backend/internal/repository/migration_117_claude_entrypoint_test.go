package repository

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// Migration 117 must stay transactional and idempotent, default every group
// to policy off, and add usage_logs.client_entrypoint as metadata-only in the
// final statement.
func TestMigration117ClaudeEntrypointIsIdempotentAndMetadataOnly(t *testing.T) {
	const name = "117_group_claude_entrypoint_policy.sql"
	data, err := migrations.FS.ReadFile(name)
	require.NoError(t, err)
	sql := string(data)
	require.Contains(t, sql, "SET LOCAL lock_timeout = '5s';")
	require.Contains(t, sql, "SET LOCAL statement_timeout = '60s';")
	require.Contains(t, sql, "ALTER TABLE groups ADD COLUMN IF NOT EXISTS claude_entrypoint_policy VARCHAR(16) NOT NULL DEFAULT 'off';")
	require.Contains(t, sql, "CHECK (claude_entrypoint_policy IN ('off', 'observe', 'enforce'))")
	require.Contains(t, sql, "CHECK (jsonb_typeof(claude_entrypoint_allowlist) = 'array')")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS group_claude_entrypoint_observations (")
	require.False(t, strings.HasSuffix(name, "_notx.sql"))
	require.NotContains(t, strings.ToUpper(sql), "CONCURRENTLY")
	require.Equal(t, "off", group.DefaultClaudeEntrypointPolicy)
	require.NotContains(t, sql, "UPDATE usage_logs")
	require.NotRegexp(t, `(?i)INDEX[^;]*ON usage_logs`, sql)
	statements := strings.Split(strings.TrimSpace(sql), ";")
	last := strings.TrimSpace(statements[len(statements)-2])
	require.True(t, strings.HasSuffix(last, "ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS client_entrypoint JSONB"), last)
	require.Equal(t, 1, strings.Count(sql, "ALTER TABLE usage_logs"))
}
