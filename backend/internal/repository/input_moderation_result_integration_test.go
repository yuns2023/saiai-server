//go:build integration

package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestModerationResultsDurabilityAndIdentityIsolation(t *testing.T) {
	ctx := context.Background()
	u, k, g, a := moderationReleaseFixture(t)
	repo := &inputModerationRepository{db: integrationDB}
	usage := newUsageLogRepositoryWithSQL(testEntClient(t), integrationDB)
	request := uuid.NewString()
	task := service.InputModerationTask{RequestID: request, Source: "openai_responses_ws", TurnNumber: 1, UserID: u.ID, APIKeyID: k.ID, GroupID: g.ID}
	result := service.InputModerationResult{Status: "pending", UpdatedAt: time.Now().UTC()}
	require.NoError(t, repo.SaveInputModerationResult(ctx, task, result))
	result.Status = "failed"
	result.Reason = "processing_failed"
	require.NoError(t, repo.SaveInputModerationResult(ctx, task, result))
	result.Status = "passed"
	result.Reason = ""
	result.Scores = map[string]float64{"PII": .1}
	result.MinConfidence = 90
	result.Diagnostics = &service.InputModerationDiagnostics{PlannedChecks: 2, CompletedChecks: 2, CacheHits: 1, DetectorCalls: 1}
	result.ModelVersion = "jev-1.13.0"
	result.DecisionSnapshot = &service.ModerationDecisionSnapshot{Policy: &service.InputModerationPolicy{RulesVersion: service.JevExpandedRulesVersion, AuditCategories: []string{"PII"}, Thresholds: map[string]int{"PII": 90}}}
	require.NoError(t, repo.SaveInputModerationResult(ctx, task, result))
	result.Status = "pending"
	result.Scores = nil
	result.Diagnostics = nil
	require.NoError(t, repo.SaveInputModerationResult(ctx, task, result)) // late retries cannot downgrade a final result
	for _, turn := range []int{1, 2} {
		log := &service.UsageLog{UserID: u.ID, APIKeyID: k.ID, GroupID: &g.ID, AccountID: a.ID, RequestID: uuid.NewString(), Model: "TEST_ONLY", CreatedAt: time.Now().UTC(), ModerationRef: &service.InputModerationUsageRef{RequestID: request, Source: task.Source, TurnNumber: turn}}
		_, err := usage.createSingle(ctx, integrationDB, log)
		require.NoError(t, err)
		logs := []service.UsageLog{{ID: log.ID}}
		require.NoError(t, usage.hydrateUsageLogModeration(ctx, logs))
		if turn == 1 {
			require.NotNil(t, logs[0].ModerationResult)
			require.Equal(t, "passed", logs[0].ModerationResult.Status)
			require.Equal(t, 1, logs[0].ModerationResult.Diagnostics.CacheHits)
			require.Equal(t, .1, logs[0].ModerationResult.Scores["PII"])
			require.Equal(t, service.JevExpandedRulesVersion, logs[0].ModerationResult.DecisionSnapshot.Policy.RulesVersion)
		} else {
			require.Nil(t, logs[0].ModerationResult)
		}
		require.Empty(t, logs[0].ModerationEvents)
	}
	// A different API key owned by the same user must never inherit this result.
	other := mustCreateApiKey(t, testEntClient(t), &service.APIKey{UserID: u.ID, GroupID: &g.ID})
	t.Cleanup(func() { _ = testEntClient(t).APIKey.DeleteOneID(other.ID).Exec(context.Background()) })
	log := &service.UsageLog{UserID: u.ID, APIKeyID: other.ID, GroupID: &g.ID, AccountID: a.ID, RequestID: uuid.NewString(), Model: "TEST_ONLY", CreatedAt: time.Now().UTC(), ModerationRef: &service.InputModerationUsageRef{RequestID: request, Source: task.Source, TurnNumber: 1}}
	_, err := usage.createSingle(ctx, integrationDB, log)
	require.NoError(t, err)
	logs := []service.UsageLog{{ID: log.ID}}
	require.NoError(t, usage.hydrateUsageLogModeration(ctx, logs))
	require.Nil(t, logs[0].ModerationResult)
}

func TestJevPolicyGroupRoundTripAndLegacyPreservation(t *testing.T) {
	ctx := context.Background()
	_, k, g, _ := moderationReleaseFixture(t)
	groups := NewGroupRepository(testEntClient(t), integrationDB)
	original, err := groups.GetByIDLite(ctx, g.ID)
	require.NoError(t, err)
	require.Nil(t, original.InputModerationPolicy)
	original.InputModerationPolicy = &service.InputModerationPolicy{RulesVersion: service.JevExpandedRulesVersion, AuditCategories: []string{"PII", "violence"}, EnforcementCategories: []string{}, Thresholds: map[string]int{"PII": 95}}
	require.NoError(t, groups.Update(ctx, original))
	loaded, err := groups.GetByIDLite(ctx, g.ID)
	require.NoError(t, err)
	require.Equal(t, original.InputModerationPolicy, loaded.InputModerationPolicy)
	keys := NewAPIKeyRepository(testEntClient(t), integrationDB)
	key, err := keys.GetByID(ctx, k.ID)
	require.NoError(t, err)
	require.NotNil(t, key.Group)
	require.Equal(t, original.InputModerationPolicy, key.Group.InputModerationPolicy)
	authKey, err := keys.GetByKeyForAuth(ctx, k.Key)
	require.NoError(t, err)
	require.NotNil(t, authKey.Group)
	require.Equal(t, original.InputModerationPolicy, authKey.Group.InputModerationPolicy)
	loaded.Name = "TEST_ONLY_RENAMED"
	require.NoError(t, groups.Update(ctx, loaded))
	loaded, err = groups.GetByIDLite(ctx, g.ID)
	require.NoError(t, err)
	require.Empty(t, loaded.InputModerationPolicy.EnforcementCategories)
}
