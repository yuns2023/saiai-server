package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	ModerationEvidenceActionView    = "view"
	ModerationEvidenceActionHold    = "hold"
	ModerationEvidenceActionRelease = "release"

	// ModerationEvidenceAuthJWT is the only auth method allowed to read
	// evidence: shared admin API keys cannot attribute access to a person.
	ModerationEvidenceAuthJWT = "jwt"

	moderationEvidenceMaxHoldDays = 365
)

var (
	ErrModerationEvidenceForbidden     = infraerrors.Forbidden("MODERATION_EVIDENCE_PERSONAL_LOGIN_REQUIRED", "moderation evidence requires a personal administrator login")
	ErrModerationEvidenceNotFound      = infraerrors.NotFound("MODERATION_EVIDENCE_NOT_FOUND", "moderation evidence not found or expired")
	ErrModerationEvidenceInvalidHold   = infraerrors.BadRequest("MODERATION_EVIDENCE_INVALID_HOLD", "hold days must be between 1 and 365 and a reason is required")
	ErrModerationEvidenceUnavailable   = infraerrors.ServiceUnavailable("MODERATION_EVIDENCE_UNAVAILABLE", "moderation evidence is unavailable")
	ErrModerationEvidenceUndecryptable = infraerrors.InternalServer("MODERATION_EVIDENCE_UNDECRYPTABLE", "moderation evidence could not be decrypted with the configured keys")
)

// InputModerationEvidenceRow is one stored, still encrypted evidence row.
type InputModerationEvidenceRow struct {
	CheckIndex int
	Kinds      []string
	AAD        ModerationEvidenceAAD
	CharCount  int
	KeyID      string
	Ciphertext string
	ExpiresAt  time.Time
	HoldUntil  *time.Time
}

// InputModerationEvidenceRecord holds the viewable (unexpired or held) rows
// of one event.
type InputModerationEvidenceRecord struct {
	EventID int64
	JobID   string
	Rows    []InputModerationEvidenceRow
}

type InputModerationEvidenceAccess struct {
	EventID     int64
	AdminUserID int64
	AuthMethod  string
	Action      string
	Reason      string
}

// HeldInputModerationEvidence is event-level metadata only, so held evidence
// stays reachable even when its result row is gone.
type HeldInputModerationEvidence struct {
	EventID int64 `json:"event_id"`
	// Null once the user or group row is hard-deleted (ON DELETE SET NULL).
	UserID         *int64    `json:"user_id"`
	GroupID        *int64    `json:"group_id"`
	Categories     []string  `json:"categories"`
	Action         string    `json:"action"`
	EventCreatedAt time.Time `json:"event_created_at"`
	EvidenceCount  int       `json:"evidence_count"`
	ExpiresAt      time.Time `json:"expires_at"`
	HoldUntil      time.Time `json:"hold_until"`
	HoldReason     string    `json:"hold_reason"`
	HoldBy         int64     `json:"hold_by"`
}

type InputModerationEvidenceRepository interface {
	// LoadInputModerationEvidence returns nil when no row is viewable.
	LoadInputModerationEvidence(ctx context.Context, eventID int64) (*InputModerationEvidenceRecord, error)
	// RecordInputModerationEvidenceAccess commits the access row and returns
	// its database time.
	RecordInputModerationEvidenceAccess(ctx context.Context, access InputModerationEvidenceAccess) (time.Time, error)
	// Hold/Release update the viewable rows and write the access row in one
	// transaction; they return the number of updated rows.
	HoldInputModerationEvidence(ctx context.Context, access InputModerationEvidenceAccess, until time.Time) (int64, error)
	ReleaseInputModerationEvidence(ctx context.Context, access InputModerationEvidenceAccess) (int64, error)
	ListHeldInputModerationEvidence(ctx context.Context, page, size int) ([]HeldInputModerationEvidence, int64, error)
	// Purge statements run on db, the connection holding the cleanup lock.
	PurgeExpiredInputModerationEvidence(ctx context.Context, db SQLExecer, limit int) (int64, error)
	PurgeInputModerationEvidenceAccess(ctx context.Context, db SQLExecer, before time.Time, limit int) (int64, error)
}

// SQLExecer is satisfied by *sql.DB, *sql.Conn and *sql.Tx.
type SQLExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type InputModerationEvidenceCheck struct {
	CheckIndex int                          `json:"check_index"`
	Kinds      []string                     `json:"kinds"`
	Sources    []InputModerationCheckSource `json:"sources"`
	CharCount  int                          `json:"char_count"`
	Text       string                       `json:"text"`
	Scores     map[string]float64           `json:"scores"`
	Categories []string                     `json:"categories"`
}

type InputModerationEvidenceView struct {
	EventID           int64                          `json:"event_id"`
	Checks            []InputModerationEvidenceCheck `json:"checks"`
	Truncated         bool                           `json:"truncated"`
	OmittedCategories []string                       `json:"omitted_categories,omitempty"`
	ExpiresAt         time.Time                      `json:"expires_at"`
	HoldUntil         *time.Time                     `json:"hold_until"`
}

// InputModerationEvidenceActor identifies the administrator for the audit.
type InputModerationEvidenceActor struct {
	UserID     int64
	AuthMethod string
}

type InputModerationEvidenceService struct {
	repo   InputModerationEvidenceRepository
	crypto *moderationEvidenceCrypto
}

func NewInputModerationEvidenceService(repo InputModerationEvidenceRepository, cfg *config.Config) *InputModerationEvidenceService {
	return &InputModerationEvidenceService{repo: repo, crypto: moderationEvidenceCryptoFromConfig(cfg)}
}

