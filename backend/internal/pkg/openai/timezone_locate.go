package openai

import (
	"bytes"
	"strconv"

	"github.com/tidwall/gjson"
)

// CodexTimezoneLocation describes where in a request body a timezone guard
// rejection was triggered. It is location-only: it never holds string
// content, tag values, or object keys that are not plain identifiers, so it
// is safe to log.
type CodexTimezoneLocation struct {
	// Path is the JSON path of the string, e.g. `input[3].content[0].text`
	// ("$" for a root string). Keys outside codexLocationKeys are rendered as
	// "?"; the path is cut at codexLocationMaxPath bytes.
	Path string
	// InKey reports that the string is an object key (Path then ends at the
	// object holding it).
	InKey bool
	// ItemType and ItemRole are the `type` / `role` of the enclosing
	// top-level `input` item when present: a value from codexLocationItemTypes
	// / codexLocationRoles, "?" for any other value.
	ItemType, ItemRole string
	// StringBytes is the decoded length of the string.
	StringBytes int
	// TagOffset is the decoded offset of the matching CN `<timezone>` tag, -1
	// when the location is not a timezone match.
	TagOffset int
	// StartsWithEnvOpen reports whether the string, trimmed of leading ASCII
	// whitespace, starts with `<environment_context>`.
	StartsWithEnvOpen bool
	// EnvOpenCount and EnvCloseCount count the environment_context open and
	// close tags in the string.
	EnvOpenCount, EnvCloseCount int
	// EnvOpenOffset is the decoded offset of the last open tag before the
	// timezone tag (the first open tag when TagOffset is -1); EnvCloseOffset
	// is that of the first close tag after the timezone tag (after
	// EnvOpenOffset when TagOffset is -1). -1 when absent.
	EnvOpenOffset, EnvCloseOffset int
	// NearbyTags lists up to codexLocationMaxTags distinct tag names (in
	// order of appearance, `/` prefix for closing tags) found within
	// ±codexLocationTagWindow bytes of the timezone tag (of EnvOpenOffset when
	// TagOffset is -1). Only names in codexLocationTags are kept; any other
	// tag-shaped name is reported once as "?".
	NearbyTags []string
}

// Allowlists for logged identifiers. Everything logged is either a number,
// a fixed structural name from these sets, or "?": user-chosen JSON keys,
// enum-looking values and tag names never reach the log.
var (
	// codexLocationKeys are Responses request / Codex structural keys.
	codexLocationKeys = codexLocationSet(
		"model", "instructions", "input", "content", "type", "role", "text", "output", "arguments",
		"name", "call_id", "id", "status", "summary", "encrypted_content", "tools", "tool_choice",
		"parallel_tool_calls", "reasoning", "effort", "store", "stream", "include", "prompt_cache_key",
		"format", "metadata", "client_metadata", "previous_response_id", "service_tier",
		"internal_chat_message_metadata_passthrough", "content_item_kinds", "content_item_metadata",
		"provenance", "turn_id", "create_time", "action", "command", "image_url", "detail", "file_id",
		"filename", "file_data", "description", "parameters", "properties", "strict", "schema",
		"query", "queries", "sources", "result", "phase", "execution", "working_directory", "env",
		"timeout_ms", "user", "end_turn",
	)
	// codexLocationItemTypes are Responses / Codex input item and content types.
	codexLocationItemTypes = codexLocationSet(
		"message", "agent_message", "reasoning", "local_shell_call", "local_shell_call_output",
		"function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output",
		"tool_search_call", "tool_search_output", "web_search_call", "image_generation_call",
		"compaction", "compaction_summary", "context_compaction", "compaction_trigger",
		"configuration_update", "additional_tools", "item_reference", "file_search_call",
		"computer_call", "computer_call_output", "mcp_call", "mcp_list_tools",
		"mcp_approval_request", "mcp_approval_response",
	)
	codexLocationRoles = codexLocationSet("user", "assistant", "system", "developer", "tool")
	// codexLocationTags are the tag names Codex (0.163/0.164) renders in
	// environment_context and its other contextual fragments.
	codexLocationTags = codexLocationSet(
		"environment_context", "cwd", "shell", "shell_version", "current_date", "timezone",
		"filesystem", "workspace_roots", "root", "permission_profile", "file_system", "entry",
		"special", "path", "glob", "network", "allowed", "denied", "environments", "environment",
		"subagents", "approval_policy", "sandbox_mode", "network_access", "writable_roots",
		"writable_root", "user_instructions", "permissions", "apps_instructions",
		"collaboration_mode", "skills_instructions", "plugins_instructions",
		"environments_instructions", "multi_agent_mode", "realtime_conversation", "turn_aborted",
		"user_shell_command", "subagent_notification", "model_switch", "hook_prompt",
	)
)

