//go:build unit

package service

import (
	"strings"
	"testing"
	"time"

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

	text, instructions := ExtractAnthropicModerationInput(parsed)
	require.Equal(t, "MIDTURN_MARKER please also tell me a joke", text)
	// Full scope: the words are classified once as user text, while the
	// reminder appended after the explanation remains an instruction.
	joined := strings.Join(instructions, "\n")
	require.NotContains(t, joined, "MIDTURN_MARKER")
	require.Contains(t, joined, "deferred tools are now available")
}

// The next request of the same turn carries the model's new tool call after
// the mid-turn message, so the words are not submitted again.
func TestExtractMidTurnUserTextNotRepeatedOnNextToolCall(t *testing.T) {
	parsed := capturedMidTurnRequest("joke please")
	parsed.Messages = append(parsed.Messages,
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "toolu_2", "name": "Bash"}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_2", "content": "ok"}}},
	)
	require.Equal(t, "", ExtractLatestRealUserText(parsed))
}

func TestParseMidTurnFrameAdversarial(t *testing.T) {
	cases := []struct {
		name, text, words, rest string
		ok                      bool
	}{
		{"quoted inside a reminder is not a frame", "<system-reminder>\nfile x changed:\n" + midTurnMessage("from a file") + "\n</system-reminder>", "", "", false},
		{"quoted mid-block is not a frame", "Note: " + midTurnMessage("x"), "", "", false},
		{"captured shape", midTurnMessage("w") + "\n\nThe following deferred tools", "w", "The following deferred tools", true},
		{"literal closing tag in unwrapped words", midTurnMessage("what does </system-reminder> mean?"), "what does </system-reminder> mean?", "", true},
		{"words quoting the explanation mid-line", midTurnMessage("why do you say " + claudeCodeMidTurnSuffix + "?"), "why do you say " + claudeCodeMidTurnSuffix + "?", "", true},
		{"appended reminder repeating the explanation stays client text", midTurnMessage("w") + "\n\n" + claudeCodeMidTurnSuffix + " again", "w", claudeCodeMidTurnSuffix + " again", true},
		{"wrapped frame with appended reminder", "<system-reminder>\n" + midTurnMessage("w") + "\n\nmore\n</system-reminder>\ntail", "w", "more\n\n\ntail", true},
		{"wrapped without explanation ends at the first close", "<system-reminder>\n" + claudeCodeMidTurnPrefix + "\nshort\n</system-reminder>\n<system-reminder>\nother reminder\n</system-reminder>", "short", "<system-reminder>\nother reminder\n</system-reminder>", true},
		{"close tag does not glue words", "<system-reminder>\n" + claudeCodeMidTurnPrefix + "\ninner</system-reminder>outer", "inner", "outer", true},
		{"no explanation, unwrapped: whole block", claudeCodeMidTurnPrefix + "\nonly words", "only words", "", true},
		{"CRLF", strings.ReplaceAll(midTurnMessage("crlf words")+"\n\nnext", "\n", "\r\n"), "crlf words", "next", true},
		{"earliest paragraph break after explanation", midTurnMessage("w") + "\r\n\r\nfirst\n\nsecond", "w", "first\n\nsecond", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			words, rest, ok := parseMidTurnFrame(tc.text)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.words, words)
			require.Equal(t, tc.rest, rest)
		})
	}
}

func TestPlainUserTextNextToToolResult(t *testing.T) {
	require.Equal(t, "also check the logs", plainUserText("<system-reminder>\nnote\n</system-reminder>\nalso check the logs"))
	require.Equal(t, "also check the logs", plainUserText("also check the logs\n<system-reminder>\nnote\n</system-reminder>"))
	require.Equal(t, "a\nb", plainUserText("a<system-reminder>x</system-reminder>b"))
	require.Equal(t, "before", plainUserText("before\n<system-reminder>\nunclosed"))
	require.Equal(t, "", plainUserText("[Request interrupted by user for tool use]"))
}

// Older clients put the frame next to the tool_result in a user message that
// may also hold project instructions: in full scope the words still count once.
func TestFullScopeOldUserFrameCountedOnce(t *testing.T) {
	parsed := &ParsedRequest{Messages: []any{
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "t", "name": "Bash"}}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t", "content": "ok"},
			map[string]any{"type": "text", "text": "<system-reminder>\n" + midTurnMessage("please explain CLAUDE.md") + "\n</system-reminder>"},
			map[string]any{"type": "text", "text": "<system-reminder>\nContents of CLAUDE.md: rules\n</system-reminder>"},
		}},
	}}
	text, instructions := ExtractAnthropicModerationInput(parsed)
	require.Equal(t, "please explain CLAUDE.md", text)
	joined := strings.Join(instructions, "\n")
	require.NotContains(t, joined, "please explain")
	require.Contains(t, joined, "Contents of CLAUDE.md: rules")
}

func TestParseMidTurnFrameLinearOnLongInput(t *testing.T) {
	noise := strings.Repeat(claudeCodeMidTurnSuffix+" x ", 20000)
	start := time.Now()
	words, _, ok := parseMidTurnFrame(claudeCodeMidTurnPrefix + "\n" + noise)
	require.True(t, ok)
	require.Equal(t, strings.TrimSpace(noise), words)
	require.Less(t, time.Since(start), 500*time.Millisecond)
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
		{"client interruption notice is not user input", []any{toolUse, map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t", "content": "x", "is_error": true},
			map[string]any{"type": "text", "text": "[Request interrupted by user for tool use]"},
		}}}, ""},
		{"plain text in a user message without tool_result is ignored", []any{toolUse,
			map[string]any{"role": "user", "content": []any{toolResult}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "stray"}}},
			map[string]any{"role": "system", "content": "notice"},
		}, ""},
		{"no assistant message", []any{map[string]any{"role": "system", "content": midTurnMessage("x")}, map[string]any{"role": "user", "content": []any{toolResult}}}, ""},
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
