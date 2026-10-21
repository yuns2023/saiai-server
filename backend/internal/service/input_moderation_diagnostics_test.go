package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestModerationCachedChecksDoNotConsumeCallBudget(t *testing.T) {
	diagnostics := newModerationDiagnostics(InputModerationTask{Text: "文", Instructions: []string{"项目指令"}}, 16)
	ctx := context.WithValue(context.Background(), moderationDiagnosticsKey{}, diagnostics)
	for check := 0; check < 16; check++ {
		require.NoError(t, ReserveInputModerationCall(ctx))
	}
	for hit := 0; hit < 30; hit++ {
		ObserveInputModerationCacheHit(ctx)
	}
	require.Equal(t, 30, diagnostics.CacheHits)
	require.ErrorIs(t, ReserveInputModerationCall(ctx), ErrInputModerationCallBudget)
	require.Equal(t, 16, diagnostics.DetectorCalls)
	require.Equal(t, "detector_calls", diagnostics.Limit)
	encoded, err := json.Marshal(diagnostics)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "项目指令")
	require.NotContains(t, string(encoded), "started")
}

func TestModerationBudgetFailureRetainsCoverageDiagnostics(t *testing.T) {
	repo := &moderationResultRepoStub{}
	client := &chunkModerationStub{}
	groups := &inputModerationGroupRepoStub{groups: []*Group{{ID: 3, InputModerationEnabled: true}}}
	svc := newInputModerationServiceForTest(nil, repo, groups, nil, nil)
	svc.client = client
	defer svc.Stop()
	task := InputModerationTask{RequestID: "budget", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "question", EnqueuedAt: time.Now().Add(-time.Second)}
	for block := 0; block < 17; block++ {
		task.Instructions = append(task.Instructions, fmt.Sprintf("project rules %d", block))
	}
	require.NoError(t, svc.process(task))
	require.Len(t, repo.results, 1)
	result := repo.results[0]
	require.Equal(t, "failed", result.Status)
	require.Equal(t, "detector_budget_exceeded", result.Reason)
	require.Equal(t, 18, result.Diagnostics.PlannedChecks)
	require.Equal(t, 16, result.Diagnostics.CompletedChecks)
	require.Equal(t, 16, result.Diagnostics.DetectorCalls)
	require.GreaterOrEqual(t, result.Diagnostics.QueueWaitMS, int64(1000))
	require.Empty(t, repo.events)
	require.Empty(t, result.Scores)
}

func TestModerationLimitsRemainBoundedBeforeDetector(t *testing.T) {
	for _, scenario := range []struct {
		name, text, reason, limit string
		instructions              []string
	}{
		{name: "characters", text: strings.Repeat("大", 300), reason: "input_too_large", limit: "input_chars"},
		{name: "plan", text: "q", reason: "plan_too_large", limit: "planned_checks"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			repo := &moderationResultRepoStub{}
			svc := newInputModerationServiceForTest(nil, repo, nil, nil, nil)
			defer svc.Stop()
			svc.cfg.MaxInputChars = 16
			if scenario.name == "plan" {
				svc.cfg.MaxInputChars = 32768
				for block := 0; block < 65; block++ {
					scenario.instructions = append(scenario.instructions, fmt.Sprint(block))
				}
			}
			require.False(t, svc.Submit(InputModerationTask{RequestID: "oversize", UserID: 1, APIKeyID: 2, GroupID: 3, Text: scenario.text, Instructions: scenario.instructions}))
			require.Equal(t, scenario.reason, repo.results[0].Reason)
			require.Equal(t, scenario.limit, repo.results[0].Diagnostics.Limit)
			require.Zero(t, repo.results[0].Diagnostics.DetectorCalls)
		})
	}
}

type cachedModerationBudgetStub struct {
	checks map[string]bool
}

func (*cachedModerationBudgetStub) UsesInputModerationCallBudget() {}

func (client *cachedModerationBudgetStub) Classify(ctx context.Context, text string) (*InputModerationClassification, error) {
	if client.checks[text] {
		ObserveInputModerationCacheHit(ctx)
	} else {
		if err := ReserveInputModerationCall(ctx); err != nil {
			return nil, err
		}
		client.checks[text] = true
	}
	return &InputModerationClassification{Safety: "safe", ModelVersion: "fixture", Scores: map[string]float64{"PII": .1}}, nil
}

