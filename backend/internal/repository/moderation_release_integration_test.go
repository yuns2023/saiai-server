//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func moderationReleaseFixture(t *testing.T) (*service.User, *service.APIKey, *service.Group, *service.Account) {
	t.Helper()
	c := testEntClient(t)
	u := mustCreateUser(t, c, &service.User{})
	g := mustCreateGroup(t, c, &service.Group{Name: "moderation-" + uuid.NewString()})
	k := mustCreateApiKey(t, c, &service.APIKey{UserID: u.ID, GroupID: &g.ID})
	a := mustCreateAccount(t, c, &service.Account{Name: "moderation-" + uuid.NewString()})
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM usage_logs WHERE user_id=$1", u.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM user_input_risk_states WHERE user_id=$1", u.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM input_moderation_events WHERE user_id=$1", u.ID)
		_ = c.APIKey.DeleteOneID(k.ID).Exec(ctx)
		_ = c.Account.DeleteOneID(a.ID).Exec(ctx)
		_ = c.Group.DeleteOneID(g.ID).Exec(ctx)
		_ = c.User.DeleteOneID(u.ID).Exec(ctx)
	})
	return u, k, g, a
}

func TestModerationReleaseAtomicCorrelationArrivalOrders(t *testing.T) {
	ctx := context.Background()
	u, k, g, a := moderationReleaseFixture(t)
	usage := newUsageLogRepositoryWithSQL(testEntClient(t), integrationDB)
	events := NewInputModerationRepository(integrationDB)
	for i, mode := range []string{"single", "batch", "best_effort"} {
		t.Run(mode, func(t *testing.T) {
			request := uuid.NewString()
			log := &service.UsageLog{UserID: u.ID, APIKeyID: k.ID, GroupID: &g.ID, AccountID: a.ID,
				RequestID: uuid.NewString(), Model: "TEST_ONLY_MODEL", CreatedAt: time.Now().UTC(),
				ModerationRef: &service.InputModerationUsageRef{RequestID: request, Source: "openai_responses_ws", TurnNumber: i + 1}}
			event := &service.InputModerationEvent{JobID: uuid.NewString(), RequestID: request, UserID: u.ID,
				APIKeyID: k.ID, GroupID: g.ID, InputHash: "TEST_ONLY_HASH", Safety: "Unsafe", Categories: []string{"PII"},
				Scores: map[string]float64{"PII": .95}, MinConfidence: 90, Action: "warning", ModelVersion: "jev-1.13.0",
				Source: "openai_responses_ws", TurnNumber: i + 1, CreatedAt: time.Now().UTC()}
			if i == 0 {
				require.NoError(t, events.InsertInputModerationEvent(ctx, event))
			}
			var err error
			switch mode {
			case "single":
				_, err = usage.createSingle(ctx, integrationDB, log)
			case "batch":
				_, err = usage.Create(ctx, log)
			default:
				err = usage.CreateBestEffort(ctx, log)
			}
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				var ref string
				err := integrationDB.QueryRowContext(ctx, "SELECT id, moderation_ref->>'RequestID' FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", log.RequestID, k.ID).Scan(&log.ID, &ref)
				return err == nil && ref == request
			}, 3*time.Second, 10*time.Millisecond)
			logs := []service.UsageLog{{ID: log.ID}}
			if i != 0 {
				require.NoError(t, usage.hydrateUsageLogModeration(ctx, logs))
				require.Empty(t, logs[0].ModerationEvents)
				require.NoError(t, events.InsertInputModerationEvent(ctx, event))
			}
			// Same request ID on a different WS turn must not attach to this row.
			other := *event
			other.JobID = uuid.NewString()
			other.TurnNumber += 10
			require.NoError(t, events.InsertInputModerationEvent(ctx, &other))
			require.NoError(t, usage.hydrateUsageLogModeration(ctx, logs))
			require.Len(t, logs[0].ModerationEvents, 1)
			require.Equal(t, .95, logs[0].ModerationEvents[0].Scores["PII"])
			inserted, err := usage.Create(ctx, log)
			require.NoError(t, err)
			require.False(t, inserted)
		})
	}
}

func TestModerationReleaseConcurrentStrikesAndReset(t *testing.T) {
	ctx := context.Background()
	u, k, g, _ := moderationReleaseFixture(t)
	repo := NewInputModerationRepository(integrationDB)
	policy := service.InputModerationEscalationPolicy{ActionMode: service.InputModerationActionModeWarnThenCooldown,
		DisableAfterHits: 3, StrikeWindow: 24 * time.Hour, Cooldown: 30 * time.Minute, DedupeWindow: time.Minute}
	makeEvent := func(request, hash string) *service.InputModerationEvent {
		return &service.InputModerationEvent{JobID: uuid.NewString(), RequestID: request, UserID: u.ID, APIKeyID: k.ID,
			GroupID: g.ID, InputHash: hash, Safety: "Unsafe", Categories: []string{"PII", "Crime"},
			Scores: map[string]float64{"PII": .95, "Crime": .96}, MinConfidence: 90, Source: "openai_responses_ws",
			TurnNumber: 1, CreatedAt: time.Now().UTC(), EnqueuedAt: time.Now().UTC()}
	}
	request := uuid.NewString()
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := repo.ApplyInputModerationIncident(ctx, makeEvent(request, fmt.Sprintf("hash-%d", i)), policy)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	state, err := repo.GetUserInputRiskState(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, 1, state.StrikeCount)
	for i := 2; i <= 3; i++ {
		transition, err := repo.ApplyInputModerationIncident(ctx, makeEvent(uuid.NewString(), uuid.NewString()), policy)
		require.NoError(t, err)
		require.Equal(t, i, transition.StrikeCount)
		if i == 2 {
			require.Equal(t, service.InputModerationActionWarning, transition.Action)
		}
		if i == 3 {
			require.NotNil(t, transition.BlockedUntil)
			require.False(t, transition.UserDisabled)
		}
	}
	// A queued old event racing a reset must not resurrect strikes after reset.
	old := makeEvent(uuid.NewString(), uuid.NewString())
	old.EnqueuedAt = time.Now().Add(-time.Minute)
	start := make(chan struct{})
	raceErrors := make(chan error, 2)
	go func() { <-start; _, err := repo.ApplyInputModerationIncident(ctx, old, policy); raceErrors <- err }()
	go func() { <-start; raceErrors <- repo.ResetUserInputRiskState(ctx, u.ID) }()
	close(start)
	require.NoError(t, <-raceErrors)
	require.NoError(t, <-raceErrors)
	state, err = repo.GetUserInputRiskState(ctx, u.ID)
	require.NoError(t, err)
	require.Zero(t, state.StrikeCount)
	require.Nil(t, state.BlockedUntil)
}
