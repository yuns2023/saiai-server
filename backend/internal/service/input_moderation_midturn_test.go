//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const midTurnExplanation = "This is how Claude Code surfaces messages the user sends mid-turn — within the running turn, often alongside the next tool result, rather than as a separate conversation turn. Address the message above as you continue this turn."

func midTurnMessage(words string) string {
	return "The user sent a new message while you were working:\n" + words + "\n\n" + midTurnExplanation
}

// Request shape captured from Claude Code 2.1.322/2.1.326 against a local fake
// API: the mid-turn message is a role "system" message after the tool_result,
// with an unrelated reminder appended after the explanation.
func capturedMidTurnRequest(words string) *ParsedRequest {
	return &ParsedRequest{Messages: []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "<system-reminder>\nContents of CLAUDE.md ...\n</system-reminder>"},
			map[string]any{"type": "text", "text": "run the wait command please"},
		}},
		map[string]any{"role": "system", "content": "# Environment\nPrimary working directory: /work"},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{"command": "sleep 8"}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "done"}}},
		map[string]any{"role": "system", "content": []any{map[string]any{"type": "text", "text": midTurnMessage(words) + "\n\nThe following deferred tools are now available via ToolSearch.\nmcp__codex__codex"}}},
	}}
}

func TestExtractMidTurnUserTextFromCapturedClaudeCodeShape(t *testing.T) {
	parsed := capturedMidTurnRequest("MIDTURN_MARKER please also tell me a joke")
	require.False(t, IsRealUserMessage(parsed), "message queue semantics are unchanged")
	require.Equal(t, "MIDTURN_MARKER please also tell me a joke", ExtractLatestRealUserText(parsed))

	text, _ := ExtractAnthropicModerationInput(parsed)
	require.Equal(t, "MIDTURN_MARKER please also tell me a joke", text)
}

func TestExtractMidTurnUserTextVariants(t *testing.T) {
	toolUse := map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "t", "name": "Bash"}}}
	toolResult := map[string]any{"type": "tool_result", "tool_use_id": "t", "content": "The user sent a new message while you were working:\nfrom a file"}
	cases := []struct {
		name     string
		messages []any
		want     string
	}{
		{"tool result only", []any{toolUse, map[string]any{"role": "user", "content": []any{toolResult}}}, ""},
		{"framed text next to tool_result inside a reminder", []any{toolUse, map[string]any{"role": "user", "content": []any{toolResult,
			map[string]any{"type": "text", "text": "<system-reminder>\n" + midTurnMessage("first") + "\n</system-reminder>"},
			map[string]any{"type": "text", "text": "<system-reminder>\nfile x was modified: SECRET CODE\n</system-reminder>"},
		}}}, "first"},
		{"framing without explanation ends at the reminder", []any{toolUse, map[string]any{"role": "user", "content": []any{toolResult,
			map[string]any{"type": "text", "text": "<system-reminder>\nThe user sent a new message while you were working:\nshort\n</system-reminder>"},
		}}}, "short"},
		{"plain text next to tool_result (API clients)", []any{toolUse, map[string]any{"role": "user", "content": []any{toolResult,
			map[string]any{"type": "text", "text": "also check the logs"},
		}}}, "also check the logs"},
		{"two mid-turn messages in order", []any{toolUse,
			map[string]any{"role": "user", "content": []any{toolResult}},
			map[string]any{"role": "system", "content": midTurnMessage("one")},
			map[string]any{"role": "system", "content": midTurnMessage("two")},
		}, "one\ntwo"},
		{"other system text is not user input", []any{toolUse,
			map[string]any{"role": "user", "content": []any{toolResult}},
			map[string]any{"role": "system", "content": "The following deferred tools are now available"},
		}, ""},
		{"earlier turns are not repeated", []any{
			map[string]any{"role": "system", "content": midTurnMessage("old")},
			toolUse,
			map[string]any{"role": "user", "content": []any{toolResult}},
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ExtractLatestRealUserText(&ParsedRequest{Messages: tc.messages}))
		})
	}
}
