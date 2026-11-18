package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInputModerationScopeDefaultsToUserInput(t *testing.T) {
	explicit := &InputModerationPolicy{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"PII"}}
	require.False(t, InputModerationIncludesInstructions(nil))
	require.False(t, InputModerationIncludesInstructions(&Group{}), "legacy nil policy")
	require.False(t, InputModerationIncludesInstructions(&Group{InputModerationPolicy: explicit}), "empty scope")
	userInput := *explicit
	userInput.InputScope = InputModerationScopeUserInput
	require.False(t, InputModerationIncludesInstructions(&Group{InputModerationPolicy: &userInput}))
	full := *explicit
	full.InputScope = InputModerationScopeFull
	require.True(t, InputModerationIncludesInstructions(&Group{InputModerationPolicy: &full}))

	instructions := []string{"CLAUDE.md rules"}
	require.Nil(t, ScopedModerationInstructions(&Group{InputModerationPolicy: explicit}, instructions))
	require.Equal(t, instructions, ScopedModerationInstructions(&Group{InputModerationPolicy: &full}, instructions))
}

func TestInputModerationScopeValidation(t *testing.T) {
	for _, scope := range []string{"", InputModerationScopeUserInput, InputModerationScopeFull} {
		p := &InputModerationPolicy{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"PII"}, InputScope: scope}
		require.NoError(t, ValidateInputModerationPolicy(p), scope)
	}
	for _, scope := range []string{"FULL", "instructions", " full"} {
		p := &InputModerationPolicy{RulesVersion: JevExpandedRulesVersion, AuditCategories: []string{"PII"}, InputScope: scope}
		require.Error(t, ValidateInputModerationPolicy(p), scope)
	}
}

// Jobs queued with instructions (before an upgrade or a scope switch) follow
// the group's current scope when processed.
func TestInputModerationProcessDropsQueuedInstructionsForUserInputScope(t *testing.T) {
	repo := &moderationResultRepoStub{}
	client := &chunkModerationStub{}
	groups := &inputModerationGroupRepoStub{groups: []*Group{{ID: 3, InputModerationEnabled: true, InputModerationMinConfidence: 90}}}
	svc := newInputModerationServiceForTest(nil, repo, groups, nil, nil)
	svc.client = client
	defer svc.Stop()

	require.NoError(t, svc.process(InputModerationTask{RequestID: "r1", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "question", Instructions: []string{"DANGER rules"}}))
	require.Equal(t, []string{"question"}, client.texts, "only the user text reaches the detector")
	require.Len(t, repo.results, 1)
	require.Equal(t, "passed", repo.results[0].Status)
	require.Zero(t, repo.results[0].Diagnostics.InstructionChars)
	require.Zero(t, repo.results[0].Diagnostics.InstructionBlocks)

	client.texts = nil
	require.NoError(t, svc.process(InputModerationTask{RequestID: "r2", UserID: 1, APIKeyID: 2, GroupID: 3, Instructions: []string{"DANGER rules"}}))
	require.Empty(t, client.texts, "instruction-only turns make no detector call")
	require.Len(t, repo.results, 2)
	require.Equal(t, "skipped", repo.results[1].Status)
	require.Equal(t, "no_new_user_text", repo.results[1].Reason)
}

func TestInputModerationProcessKeepsInstructionsForFullScope(t *testing.T) {
	repo := &moderationResultRepoStub{}
	client := &chunkModerationStub{}
	groups := &inputModerationGroupRepoStub{groups: []*Group{fullScopeGroup(&Group{ID: 3, InputModerationEnabled: true, InputModerationMinConfidence: 90})}}
	svc := newInputModerationServiceForTest(nil, repo, groups, nil, nil)
	svc.client = client
	defer svc.Stop()

	require.NoError(t, svc.process(InputModerationTask{RequestID: "r1", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "question", Instructions: []string{"stable rules"}}))
	require.ElementsMatch(t, []string{"stable rules\n\nquestion", "stable rules"}, client.texts)
}
