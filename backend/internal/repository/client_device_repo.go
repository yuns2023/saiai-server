package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

// clientDeviceRepository persists SAIAI client proof device keys
// (migration 116, docs/CLIENT_PROOF_V1.md).
type clientDeviceRepository struct {
	db *sql.DB
}

func NewClientDeviceRepository(db *sql.DB) service.ClientDeviceRepository {
	return &clientDeviceRepository{db: db}
}

const clientDeviceColumns = `id, device_id, user_id, first_api_key_id, public_key, public_key_sha256, client_version, os, arch,
	created_at, last_seen_at, COALESCE(last_ip, ''), last_claude_at, last_codex_at, revoked_at, revoked_by`

type rowScanner interface{ Scan(...any) error }

func scanClientDevice(row rowScanner) (*service.ClientDevice, error) {
	var (
		device       service.ClientDevice
		firstAPIKey  sql.NullInt64
		lastSeen     sql.NullTime
		lastClaude   sql.NullTime
		lastCodex    sql.NullTime
		revokedAt    sql.NullTime
		revokedBy    sql.NullInt64
		clientVer    string
		osName, arch string
	)
	if err := row.Scan(&device.ID, &device.DeviceID, &device.UserID, &firstAPIKey, &device.PublicKey, &device.PublicKeySHA256,
		&clientVer, &osName, &arch, &device.CreatedAt, &lastSeen, &device.LastIP, &lastClaude, &lastCodex, &revokedAt, &revokedBy); err != nil {
		return nil, err
	}
	device.ClientVersion, device.OS, device.Arch = clientVer, osName, arch
	if firstAPIKey.Valid {
		v := firstAPIKey.Int64
		device.FirstAPIKeyID = &v
	}
	device.LastSeenAt = nullTimePtr(lastSeen)
	device.LastClaudeAt = nullTimePtr(lastClaude)
	device.LastCodexAt = nullTimePtr(lastCodex)
	device.RevokedAt = nullTimePtr(revokedAt)
	if revokedBy.Valid {
		v := revokedBy.Int64
		device.RevokedBy = &v
	}
	return &device, nil
}

func nullTimePtr(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}

func (r *clientDeviceRepository) Enroll(ctx context.Context, input service.ClientDeviceEnrollInput, limit int) (*service.ClientDevice, service.ClientDeviceEnrollOutcome, error) {
	if r == nil || r.db == nil {
		return nil, 0, errors.New("client device repository unavailable")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize enrollments per user so the limit check and insert are atomic.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("client-device-user:%d", input.UserID)); err != nil {
		return nil, 0, err
	}
	existing, err := scanClientDevice(tx.QueryRowContext(ctx, `SELECT `+clientDeviceColumns+` FROM client_devices WHERE user_id=$1 AND public_key_sha256=$2`, input.UserID, input.PublicKeySHA256))
	switch {
	case err == nil:
		if existing.RevokedAt != nil {
			return existing, service.ClientDeviceEnrollRevoked, nil
		}
		// Keep the informational client metadata current.
		if _, err := tx.ExecContext(ctx, `UPDATE client_devices SET client_version=$2, os=$3, arch=$4 WHERE id=$1`,
			existing.ID, input.ClientVersion, input.OS, input.Arch); err != nil {
			return nil, 0, err
		}
		if err := tx.Commit(); err != nil {
			return nil, 0, err
		}
		existing.ClientVersion, existing.OS, existing.Arch = input.ClientVersion, input.OS, input.Arch
		return existing, service.ClientDeviceEnrollExisting, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, 0, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM client_devices WHERE user_id=$1 AND revoked_at IS NULL`, input.UserID).Scan(&active); err != nil {
		return nil, 0, err
	}
	if active >= limit {
		return nil, service.ClientDeviceEnrollLimit, nil
	}
	var firstAPIKey any
	if input.APIKeyID > 0 {
		firstAPIKey = input.APIKeyID
	}
	created, err := scanClientDevice(tx.QueryRowContext(ctx, `
		INSERT INTO client_devices (device_id, user_id, first_api_key_id, public_key, public_key_sha256, client_version, os, arch)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING `+clientDeviceColumns,
		input.DeviceID, input.UserID, firstAPIKey, input.PublicKey, input.PublicKeySHA256, input.ClientVersion, input.OS, input.Arch))
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return created, service.ClientDeviceEnrollCreated, nil
}

func (r *clientDeviceRepository) GetByDeviceID(ctx context.Context, deviceID string) (*service.ClientDevice, error) {
	device, err := scanClientDevice(r.db.QueryRowContext(ctx, `SELECT `+clientDeviceColumns+` FROM client_devices WHERE device_id=$1`, deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return device, err
}

func (r *clientDeviceRepository) ListByUser(ctx context.Context, userID int64) ([]service.ClientDevice, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+clientDeviceColumns+` FROM client_devices WHERE user_id=$1
		ORDER BY (revoked_at IS NULL) DESC, COALESCE(last_seen_at, created_at) DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ClientDevice, 0)
	for rows.Next() {
		device, err := scanClientDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *device)
	}
	return out, rows.Err()
}

