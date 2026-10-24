package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type moderationResultRepoStub struct {
	inputModerationEventRepoStub
	results []InputModerationResult
	tasks   []InputModerationTask
}

func (r *moderationResultRepoStub) SaveInputModerationResult(_ context.Context, t InputModerationTask, v InputModerationResult) error {
	r.results = append(r.results, v)
	r.tasks = append(r.tasks, t)
	return nil
}

// Flagged results are written in the event transaction.
func (r *moderationResultRepoStub) recordEventResult(event *InputModerationEvent) {
	if event.Result != nil && event.ResultTask != nil {
		r.results = append(r.results, *event.Result)
		r.tasks = append(r.tasks, *event.ResultTask)
	}
}
func (r *moderationResultRepoStub) InsertInputModerationEvent(ctx context.Context, event *InputModerationEvent) error {
	r.recordEventResult(event)
	return r.inputModerationEventRepoStub.InsertInputModerationEvent(ctx, event)
}
func (r *moderationResultRepoStub) ApplyInputModerationIncident(ctx context.Context, event *InputModerationEvent, policy InputModerationEscalationPolicy) (*InputModerationTransition, error) {
	r.recordEventResult(event)
	return r.inputModerationEventRepoStub.ApplyInputModerationIncident(ctx, event, policy)
}
func TestInputModerationResultsSeparatePassesFromRiskEvents(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		score        float64
		failure      bool
		enabled      bool
	}{
		{"pass", "passed", .1, false, true}, {"threshold hit", "flagged", .9, false, true}, {"provider failure", "failed", 0, true, true}, {"policy disabled", "skipped", 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &moderationResultRepoStub{}
			client := &inputModerationClientStub{result: &InputModerationClassification{Safety: "safe", Scores: map[string]float64{"PII": tc.score}, ModelVersion: "TEST_ONLY_MODEL"}}
			if tc.failure {
				client.err = errors.New("TEST_ONLY_PROVIDER_SECRET")
			}
			groups := &inputModerationGroupRepoStub{groups: []*Group{{ID: 3, InputModerationEnabled: tc.enabled, InputModerationMinConfidence: 90}}}
			svc := newInputModerationServiceForTest(client, &repo.inputModerationEventRepoStub, groups, nil, nil)
			svc.eventRepo = repo
			defer svc.Stop()
			err := svc.process(InputModerationTask{RequestID: "request", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "TEST_ONLY_TEXT"})
			require.Equal(t, tc.failure, err != nil)
			require.Len(t, repo.results, 1)
			require.Equal(t, tc.status, repo.results[0].Status)
			require.NotContains(t, repo.results[0].Reason, "TEST_ONLY")
			if tc.status == "passed" {
				require.Equal(t, .1, repo.results[0].Scores["PII"])
				require.Equal(t, 90, repo.results[0].MinConfidence)
			}
			if tc.status != "flagged" {
				require.Empty(t, repo.events)
			} else {
				require.Len(t, repo.events, 1)
			}
		})
	}
}
func TestInputModerationResultsSkipWithoutCallingProviderAndExpire(t *testing.T) {
	repo := &moderationResultRepoStub{}
	client := &inputModerationClientStub{}
	svc := newInputModerationServiceForTest(client, &repo.inputModerationEventRepoStub, nil, nil, nil)
	svc.eventRepo = repo
	defer svc.Stop()
	task := InputModerationTask{RequestID: "request", UserID: 1, APIKeyID: 2, GroupID: 3}
	require.False(t, svc.Submit(task))
	require.Equal(t, "skipped", repo.results[0].Status)
	require.Zero(t, client.calls)
	svc.groupRepo = &inputModerationGroupRepoStub{groups: []*Group{{ID: 3}}}
	task.EnqueuedAt = time.Now().Add(-48 * time.Hour)
	require.NoError(t, svc.process(task))
	require.Equal(t, "expired", repo.results[1].Reason)
	require.Zero(t, client.calls)
}
