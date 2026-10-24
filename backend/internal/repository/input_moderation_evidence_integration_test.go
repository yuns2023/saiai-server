//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func evidenceTestEvent(u *service.User, k *service.APIKey, g *service.Group, request string) *service.InputModerationEvent {
	job := uuid.NewString()
	now := time.Now().UTC()
	aad := service.ModerationEvidenceAAD{JobID: job, UserID: u.ID, GroupID: g.ID, CheckIndex: 1, Kinds: []string{"instructions"}}
	task := service.InputModerationTask{RequestID: request, Source: "anthropic_messages", UserID: u.ID, APIKeyID: k.ID, GroupID: g.ID}
	return &service.InputModerationEvent{
		JobID: job, RequestID: request, UserID: u.ID, APIKeyID: k.ID, GroupID: g.ID, InputHash: uuid.NewString(),
		Safety: "unsafe", Categories: []string{"PII"}, Scores: map[string]float64{"PII": .99}, MinConfidence: 90,
		Action: service.InputModerationActionAudit, Source: "anthropic_messages", CreatedAt: now, EnqueuedAt: now,
		Evidence: []service.InputModerationEvidence{{CheckIndex: 1, Kinds: aad.Kinds, AAD: aad, CharCount: 4, KeyID: "TEST_ONLY_KEY",
			Ciphertext: "TEST_ONLY_CIPHERTEXT", ExpiresAt: now.Add(90 * 24 * time.Hour)}},
		Result: &service.InputModerationResult{Status: "flagged", UpdatedAt: now,
			Diagnostics: &service.InputModerationDiagnostics{EvidenceStatus: service.ModerationEvidenceStored, EvidenceCount: 1}},
		ResultTask: &task,
	}
}

func evidenceRowCount(t *testing.T, userID int64) int {
	t.Helper()
	var n int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM input_moderation_evidence v
		JOIN input_moderation_events e ON e.id = v.event_id WHERE e.user_id = $1`, userID).Scan(&n))
	return n
}

func eventIDForJob(t *testing.T, job string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT id FROM input_moderation_events WHERE job_id = $1::uuid`, job).Scan(&id))
	return id
}

func TestModerationEvidenceWrittenWithEventAndResultOnBothPaths(t *testing.T) {
	ctx := context.Background()
	u, k, g, _ := moderationReleaseFixture(t)
	repo := &inputModerationRepository{db: integrationDB}
	policy := service.InputModerationEscalationPolicy{ActionMode: service.InputModerationActionModeWarnThenCooldown, DisableAfterHits: 3}
	for _, incident := range []bool{false, true} {
		request := uuid.NewString()
		event := evidenceTestEvent(u, k, g, request)
		if incident {
			_, err := repo.ApplyInputModerationIncident(ctx, event, policy)
			require.NoError(t, err)
		} else {
			require.NoError(t, repo.InsertInputModerationEvent(ctx, event))
		}
		eventID := eventIDForJob(t, event.JobID)
		record, err := repo.LoadInputModerationEvidence(ctx, eventID)
		require.NoError(t, err)
		require.Len(t, record.Rows, 1)
		require.Equal(t, event.Evidence[0].AAD, record.Rows[0].AAD)
		require.Equal(t, "TEST_ONLY_CIPHERTEXT", record.Rows[0].Ciphertext)
		var status, evidenceStatus string
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status, diagnostics->>'evidence_status' FROM input_moderation_results WHERE request_id = $1`, request).Scan(&status, &evidenceStatus))
		require.Equal(t, "flagged", status)
		require.Equal(t, service.ModerationEvidenceStored, evidenceStatus)

		// A duplicate job writes nothing and leaves committed rows untouched.
		duplicate := *event
		duplicate.Evidence = []service.InputModerationEvidence{event.Evidence[0], event.Evidence[0]}
		duplicate.Evidence[0].Ciphertext = "TEST_ONLY_REPLACED"
		duplicate.Evidence[1].CheckIndex = 9
		duplicate.Result = &service.InputModerationResult{Status: "flagged", Reason: "TEST_ONLY_REPLACED", UpdatedAt: time.Now().UTC()}
		if incident {
			transition, err := repo.ApplyInputModerationIncident(ctx, &duplicate, policy)
			require.NoError(t, err)
			require.True(t, transition.Duplicate)
		} else {
			require.NoError(t, repo.InsertInputModerationEvent(ctx, &duplicate))
		}
		record, err = repo.LoadInputModerationEvidence(ctx, eventID)
		require.NoError(t, err)
		require.Len(t, record.Rows, 1)
		require.Equal(t, "TEST_ONLY_CIPHERTEXT", record.Rows[0].Ciphertext)
		var reason string
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT reason FROM input_moderation_results WHERE request_id = $1`, request).Scan(&reason))
		require.Empty(t, reason)
	}
}