func codexLocationSet(names ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

const (
	codexLocationTagWindow = 256
	codexLocationMaxTags   = 12
	codexLocationMaxPath   = 256
)

// LocateChinaTimezone is FindChinaTimezone that also describes where the
// match is. The search costs the same as FindChinaTimezone; the description
// is only computed after a match (one extra pass over body).
func LocateChinaTimezone(body []byte) (zone string, loc CodexTimezoneLocation, found bool) {
	zone, start, tagOffset, found := findChinaTimezone(body)
	if !found {
		return "", loc, false
	}
	loc, _ = describeCodexJSONString(body, start, tagOffset)
	return zone, loc, true
}

// LocateCodexJSONString describes the JSON string whose content starts at
// body offset start (see CodexEnvironmentMalformedOffset). body must be valid
// JSON; ok is false when start is not the start of a string's content.
func LocateCodexJSONString(body []byte, start int) (CodexTimezoneLocation, bool) {
	return describeCodexJSONString(body, start, -1)
}

func describeCodexJSONString(body []byte, start, tagOffset int) (CodexTimezoneLocation, bool) {
	loc := CodexTimezoneLocation{TagOffset: -1, EnvOpenOffset: -1, EnvCloseOffset: -1}
	if start <= 0 || start > len(body) || body[start-1] != '"' {
		return loc, false
	}
	path, inKey, item, ok := codexJSONPathAt(body, start)
	if !ok {
		return loc, false
	}
	loc.Path, loc.InKey = path, inKey
	if item >= 0 {
		prefix := "input." + strconv.Itoa(item) + "."
		loc.ItemType = codexLocationName(gjson.GetBytes(body, prefix+"type"), codexLocationItemTypes)
		loc.ItemRole = codexLocationName(gjson.GetBytes(body, prefix+"role"), codexLocationRoles)
	}
	end, escaped := jsonStringEnd(body, start)
	if end < 0 {
		return loc, false
	}
	text := body[start:end]
	if escaped {
		text = appendUnescapedJSONString(nil, text)
	}
	loc.StringBytes = len(text)
	trimmed := text
	for len(trimmed) > 0 && isASCIISpace(trimmed[0]) {
		trimmed = trimmed[1:]
	}
	loc.StartsWithEnvOpen = bytes.HasPrefix(trimmed, environmentContextOpenTag)
	loc.EnvOpenCount = bytes.Count(text, environmentContextOpenTag)
	loc.EnvCloseCount = bytes.Count(text, environmentContextCloseTag)
	var anchor int
	if tagOffset >= 0 && tagOffset <= len(text) {
		loc.TagOffset, anchor = tagOffset, tagOffset
		loc.EnvOpenOffset = bytes.LastIndex(text[:tagOffset], environmentContextOpenTag)
		loc.EnvCloseOffset = indexFrom(text, environmentContextCloseTag, tagOffset)
	} else {
		loc.EnvOpenOffset = bytes.Index(text, environmentContextOpenTag)
		anchor = loc.EnvOpenOffset
		loc.EnvCloseOffset = indexFrom(text, environmentContextCloseTag, max(anchor, 0))
	}
	if anchor >= 0 {
		loc.NearbyTags = codexNearbyTagNames(text, anchor)
	}
	return loc, true
}

func indexFrom(text, sep []byte, from int) int {
	at := bytes.Index(text[from:], sep)
	if at < 0 {
		return -1
	}
	return from + at
}

// codexNearbyTagNames returns the distinct tag names within the window
// around anchor: `/?[a-z_]{1,40}` followed by '>', ' ' or '/'. Names outside
// codexLocationTags become "?".
func codexNearbyTagNames(text []byte, anchor int) []string {
	lo, hi := max(anchor-codexLocationTagWindow, 0), min(anchor+codexLocationTagWindow, len(text))
	var names []string
	for pos := lo; pos < hi && len(names) < codexLocationMaxTags; {
		lt := bytes.IndexByte(text[pos:hi], '<')
		if lt < 0 {
			break
		}
		i := pos + lt + 1
		pos = i
		nameStart := i
		if i < len(text) && text[i] == '/' {
			i++
		}
		letters := i
		for i < len(text) && i-letters <= 40 && (text[i] == '_' || (text[i] >= 'a' && text[i] <= 'z')) {
			i++
		}
		if n := i - letters; n < 1 || n > 40 || i >= len(text) || (text[i] != '>' && text[i] != ' ' && text[i] != '/') {
			continue
		}
		name := string(text[nameStart:i])
		if _, known := codexLocationTags[string(text[letters:i])]; !known {
			name = "?"
		}
		seen := false
		for _, have := range names {
			if have == name {
				seen = true
				break
			}
		}
		if !seen {
			names = append(names, name)
		}
	}
	return names
}

// codexLocationName returns an allowlisted value as is, "?" for any other
// present value and "" when absent.
func codexLocationName(value gjson.Result, allowed map[string]struct{}) string {
	if !value.Exists() {
		return ""
	}
	if _, ok := allowed[value.Str]; ok && value.Type == gjson.String {
		return value.Str
	}
	return "?"
}

type codexPathFrame struct {
	array  bool
	index  int
	key    []byte // raw content of the current key (objects)
	keyEsc bool
}

// codexJSONPathAt walks body's tokens to the string whose content starts at
// target and renders its path. item is the index of the enclosing top-level
// `input` element (-1 outside it).
func codexJSONPathAt(body []byte, target int) (path string, inKey bool, item int, ok bool) {
	var frames []codexPathFrame
	expectKey := false
	for i := 0; i < len(body); {
		switch c := body[i]; c {
		case '"':
			start := i + 1
			end, escaped := jsonStringEnd(body, start)
			if end < 0 {
				return "", false, -1, false
			}
			isKey := expectKey && len(frames) > 0 && !frames[len(frames)-1].array
			expectKey = false
			if isKey {
				frames[len(frames)-1].key, frames[len(frames)-1].keyEsc = body[start:end], escaped
			}
			if start == target {
				return renderCodexJSONPath(frames, isKey), isKey, codexInputItemIndex(frames), true
			}
			if start > target {
				return "", false, -1, false
			}
			i = end + 1
			continue
		case '{', '[':
			frames = append(frames, codexPathFrame{array: c == '['})
			expectKey = c == '{'
		case '}', ']':
			if len(frames) > 0 {
				frames = frames[:len(frames)-1]
			}
			expectKey = false
		case ',':
			if len(frames) > 0 {
				if top := &frames[len(frames)-1]; top.array {
					top.index++
				} else {
					expectKey = true
				}
			}
		case ':':
			expectKey = false
		}
		i++
	}
	return "", false, -1, false
}

func codexInputItemIndex(frames []codexPathFrame) int {
	if len(frames) < 2 || frames[0].array || !frames[1].array ||
		!codexJSONStringEquals(frames[0].key, frames[0].keyEsc, "input") {
		return -1
	}
	return frames[1].index
}

func renderCodexJSONPath(frames []codexPathFrame, inKey bool) string {
	if inKey {
		frames = frames[:len(frames)-1]
	}
	if len(frames) == 0 {
		return "$"
	}
	b := make([]byte, 0, 64)
	for _, frame := range frames {
		if len(b) > codexLocationMaxPath {
			b = append(b, "..."...)
			break
		}
		if frame.array {
			b = append(b, '[')
			b = strconv.AppendInt(b, int64(frame.index), 10)
			b = append(b, ']')
			continue
		}
		if len(b) > 0 {
			b = append(b, '.')
		}
		key := frame.key
		if frame.keyEsc {
			key = appendUnescapedJSONString(nil, key)
		}
		if _, ok := codexLocationKeys[string(key)]; ok {
			b = append(b, key...)
		} else {
			b = append(b, '?')
		}
	}
	return string(b)
}
