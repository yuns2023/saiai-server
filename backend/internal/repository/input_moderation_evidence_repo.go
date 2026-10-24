package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Viewable evidence: unexpired, or held past its expiry. Cleanup deletes
// unheld evidence that expired or whose user is gone (soft or hard deleted),
// re-checking on the locked row so a committed hold wins.
const (
	moderationEvidenceViewable  = `(expires_at > NOW() OR hold_until > NOW())`
	moderationEvidenceUserGone  = `NOT EXISTS (SELECT 1 FROM input_moderation_events e JOIN users u ON u.id = e.user_id WHERE e.id = v.event_id AND u.deleted_at IS NULL)`
	moderationEvidencePurgeable = `(v.hold_until IS NULL OR v.hold_until < NOW()) AND (v.expires_at < NOW() OR ` + moderationEvidenceUserGone + `)`
)

func NewInputModerationEvidenceRepository(db *sql.DB) service.InputModerationEvidenceRepository {
	return &inputModerationRepository{db: db}
}

func (r *inputModerationRepository) LoadInputModerationEvidence(ctx context.Context, eventID int64) (*service.InputModerationEvidenceRecord, error) {
	record := &service.InputModerationEvidenceRecord{EventID: eventID}
	err := r.db.QueryRowContext(ctx, `SELECT job_id::text FROM input_moderation_events WHERE id = $1`, eventID).Scan(&record.JobID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load input moderation evidence event: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT check_index, kinds, aad, char_count, key_id, ciphertext, expires_at, hold_until
		FROM input_moderation_evidence
		WHERE event_id = $1 AND `+moderationEvidenceViewable+`
		ORDER BY check_index`, eventID)
	if err != nil {
		return nil, fmt.Errorf("load input moderation evidence: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var row service.InputModerationEvidenceRow
		var kinds, aad []byte
		var holdUntil sql.NullTime
		if err := rows.Scan(&row.CheckIndex, &kinds, &aad, &row.CharCount, &row.KeyID, &row.Ciphertext, &row.ExpiresAt, &holdUntil); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(kinds, &row.Kinds); err != nil {
			return nil, fmt.Errorf("decode evidence kinds: %w", err)
		}
		if err := json.Unmarshal(aad, &row.AAD); err != nil {
			return nil, fmt.Errorf("decode evidence context: %w", err)
		}
		if holdUntil.Valid {
			value := holdUntil.Time
			row.HoldUntil = &value
		}
		record.Rows = append(record.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(record.Rows) == 0 {
		return nil, nil
	}
	return record, nil
}

func insertModerationEvidenceAccess(ctx context.Context, db sqlExecutor, access service.InputModerationEvidenceAccess) error {
	if _, err := db.ExecContext(ctx, `
		INSERT INTO input_moderation_evidence_access (event_id, admin_user_id, auth_method, action, reason)
		VALUES ($1, $2, $3, $4, $5)`,
		access.EventID, access.AdminUserID, access.AuthMethod, access.Action, access.Reason); err != nil {
		return fmt.Errorf("record input moderation evidence access: %w", err)
	}
	return nil
}

// RecordInputModerationEvidenceAccess commits the access row and returns its
// database time, used to re-check expiry before anything is decrypted.
func (r *inputModerationRepository) RecordInputModerationEvidenceAccess(ctx context.Context, access service.InputModerationEvidenceAccess) (time.Time, error) {
	var at time.Time
	if err := r.db.QueryRowContext(ctx, `
		INSERT INTO input_moderation_evidence_access (event_id, admin_user_id, auth_method, action, reason)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING clock_timestamp()`,
		access.EventID, access.AdminUserID, access.AuthMethod, access.Action, access.Reason).Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("record input moderation evidence access: %w", err)
	}
	return at, nil
}

func (r *inputModerationRepository) updateModerationEvidenceHold(ctx context.Context, access service.InputModerationEvidenceAccess, after string, query string, args ...any) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("update input moderation evidence hold: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated == 0 {
		return 0, err
	}
	if after != "" {
		if _, err := tx.ExecContext(ctx, after, access.EventID); err != nil {
			return 0, fmt.Errorf("update input moderation evidence hold: %w", err)
		}
	}
	if err := insertModerationEvidenceAccess(ctx, tx, access); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return updated, nil
}

func (r *inputModerationRepository) HoldInputModerationEvidence(ctx context.Context, access service.InputModerationEvidenceAccess, until time.Time) (int64, error) {
	return r.updateModerationEvidenceHold(ctx, access, "", `
		UPDATE input_moderation_evidence SET hold_until = $2, hold_reason = $3, hold_by = $4
		WHERE event_id = $1 AND `+moderationEvidenceViewable,
		access.EventID, until, access.Reason, access.AdminUserID)
}

func (r *inputModerationRepository) ReleaseInputModerationEvidence(ctx context.Context, access service.InputModerationEvidenceAccess) (int64, error) {
	// Released evidence of a deleted user goes immediately, as it would have at
	// deletion time without the hold.
	return r.updateModerationEvidenceHold(ctx, access, `
		DELETE FROM input_moderation_evidence v WHERE v.event_id = $1 AND `+moderationEvidenceUserGone, `
		UPDATE input_moderation_evidence SET hold_until = NULL, hold_reason = NULL, hold_by = NULL
		WHERE event_id = $1 AND hold_until IS NOT NULL`, access.EventID)
}

func (r *inputModerationRepository) ListHeldInputModerationEvidence(ctx context.Context, page, size int) ([]service.HeldInputModerationEvidence, int64, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT event_id) FROM input_moderation_evidence WHERE hold_until > NOW()`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count held input moderation evidence: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT e.id, e.user_id, e.group_id, e.categories, e.action, e.created_at, COUNT(*), MAX(v.expires_at), MAX(v.hold_until),
			(array_agg(COALESCE(v.hold_reason, '') ORDER BY v.hold_until DESC))[1],
			(array_agg(COALESCE(v.hold_by, 0) ORDER BY v.hold_until DESC))[1]
		FROM input_moderation_evidence v
		JOIN input_moderation_events e ON e.id = v.event_id
		WHERE v.hold_until > NOW()
		GROUP BY e.id
		ORDER BY MAX(v.hold_until) DESC, e.id DESC
		LIMIT $1 OFFSET $2`, size, (page-1)*size)
	if err != nil {
		return nil, 0, fmt.Errorf("list held input moderation evidence: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.HeldInputModerationEvidence, 0)
	for rows.Next() {
		var item service.HeldInputModerationEvidence
		var categories []byte
		if err := rows.Scan(&item.EventID, &item.UserID, &item.GroupID, &categories, &item.Action, &item.EventCreatedAt,
			&item.EvidenceCount, &item.ExpiresAt, &item.HoldUntil, &item.HoldReason, &item.HoldBy); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(categories, &item.Categories); err != nil {
			return nil, 0, err
		}
		out = append(out, item)
	}
	return out, total, rows.Err()
}

func (r *inputModerationRepository) PurgeExpiredInputModerationEvidence(ctx context.Context, db service.SQLExecer, limit int) (int64, error) {
	result, err := db.ExecContext(ctx, `
		DELETE FROM input_moderation_evidence v
		WHERE v.id IN (
			SELECT v.id FROM input_moderation_evidence v
			WHERE `+moderationEvidencePurgeable+`
			ORDER BY v.id LIMIT $1
			FOR UPDATE OF v SKIP LOCKED)
		AND `+moderationEvidencePurgeable, limit)
	if err != nil {
		return 0, fmt.Errorf("purge input moderation evidence: %w", err)
	}
	return result.RowsAffected()
}

func (r *inputModerationRepository) PurgeInputModerationEvidenceAccess(ctx context.Context, db service.SQLExecer, before time.Time, limit int) (int64, error) {
	result, err := db.ExecContext(ctx, `
		DELETE FROM input_moderation_evidence_access
		WHERE id IN (SELECT id FROM input_moderation_evidence_access WHERE created_at < $1 ORDER BY id LIMIT $2)`, before, limit)
	if err != nil {
		return 0, fmt.Errorf("purge input moderation evidence access: %w", err)
	}
	return result.RowsAffected()
}

// deleteUserModerationEvidence removes a deleted user's evidence except rows
// under an active hold. It runs inside the soft-delete transaction.
func deleteUserModerationEvidence(ctx context.Context, db sqlExecutor, userID int64) error {
	if _, err := db.ExecContext(ctx, `
		DELETE FROM input_moderation_evidence v
		USING input_moderation_events e
		WHERE v.event_id = e.id AND e.user_id = $1 AND (v.hold_until IS NULL OR v.hold_until <= NOW())`, userID); err != nil {
		return fmt.Errorf("delete user input moderation evidence: %w", err)
	}
	return nil
}
