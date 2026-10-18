package service

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestJevPolicyValidationAndRegistry(t *testing.T) {
	catalog := JevCategoryCatalog()
	require.Len(t, catalog, 19)
	seen := map[string]bool{}
	legacy := 0
	for _, c := range catalog {
		require.False(t, seen[c.ID])
		seen[c.ID] = true
		require.Empty(t, c.Rule)
		if c.Legacy {
			legacy++
		} else {
			require.False(t, c.EnforcementAllowed)
		}
	}
	require.Equal(t, 4, legacy)
	for _, p := range []*InputModerationPolicy{
		{RulesVersion: "unknown"},
		{RulesVersion: JevLegacyRulesVersion, AuditCategories: []string{"violence"}},
		{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"unknown"}},
		{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"PII", "PII"}},
		{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"PII"}, EnforcementCategories: []string{"Jailbreak"}},
		{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"self-harm/intent"}, EnforcementCategories: []string{"self-harm/intent"}},
		{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"violence"}, EnforcementCategories: []string{"violence"}},
		{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"PII"}, Thresholds: map[string]int{"PII": 101}},
		{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"PII"}, Thresholds: map[string]int{"violence": 90}},
	} {
		require.Error(t, ValidateInputModerationPolicy(p))
	}
	require.NoError(t, ValidateInputModerationPolicy(&InputModerationPolicy{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"PII", "violence"}, EnforcementCategories: []string{"PII"}, Thresholds: map[string]int{"PII": 95}}))
	require.NoError(t, ValidateLegacyModerationCategories([]string{"pii"}))
	require.Error(t, ValidateLegacyModerationCategories([]string{"Violent"}))
}

type policyModerationStub struct {
	scores map[string]float64
	calls  int
}

func (s *policyModerationStub) Classify(context.Context, string) (*InputModerationClassification, error) {
	panic("versioned policy must use policy client")
}
func (s *policyModerationStub) ClassifyWithPolicy(_ context.Context, _ string, _ *InputModerationPolicy) (*InputModerationClassification, error) {
	s.calls++
	return &InputModerationClassification{Safety: "unsafe", Scores: s.scores, ModelVersion: "TEST_ONLY"}, nil
}
func TestJevPolicySeparatesAuditAndEnforcement(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enforce    []string
		scores     map[string]float64
		threshold  int
		wantStatus string
		wantEvents int
		wantAction string
	}{
		{"new category only", []string{"PII"}, map[string]float64{"PII": .1, "violence": .99}, 90, "flagged", 1, InputModerationActionCategoryNotSelected},
		{"empty enforcement", []string{}, map[string]float64{"PII": .99, "violence": .1}, 90, "flagged", 1, InputModerationActionCategoryNotSelected},
		{"category threshold", []string{"PII"}, map[string]float64{"PII": .92, "violence": .1}, 95, "passed", 0, ""},
		{"approved category", []string{"PII"}, map[string]float64{"PII": .95, "violence": .99}, 95, "flagged", 1, InputModerationActionWarning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &InputModerationPolicy{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"PII", "violence"}, EnforcementCategories: tc.enforce, Thresholds: map[string]int{"PII": tc.threshold}}
			g := &Group{ID: 3, InputModerationEnabled: true, InputModerationAutoDisableUser: true, InputModerationMinConfidence: 90, InputModerationPolicy: p}
			repo := &moderationResultRepoStub{}
			repo.transition = &InputModerationTransition{Action: InputModerationActionWarning}
			svc := newInputModerationServiceForTest(&policyModerationStub{scores: tc.scores}, repo, &inputModerationGroupRepoStub{groups: []*Group{g}}, nil, nil)
			defer svc.Stop()
			require.NoError(t, svc.process(InputModerationTask{RequestID: "request", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "TEST_ONLY"}))
			require.Len(t, repo.results, 1)
			require.Equal(t, tc.wantStatus, repo.results[0].Status)
			require.Len(t, repo.events, tc.wantEvents)
			if tc.wantEvents > 0 {
				require.Equal(t, tc.wantAction, repo.events[0].Action)
			}
			require.Equal(t, tc.threshold, repo.results[0].DecisionSnapshot.Policy.Thresholds["PII"])
			require.Equal(t, 90, repo.results[0].DecisionSnapshot.Policy.Thresholds["violence"])
		})
	}
}
func TestJevPolicyChangeDuringInferenceSkipsAndMissingScoresFail(t *testing.T) {
	before := &Group{ID: 3, InputModerationEnabled: true, InputModerationMinConfidence: 90, InputModerationPolicy: &InputModerationPolicy{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"PII"}}}
	after := *before
	after.InputModerationPolicy = &InputModerationPolicy{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"violence"}}
	repo := &moderationResultRepoStub{}
	svc := newInputModerationServiceForTest(&policyModerationStub{scores: map[string]float64{"PII": .99}}, repo, &inputModerationGroupRepoStub{groups: []*Group{before, &after}}, nil, nil)
	defer svc.Stop()
	require.NoError(t, svc.process(InputModerationTask{RequestID: "request", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "TEST_ONLY"}))
	require.Equal(t, "policy_changed", repo.results[0].Reason)
	require.Empty(t, repo.events)
	svc.groupRepo = &inputModerationGroupRepoStub{groups: []*Group{&after}}
	require.ErrorContains(t, svc.process(InputModerationTask{RequestID: "request2", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "TEST_ONLY"}), "incomplete moderation scores")
	require.Equal(t, "failed", repo.results[1].Status)
}
func TestJevLegacyPolicyEmptyStillMeansAllAndSnapshotsAreIndependent(t *testing.T) {
	g := &Group{InputModerationMinConfidence: 90}
	p := EffectiveInputModerationPolicy(g)
	require.Len(t, p.AuditCategories, 4)
	require.Len(t, p.EnforcementCategories, 4)
	g.InputModerationPolicy = &InputModerationPolicy{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"PII"}, EnforcementCategories: []string{}}
	snapshot := moderationDecisionSnapshot(g)
	snapshot.Policy.Thresholds["PII"] = 95
	require.Empty(t, g.InputModerationPolicy.Thresholds)
	require.Empty(t, snapshot.Policy.EnforcementCategories)
	data, err := json.Marshal(APIKeyAuthGroupSnapshot{InputModerationPolicy: g.InputModerationPolicy})
	require.NoError(t, err)
	require.Contains(t, string(data), "jev-expanded-v1")
}
