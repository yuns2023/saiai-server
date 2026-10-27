package repository

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// Migration 116 must stay transactional and idempotent, add the usage_logs
// column as metadata-only (nullable, no default, no backfill, no index) and
// default every existing group to policy off.
func TestMigration116ClientDevicesIsIdempotentAndMetadataOnly(t *testing.T) {
	const name = "116_client_devices_and_attestation.sql"
	data, err := migrations.FS.ReadFile(name)
	require.NoError(t, err)
	sql := string(data)
	require.Contains(t, sql, "SET LOCAL lock_timeout = '5s';")
	require.Contains(t, sql, "SET LOCAL statement_timeout = '60s';")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS client_devices (")
	require.Contains(t, sql, "UNIQUE (device_id)")
	require.Contains(t, sql, "UNIQUE (user_id, public_key_sha256)")
	require.Contains(t, sql, "ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS client_attestation SMALLINT;")
	require.NotContains(t, sql, "UPDATE usage_logs")
	require.NotRegexp(t, `(?i)INDEX[^;]*ON usage_logs`, sql)
	require.Contains(t, sql, "ALTER TABLE groups ADD COLUMN IF NOT EXISTS client_attestation_policy VARCHAR(16) NOT NULL DEFAULT 'off';")
	require.Contains(t, sql, "CHECK (client_attestation_policy IN ('off', 'observe', 'enforce'))")
	require.False(t, strings.HasSuffix(name, "_notx.sql"))
	require.NotContains(t, strings.ToUpper(sql), "CONCURRENTLY")
	require.Equal(t, "off", group.DefaultClientAttestationPolicy)
}
