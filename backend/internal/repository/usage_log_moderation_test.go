package repository

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"regexp"
	"testing"
)

func TestUsageModerationLinkCapturesFinalBillingIdentity(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := &usageLogRepository{sql: db}
	groupID := int64(3)
	mock.ExpectExec("INSERT INTO input_moderation_usage_links").WithArgs("local-id", "openai_responses_ws", 2, int64(1), int64(2), int64(3), "upstream-billing-id").WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.LinkInputModerationUsage(context.Background(), &service.UsageLog{UserID: 1, APIKeyID: 2, GroupID: &groupID, RequestID: "upstream-billing-id", ModerationRef: &service.InputModerationUsageRef{RequestID: "local-id", Source: "openai_responses_ws", TurnNumber: 2}}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageModerationHydrationScopesIdentityAndTurn(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := &usageLogRepository{sql: db}
	// Require all identity boundaries in the query, not just request_id.
	query := `SELECT DISTINCT u.id, e.id, e.categories, e.scores, e.min_confidence,
 e.action, e.counted_as_strike, COALESCE(e.model_version,'')
 FROM usage_logs u JOIN input_moderation_usage_links l
 ON l.usage_request_id=u.request_id AND l.user_id=u.user_id AND l.api_key_id=u.api_key_id AND l.group_id=u.group_id
 JOIN input_moderation_events e ON e.request_id=l.request_id AND e.source=l.source
 AND COALESCE(e.turn_number,0)=l.turn_number AND e.user_id=l.user_id AND e.api_key_id=l.api_key_id AND e.group_id=l.group_id
 WHERE u.id=ANY($1) ORDER BY u.id,e.id`
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs("{7,8}").WillReturnRows(sqlmock.NewRows([]string{"id", "event_id", "categories", "scores", "min_confidence", "action", "counted_as_strike", "model_version"}).AddRow(8, 20, `["PII"]`, `{"PII":0.95}`, 90, "warning", true, "jev-1.13.0"))
	logs := []service.UsageLog{{ID: 7}, {ID: 8}}
	require.NoError(t, repo.hydrateUsageLogModeration(context.Background(), logs))
	require.Empty(t, logs[0].ModerationEvents)
	require.Len(t, logs[1].ModerationEvents, 1)
	require.Equal(t, .95, logs[1].ModerationEvents[0].Scores["PII"])
	require.Equal(t, 90, *logs[1].ModerationEvents[0].MinConfidence)
	require.NoError(t, mock.ExpectationsWereMet())
}
