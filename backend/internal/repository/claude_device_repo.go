package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type claudeDeviceRepository struct {
	client    *dbent.Client
	db        *sql.DB
	encryptor service.SecretEncryptor
}

func NewClaudeDeviceRepository(client *dbent.Client, db *sql.DB, encryptor service.SecretEncryptor) service.ClaudeDeviceRepository {
	return &claudeDeviceRepository{client: client, db: db, encryptor: encryptor}
}

func (r *claudeDeviceRepository) CheckAndRegister(ctx context.Context, userID, groupID, apiKeyID int64, deviceHash, deviceID, mode string, baseLimit int, hints service.ClientHints) (*service.ClaudeDeviceRegistrationResult, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("claude device repository unavailable")
	}
	if r.encryptor == nil {
		return nil, fmt.Errorf("claude device encryption unavailable")
	}
	if baseLimit <= 0 {
		baseLimit = 3
	}
	// Persist only the display headers, never authenticated ownership or credentials.
	hints.Owner = nil
	hintsJSON, err := json.Marshal(hints)
	if err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	lockKey := fmt.Sprintf("claude-user:%d", userID)
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return nil, err
	}
	var bonus int
	if err := tx.QueryRowContext(ctx, `SELECT claude_device_base_limit, COALESCE((SELECT bonus_devices FROM user_claude_device_quotas WHERE user_id=$1), 0) FROM users WHERE id=$1 AND deleted_at IS NULL`, userID).Scan(&baseLimit, &bonus); err != nil {
		return nil, err
	}
	limit := baseLimit + bonus
	var existing bool
	var existingID int64
	var existingEncrypted sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT id, device_id_encrypted FROM claude_user_devices WHERE user_id=$1 AND device_hash=$2 AND revoked_at IS NULL ORDER BY last_seen_at DESC LIMIT 1`, userID, deviceHash).Scan(&existingID, &existingEncrypted); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	existing = existingID > 0
	if existing {
		updates := `UPDATE claude_user_devices SET last_seen_at=NOW(), client_hints=client_hints || $2::jsonb WHERE id=$1`
		args := []any{existingID, string(hintsJSON)}
		if !existingEncrypted.Valid || strings.TrimSpace(existingEncrypted.String) == "" {
			encrypted, err := r.encryptor.Encrypt(deviceID)
			if err != nil {
				return nil, fmt.Errorf("encrypt Claude device ID: %w", err)
			}
			updates = `UPDATE claude_user_devices SET last_seen_at=NOW(), client_hints=client_hints || $2::jsonb, device_id_encrypted=$3 WHERE id=$1`
			args = append(args, encrypted)
		}
		_, err := tx.ExecContext(ctx, updates, args...)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &service.ClaudeDeviceRegistrationResult{Allowed: true, Existing: true, EffectiveLimit: limit}, nil
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT device_hash) FROM claude_user_devices WHERE user_id=$1 AND revoked_at IS NULL`, userID).Scan(&active); err != nil {
		return nil, err
	}
	over := active >= limit
	if over && strings.EqualFold(mode, "enforce") {
		_ = tx.Commit()
		return &service.ClaudeDeviceRegistrationResult{Allowed: false, ActiveDevices: active, EffectiveLimit: limit}, nil
	}
	encrypted, err := r.encryptor.Encrypt(deviceID)
	if err != nil {
		return nil, fmt.Errorf("encrypt Claude device ID: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO claude_user_devices (user_id, group_id, device_hash, device_id_encrypted, first_api_key_id, client_hints, first_seen_at, last_seen_at, revoked_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,NOW(),NOW(),NULL)
		ON CONFLICT (user_id,group_id,device_hash) DO UPDATE SET last_seen_at=NOW(), revoked_at=NULL, device_id_encrypted=EXCLUDED.device_id_encrypted, client_hints=claude_user_devices.client_hints || EXCLUDED.client_hints`, userID, groupID, deviceHash, encrypted, apiKeyID, string(hintsJSON))
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
 INSERT INTO user_claude_device_quotas (user_id, bonus_devices) VALUES ($1,$2)
 ON CONFLICT (user_id) DO UPDATE SET bonus_devices=user_claude_device_quotas.bonus_devices + EXCLUDED.bonus_devices, updated_at=NOW()`, userID, count)
	if err == nil {
		slog.Log(ctx, slog.LevelInfo, "claude_device_bonus_added", "user_id", userID, "group_id", groupID, "bonus_devices", count)
	}
	return err
}