func TestModerationWarmInstructionsAllowMoreThanSixteenChecks(t *testing.T) {
	client := &cachedModerationBudgetStub{checks: map[string]bool{}}
	svc := &InputModerationService{client: client}
	task := InputModerationTask{UserID: 1, GroupID: 2, Text: "new question"}
	for block := 0; block < 20; block++ {
		text := fmt.Sprintf("stable rules %d", block)
		task.Instructions = append(task.Instructions, text)
		client.checks[text] = true
	}
	task.Diagnostics = newModerationDiagnostics(task, 32768)
	_, _, err := svc.classifyInput(context.Background(), task, &Group{})
	require.NoError(t, err)
	require.Equal(t, 21, task.Diagnostics.CompletedChecks)
	require.Equal(t, 20, task.Diagnostics.CacheHits)
	require.Equal(t, 1, task.Diagnostics.DetectorCalls)
	task.Text = "different question"
	task.Diagnostics = newModerationDiagnostics(task, 32768)
	_, _, err = svc.classifyInput(context.Background(), task, &Group{})
	require.NoError(t, err)
	require.Equal(t, 1, task.Diagnostics.DetectorCalls)
	task.Instructions[0] = "changed instructions"
	task.Diagnostics = newModerationDiagnostics(task, 32768)
	_, _, err = svc.classifyInput(context.Background(), task, &Group{})
	require.NoError(t, err)
	require.Equal(t, 2, task.Diagnostics.DetectorCalls)
}

func TestModerationTimeoutIsNotInputTooLarge(t *testing.T) {
	repo := &moderationResultRepoStub{}
	client := &inputModerationClientStub{err: context.DeadlineExceeded}
	groups := &inputModerationGroupRepoStub{groups: []*Group{{ID: 3, InputModerationEnabled: true}}}
	svc := newInputModerationServiceForTest(client, repo, groups, nil, nil)
	defer svc.Stop()
	require.ErrorIs(t, svc.process(InputModerationTask{RequestID: "timeout", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "question"}), context.DeadlineExceeded)
	require.Equal(t, "timeout", repo.results[0].Reason)
	require.Equal(t, 1, repo.results[0].Diagnostics.DetectorCalls)
	require.Zero(t, repo.results[0].Diagnostics.CompletedChecks)
}

type cancelledModerationClient struct{}

func (*cancelledModerationClient) Classify(ctx context.Context, _ string) (*InputModerationClassification, error) {
	<-ctx.Done()
	return nil, errors.New("sanitized transport failure")
}

func TestModerationRecognizesTimeoutBehindSanitizedTransportError(t *testing.T) {
	repo := &moderationResultRepoStub{}
	groups := &inputModerationGroupRepoStub{groups: []*Group{{ID: 3, InputModerationEnabled: true}}}
	svc := newInputModerationServiceForTest(&cancelledModerationClient{}, repo, groups, nil, nil)
	defer svc.Stop()
	require.ErrorIs(t, svc.process(InputModerationTask{RequestID: "timeout", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "question"}), context.DeadlineExceeded)
	require.Equal(t, "timeout", repo.results[0].Reason)
}

type failedModerationResultRepo struct {
	inputModerationEventRepoStub
}

func (*failedModerationResultRepo) SaveInputModerationResult(context.Context, InputModerationTask, InputModerationResult) error {
	return errors.New("fixture storage unavailable")
}

func TestModerationResultStorageFailureCountsDetectorCallsOnce(t *testing.T) {
	before := InputModerationMetricsSnapshot()
	repo := &failedModerationResultRepo{}
	client := &chunkModerationStub{}
	groups := &inputModerationGroupRepoStub{groups: []*Group{{ID: 3, InputModerationEnabled: true, InputModerationMinConfidence: 90}}}
	svc := newInputModerationServiceForTest(client, repo, groups, nil, nil)
	defer svc.Stop()
	require.Error(t, svc.process(InputModerationTask{RequestID: "storage", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "question"}))
	after := InputModerationMetricsSnapshot()
	require.Equal(t, before.DetectorCalls+1, after.DetectorCalls)
	require.Equal(t, before.Results["failed/processing_failed"]+1, after.Results["failed/processing_failed"])
	require.Equal(t, before.Results["passed/"], after.Results["passed/"])
}

func TestModerationRuntimeMetricsCountAttemptsNotPending(t *testing.T) {
	metrics := moderationMetrics{ModerationRuntimeMetrics: ModerationRuntimeMetrics{Results: map[string]uint64{}}}
	metrics.observe(InputModerationResult{Status: "pending"})
	metrics.observe(InputModerationResult{Status: "failed", Reason: "timeout", Diagnostics: &InputModerationDiagnostics{DetectorCalls: 2, CacheHits: 3}})
	metrics.observe(InputModerationResult{Status: "passed", Diagnostics: &InputModerationDiagnostics{CompletedChecks: 5}})
	require.Equal(t, uint64(1), metrics.Results["failed/timeout"])
	require.Equal(t, uint64(1), metrics.Results["passed/"])
	require.Len(t, metrics.Results, 2)
	require.Equal(t, uint64(2), metrics.DetectorCalls)
	require.Equal(t, uint64(3), metrics.CacheHits)
}
