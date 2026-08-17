package repository

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestInputModerationHTTPClientClassify(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, map[string]any{"text": "用户输入"}, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"safety":"Unsafe","categories":["Jailbreak"],"model_version":"qwen-test"}`))
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Gateway.InputModeration.Endpoint = server.URL
	cfg.Gateway.InputModeration.RequestTimeoutSeconds = 1
	client := NewInputModerationClient(cfg)

	result, err := client.Classify(context.Background(), "用户输入")
	require.NoError(t, err)
	require.Equal(t, "Unsafe", result.Safety)
	require.Equal(t, []string{"Jailbreak"}, result.Categories)
	require.Equal(t, "qwen-test", result.ModelVersion)
}

func TestInputModerationHTTPClientRejectsInvalidEndpoint(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.InputModeration.Endpoint = "file:///tmp/moderation"
	client := NewInputModerationClient(cfg)

	_, err := client.Classify(context.Background(), "test")
	require.ErrorContains(t, err, "invalid input moderation sidecar endpoint")
}

func TestInsertInputModerationEventPersistsSourceAndTurn(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	createdAt := time.Unix(200, 0).UTC()
	mock.ExpectExec("INSERT INTO input_moderation_events").
		WithArgs(
			"00000000-0000-4000-8000-000000000001",
			"req-1",
			int64(1),
			int64(2),
			int64(3),
			"hash",
			service.InputModerationSafetyUnsafe,
			`["Jailbreak"]`,
			service.InputModerationActionAutoDisabled,
			"model-v1",
			"policy-v1",
			service.InputModerationSourceOpenAIResponsesWS,
			2,
			createdAt,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	repo := NewInputModerationRepository(db)
	err = repo.InsertInputModerationEvent(context.Background(), &service.InputModerationEvent{
		JobID:         "00000000-0000-4000-8000-000000000001",
		RequestID:     "req-1",
		UserID:        1,
		APIKeyID:      2,
		GroupID:       3,
		InputHash:     "hash",
		Safety:        service.InputModerationSafetyUnsafe,
		Categories:    []string{"Jailbreak"},
		Action:        service.InputModerationActionAutoDisabled,
		ModelVersion:  "model-v1",
		PolicyVersion: "policy-v1",
		Source:        service.InputModerationSourceOpenAIResponsesWS,
		TurnNumber:    2,
		CreatedAt:     createdAt,
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyInputModerationIncidentCooldownThenDisable(t *testing.T) {
	tests := []struct {
		name         string
		priorStrikes int
		windowStart  any
		wantAction   string
		wantStrikes  int
		wantDisabled bool
		wantCooldown bool
		actionMode   string
	}{
		{name: "first incident cools down", priorStrikes: 0, wantAction: service.InputModerationActionCooldown, wantStrikes: 1, wantCooldown: true, actionMode: service.InputModerationActionModeCooldownThenDisable},
		{name: "second incident disables", priorStrikes: 1, windowStart: time.Unix(900, 0).UTC(), wantAction: service.InputModerationActionAutoDisabled, wantStrikes: 2, wantDisabled: true, actionMode: service.InputModerationActionModeCooldownThenDisable},
		{name: "expired strike window resets", priorStrikes: 1, windowStart: time.Unix(1000, 0).UTC().Add(-25 * time.Hour), wantAction: service.InputModerationActionCooldown, wantStrikes: 1, wantCooldown: true, actionMode: service.InputModerationActionModeCooldownThenDisable},
		{name: "immediate mode disables first incident", priorStrikes: 0, wantAction: service.InputModerationActionAutoDisabled, wantStrikes: 1, wantDisabled: true, actionMode: service.InputModerationActionModeImmediateDisable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			now := time.Unix(1000, 0).UTC()

			mock.ExpectBegin()
			mock.ExpectQuery("INSERT INTO input_moderation_events").
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(11))
			mock.ExpectQuery("SELECT role, status, updated_at").
				WithArgs(int64(1)).
				WillReturnRows(sqlmock.NewRows([]string{"role", "status", "updated_at"}).AddRow(service.RoleUser, service.StatusActive, now.Add(-time.Minute)))
			mock.ExpectExec("INSERT INTO user_input_risk_states").
				WithArgs(int64(1)).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery("SELECT strike_count, strike_window_started_at, blocked_until, last_incident_at, reset_at").
				WithArgs(int64(1)).
				WillReturnRows(sqlmock.NewRows([]string{"strike_count", "strike_window_started_at", "blocked_until", "last_incident_at", "reset_at"}).AddRow(tt.priorStrikes, tt.windowStart, nil, nil, nil))
			mock.ExpectQuery("SELECT EXISTS").
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			if tt.wantDisabled {
				mock.ExpectExec("UPDATE users SET status = 'disabled'").
					WithArgs(int64(1)).
					WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectExec("UPDATE user_input_risk_states").
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("UPDATE input_moderation_events SET action").
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()

			repo := NewInputModerationRepository(db)
			transition, err := repo.ApplyInputModerationIncident(context.Background(), &service.InputModerationEvent{
				JobID: "00000000-0000-4000-8000-000000000001", UserID: 1, APIKeyID: 2, GroupID: 3,
				InputHash: "hash", Safety: service.InputModerationSafetyUnsafe,
				Categories: []string{"Jailbreak"}, Action: service.InputModerationActionAudit,
				Source: service.InputModerationSourceOpenAIResponsesHTTP, EnqueuedAt: now.Add(-time.Second), CreatedAt: now,
			}, service.InputModerationEscalationPolicy{
				ActionMode: tt.actionMode,
				Cooldown:   30 * time.Minute, DisableAfterHits: 2,
				StrikeWindow: 24 * time.Hour, DedupeWindow: 5 * time.Minute,
			})

			require.NoError(t, err)
			require.Equal(t, tt.wantAction, transition.Action)
			require.Equal(t, tt.wantStrikes, transition.StrikeCount)
			require.Equal(t, tt.wantDisabled, transition.UserDisabled)
			require.Equal(t, tt.wantCooldown, transition.BlockedUntil != nil)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestApplyInputModerationIncidentDeduplicatesSameContent(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	now := time.Unix(1000, 0).UTC()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO input_moderation_events").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(12))
	mock.ExpectQuery("SELECT role, status, updated_at").WillReturnRows(sqlmock.NewRows([]string{"role", "status", "updated_at"}).AddRow(service.RoleUser, service.StatusActive, now.Add(-time.Minute)))
	mock.ExpectExec("INSERT INTO user_input_risk_states").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT strike_count, strike_window_started_at, blocked_until, last_incident_at, reset_at").WillReturnRows(sqlmock.NewRows([]string{"strike_count", "strike_window_started_at", "blocked_until", "last_incident_at", "reset_at"}).AddRow(1, now.Add(-time.Hour), now.Add(29*time.Minute), now.Add(-time.Minute), nil))
	mock.ExpectQuery("SELECT EXISTS").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("UPDATE input_moderation_events SET action").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	repo := NewInputModerationRepository(db)
	transition, err := repo.ApplyInputModerationIncident(context.Background(), &service.InputModerationEvent{
		JobID: "00000000-0000-4000-8000-000000000002", UserID: 1, APIKeyID: 2, GroupID: 3,
		InputHash: "same-hash", Safety: service.InputModerationSafetyUnsafe,
		Source: service.InputModerationSourceOpenAIResponsesWS, EnqueuedAt: now.Add(-time.Second), CreatedAt: now,
	}, service.InputModerationEscalationPolicy{DedupeWindow: 5 * time.Minute})

	require.NoError(t, err)
	require.True(t, transition.Duplicate)
	require.Equal(t, 1, transition.StrikeCount)
	require.Equal(t, service.InputModerationActionDuplicateIgnored, transition.Action)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyInputModerationIncidentReplaysIdempotentResult(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	blockedUntil := time.Unix(2000, 0).UTC()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO input_moderation_events").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery("SELECT action FROM input_moderation_events").WillReturnRows(sqlmock.NewRows([]string{"action"}).AddRow(service.InputModerationActionCooldown))
	mock.ExpectQuery("SELECT strike_count, blocked_until FROM user_input_risk_states").WillReturnRows(sqlmock.NewRows([]string{"strike_count", "blocked_until"}).AddRow(1, blockedUntil))
	mock.ExpectCommit()

	repo := NewInputModerationRepository(db)
	transition, err := repo.ApplyInputModerationIncident(context.Background(), &service.InputModerationEvent{
		JobID: "00000000-0000-4000-8000-000000000003", UserID: 1, InputHash: "hash",
	}, service.InputModerationEscalationPolicy{})

	require.NoError(t, err)
	require.True(t, transition.Duplicate)
	require.Equal(t, service.InputModerationActionCooldown, transition.Action)
	require.Equal(t, 1, transition.StrikeCount)
	require.NotNil(t, transition.BlockedUntil)
	require.WithinDuration(t, blockedUntil, *transition.BlockedUntil, time.Second)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyInputModerationIncidentIgnoresJobQueuedBeforeManualReset(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	enqueuedAt := time.Unix(1000, 0).UTC()
	resetAt := enqueuedAt.Add(time.Minute)

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO input_moderation_events").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(13))
	mock.ExpectQuery("SELECT role, status, updated_at").WillReturnRows(sqlmock.NewRows([]string{"role", "status", "updated_at"}).AddRow(service.RoleUser, service.StatusActive, resetAt))
	mock.ExpectExec("INSERT INTO user_input_risk_states").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT strike_count, strike_window_started_at, blocked_until, last_incident_at, reset_at").WillReturnRows(sqlmock.NewRows([]string{"strike_count", "strike_window_started_at", "blocked_until", "last_incident_at", "reset_at"}).AddRow(0, nil, nil, nil, resetAt))
	mock.ExpectExec("UPDATE input_moderation_events SET action").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	repo := NewInputModerationRepository(db)
	transition, err := repo.ApplyInputModerationIncident(context.Background(), &service.InputModerationEvent{
		JobID: "00000000-0000-4000-8000-000000000004", UserID: 1, InputHash: "hash", EnqueuedAt: enqueuedAt, CreatedAt: resetAt.Add(time.Minute),
	}, service.InputModerationEscalationPolicy{})

	require.NoError(t, err)
	require.Equal(t, service.InputModerationActionStaleUserState, transition.Action)
	require.Zero(t, transition.StrikeCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInputModerationQueueMessagesExtractsEncryptedPayload(t *testing.T) {
	messages := inputModerationQueueMessages([]redis.XStream{{
		Stream: inputModerationQueueStream,
		Messages: []redis.XMessage{
			{ID: "1-0", Values: map[string]any{inputModerationPayloadKey: "cipher-one"}},
			{ID: "2-0", Values: map[string]any{inputModerationPayloadKey: []byte("cipher-two")}},
			{ID: "3-0", Values: map[string]any{}},
		},
	}})

	require.Equal(t, []service.InputModerationQueueMessage{
		{ID: "1-0", Ciphertext: "cipher-one"},
		{ID: "2-0", Ciphertext: "cipher-two"},
	}, messages)
}