func (r *claudeDeviceRepository) ListUserDevices(ctx context.Context, userID int64, groupID *int64) ([]service.ClaudeUserDevice, error) {
	query := `SELECT DISTINCT ON (device_hash) id,user_id,group_id,device_hash,device_id_encrypted,first_seen_at,last_seen_at,revoked_at,client_hints FROM claude_user_devices WHERE user_id=$1`
	args := []any{userID}
	if groupID != nil {
		query += ` AND group_id=$2`
		args = append(args, *groupID)
	}
	query += ` ORDER BY device_hash, (revoked_at IS NULL) DESC, last_seen_at DESC`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []service.ClaudeUserDevice
	for rows.Next() {
		var item service.ClaudeUserDevice
		var encrypted sql.NullString
		var revoked sql.NullTime
		var hintsJSON []byte
		if err := rows.Scan(&item.ID, &item.UserID, &item.GroupID, &item.DeviceHash, &encrypted, &item.FirstSeenAt, &item.LastSeenAt, &revoked, &hintsJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(hintsJSON, &item.ClientHints); err != nil {
			return nil, fmt.Errorf("decode Claude device client hints: %w", err)
		}
		if encrypted.Valid && strings.TrimSpace(encrypted.String) != "" {
			item.DeviceID, err = r.encryptor.Decrypt(encrypted.String)
			if err != nil {
				return nil, fmt.Errorf("decrypt Claude device ID: %w", err)
			}
		}
		if revoked.Valid {
			value := revoked.Time
			item.RevokedAt = &value
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListUserDeviceSummaries returns one aggregate per user for the admin user
// list. Distinct active devices and redeemed bonuses are scoped to the user.
func (r *claudeDeviceRepository) ListUserDeviceSummaries(ctx context.Context, userIDs []int64) (map[int64]service.ClaudeUserDeviceSummary, error) {
	out := make(map[int64]service.ClaudeUserDeviceSummary)
	if r == nil || r.db == nil || len(userIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, `
        SELECT u.id, (SELECT COUNT(DISTINCT device_hash) FROM claude_user_devices d WHERE d.user_id=u.id AND d.revoked_at IS NULL)::int,
        (u.claude_device_base_limit + COALESCE((SELECT bonus_devices FROM user_claude_device_quotas q WHERE q.user_id=u.id),0))::int, true
        FROM users u WHERE u.id=ANY($1) AND u.deleted_at IS NULL`, pq.Array(userIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var userID int64
		var active, limit int
		var hasLimit bool
		if err := rows.Scan(&userID, &active, &limit, &hasLimit); err != nil {
			return nil, err
		}
		summary := service.ClaudeUserDeviceSummary{ActiveDevices: active}
		if hasLimit {
			summary.EffectiveLimit = &limit
		}
		out[userID] = summary
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *claudeDeviceRepository) RevokeUserDevice(ctx context.Context, userID, deviceID int64) error {
	result, err := r.db.ExecContext(ctx, `UPDATE claude_user_devices SET revoked_at=NOW() WHERE user_id=$2 AND device_hash=(SELECT device_hash FROM claude_user_devices WHERE id=$1 AND user_id=$2) AND revoked_at IS NULL`, deviceID, userID)
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
	slog.Log(ctx, slog.LevelInfo, "claude_device_revoked", "user_id", userID, "device_registration_id", deviceID)
	return nil
}
