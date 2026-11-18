package service

import "strings"

// Claude Code delivers a message the user types while a tool runs inside the
// running turn instead of as a new user turn. Measured on Claude Code 2.1.322
// and 2.1.326 against a local fake API: a role "system" message after the
// tool_result whose text is
//
//	The user sent a new message while you were working:\n<words>\n\n
//	This is how Claude Code surfaces messages ... continue this turn.\n\n<other reminders>
//
// Older clients may place the same framing in a text block next to the
// tool_result, optionally wrapped in <system-reminder>.
const (
	claudeCodeMidTurnPrefix = "The user sent a new message while you were working:"
	claudeCodeMidTurnSuffix = "This is how Claude Code surfaces messages the user sends mid-turn"
	systemReminderOpen      = "<system-reminder>"
	systemReminderClose     = "</system-reminder>"
)

// Client-generated texts that accompany tool results without being user words.
var claudeCodeToolResultNotices = []string{"[Request interrupted by user"}

// parseMidTurnFrame recognizes a text block that IS a framed mid-turn message:
// the framing must open the block (after optional whitespace and an optional
// <system-reminder> wrapper), so files, tool output or reminders that merely
// quote the sentence are not user input. It returns the user's words and the
// text after the client's explanation (other reminders appended to the block).
// The user's words end at the LAST explanation paragraph, so words quoting the
// explanation or a literal closing tag stay intact. Each call is linear.
func parseMidTurnFrame(text string) (words, rest string, ok bool) {
	body := strings.TrimLeft(text, " \t\r\n")
	wrapped := strings.HasPrefix(body, systemReminderOpen)
	if wrapped {
		body = strings.TrimLeft(body[len(systemReminderOpen):], " \t\r\n")
	}
	if !strings.HasPrefix(body, claudeCodeMidTurnPrefix) {
		return "", "", false
	}
	body = body[len(claudeCodeMidTurnPrefix):]
	if wrapped {
		if i := strings.LastIndex(body, systemReminderClose); i >= 0 {
			rest = body[i+len(systemReminderClose):]
			body = body[:i]
		}
	}
	if i := lastParagraphIndex(body, claudeCodeMidTurnSuffix); i >= 0 {
		after := body[i:]
		end := len(after)
		if j := strings.Index(after, "\n\n"); j >= 0 {
			end = j
		} else if j := strings.Index(after, "\r\n\r\n"); j >= 0 {
			end = j
		}
		rest = after[end:] + rest
		body = body[:i]
	}
	return strings.TrimSpace(body), strings.TrimSpace(rest), true
}

// lastParagraphIndex finds the last occurrence of marker that starts a
// paragraph, scanning forward once.
func lastParagraphIndex(text, marker string) int {
	found := -1
	for offset := 0; offset < len(text); {
		i := strings.Index(text[offset:], marker)
		if i < 0 {
			break
		}
		i += offset
		before := strings.TrimRight(text[:i], " \t")
		if strings.HasSuffix(before, "\n\n") || strings.HasSuffix(before, "\r\n\r\n") {
			found = i
		}
		offset = i + len(marker)
	}
	return found
}

// extractMidTurnUserText returns user-authored text of the current turn when
// the last message is not a real user message: framed mid-turn messages in the
// user/system messages after the last assistant message, plus plain text
// blocks next to a tool_result in the same user message. Whole-block system
// reminders, client notices and tool results are skipped; earlier turns are
// not repeated; other system text stays out of the user input.
func extractMidTurnUserText(messages []any) string {
	start := -1
	for i := len(messages) - 1; i >= 0; i-- {
		msg, _ := messages[i].(map[string]any)
		if role, _ := msg["role"].(string); role == "assistant" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return ""
	}
	var parts []string
	for _, item := range messages[start:] {
		msg, _ := item.(map[string]any)
		role, _ := msg["role"].(string)
		if role != "user" && role != "system" {
			continue
		}
		plainAllowed := role == "user" && hasToolResultBlock(msg["content"])
		for _, text := range messageTextBlocks(msg["content"]) {
			if words, _, ok := parseMidTurnFrame(text); ok {
				if words != "" {
					parts = append(parts, words)
				}
				continue
			}
			if plainAllowed && !clientGeneratedToolResultText(text) {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
	}
	return strings.Join(parts, "\n")
}

func clientGeneratedToolResultText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.HasPrefix(trimmed, systemReminderOpen) {
		return true
	}
	for _, notice := range claudeCodeToolResultNotices {
		if strings.HasPrefix(trimmed, notice) {
			return true
		}
	}
	return false
}

func hasToolResultBlock(content any) bool {
	blocks, _ := content.([]any)
	for _, item := range blocks {
		block, _ := item.(map[string]any)
		if kind, _ := block["type"].(string); kind == "tool_result" || kind == "tool_use_result" {
			return true
		}
	}
	return false
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

// withoutMidTurnWords replaces framed mid-turn blocks of a system message by
// the reminders appended after the client's explanation, so in full scope the
// user's words are classified once, as user text, not again as instructions.
func withoutMidTurnWords(content any) any {
	switch value := content.(type) {
	case string:
		if _, rest, ok := parseMidTurnFrame(value); ok {
			return rest
		}
		return value
	case []any:
		out := make([]any, 0, len(value))
		for _, item := range value {
			block, ok := item.(map[string]any)
			if ok {
				if text, isText := block["text"].(string); isText {
					if typeName, _ := block["type"].(string); typeName == "" || typeName == "text" {
						if _, rest, framed := parseMidTurnFrame(text); framed {
							out = append(out, map[string]any{"type": "text", "text": rest})
							continue
						}
					}
				}
			}
			out = append(out, item)
		}
		return out
	}
	return content
}