func (r *clientDeviceRepository) Revoke(ctx context.Context, userID, id, revokedBy int64) (string, error) {
	var revokedByArg any
	if revokedBy > 0 {
		revokedByArg = revokedBy
	}
	var deviceID string
	err := r.db.QueryRowContext(ctx, `UPDATE client_devices SET revoked_at=NOW(), revoked_by=$3
		WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL RETURNING device_id`, id, userID, revokedByArg).Scan(&deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", service.ErrClientDeviceNotFound
	}
	return deviceID, err
}

func (r *clientDeviceRepository) Touch(ctx context.Context, id int64, touch service.ClientDeviceTouch) error {
	var lastIP any
	if touch.IP != "" {
		lastIP = touch.IP
	}
	_, err := r.db.ExecContext(ctx, `UPDATE client_devices SET
		last_seen_at = CASE WHEN $2 THEN NOW() ELSE last_seen_at END,
		last_ip = CASE WHEN $2 AND $3::text IS NOT NULL THEN $3::text ELSE last_ip END,
		last_claude_at = CASE WHEN $4 THEN NOW() ELSE last_claude_at END,
		last_codex_at = CASE WHEN $5 THEN NOW() ELSE last_codex_at END
		WHERE id=$1`, id, touch.Seen, lastIP, touch.Claude, touch.Codex)
	return err
}

func (r *clientDeviceRepository) CountActiveCodexDevices(ctx context.Context, userIDs []int64, since time.Time) (map[int64]int, error) {
	out := make(map[int64]int, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT user_id, COUNT(*)::int FROM client_devices
		WHERE user_id = ANY($1) AND revoked_at IS NULL AND last_codex_at >= $2 GROUP BY user_id`, pq.Array(userIDs), since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var userID int64
		var count int
		if err := rows.Scan(&userID, &count); err != nil {
			return nil, err
		}
		out[userID] = count
	}
	return out, rows.Err()
}

// CountClientAttestations aggregates usage_logs over the page's users. It is
// served by idx_usage_logs_user_created (user_id, created_at).
func (r *clientDeviceRepository) CountClientAttestations(ctx context.Context, userIDs []int64, since time.Time) (map[int64]map[int16]int64, error) {
	out := make(map[int64]map[int16]int64, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT user_id, client_attestation, COUNT(*) FROM usage_logs
		WHERE user_id = ANY($1) AND created_at >= $2 AND client_attestation IS NOT NULL
		GROUP BY user_id, client_attestation`, pq.Array(userIDs), since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var userID, count int64
		var status int16
		if err := rows.Scan(&userID, &status, &count); err != nil {
			return nil, err
		}
		if out[userID] == nil {
			out[userID] = make(map[int16]int64)
		}
		out[userID][status] = count
	}
	return out, rows.Err()
}

const clientProofNonceKeyPrefix = "client_proof:nonce:"

type clientProofNonceStore struct {
	rdb *redis.Client
}

// NewClientProofNonceStore stores seen proof nonces in Redis (SET NX EX).
func NewClientProofNonceStore(rdb *redis.Client) service.ClientProofNonceStore {
	return &clientProofNonceStore{rdb: rdb}
}

// Claim honors the caller's deadline even though the shared client keeps
// ContextTimeoutEnabled off (socket reads then wait for ReadTimeout): the
// command runs in a goroutine bounded by that ReadTimeout and the caller
// stops waiting at its own deadline. A late reply is discarded; the nonce
// may then already be stored, which only turns a later reuse into replay.
func (s *clientProofNonceStore) Claim(ctx context.Context, deviceID, nonce string, ttl time.Duration) (bool, error) {
	if s == nil || s.rdb == nil {
		return false, errors.New("client proof nonce store unavailable")
	}
	type result struct {
		fresh bool
		err   error
	}
	done := make(chan result, 1)
	go func() {
		fresh, err := s.rdb.SetNX(context.WithoutCancel(ctx), clientProofNonceKeyPrefix+deviceID+":"+nonce, 1, ttl).Result()
		done <- result{fresh: fresh, err: err}
	}()
	select {
	case r := <-done:
		return r.fresh, r.err
	case <-ctx.Done():
		return false, ctx.Err()
	}
}