func TestModerationEvidenceHoldCleanupAndExpiry(t *testing.T) {
	ctx := context.Background()
	u, k, g, _ := moderationReleaseFixture(t)
	repo := &inputModerationRepository{db: integrationDB}
	expired := evidenceTestEvent(u, k, g, uuid.NewString())
	held := evidenceTestEvent(u, k, g, uuid.NewString())
	for _, event := range []*service.InputModerationEvent{expired, held} {
		require.NoError(t, repo.InsertInputModerationEvent(ctx, event))
	}
	expiredID, heldID := eventIDForJob(t, expired.JobID), eventIDForJob(t, held.JobID)
	admin := service.InputModerationEvidenceAccess{AdminUserID: 1, AuthMethod: "jwt"}

	// Hold while viewable, then let both rows expire.
	hold := admin
	hold.EventID, hold.Action, hold.Reason = heldID, "hold", "TEST_ONLY case"
	n, err := repo.HoldInputModerationEvidence(ctx, hold, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	_, err = integrationDB.ExecContext(ctx, `UPDATE input_moderation_evidence SET expires_at = NOW() - INTERVAL '1 minute' WHERE event_id = ANY($1)`, pq.Array([]int64{expiredID, heldID}))
	require.NoError(t, err)

	// Expired, unheld evidence is not viewable and cannot be held anymore.
	record, err := repo.LoadInputModerationEvidence(ctx, expiredID)
	require.NoError(t, err)
	require.Nil(t, record)
	late := admin
	late.EventID, late.Action, late.Reason = expiredID, "hold", "TEST_ONLY late"
	n, err = repo.HoldInputModerationEvidence(ctx, late, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Zero(t, n)
	record, err = repo.LoadInputModerationEvidence(ctx, heldID)
	require.NoError(t, err)
	require.Len(t, record.Rows, 1)

	held2, total, err := repo.ListHeldInputModerationEvidence(ctx, 1, 50)
	require.NoError(t, err)
	require.GreaterOrEqual(t, total, int64(1))
	found := false
	for _, item := range held2 {
		if item.EventID == heldID {
			found = true
			require.Equal(t, "TEST_ONLY case", item.HoldReason)
			require.Equal(t, u.ID, *item.UserID)
		}
	}
	require.True(t, found)

	// Cleanup deletes the expired row and keeps the held one.
	_, err = repo.PurgeExpiredInputModerationEvidence(ctx, integrationDB, 1000)
	require.NoError(t, err)
	require.Equal(t, 1, evidenceRowCount(t, u.ID))

	release := admin
	release.EventID, release.Action = heldID, "release"
	n, err = repo.ReleaseInputModerationEvidence(ctx, release)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	_, err = repo.PurgeExpiredInputModerationEvidence(ctx, integrationDB, 1000)
	require.NoError(t, err)
	require.Zero(t, evidenceRowCount(t, u.ID))

	// Hold and release were audited; the failed late hold was not.
	var actions []string
	rows, err := integrationDB.QueryContext(ctx, `SELECT action FROM input_moderation_evidence_access WHERE event_id = ANY($1) ORDER BY id`, pq.Array([]int64{expiredID, heldID}))
	require.NoError(t, err)
	for rows.Next() {
		var action string
		require.NoError(t, rows.Scan(&action))
		actions = append(actions, action)
	}
	require.NoError(t, rows.Close())
	require.Equal(t, []string{"hold", "release"}, actions)

	// Old access rows are purged by age.
	_, err = integrationDB.ExecContext(ctx, `UPDATE input_moderation_evidence_access SET created_at = NOW() - INTERVAL '800 days' WHERE event_id = $1`, heldID)
	require.NoError(t, err)
	purged, err := repo.PurgeInputModerationEvidenceAccess(ctx, integrationDB, time.Now().Add(-730*24*time.Hour), 1000)
	require.NoError(t, err)
	require.Equal(t, int64(2), purged)
}

func TestModerationEvidenceRemovedOnUserSoftDelete(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	u, k, g, _ := moderationReleaseFixture(t)
	repo := &inputModerationRepository{db: integrationDB}
	plain := evidenceTestEvent(u, k, g, uuid.NewString())
	held := evidenceTestEvent(u, k, g, uuid.NewString())
	require.NoError(t, repo.InsertInputModerationEvent(ctx, plain))
	require.NoError(t, repo.InsertInputModerationEvent(ctx, held))
	_, err := repo.HoldInputModerationEvidence(ctx, service.InputModerationEvidenceAccess{EventID: eventIDForJob(t, held.JobID), AdminUserID: 1, AuthMethod: "jwt", Action: "hold", Reason: "TEST_ONLY"}, time.Now().Add(time.Hour))
	require.NoError(t, err)

	users := newUserRepositoryWithSQL(client, integrationDB)
	require.NoError(t, users.Delete(ctx, u.ID))
	require.Equal(t, 1, evidenceRowCount(t, u.ID), "held evidence survives user deletion")

	// A late in-flight job for the deleted user stores no evidence.
	late := evidenceTestEvent(u, k, g, uuid.NewString())
	require.NoError(t, repo.InsertInputModerationEvent(ctx, late))
	require.Equal(t, 1, evidenceRowCount(t, u.ID))
	var evidenceStatus string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT diagnostics->>'evidence_status' FROM input_moderation_results WHERE request_id = $1`, late.RequestID).Scan(&evidenceStatus))
	require.Equal(t, service.ModerationEvidenceUserDeleted, evidenceStatus)

	// The incident path records the event without evidence or a strike.
	incident := evidenceTestEvent(u, k, g, uuid.NewString())
	transition, err := repo.ApplyInputModerationIncident(ctx, incident, service.InputModerationEscalationPolicy{})
	require.NoError(t, err)
	require.Equal(t, service.InputModerationActionAlreadyInactive, transition.Action)
	require.Equal(t, 1, evidenceRowCount(t, u.ID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT diagnostics->>'evidence_status' FROM input_moderation_results WHERE request_id = $1`, incident.RequestID).Scan(&evidenceStatus))
	require.Equal(t, service.ModerationEvidenceUserDeleted, evidenceStatus)
}

