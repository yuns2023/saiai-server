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
// remaining text after the client's explanation (other reminders). When the
// boundary is ambiguous the words end early: client text is never attributed
// to the user. Each call is linear in the block length.
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
	suffixAt := firstParagraphIndex(body, claudeCodeMidTurnSuffix)
	closeAt := -1
	if wrapped {
		closeAt = strings.Index(body, systemReminderClose)
	}
	switch {
	case suffixAt >= 0 && (closeAt < 0 || suffixAt < closeAt):
		words = body[:suffixAt]
		after := body[suffixAt:]
		end := earliestIndex(after, "\n\n", "\r\n\r\n")
		if wrapped {
			if i := strings.Index(after, systemReminderClose); i >= 0 && (end < 0 || i < end) {
				end = i
			}
		}
		if end >= 0 {
			rest = after[end:]
		}
	case closeAt >= 0:
		words = body[:closeAt]
		rest = body[closeAt:]
	default:
		words = body
	}
	if wrapped {
		rest = strings.Replace(rest, systemReminderClose, "\n", 1)
	}
	return strings.TrimSpace(words), strings.TrimSpace(rest), true
}

// firstParagraphIndex finds the first occurrence of marker that starts a
// paragraph, scanning forward once.
func firstParagraphIndex(text, marker string) int {
	for offset := 0; offset < len(text); {
		i := strings.Index(text[offset:], marker)
		if i < 0 {
			return -1
		}
		i += offset
		before := strings.TrimRight(text[:i], " \t")
		if strings.HasSuffix(before, "\n\n") || strings.HasSuffix(before, "\r\n\r\n") {
			return i
		}
		offset = i + len(marker)
	}
	return -1
}

func earliestIndex(text string, seps ...string) int {
	best := -1
	for _, sep := range seps {
		if i := strings.Index(text, sep); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
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
			if plainAllowed {
				if plain := plainUserText(text); plain != "" {
					parts = append(parts, plain)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

// plainUserText keeps the text outside closed <system-reminder> envelopes, in
// order; an unclosed envelope drops the rest of the block, and client
// interruption notices are not user words.
func plainUserText(text string) string {
	var out strings.Builder
	for {
		open := strings.Index(text, systemReminderOpen)
		if open < 0 {
			out.WriteString(text)
			break
		}
		out.WriteString(text[:open])
		closeAt := strings.Index(text[open:], systemReminderClose)
		if closeAt < 0 {
			break
		}
		out.WriteString("\n")
		text = text[open+closeAt+len(systemReminderClose):]
	}
	plain := strings.TrimSpace(out.String())
	for _, notice := range claudeCodeToolResultNotices {
		if strings.HasPrefix(plain, notice) {
			return ""
		}
	}
	return plain
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

// withoutMidTurnWords replaces framed mid-turn blocks of a message by
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
