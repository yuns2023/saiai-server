package repository

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// Migration 119 must stay transactional and re-create the same-named check with
// only the platform list widened.
func TestMigration119HostedUsageAnthropicReplacesCheck(t *testing.T) {
	const name = "119_hosted_usage_anthropic.sql"
	data, err := migrations.FS.ReadFile(name)
	require.NoError(t, err)
	sql := string(data)
	require.Contains(t, sql, "SET LOCAL lock_timeout = '5s';")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS groups_hosted_usage_policy;")
	require.Contains(t, sql, "ADD CONSTRAINT groups_hosted_usage_policy CHECK (")
	require.Contains(t, sql, "platform IN ('openai', 'anthropic') AND is_exclusive AND subscription_type = 'standard' AND rate_multiplier = 0")
	require.False(t, strings.HasSuffix(name, "_notx.sql"))
	require.NotContains(t, strings.ToUpper(sql), "CONCURRENTLY")
}
