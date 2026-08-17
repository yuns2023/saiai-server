package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const inputModerationMaxResponseBytes = 64 * 1024

type inputModerationRepository struct {
	db *sql.DB
}

func NewInputModerationRepository(db *sql.DB) service.InputModerationEventRepository {
	return &inputModerationRepository{db: db}
}

func (r *inputModerationRepository) InsertInputModerationEvent(ctx context.Context, event *service.InputModerationEvent) error {
	if r == nil || r.db == nil || event == nil {
		return fmt.Errorf("input moderation event repository is unavailable")
	}
	categories, err := json.Marshal(event.Categories)
	if err != nil {
		return fmt.Errorf("marshal categories: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO input_moderation_events (
			job_id, request_id, user_id, api_key_id, group_id, input_hash,
			safety, categories, action, model_version, policy_version, source, turn_number, created_at
		) VALUES ($1::uuid, NULLIF($2, ''), $3, $4, $5, $6, $7, $8::jsonb, $9, NULLIF($10, ''), NULLIF($11, ''), $12, NULLIF($13, 0), $14)
		ON CONFLICT (job_id) DO NOTHING`,
		event.JobID,
		event.RequestID,
		event.UserID,
		event.APIKeyID,
		event.GroupID,
		event.InputHash,
		event.Safety,
		string(categories),
		event.Action,
		event.ModelVersion,
		event.PolicyVersion,
		event.Source,
		event.TurnNumber,
		event.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert input moderation event: %w", err)
	}
	return nil
}

func (r *inputModerationRepository) ApplyInputModerationIncident(
	ctx context.Context,
	event *service.InputModerationEvent,
	policy service.InputModerationEscalationPolicy,
) (*service.InputModerationTransition, error) {
	if r == nil || r.db == nil || event == nil {
		return nil, fmt.Errorf("input moderation repository is unavailable")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin input moderation transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	categories, err := json.Marshal(event.Categories)
	if err != nil {
		return nil, fmt.Errorf("marshal categories: %w", err)
	}
	var eventID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO input_moderation_events (
			job_id, request_id, user_id, api_key_id, group_id, input_hash,
			safety, categories, action, model_version, policy_version, source,
			turn_number, counted_as_strike, created_at
		) VALUES ($1::uuid, NULLIF($2, ''), $3, $4, $5, $6, $7, $8::jsonb,
			'processing', NULLIF($9, ''), NULLIF($10, ''), $11, NULLIF($12, 0), false, $13)
		ON CONFLICT (job_id) DO NOTHING
		RETURNING id`,
		event.JobID, event.RequestID, event.UserID, event.APIKeyID, event.GroupID,
		event.InputHash, event.Safety, string(categories), event.ModelVersion,
		event.PolicyVersion, event.Source, event.TurnNumber, event.CreatedAt,
	).Scan(&eventID)
	if errors.Is(err, sql.ErrNoRows) {
		var action string
		if err := tx.QueryRowContext(ctx, `SELECT action FROM input_moderation_events WHERE job_id = $1::uuid`, event.JobID).Scan(&action); err != nil {
			return nil, fmt.Errorf("load idempotent input moderation result: %w", err)
		}
		transition := &service.InputModerationTransition{
			Action:       action,
			Duplicate:    true,
			UserDisabled: action == service.InputModerationActionAutoDisabled,
		}
		var blockedUntil sql.NullTime
		_ = tx.QueryRowContext(ctx, `SELECT strike_count, blocked_until FROM user_input_risk_states WHERE user_id = $1`, event.UserID).
			Scan(&transition.StrikeCount, &blockedUntil)
		if blockedUntil.Valid {
			value := blockedUntil.Time
			transition.BlockedUntil = &value
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return transition, nil
	}
	if err != nil {
		return nil, fmt.Errorf("insert input moderation incident: %w", err)
	}

	var role, status string
	var userUpdatedAt time.Time
	if err := tx.QueryRowContext(ctx, `
		SELECT role, status, updated_at
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE`, event.UserID).Scan(&role, &status, &userUpdatedAt); err != nil {
		return nil, fmt.Errorf("lock input moderation user: %w", err)
	}
	finishWithoutStrike := func(action string) (*service.InputModerationTransition, error) {
		if _, err := tx.ExecContext(ctx, `
			UPDATE input_moderation_events SET action = $2, counted_as_strike = false WHERE id = $1`,
			eventID, action); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &service.InputModerationTransition{Action: action}, nil
	}
	if role == service.RoleAdmin {
		return finishWithoutStrike(service.InputModerationActionIgnoredAdmin)
	}
	if status != service.StatusActive {
		return finishWithoutStrike(service.InputModerationActionAlreadyInactive)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_input_risk_states (user_id)
		VALUES ($1)
		ON CONFLICT (user_id) DO NOTHING`, event.UserID); err != nil {
		return nil, fmt.Errorf("initialize user input risk state: %w", err)
	}
	var strikeCount int
	var windowStart, blockedUntil, lastIncident, resetAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		SELECT strike_count, strike_window_started_at, blocked_until, last_incident_at, reset_at
		FROM user_input_risk_states
		WHERE user_id = $1
		FOR UPDATE`, event.UserID).Scan(&strikeCount, &windowStart, &blockedUntil, &lastIncident, &resetAt); err != nil {
		return nil, fmt.Errorf("lock user input risk state: %w", err)
	}
	if resetAt.Valid && !event.EnqueuedAt.IsZero() && event.EnqueuedAt.Before(resetAt.Time) {
		return finishWithoutStrike(service.InputModerationActionStaleUserState)
	}

	dedupeWindow := policy.DedupeWindow
	if dedupeWindow <= 0 {
		dedupeWindow = 5 * time.Minute
	}
	var duplicate bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM input_moderation_events
			WHERE user_id = $1 AND input_hash = $2 AND counted_as_strike = true
			  AND created_at >= $3 AND id <> $4
		)`, event.UserID, event.InputHash, event.CreatedAt.Add(-dedupeWindow), eventID).Scan(&duplicate); err != nil {
		return nil, fmt.Errorf("check input moderation incident duplicate: %w", err)
	}
	if duplicate {
		transition, err := finishWithoutStrike(service.InputModerationActionDuplicateIgnored)
		if transition != nil {
			transition.Duplicate = true
			transition.StrikeCount = strikeCount
			if blockedUntil.Valid {
				value := blockedUntil.Time
				transition.BlockedUntil = &value
			}
		}
		return transition, err
	}

	strikeWindow := policy.StrikeWindow
	if strikeWindow <= 0 {
		strikeWindow = 24 * time.Hour
	}
	now := event.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !windowStart.Valid || !now.Before(windowStart.Time.Add(strikeWindow)) {
		strikeCount = 0
		windowStart = sql.NullTime{Time: now, Valid: true}
	}
	strikeCount++
	disableAfterHits := policy.DisableAfterHits
	if disableAfterHits <= 0 {
		disableAfterHits = 2
	}
	actionMode := strings.ToLower(strings.TrimSpace(policy.ActionMode))
	shouldDisable := actionMode == service.InputModerationActionModeImmediateDisable || strikeCount >= disableAfterHits
	action := service.InputModerationActionCooldown
	var nextBlockedUntil *time.Time
	userDisabled := false
	if shouldDisable {
		result, err := tx.ExecContext(ctx, `
			UPDATE users SET status = 'disabled', updated_at = NOW()
			WHERE id = $1 AND status = 'active' AND role <> 'admin'`, event.UserID)
		if err != nil {
			return nil, fmt.Errorf("disable user after input moderation escalation: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if rows == 1 {
			action = service.InputModerationActionAutoDisabled
			userDisabled = true
		} else {
			action = service.InputModerationActionStaleUserState
		}
	} else {
		cooldown := policy.Cooldown
		if cooldown <= 0 {
			cooldown = 30 * time.Minute
		}
		value := now.Add(cooldown)
		nextBlockedUntil = &value
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE user_input_risk_states
		SET strike_count = $2,
		    strike_window_started_at = $3,
		    blocked_until = $4,
		    last_event_id = $5,
		    last_incident_at = $6,
		    updated_at = NOW()
		WHERE user_id = $1`,
		event.UserID, strikeCount, windowStart.Time, nextBlockedUntil, eventID, now); err != nil {
		return nil, fmt.Errorf("update user input risk state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE input_moderation_events SET action = $2, counted_as_strike = true WHERE id = $1`,
		eventID, action); err != nil {
		return nil, fmt.Errorf("finalize input moderation incident: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit input moderation transition: %w", err)
	}
	return &service.InputModerationTransition{
		Action:       action,
		StrikeCount:  strikeCount,
		BlockedUntil: nextBlockedUntil,
		UserDisabled: userDisabled,
	}, nil
}