func TestModerationIncidentFingerprintDedupWithinStrikeWindow(t *testing.T) {
	ctx := context.Background()
	u, k, g, _ := moderationReleaseFixture(t)
	repo := NewInputModerationRepository(integrationDB)
	policy := service.InputModerationEscalationPolicy{ActionMode: service.InputModerationActionModeWarnThenCooldown,
		DisableAfterHits: 5, StrikeWindow: 24 * time.Hour, Cooldown: 30 * time.Minute, DedupeWindow: time.Minute}
	apply := func(fingerprint string) *service.InputModerationTransition {
		t.Helper()
		event := evidenceTestEvent(u, k, g, uuid.NewString())
		event.Evidence, event.Result = nil, nil
		event.IncidentFingerprint = fingerprint
		transition, err := repo.ApplyInputModerationIncident(ctx, event, policy)
		require.NoError(t, err)
		return transition
	}
	bundleA := "a000000000000000000000000000000000000000000000000000000000000000"
	bundleAB := "b000000000000000000000000000000000000000000000000000000000000000"
	require.Equal(t, 1, apply(bundleA).StrikeCount)
	repeat := apply(bundleA)
	require.True(t, repeat.Duplicate)
	require.Equal(t, service.InputModerationActionDuplicateIgnored, repeat.Action)
	// A -> A+B: a new enforcement category changes the fingerprint and counts.
	require.Equal(t, 2, apply(bundleAB).StrikeCount)
	// Without a fingerprint every new input counts.
	require.Equal(t, 3, apply("").StrikeCount)
	// After a reset the same bundle counts again, once.
	require.NoError(t, repo.ResetUserInputRiskState(ctx, u.ID))
	time.Sleep(10 * time.Millisecond)
	again := apply(bundleA)
	require.False(t, again.Duplicate)
	require.Equal(t, 1, again.StrikeCount)
	require.True(t, apply(bundleA).Duplicate)
}

func TestInputRiskAlertsExposeEvidencePerEvent(t *testing.T) {
	ctx := context.Background()
	u, k, g, _ := moderationReleaseFixture(t)
	repo := &inputModerationRepository{db: integrationDB}
	event := evidenceTestEvent(u, k, g, uuid.NewString())
	require.NoError(t, repo.InsertInputModerationEvent(ctx, event))
	usage := newUsageLogRepositoryWithSQL(testEntClient(t), integrationDB)
	alerts, _, err := usage.ListInputRiskAlerts(ctx, u.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 1, 20)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	require.Len(t, alerts[0].Events, 1)
	require.Equal(t, eventIDForJob(t, event.JobID), alerts[0].Events[0].EventID)
	require.Equal(t, 1, alerts[0].Events[0].EvidenceCount)
	require.Equal(t, service.ModerationEvidenceStored, alerts[0].Events[0].EvidenceStatus)
}

