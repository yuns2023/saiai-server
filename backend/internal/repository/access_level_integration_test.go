//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccessLevelBalanceModes(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()

	var baseID int64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT id FROM access_levels ORDER BY rank ASC, id ASC LIMIT 1`).Scan(&baseID))

	var middleID, highID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
		INSERT INTO access_levels (name, rank, balance_threshold, payg_discount_multiplier)
		VALUES ('integration-middle', 1000, 10, 0.9)
		RETURNING id
	`).Scan(&middleID))
	require.NoError(t, tx.QueryRowContext(ctx, `
		INSERT INTO access_levels (name, rank, balance_threshold, payg_discount_multiplier)
		VALUES ('integration-high', 1001, 20, 0.8)
		RETURNING id
	`).Scan(&highID))

	_, err := tx.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at)
		VALUES ('access_level_balance_maintenance_enabled', 'false', NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()
	`)
	require.NoError(t, err)

	var userID, autoLevelID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
		INSERT INTO users (email, password_hash, role, balance, concurrency, status, username, notes)
		VALUES ($1, 'test-hash', 'user', 0, 1, 'active', '', '')
		RETURNING id, auto_level_id
	`, fmt.Sprintf("access-level-%d@example.test", time.Now().UnixNano())).Scan(&userID, &autoLevelID))
	require.Equal(t, baseID, autoLevelID)

	// Growth mode promotes on balance increases and never demotes on spending.
	require.NoError(t, tx.QueryRowContext(ctx, `UPDATE users SET balance = 15 WHERE id = $1 RETURNING auto_level_id`, userID).Scan(&autoLevelID))
	require.Equal(t, middleID, autoLevelID)
	require.NoError(t, tx.QueryRowContext(ctx, `UPDATE users SET balance = 5 WHERE id = $1 RETURNING auto_level_id`, userID).Scan(&autoLevelID))
	require.Equal(t, middleID, autoLevelID)

	// Maintenance mode applies both promotions and demotions using current balance.
	_, err = tx.ExecContext(ctx, `UPDATE settings SET value = 'true', updated_at = NOW() WHERE key = 'access_level_balance_maintenance_enabled'`)
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `UPDATE users SET balance = balance WHERE id = $1 RETURNING auto_level_id`, userID).Scan(&autoLevelID))
	require.Equal(t, baseID, autoLevelID)
	require.NoError(t, tx.QueryRowContext(ctx, `UPDATE users SET balance = 25 WHERE id = $1 RETURNING auto_level_id`, userID).Scan(&autoLevelID))
	require.Equal(t, highID, autoLevelID)
	require.NoError(t, tx.QueryRowContext(ctx, `UPDATE users SET balance = 15 WHERE id = $1 RETURNING auto_level_id`, userID).Scan(&autoLevelID))
	require.Equal(t, middleID, autoLevelID)

	var eventCount int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_access_level_events WHERE user_id = $1`, userID).Scan(&eventCount))
	require.GreaterOrEqual(t, eventCount, 4)
}
