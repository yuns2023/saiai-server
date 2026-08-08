//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

var paymentMigrationDatabaseSequence uint64

func TestNativePaymentMigrations_UpgradeFrom084PreservesDataAndEnforcesContracts(t *testing.T) {
	ctx := context.Background()
	db := openIsolatedPaymentMigrationDatabase(t)

	legacyFS := migrationsThrough084(t)
	require.NoError(t, applyMigrationsFS(ctx, db, legacyFS))
	var beforeCount int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&beforeCount))

	var userID int64
	require.NoError(t, db.QueryRowContext(ctx, `
		INSERT INTO users (email, password_hash, balance)
		VALUES ('payment-upgrade@example.test', 'test-only-hash', 42.125)
		RETURNING id
	`).Scan(&userID))
	_, err := db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES
			('purchase_subscription_enabled', 'true', NOW()),
			('purchase_subscription_url', 'https://legacy-pay.example.test', NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()
	`)
	require.NoError(t, err)

	require.NoError(t, applyMigrationsFS(ctx, db, migrations.FS))
	require.NoError(t, applyMigrationsFS(ctx, db, migrations.FS), "new payment migrations must be idempotent")

	var afterCount int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&afterCount))
	require.Equal(t, beforeCount+2, afterCount, "upgrade from 084 should apply exactly 085 and 086")

	var balance float64
	require.NoError(t, db.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", userID).Scan(&balance))
	require.InDelta(t, 42.125, balance, 0.00000001, "existing balance must be preserved")
	requireSettingValue(t, db, "payment_enabled", "false")
	requireSettingValue(t, db, "purchase_subscription_enabled", "false")
	requireSettingValue(t, db, "purchase_subscription_url", "https://legacy-pay.example.test")

	var providerID int64
	require.NoError(t, db.QueryRowContext(ctx, `
		INSERT INTO payment_provider_instances
			(provider_key, name, config_encrypted, supported_types, balance_credit_rate, enabled)
		VALUES ('mock', 'Migration mock', 'TEST_ONLY_CIPHERTEXT', 'alipay', 0.2, false)
		RETURNING id
	`).Scan(&providerID))
	var orderID int64
	require.NoError(t, db.QueryRowContext(ctx, `
		INSERT INTO payment_orders
			(user_id, user_email, amount, pay_amount, currency, balance_credit_rate,
			 recharge_code, order_type, out_trade_no, payment_type, provider_key,
			 provider_instance_id, provider_snapshot_encrypted, status, expires_at)
		VALUES
			($1, 'payment-upgrade@example.test', 10, 50, 'CNY', 0.2,
			 'PAY-MIGRATION-1', 'balance', 'SA-MIGRATION-1', 'alipay', 'mock',
			 $2, 'TEST_ONLY_ORDER_CIPHERTEXT', 'COMPLETED', NOW() + INTERVAL '1 hour')
		RETURNING id
	`, userID, providerID).Scan(&orderID))

	_, err = db.ExecContext(ctx, "UPDATE payment_orders SET status = 'UNKNOWN' WHERE id = $1", orderID)
	require.Error(t, err, "unknown payment status must be rejected")
	_, err = db.ExecContext(ctx, "UPDATE payment_orders SET refund_mode = 'unsafe' WHERE id = $1", orderID)
	require.Error(t, err, "unknown refund mode must be rejected")
	_, err = db.ExecContext(ctx, "UPDATE payment_orders SET refund_amount = -1 WHERE id = $1", orderID)
	require.Error(t, err, "negative refund amount must be rejected")
	_, err = db.ExecContext(ctx, "UPDATE payment_orders SET order_type = 'subscription' WHERE id = $1", orderID)
	require.Error(t, err, "subscription orders without an immutable entitlement snapshot must be rejected")
	_, err = db.ExecContext(ctx, "DELETE FROM payment_provider_instances WHERE id = $1", providerID)
	require.Error(t, err, "provider snapshots with order history must be retained")
}

func migrationsThrough084(t *testing.T) fstest.MapFS {
	t.Helper()
	files, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)
	result := make(fstest.MapFS)
	for _, name := range files {
		if strings.HasPrefix(name, "085_") || strings.HasPrefix(name, "086_") {
			continue
		}
		data, readErr := fs.ReadFile(migrations.FS, name)
		require.NoError(t, readErr)
		result[name] = &fstest.MapFile{Data: data}
	}
	return result
}

func openIsolatedPaymentMigrationDatabase(t *testing.T) *sql.DB {
	t.Helper()
	sequence := atomic.AddUint64(&paymentMigrationDatabaseSequence, 1)
	databaseName := fmt.Sprintf("payment_migration_%d_%d", time.Now().UnixNano(), sequence)
	_, err := integrationDB.ExecContext(context.Background(), `CREATE DATABASE "`+databaseName+`"`)
	require.NoError(t, err)

	parsed, err := url.Parse(integrationPostgresDSN)
	require.NoError(t, err)
	parsed.Path = "/" + databaseName
	db, err := sql.Open("postgres", parsed.String())
	require.NoError(t, err)
	require.NoError(t, pingWithTimeout(context.Background(), db, 10*time.Second))
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = integrationDB.ExecContext(context.Background(), `
			SELECT pg_terminate_backend(pid)
			FROM pg_stat_activity
			WHERE datname = $1 AND pid <> pg_backend_pid()
		`, databaseName)
		_, _ = integrationDB.ExecContext(context.Background(), `DROP DATABASE IF EXISTS "`+databaseName+`"`)
	})
	return db
}

func requireSettingValue(t *testing.T, db *sql.DB, key, expected string) {
	t.Helper()
	var actual string
	require.NoError(t, db.QueryRowContext(context.Background(), "SELECT value FROM settings WHERE key = $1", key).Scan(&actual))
	require.Equal(t, expected, actual, "setting %s", key)
}
