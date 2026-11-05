package repository

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// Migration 120 must stay transactional and idempotent: a metadata-only column
// add with a constant default and a same-named range check.
func TestMigration120APIKeyMaxConcurrencyShape(t *testing.T) {
	const name = "120_api_key_max_concurrency.sql"
	data, err := migrations.FS.ReadFile(name)
	require.NoError(t, err)
	sql := string(data)
	require.Contains(t, sql, "SET LOCAL lock_timeout = '5s';")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS max_concurrency integer NOT NULL DEFAULT 5;")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS api_keys_max_concurrency_range;")
	require.Contains(t, sql, "CHECK (max_concurrency BETWEEN 1 AND 1000)")
	require.False(t, strings.HasSuffix(name, "_notx.sql"))
	require.NotContains(t, strings.ToUpper(sql), "CONCURRENTLY")
}