func (r *inputModerationRepository) GetUserInputRiskState(ctx context.Context, userID int64) (*service.UserInputRiskState, error) {
	if r == nil || r.db == nil || userID <= 0 {
		return nil, nil
	}
	var state service.UserInputRiskState
	var windowStart, blockedUntil, lastIncident, resetAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT user_id, strike_count, strike_window_started_at, blocked_until, last_incident_at, reset_at, updated_at
		FROM user_input_risk_states
		WHERE user_id = $1`, userID).Scan(
		&state.UserID, &state.StrikeCount, &windowStart, &blockedUntil, &lastIncident, &resetAt, &state.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user input risk state: %w", err)
	}
	if windowStart.Valid {
		value := windowStart.Time
		state.StrikeWindowStartedAt = &value
	}
	if blockedUntil.Valid {
		value := blockedUntil.Time
		state.BlockedUntil = &value
	}
	if lastIncident.Valid {
		value := lastIncident.Time
		state.LastIncidentAt = &value
	}
	if resetAt.Valid {
		value := resetAt.Time
		state.ResetAt = &value
	}
	return &state, nil
}

func (r *inputModerationRepository) ResetUserInputRiskState(ctx context.Context, userID int64) error {
	if r == nil || r.db == nil || userID <= 0 {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO user_input_risk_states (user_id, strike_count, strike_window_started_at, blocked_until, last_event_id, last_incident_at, reset_at, updated_at)
		VALUES ($1, 0, NULL, NULL, NULL, NULL, NOW(), NOW())
		ON CONFLICT (user_id) DO UPDATE SET
			strike_count = 0,
			strike_window_started_at = NULL,
			blocked_until = NULL,
			last_event_id = NULL,
			last_incident_at = NULL,
			reset_at = NOW(),
			updated_at = NOW()`, userID)
	if err != nil {
		return fmt.Errorf("reset user input risk state: %w", err)
	}
	return nil
}

