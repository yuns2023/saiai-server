package service

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestModerationContextExtraction(t *testing.T) {
	parsed := &ParsedRequest{System: []any{map[string]any{"type": "text", "text": "system project rules"}}, Messages: []any{
		map[string]any{"role": "user", "content": "# AGENTS.md\nproject instructions"},
		map[string]any{"role": "assistant", "content": "unrelated assistant history"},
		map[string]any{"role": "user", "content": "new question"},
	}}
	text, instructions := ExtractAnthropicModerationInput(parsed)
	require.Equal(t, "new question", text)
	require.Equal(t, []string{"system project rules", "# AGENTS.md\nproject instructions"}, instructions)
	text, instructions = ExtractOpenAIModerationInput([]byte(`{"instructions":"global instructions","input":[{"role":"developer","content":[{"type":"input_text","text":"developer rules"}]},{"role":"user","content":"AGENTS.md project rules"},{"type":"function_call_output","output":"unrelated output"},{"role":"user","content":"question"}]}`))
	require.Equal(t, "question", text)
	require.Equal(t, []string{"global instructions", "developer rules", "AGENTS.md project rules"}, instructions)
	// Instructions still get checked on tool-only requests without reviving old questions.
	text, instructions = ExtractOpenAIModerationInput([]byte(`{"instructions":"rules","input":[{"role":"user","content":"old question"},{"type":"function_call_output","output":"result"}]}`))
	require.Empty(t, text)
	require.Equal(t, []string{"rules"}, instructions)
}
func TestModerationProjectFileRead(t *testing.T) {
	parsed := &ParsedRequest{Messages: []any{
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "read1", "name": "Read", "input": map[string]any{"file_path": "C:\\project\\CLAUDE.md"}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "read1", "content": "project file body"}, map[string]any{"type": "tool_result", "tool_use_id": "unrelated", "content": "secret unrelated output"}}},
	}}
	text, instructions := ExtractAnthropicModerationInput(parsed)
	require.Empty(t, text)
	require.Equal(t, []string{"project file body"}, instructions)
}
func TestModerationChunksCoverTailAndUnicode(t *testing.T) {
	input := strings.Repeat("文", 35) + "尾部风险"
	chunks, err := moderationChunks(input, 16)
	require.NoError(t, err)
	require.Len(t, chunks, 3)
	require.True(t, strings.HasSuffix(chunks[len(chunks)-1], "尾部风险"))
	reconstructed := chunks[0]
	for _, chunk := range chunks[1:] {
		reconstructed += string([]rune(chunk)[2:])
	}
	require.Equal(t, input, reconstructed)
	_, err = moderationChunks(strings.Repeat("a", 300), 16)
	require.ErrorIs(t, err, errModerationTooLarge)
}
func TestModerationStableAndJointPlans(t *testing.T) {
	chunks, coverage, err := moderationTaskChunks(InputModerationTask{Text: "question", Instructions: []string{"stable rules"}}, 32768)
	require.NoError(t, err)
	require.Empty(t, coverage)
	require.Equal(t, []string{"stable rules\n\nquestion", "stable rules"}, chunks)
}

type chunkModerationStub struct{ texts []string }

func (c *chunkModerationStub) Classify(_ context.Context, text string) (*InputModerationClassification, error) {
	c.texts = append(c.texts, text)
	score := 0.1
	if strings.Contains(text, "DANGER") {
		score = .95
	}
	return &InputModerationClassification{Safety: "safe", Scores: map[string]float64{"PII": score}, ModelVersion: "fixture"}, nil
}
func TestModerationChunkTailRaisesRisk(t *testing.T) {
	client := &chunkModerationStub{}
	svc := &InputModerationService{client: client}
	svc.cfg.MaxInputChars = 16
	result, coverage, err := svc.classifyInput(context.Background(), InputModerationTask{UserID: 1, GroupID: 2, Text: strings.Repeat("x", 20) + "DANGER"}, &Group{})
	require.NoError(t, err)
	require.Equal(t, moderationCoverageChunked, coverage)
	require.Equal(t, .95, result.Scores["PII"])
	require.Greater(t, len(client.texts), 1)
}

func TestModerationEmbeddedInstructionReuse(t *testing.T) {
	envelope := "<system-reminder>Contents of CLAUDE.md: stable project rules</system-reminder>"
	text, parts := separateProjectInstructions(envelope+"\nnew question", nil)
	require.Equal(t, "new question", text)
	require.Equal(t, []string{envelope}, parts)
	text, parts = separateProjectInstructions("<system-reminder>ordinary context</system-reminder>question", nil)
	require.Contains(t, text, "ordinary context")
	require.Empty(t, parts)
	text, parts = separateProjectInstructions("<system-reminder>CLAUDE.md unclosed", nil)
	require.Contains(t, text, "unclosed")
	require.Empty(t, parts)
}
func TestModerationOversizeRecordsFailureWithoutInference(t *testing.T) {
	repo := &moderationResultRepoStub{}
	client := &inputModerationClientStub{}
	svc := newInputModerationServiceForTest(client, repo, nil, nil, nil)
	defer svc.Stop()
	svc.cfg.MaxInputChars = 16
	require.False(t, svc.Submit(InputModerationTask{RequestID: "oversize", UserID: 1, APIKeyID: 2, GroupID: 3, Text: strings.Repeat("x", 300)}))
	require.Zero(t, client.calls)
	require.Len(t, repo.results, 1)
	require.Equal(t, "failed", repo.results[0].Status)
	require.Equal(t, "input_too_large", repo.results[0].Reason)
}

