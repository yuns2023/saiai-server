//go:build integration

package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestInputRiskAlertsCountAndHistory(t *testing.T) {
	ctx := context.Background()
	u, k, g, a := moderationReleaseFixture(t)
	results := &inputModerationRepository{db: integrationDB}
	users := &userRepository{sql: integrationDB}
	usage := newUsageLogRepositoryWithSQL(testEntClient(t), integrationDB)
	end := time.Now().UTC()
	start := end.Add(-30 * 24 * time.Hour)
	request := uuid.NewString()
	task := service.InputModerationTask{RequestID: request, Source: "openai_responses_ws", UserID: u.ID, APIKeyID: k.ID, GroupID: g.ID}
	for i, status := range []string{"flagged", "flagged", "passed", "failed", "pending", "skipped", "flagged"} {
		task.TurnNumber = i
		at := start.Add(time.Hour)
		if i == 6 {
			at = start.Add(-time.Second)
		}
		result := service.InputModerationResult{Status: status, UpdatedAt: at, Scores: map[string]float64{"PII": .95, "violence": .96}, MinConfidence: 90}
		require.NoError(t, results.SaveInputModerationResult(ctx, task, result))
		require.NoError(t, results.SaveInputModerationResult(ctx, task, result)) // retry cannot double count
	}
	counts, err := users.CountInputRiskAlerts(ctx, []int64{u.ID}, start, end)
	require.NoError(t, err)
	require.Equal(t, int64(2), counts[u.ID]) // no usage rows required
	alerts, total, err := usage.ListInputRiskAlerts(ctx, u.ID, start, end, 1, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, alerts, 1)
	require.Equal(t, 0, alerts[0].TurnNumber)
	require.Len(t, alerts[0].Result.Scores, 2)
	alerts, _, err = usage.ListInputRiskAlerts(ctx, u.ID, start, end, 2, 1)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	require.Equal(t, 1, alerts[0].TurnNumber)
	// Different API key/turn must not inherit another detection.
	other := mustCreateApiKey(t, testEntClient(t), &service.APIKey{UserID: u.ID, GroupID: &g.ID})
	t.Cleanup(func() { _ = testEntClient(t).APIKey.DeleteOneID(other.ID).Exec(context.Background()) })
	for i, key := range []int64{k.ID, other.ID, k.ID} {
		turn := 0
		if i == 2 {
			turn = 2
		}
		log := &service.UsageLog{UserID: u.ID, APIKeyID: key, GroupID: &g.ID, AccountID: a.ID, RequestID: uuid.NewString(), Model: "TEST_ONLY", CreatedAt: start.Add(time.Hour), ModerationRef: &service.InputModerationUsageRef{RequestID: request, Source: task.Source, TurnNumber: turn}}
		_, err := usage.createSingle(ctx, integrationDB, log)
		require.NoError(t, err)
	}
	filters := usagestats.UsageLogFilters{UserID: u.ID, ModerationFlagged: true, StartTime: &start, EndTime: &end, ExactTotal: true}
	logs, page, err := usage.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 20}, filters)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Equal(t, int64(1), page.Total)
	stats, err := usage.GetStatsWithFilters(ctx, filters)
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.TotalRequests)
	// Wrong user never sees the result, even with the same time window.
	alerts, total, err = usage.ListInputRiskAlerts(ctx, u.ID+999999, start, end, 1, 20)
	require.NoError(t, err)
	require.Empty(t, alerts)
	require.Zero(t, total)
}
