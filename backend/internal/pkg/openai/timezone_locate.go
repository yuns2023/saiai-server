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
	// ("$" for a root string). Keys that are not `[A-Za-z0-9_]{1,64}` are
	// rendered as "?"; the path is cut at codexLocationMaxPath bytes.
	Path string
	// InKey reports that the string is an object key (Path then ends at the
	// object holding it).
	InKey bool
	// ItemType and ItemRole are the `type` / `role` of the enclosing
	// top-level `input` item, when present and `[a-z_]{1,40}` ("?" otherwise).
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
	// NearbyTags lists up to codexLocationMaxTags distinct tag names
	// (`/?[a-z_]{1,40}`, in order of appearance) found within
	// ±codexLocationTagWindow bytes of the timezone tag (of EnvOpenOffset when
	// TagOffset is -1).
	NearbyTags []string
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
		loc.ItemType = codexLocationName(gjson.GetBytes(body, prefix+"type"))
		loc.ItemRole = codexLocationName(gjson.GetBytes(body, prefix+"role"))
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
// around anchor; only names matching `/?[a-z_]{1,40}` followed by '>', ' '
// or '/' are kept.
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

// codexLocationName returns a schema enum value (`[a-z_]{1,40}`) as is, "?"
// for any other present value and "" when absent.
func codexLocationName(value gjson.Result) string {
	if !value.Exists() {
		return ""
	}
	if value.Type == gjson.String && isCodexLocationIdent(value.Str, 40, false) {
		return value.Str
	}
	return "?"
}

func isCodexLocationIdent(s string, maxLen int, allowUpperDigits bool) bool {
	if len(s) == 0 || len(s) > maxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || (c >= 'a' && c <= 'z'):
		case allowUpperDigits && ((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')):
		default:
			return false
		}
	}
	return true
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
		if isCodexLocationIdent(string(key), 64, true) {
			b = append(b, key...)
		} else {
			b = append(b, '?')
		}
	}
	return string(b)
}