func TestModerationStableSystemBlocks(t *testing.T) {
	parsed := &ParsedRequest{System: []any{map[string]any{"type": "text", "text": "variable request metadata"}, map[string]any{"type": "text", "text": "stable project instructions"}}}
	_, parts := ExtractAnthropicModerationInput(parsed)
	require.Equal(t, []string{"variable request metadata", "stable project instructions"}, parts)
}

func TestModerationProjectReminderAlongsideToolResult(t *testing.T) {
	parsed := &ParsedRequest{Messages: []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "<system-reminder>CLAUDE.md: project instructions</system-reminder>"},
		map[string]any{"type": "tool_result", "tool_use_id": "unrelated", "content": "ordinary output"},
	}}}}
	text, parts := ExtractAnthropicModerationInput(parsed)
	require.Empty(t, text)
	require.Equal(t, []string{"<system-reminder>CLAUDE.md: project instructions</system-reminder>"}, parts)
}

func TestModerationInstructionBundlesPackSmallBlocksStably(t *testing.T) {
	var blocks []string
	for block := 0; block < 136; block++ {
		blocks = append(blocks, fmt.Sprintf("<system-reminder>project rule %d: %s</system-reminder>", block, strings.Repeat("keep it tidy ", 30)))
	}
	bundles := moderationInstructionBundles(blocks, 32768)
	require.Less(t, len(bundles), 40)
	joined := strings.Join(bundles, "\n\n")
	require.Equal(t, strings.Join(blocks, "\n\n"), joined) // order and content preserved
	for _, bundle := range bundles {
		require.LessOrEqual(t, utf8.RuneCountInString(bundle), 32768)
	}
	// Editing one block changes its bundle and at most one neighbour.
	edited := append([]string(nil), blocks...)
	edited[70] = "edited rule"
	after := moderationInstructionBundles(edited, 32768)
	unchanged := map[string]bool{}
	for _, bundle := range after {
		unchanged[bundle] = true
	}
	changed := 0
	for _, bundle := range bundles {
		if !unchanged[bundle] {
			changed++
		}
	}
	require.LessOrEqual(t, changed, 2)
	// Oversized blocks stay standalone; empty blocks vanish.
	big := strings.Repeat("b", 40000)
	require.Equal(t, []string{"a", big, "c"}, moderationInstructionBundles([]string{"a", big, " ", "c"}, 32768)[0:3:3][:3])
}

func TestModerationManySmallInstructionsNoLongerExceedPlan(t *testing.T) {
	task := InputModerationTask{Text: "new question"}
	for block := 0; block < 136; block++ {
		task.Instructions = append(task.Instructions, fmt.Sprintf("rule %d %s", block, strings.Repeat("x", 450)))
	}
	task.Diagnostics = newModerationDiagnostics(task, 32768)
	chunks, coverage, err := moderationTaskChunks(task, 32768)
	require.NoError(t, err)
	require.Equal(t, moderationCoverageChunked, coverage)
	require.Less(t, len(chunks), 16)
	require.Equal(t, len(chunks), task.Diagnostics.PlannedChecks)
	require.Positive(t, task.Diagnostics.InstructionBundles)
	require.False(t, task.Diagnostics.JointSkipped)
}

func TestModerationOversizedJointChecksNewTextAlone(t *testing.T) {
	task := InputModerationTask{Text: "question DANGER"}
	for block := 0; block < 40; block++ {
		task.Instructions = append(task.Instructions, fmt.Sprintf("rule %d %s", block, strings.Repeat("y", 15000)))
	}
	task.Diagnostics = newModerationDiagnostics(task, 32768)
	chunks, coverage, err := moderationTaskChunks(task, 32768)
	require.NoError(t, err)
	require.Equal(t, moderationCoverageJointSkipped, coverage)
	require.Equal(t, "question DANGER", chunks[0])
	require.True(t, task.Diagnostics.JointSkipped)
	// Without new text the plan holds only the instruction bundles.
	task.Text = ""
	chunks, coverage, err = moderationTaskChunks(task, 32768)
	require.NoError(t, err)
	require.Equal(t, moderationCoverageJointSkipped, coverage)
	for _, chunk := range chunks {
		require.NotEmpty(t, chunk)
	}
	// New text that is itself too large still fails.
	task.Text = strings.Repeat("z", 600000)
	_, _, err = moderationTaskChunks(task, 32768)
	require.ErrorIs(t, err, errModerationTooLarge)
}

func TestModerationJointSkippedIsRecordedAsCoverageReason(t *testing.T) {
	repo := &moderationResultRepoStub{}
	groups := &inputModerationGroupRepoStub{groups: []*Group{{ID: 3, InputModerationEnabled: true}}}
	svc := newInputModerationServiceForTest(nil, repo, groups, nil, nil)
	client := &cachedModerationBudgetStub{checks: map[string]bool{}}
	svc.client = client
	svc.cfg.MaxInputChars = 32768
	defer svc.Stop()
	task := InputModerationTask{RequestID: "joint", UserID: 1, APIKeyID: 2, GroupID: 3, Text: "question"}
	for block := 0; block < 40; block++ {
		task.Instructions = append(task.Instructions, fmt.Sprintf("rule %d %s", block, strings.Repeat("y", 15000)))
	}
	// Warm instruction bundles from earlier turns are cache hits.
	for _, bundle := range moderationInstructionBundles(task.Instructions, 32768) {
		client.checks[bundle] = true
	}
	require.NoError(t, svc.process(task))
	require.Len(t, repo.results, 1)
	// Coverage is disclosed whatever the verdict (this bare group flags any score).
	require.Equal(t, moderationCoverageJointSkipped, repo.results[0].Reason)
	require.Equal(t, 1, repo.results[0].Diagnostics.DetectorCalls)
}