type inputModerationHTTPClient struct {
	endpoint string
	client   *http.Client
}

func NewInputModerationClient(cfg *config.Config) service.InputModerationClient {
	endpoint := ""
	timeout := 15 * time.Second
	if cfg != nil {
		endpoint = strings.TrimSpace(cfg.Gateway.InputModeration.Endpoint)
		if cfg.Gateway.InputModeration.RequestTimeoutSeconds > 0 {
			timeout = time.Duration(cfg.Gateway.InputModeration.RequestTimeoutSeconds) * time.Second
		}
	}
	return &inputModerationHTTPClient{
		endpoint: endpoint,
		client:   &http.Client{Timeout: timeout},
	}
}

type inputModerationRequest struct {
	Text string `json:"text"`
}

type inputModerationResponse struct {
	Safety       string   `json:"safety"`
	Categories   []string `json:"categories"`
	ModelVersion string   `json:"model_version"`
}

func (c *inputModerationHTTPClient) Classify(ctx context.Context, text string) (*service.InputModerationClassification, error) {
	if c == nil || c.client == nil || strings.TrimSpace(c.endpoint) == "" {
		return nil, fmt.Errorf("input moderation sidecar endpoint is not configured")
	}
	parsed, err := url.ParseRequestURI(c.endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid input moderation sidecar endpoint")
	}
	payload, err := json.Marshal(inputModerationRequest{Text: text})
	if err != nil {
		return nil, fmt.Errorf("marshal input moderation request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create input moderation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call input moderation sidecar: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, inputModerationMaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read input moderation response: %w", err)
	}
	if len(body) > inputModerationMaxResponseBytes {
		return nil, fmt.Errorf("input moderation response exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("input moderation sidecar returned status %d", resp.StatusCode)
	}
	var decoded inputModerationResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode input moderation response: %w", err)
	}
	if strings.TrimSpace(decoded.Safety) == "" {
		return nil, fmt.Errorf("input moderation sidecar omitted safety")
	}
	return &service.InputModerationClassification{
		Safety:       decoded.Safety,
		Categories:   decoded.Categories,
		ModelVersion: decoded.ModelVersion,
	}, nil
}
