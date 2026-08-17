package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type claudeDeviceRepository struct {
	client *dbent.Client
	db     *sql.DB
}

func NewClaudeDeviceRepository(client *dbent.Client, db *sql.DB) service.ClaudeDeviceRepository {
	return &claudeDeviceRepository{client: client, db: db}
}

func (r *claudeDeviceRepository) CheckAndRegister(ctx context.Context, userID, groupID, apiKeyID int64, deviceHash, mode string, baseLimit int) (*service.ClaudeDeviceRegistrationResult, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("claude device repository unavailable")
	}
	if baseLimit <= 0 {
		baseLimit = 1
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	lockKey := fmt.Sprintf("%d:%d", userID, groupID)
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return nil, err
	}
	var bonus int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT bonus_devices FROM user_group_claude_device_quotas WHERE user_id=$1 AND group_id=$2), 0)`, userID, groupID).Scan(&bonus); err != nil {
		return nil, err
	}
	limit := baseLimit + bonus
	var existing bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM claude_user_devices WHERE user_id=$1 AND group_id=$2 AND device_hash=$3 AND revoked_at IS NULL)`, userID, groupID, deviceHash).Scan(&existing); err != nil {
		return nil, err
	}
	if existing {
		_, err := tx.ExecContext(ctx, `UPDATE claude_user_devices SET last_seen_at=NOW() WHERE user_id=$1 AND group_id=$2 AND device_hash=$3`, userID, groupID, deviceHash)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &service.ClaudeDeviceRegistrationResult{Allowed: true, Existing: true, EffectiveLimit: limit}, nil
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM claude_user_devices WHERE user_id=$1 AND group_id=$2 AND revoked_at IS NULL`, userID, groupID).Scan(&active); err != nil {
		return nil, err
	}
	over := active >= limit
	if over && strings.EqualFold(mode, "enforce") {
		_ = tx.Commit()
		return &service.ClaudeDeviceRegistrationResult{Allowed: false, ActiveDevices: active, EffectiveLimit: limit}, nil
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO claude_user_devices (user_id, group_id, device_hash, first_api_key_id, first_seen_at, last_seen_at, revoked_at)
		VALUES ($1,$2,$3,$4,NOW(),NOW(),NULL)
		ON CONFLICT (user_id,group_id,device_hash) DO UPDATE SET last_seen_at=NOW(), revoked_at=NULL`, userID, groupID, deviceHash, apiKeyID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &service.ClaudeDeviceRegistrationResult{Allowed: true, Registered: true, OverLimitAudit: over, ActiveDevices: active + 1, EffectiveLimit: limit}, nil
}

func (r *claudeDeviceRepository) AddBonusDevices(ctx context.Context, userID, groupID int64, count int) error {
	if count <= 0 {
		return fmt.Errorf("device count must be positive")
	}
	client := clientFromContext(ctx, r.client)
	_, err := client.ExecContext(ctx, `
		INSERT INTO user_group_claude_device_quotas (user_id, group_id, bonus_devices, created_at, updated_at)
		VALUES ($1,$2,$3,NOW(),NOW())
		ON CONFLICT (user_id,group_id) DO UPDATE SET bonus_devices=user_group_claude_device_quotas.bonus_devices + EXCLUDED.bonus_devices, updated_at=NOW()`, userID, groupID, count)
	return err
}

func (r *claudeDeviceRepository) ListUserDevices(ctx context.Context, userID int64, groupID *int64) ([]service.ClaudeUserDevice, error) {
	query := `SELECT id,user_id,group_id,device_hash,first_seen_at,last_seen_at,revoked_at FROM claude_user_devices WHERE user_id=$1`
	args := []any{userID}
	if groupID != nil {
		query += ` AND group_id=$2`
		args = append(args, *groupID)
	}
	query += ` ORDER BY last_seen_at DESC`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []service.ClaudeUserDevice
	for rows.Next() {
		var item service.ClaudeUserDevice
		var revoked sql.NullTime
		if err := rows.Scan(&item.ID, &item.UserID, &item.GroupID, &item.DeviceHash, &item.FirstSeenAt, &item.LastSeenAt, &revoked); err != nil {
			return nil, err
		}
		if revoked.Valid {
			value := revoked.Time
			item.RevokedAt = &value
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *claudeDeviceRepository) RevokeUserDevice(ctx context.Context, userID, deviceID int64) error {
	result, err := r.db.ExecContext(ctx, `UPDATE claude_user_devices SET revoked_at=NOW() WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, deviceID, userID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("claude device not found")
	}
	return nil
}