func TestModerationFingerprintDedupSurvivesOutOfOrderCommits(t *testing.T) {
	ctx := context.Background()
	u, k, g, _ := moderationReleaseFixture(t)
	repo := NewInputModerationRepository(integrationDB)
	policy := service.InputModerationEscalationPolicy{ActionMode: service.InputModerationActionModeWarnThenCooldown,
		DisableAfterHits: 5, StrikeWindow: 24 * time.Hour, Cooldown: 30 * time.Minute, DedupeWindow: time.Minute}
	apply := func(fingerprint string, createdAt time.Time) *service.InputModerationTransition {
		t.Helper()
		event := evidenceTestEvent(u, k, g, uuid.NewString())
		event.Evidence, event.Result = nil, nil
		event.IncidentFingerprint = fingerprint
		event.CreatedAt, event.EnqueuedAt = createdAt, createdAt
		transition, err := repo.ApplyInputModerationIncident(ctx, event, policy)
		require.NoError(t, err)
		return transition
	}
	bundleA := "c000000000000000000000000000000000000000000000000000000000000000"
	bundleB := "d000000000000000000000000000000000000000000000000000000000000000"
	now := time.Now().UTC()
	// B opens the window; A was created earlier but commits later and counts.
	require.Equal(t, 1, apply(bundleB, now).StrikeCount)
	require.Equal(t, 2, apply(bundleA, now.Add(-time.Minute)).StrikeCount)
	// A repeated bundle A must see that strike although its created_at is
	// before the window start.
	repeat := apply(bundleA, now.Add(time.Second))
	require.True(t, repeat.Duplicate)
	require.Equal(t, 2, repeat.StrikeCount)
}

func TestModerationEvidenceOfDeletedUserIsNotKeptAfterRelease(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	u, k, g, _ := moderationReleaseFixture(t)
	repo := &inputModerationRepository{db: integrationDB}
	released := evidenceTestEvent(u, k, g, uuid.NewString())
	lapsed := evidenceTestEvent(u, k, g, uuid.NewString())
	require.NoError(t, repo.InsertInputModerationEvent(ctx, released))
	require.NoError(t, repo.InsertInputModerationEvent(ctx, lapsed))
	releasedID, lapsedID := eventIDForJob(t, released.JobID), eventIDForJob(t, lapsed.JobID)
	for _, id := range []int64{releasedID, lapsedID} {
		_, err := repo.HoldInputModerationEvidence(ctx, service.InputModerationEvidenceAccess{EventID: id, AdminUserID: 1, AuthMethod: "jwt", Action: "hold", Reason: "TEST_ONLY"}, time.Now().Add(time.Hour))
		require.NoError(t, err)
	}
	require.NoError(t, newUserRepositoryWithSQL(client, integrationDB).Delete(ctx, u.ID))
	require.Equal(t, 2, evidenceRowCount(t, u.ID))

	// Release deletes the deleted user's evidence in the same transaction.
	n, err := repo.ReleaseInputModerationEvidence(ctx, service.InputModerationEvidenceAccess{EventID: releasedID, AdminUserID: 1, AuthMethod: "jwt", Action: "release"})
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	record, err := repo.LoadInputModerationEvidence(ctx, releasedID)
	require.NoError(t, err)
	require.Nil(t, record)

	// A lapsed hold is cleaned up before the original expiry.
	_, err = integrationDB.ExecContext(ctx, `UPDATE input_moderation_evidence SET hold_until = NOW() - INTERVAL '1 minute' WHERE event_id = $1`, lapsedID)
	require.NoError(t, err)
	_, err = repo.PurgeExpiredInputModerationEvidence(ctx, integrationDB, 1000)
	require.NoError(t, err)
	require.Zero(t, evidenceRowCount(t, u.ID))
}

func TestModerationHeldListToleratesDeletedUserAndGroup(t *testing.T) {
	ctx := context.Background()
	u, k, g, _ := moderationReleaseFixture(t)
	repo := &inputModerationRepository{db: integrationDB}
	event := evidenceTestEvent(u, k, g, uuid.NewString())
	require.NoError(t, repo.InsertInputModerationEvent(ctx, event))
	eventID := eventIDForJob(t, event.JobID)
	_, err := repo.HoldInputModerationEvidence(ctx, service.InputModerationEvidenceAccess{EventID: eventID, AdminUserID: 1, AuthMethod: "jwt", Action: "hold", Reason: "TEST_ONLY"}, time.Now().Add(time.Hour))
	require.NoError(t, err)
	// Hard deletes set the event's user/group to NULL.
	_, err = integrationDB.ExecContext(ctx, `UPDATE input_moderation_events SET user_id = NULL, group_id = NULL WHERE id = $1`, eventID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM input_moderation_events WHERE id = $1`, eventID)
	})
	items, _, err := repo.ListHeldInputModerationEvidence(ctx, 1, 100)
	require.NoError(t, err)
	found := false
	for _, item := range items {
		if item.EventID == eventID {
			found = true
			require.Nil(t, item.UserID)
			require.Nil(t, item.GroupID)
		}
	}
	require.True(t, found)
}