func (s *InputModerationEvidenceService) authorize(actor InputModerationEvidenceActor) error {
	if s == nil || s.repo == nil {
		return ErrModerationEvidenceUnavailable
	}
	if actor.UserID <= 0 || actor.AuthMethod != ModerationEvidenceAuthJWT {
		return ErrModerationEvidenceForbidden
	}
	return nil
}

// View records the access and commits it before any ciphertext is decrypted,
// so text is never returned without an audit row.
func (s *InputModerationEvidenceService) View(ctx context.Context, eventID int64, actor InputModerationEvidenceActor) (*InputModerationEvidenceView, error) {
	if err := s.authorize(actor); err != nil {
		return nil, err
	}
	record, err := s.repo.LoadInputModerationEvidence(ctx, eventID)
	if err != nil {
		return nil, err
	}
	if record == nil || len(record.Rows) == 0 {
		return nil, ErrModerationEvidenceNotFound
	}
	if s.crypto == nil {
		return nil, ErrModerationEvidenceUnavailable
	}
	accessedAt, err := s.repo.RecordInputModerationEvidenceAccess(ctx, InputModerationEvidenceAccess{
		EventID: eventID, AdminUserID: actor.UserID, AuthMethod: actor.AuthMethod, Action: ModerationEvidenceActionView,
	})
	if err != nil {
		return nil, err
	}
	// Rows that expired while the audit committed are not returned.
	rows := record.Rows[:0:0]
	for _, row := range record.Rows {
		if row.ExpiresAt.After(accessedAt) || (row.HoldUntil != nil && row.HoldUntil.After(accessedAt)) {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil, ErrModerationEvidenceNotFound
	}
	record.Rows = rows
	view := &InputModerationEvidenceView{EventID: eventID}
	for _, row := range record.Rows {
		// The AAD binds the ciphertext to this event and position; a row moved
		// to another event or index fails here or in GCM authentication.
		if row.AAD.JobID != record.JobID || row.AAD.CheckIndex != row.CheckIndex || !reflect.DeepEqual(row.AAD.Kinds, row.Kinds) {
			slog.Error("input_moderation_evidence_context_mismatch", "event_id", eventID, "check_index", row.CheckIndex)
			return nil, ErrModerationEvidenceUndecryptable
		}
		plaintext, err := s.crypto.Open(row.KeyID, row.AAD, row.Ciphertext)
		if err != nil {
			slog.Error("input_moderation_evidence_decrypt_failed", "event_id", eventID, "check_index", row.CheckIndex, "key_id", row.KeyID, "error", err)
			return nil, ErrModerationEvidenceUndecryptable
		}
		var content moderationEvidencePlaintext
		if err := json.Unmarshal(plaintext, &content); err != nil {
			return nil, ErrModerationEvidenceUndecryptable
		}
		view.Truncated = view.Truncated || content.Truncated
		if len(content.OmittedCategories) > 0 {
			view.OmittedCategories = content.OmittedCategories
		}
		view.Checks = append(view.Checks, InputModerationEvidenceCheck{
			CheckIndex: row.CheckIndex, Kinds: row.Kinds, Sources: content.Sources, CharCount: row.CharCount,
			Text: content.Text, Scores: content.Scores, Categories: content.Categories,
		})
		if row.ExpiresAt.After(view.ExpiresAt) {
			view.ExpiresAt = row.ExpiresAt
		}
		if row.HoldUntil != nil && (view.HoldUntil == nil || row.HoldUntil.After(*view.HoldUntil)) {
			value := *row.HoldUntil
			view.HoldUntil = &value
		}
	}
	return view, nil
}

func (s *InputModerationEvidenceService) Hold(ctx context.Context, eventID int64, actor InputModerationEvidenceActor, days int, reason string) (time.Time, error) {
	if err := s.authorize(actor); err != nil {
		return time.Time{}, err
	}
	reason = strings.TrimSpace(reason)
	if days < 1 || days > moderationEvidenceMaxHoldDays || reason == "" {
		return time.Time{}, ErrModerationEvidenceInvalidHold
	}
	until := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
	updated, err := s.repo.HoldInputModerationEvidence(ctx, InputModerationEvidenceAccess{
		EventID: eventID, AdminUserID: actor.UserID, AuthMethod: actor.AuthMethod, Action: ModerationEvidenceActionHold, Reason: reason,
	}, until)
	if err != nil {
		return time.Time{}, err
	}
	if updated == 0 {
		return time.Time{}, ErrModerationEvidenceNotFound
	}
	return until, nil
}

func (s *InputModerationEvidenceService) Release(ctx context.Context, eventID int64, actor InputModerationEvidenceActor, reason string) error {
	if err := s.authorize(actor); err != nil {
		return err
	}
	updated, err := s.repo.ReleaseInputModerationEvidence(ctx, InputModerationEvidenceAccess{
		EventID: eventID, AdminUserID: actor.UserID, AuthMethod: actor.AuthMethod, Action: ModerationEvidenceActionRelease, Reason: strings.TrimSpace(reason),
	})
	if err != nil {
		return err
	}
	if updated == 0 {
		return ErrModerationEvidenceNotFound
	}
	return nil
}

func (s *InputModerationEvidenceService) ListHeld(ctx context.Context, actor InputModerationEvidenceActor, page, size int) ([]HeldInputModerationEvidence, int64, error) {
	if err := s.authorize(actor); err != nil {
		return nil, 0, err
	}
	return s.repo.ListHeldInputModerationEvidence(ctx, page, size)
}
