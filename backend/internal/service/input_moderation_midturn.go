package service

import (
	"regexp"
	"strings"
)

// Claude Code delivers a message the user types while a tool runs inside the
// running turn instead of as a new user turn (measured on Claude Code 2.1.322
// and 2.1.326: a role "system" message after the tool_result; older clients
// may put the text next to the tool_result). The wording below is the
// client's fixed framing around the user's words.
const (
	claudeCodeMidTurnPrefix = "The user sent a new message while you were working:"
	claudeCodeMidTurnSuffix = "This is how Claude Code surfaces messages the user sends mid-turn"
	systemReminderClose     = "</system-reminder>"
)

var systemReminderEnvelope = regexp.MustCompile(`(?is)<system-reminder>.*?</system-reminder>`)

// extractMidTurnUserText returns user-authored text delivered during the
// current turn of a request whose last message is not a real user message:
// the client-framed mid-turn messages in the trailing user/system messages
// after the last assistant message, plus plain text placed next to a
// tool_result (system reminders removed). Earlier turns are not repeated, and
// other system message text stays out of the user input.
func extractMidTurnUserText(messages []any) string {
	start := len(messages)
	for start > 0 {
		msg, _ := messages[start-1].(map[string]any)
		if role, _ := msg["role"].(string); role == "assistant" {
			break
		}
		start--
	}
	var parts []string
	for _, item := range messages[start:] {
		msg, _ := item.(map[string]any)
		role, _ := msg["role"].(string)
		if role != "user" && role != "system" {
			continue
		}
		for _, text := range messageTextBlocks(msg["content"]) {
			segments, rest := splitMidTurnMessages(text)
			parts = append(parts, segments...)
			if role == "user" {
				if plain := strings.TrimSpace(systemReminderEnvelope.ReplaceAllString(rest, "")); plain != "" {
					parts = append(parts, plain)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

// messageTextBlocks lists top-level text of a message; tool results are skipped.
func messageTextBlocks(content any) []string {
	switch value := content.(type) {
	case string:
		return []string{value}
	case []any:
		var texts []string
		for _, item := range value {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if typeName, _ := block["type"].(string); typeName != "" && typeName != "text" {
				continue
			}
			if text, _ := block["text"].(string); text != "" {
				texts = append(texts, text)
			}
		}
		return texts
	}
	return nil
}

// splitMidTurnMessages extracts the user's words from each client-framed
// mid-turn message and returns the remaining text without those messages
// (framing and the client's explanatory paragraph included).
func splitMidTurnMessages(text string) ([]string, string) {
	var segments []string
	var rest strings.Builder
	for {
		idx := strings.Index(text, claudeCodeMidTurnPrefix)
		if idx < 0 {
			rest.WriteString(text)
			return segments, rest.String()
		}
		rest.WriteString(text[:idx])
		body := text[idx+len(claudeCodeMidTurnPrefix):]
		end, resume := len(body), len(body)
		if i := strings.Index(body, claudeCodeMidTurnSuffix); i >= 0 {
			end = i
			// Drop the explanatory paragraph that follows the user's words.
			resume = len(body)
			if j := strings.Index(body[i:], "\n\n"); j >= 0 {
				resume = i + j
			}
			if j := strings.Index(body[i:], systemReminderClose); j >= 0 && i+j < resume {
				resume = i + j
			}
		}
		if i := strings.Index(body, systemReminderClose); i >= 0 && i < end {
			end, resume = i, i
		}
		if segment := strings.TrimSpace(body[:end]); segment != "" {
			segments = append(segments, segment)
		}
		text = body[resume:]
	}
}
