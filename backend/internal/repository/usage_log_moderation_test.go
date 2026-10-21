package repository

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"regexp"
	"testing"
	"time"
)

func TestUsageModerationCorrelationInUsageInsert(t *testing.T) {
	ref := &service.InputModerationUsageRef{RequestID: "local-id", Source: "openai_responses_ws", TurnNumber: 2}
	log := &service.UsageLog{RequestID: "billing-id", ModerationRef: ref}
	prepared := prepareUsageLogInsert(log)
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
	body, ok := prepared.args[len(prepared.args)-2].(string)
	require.True(t, ok)
	require.JSONEq(t, `{"RequestID":"local-id","Source":"openai_responses_ws","TurnNumber":2}`, body)
	require.Nil(t, moderationRefJSON(nil))
}

func TestUsageModerationEnrichmentFailureIsolated(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &usageLogRepository{sql: db}
	mock.ExpectQuery("SELECT DISTINCT u.id").WillReturnError(context.DeadlineExceeded)
	logs := []service.UsageLog{{ID: 7, ModerationEvents: []service.UsageModerationEvent{{Action: "warning"}}}}
	repo.enrichUsageLogModeration(context.Background(), logs)
	require.True(t, logs[0].ModerationUnavailable)
	require.Empty(t, logs[0].ModerationEvents)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageModerationHydrationScopesIdentityAndTurn(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &usageLogRepository{sql: db}
	// Require all identity boundaries in the query, not just request_id.
	query := `SELECT DISTINCT u.id, e.id, e.categories, e.scores, e.min_confidence,
 e.action, e.counted_as_strike, COALESCE(e.model_version,'')
 FROM usage_logs u JOIN input_moderation_events e
 ON e.request_id=u.moderation_ref->>'RequestID' AND e.source=u.moderation_ref->>'Source'
 AND COALESCE(e.turn_number,0)::text=u.moderation_ref->>'TurnNumber'
 AND e.user_id=u.user_id AND e.api_key_id=u.api_key_id AND e.group_id=u.group_id
 WHERE u.id=ANY($1) ORDER BY u.id,e.id`
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs("{7,8}").WillReturnRows(sqlmock.NewRows([]string{"id", "event_id", "categories", "scores", "min_confidence", "action", "counted_as_strike", "model_version"}).AddRow(8, 20, `["PII"]`, `{"PII":0.95}`, 90, "warning", true, "jev-1.13.0"))
	mock.ExpectQuery("SELECT u.id,r.status").WithArgs("{7,8}").WillReturnRows(sqlmock.NewRows([]string{"id", "status", "reason", "scores", "min_confidence", "model_version", "updated_at", "decision_snapshot", "diagnostics"}).AddRow(7, "passed", "", `{"PII":0.1}`, 90, "jev-1.13.0", time.Now(), `{"policy":{"rules_version":"jev-v2","audit_categories":["PII"],"enforcement_categories":[],"thresholds":{"PII":90}},"auto_enforce":false}`, `{"planned_checks":2,"completed_checks":2,"cache_hits":1,"detector_calls":1}`))
	logs := []service.UsageLog{{ID: 7}, {ID: 8}}
	require.NoError(t, repo.hydrateUsageLogModeration(context.Background(), logs))
	require.Empty(t, logs[0].ModerationEvents)
	require.Equal(t, "passed", logs[0].ModerationResult.Status)
	require.Equal(t, 90, logs[0].ModerationResult.DecisionSnapshot.Policy.Thresholds["PII"])
	require.Equal(t, .1, logs[0].ModerationResult.Scores["PII"])
	require.Equal(t, 1, logs[0].ModerationResult.Diagnostics.CacheHits)
	require.Len(t, logs[1].ModerationEvents, 1)
	require.Equal(t, .95, logs[1].ModerationEvents[0].Scores["PII"])
	require.Equal(t, 90, *logs[1].ModerationEvents[0].MinConfidence)
	require.NoError(t, mock.ExpectationsWereMet())
}
