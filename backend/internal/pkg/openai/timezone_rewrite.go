package openai

import (
	"bytes"
	"errors"
	"sync"
	"time"
	_ "time/tzdata" // the rewrite zone must never silently fall back to UTC

	"github.com/tidwall/gjson"
)

// CodexRewriteTimezone is the zone a "rewrite" group reports upstream in
// Codex `<environment_context>` blocks.
const CodexRewriteTimezone = "America/Los_Angeles"

// ErrCodexEnvironmentMalformed reports a body whose environment_context cannot
// be rewritten safely. Callers must reject the request (fail closed) rather
// than forward the original bytes.
var ErrCodexEnvironmentMalformed = errors.New("codex environment_context is malformed")

var (
	environmentContextWord     = []byte("environment_context")
	environmentContextOpenTag  = []byte("<environment_context>")
	environmentContextCloseTag = []byte("</environment_context>")
	currentDateOpenTag         = []byte("<current_date>")
	currentDateCloseTag        = []byte("</current_date>")

	codexRewriteLocationOnce sync.Once
	codexRewriteLocation     *time.Location
	codexRewriteLocationErr  error
)

// CodexEnvironmentRewriteStats counts what a rewrite changed. It never holds
// request content.
type CodexEnvironmentRewriteStats struct {
	Blocks    int // environment_context blocks found in JSON string values
	Timezones int // <timezone> values replaced
	Dates     int // <current_date> values replaced
}

// Changed reports whether the rewrite modified any byte.
func (s CodexEnvironmentRewriteStats) Changed() bool {
	return s.Timezones > 0 || s.Dates > 0
}

// CodexRewriteDate returns now's calendar date in CodexRewriteTimezone. The
// zone comes from the system or the embedded tzdata; a load failure is
// returned (callers fail closed) instead of falling back to UTC.
func CodexRewriteDate(now time.Time) (string, error) {
	codexRewriteLocationOnce.Do(func() {
		codexRewriteLocation, codexRewriteLocationErr = time.LoadLocation(CodexRewriteTimezone)
	})
	if codexRewriteLocationErr != nil {
		return "", codexRewriteLocationErr
	}
	return now.In(codexRewriteLocation).Format("2006-01-02"), nil
}

// RewriteCodexEnvironmentTime replaces, inside every
// `<environment_context>`…`</environment_context>` block found in a JSON
// string value (object keys are ignored), each `<timezone>` value that is not
// CodexRewriteTimezone with CodexRewriteTimezone and each `<current_date>`
// value with today. Tags outside blocks and `<current_date status=… />` style
// tags are untouched.
//
// The edit is a raw span replacement: every byte outside the replaced value
// spans is identical to body (escapes, key order and whitespace included),
// and the replacement text is plain ASCII. Strings are decoded once with a
// decoded→raw offset map, so `<`, `\/` and any other escape are handled
// without double-decoding. body is never modified; when nothing changes the
// same slice is returned. A body without "environment_context" and without
// a `\u` escape returns immediately.
//
// An invalid JSON body, an unclosed or nested block, or an unpaired/nested
// tag inside a block returns ErrCodexEnvironmentMalformed. The function is
// idempotent for a fixed today.
func RewriteCodexEnvironmentTime(body []byte, today string) ([]byte, CodexEnvironmentRewriteStats, error) {
	var stats CodexEnvironmentRewriteStats
	if !bytes.Contains(body, environmentContextWord) && !bytes.Contains(body, jsonUnicodeEsc) {
		return body, stats, nil
	}
	if !gjson.ValidBytes(body) {
		return body, stats, ErrCodexEnvironmentMalformed
	}
	rw := codexEnvironmentRewriter{
		zone:  []byte(CodexRewriteTimezone),
		today: []byte(today),
	}
	if err := rw.scan(body); err != nil {
		return body, CodexEnvironmentRewriteStats{}, err
	}
	if len(rw.edits) == 0 {
		return body, rw.stats, nil
	}
	for i, edit := range rw.edits {
		if edit.start > edit.end || (i > 0 && edit.start < rw.edits[i-1].end) {
			return body, CodexEnvironmentRewriteStats{}, ErrCodexEnvironmentMalformed
		}
	}
	return rw.apply(body), rw.stats, nil
}

type codexEnvironmentEdit struct {
	start, end  int
	replacement []byte
}

type codexEnvironmentRewriter struct {
	zone  []byte
	today []byte
	edits []codexEnvironmentEdit
	stats CodexEnvironmentRewriteStats
	buf   []byte
	offs  []int
}

// scan walks the JSON tokens of a valid body, tracking whether each string
// is an object key, and rewrites string values.
func (rw *codexEnvironmentRewriter) scan(body []byte) error {
	stack := make([]byte, 0, 16)
	expectKey := false
	for i := 0; i < len(body); {
		switch body[i] {
		case '"':
			start := i + 1
			end, escaped := jsonStringEnd(body, start)
			if end < 0 {
				return ErrCodexEnvironmentMalformed
			}
			isKey := expectKey && len(stack) > 0 && stack[len(stack)-1] == '{'
			expectKey = false
			if !isKey {
				if err := rw.stringValue(body, start, end, escaped); err != nil {
					return err
				}
			}
			i = end + 1
			continue
		case '{':
			stack = append(stack, '{')
			expectKey = true
		case '[':
			stack = append(stack, '[')
			expectKey = false
		case '}', ']':
			if len(stack) == 0 {
				return ErrCodexEnvironmentMalformed
			}
			stack = stack[:len(stack)-1]
			expectKey = false
		case ',':
			expectKey = len(stack) > 0 && stack[len(stack)-1] == '{'
		case ':':
			expectKey = false
		}
		i++
	}
	return nil
}

// stringValue rewrites the blocks of one string value whose raw content is
// body[start:end].
func (rw *codexEnvironmentRewriter) stringValue(body []byte, start, end int, escaped bool) error {
	raw := body[start:end]
	hasWord := bytes.Contains(raw, environmentContextWord)
	if !escaped {
		if !hasWord {
			return nil
		}
		return rw.rewriteText(raw, nil, start)
	}
	if !hasWord {
		if !bytes.Contains(raw, jsonUnicodeEsc) {
			return nil
		}
		// A \u escape can hide the word; check the decoded text first so
		// plain strings never pay for the offset map.
		rw.buf = appendUnescapedJSONString(rw.buf[:0], raw)
		if !bytes.Contains(rw.buf, environmentContextWord) {
			return nil
		}
	}
	rw.buf, rw.offs = appendUnescapedJSONStringWithOffsets(rw.buf[:0], rw.offs[:0], raw)
	return rw.rewriteText(rw.buf, rw.offs, start)
}

// rewriteText records edits for the blocks in decoded text. offs maps a
// decoded index to its raw offset (nil means identity); rawBase is the body
// offset of the raw string content.
func (rw *codexEnvironmentRewriter) rewriteText(text []byte, offs []int, rawBase int) error {
	rawOffset := func(decoded int) int {
		if offs == nil {
			return rawBase + decoded
		}
		return rawBase + offs[decoded]
	}
	for pos := 0; ; {
		open := bytes.Index(text[pos:], environmentContextOpenTag)
		if open < 0 {
			return nil
		}
		blockStart := pos + open + len(environmentContextOpenTag)
		closeAt := bytes.Index(text[blockStart:], environmentContextCloseTag)
		if closeAt < 0 {
			return ErrCodexEnvironmentMalformed
		}
		blockEnd := blockStart + closeAt
		block := text[blockStart:blockEnd]
		if bytes.Contains(block, environmentContextOpenTag) {
			return ErrCodexEnvironmentMalformed
		}
		rw.stats.Blocks++
		if err := validateCodexEnvironmentTags(block); err != nil {
			return err
		}
		zones, err := codexEnvironmentTagValues(block, timezoneOpenTag, timezoneCloseTag)
		if err != nil {
			return err
		}
		dates, err := codexEnvironmentTagValues(block, currentDateOpenTag, currentDateCloseTag)
		if err != nil {
			return err
		}
		// Tags never overlap (each value is closed before the next opening
		// tag), but zone and date spans interleave; keep edits sorted.
		for _, span := range mergeCodexEnvironmentSpans(zones, dates) {
			value := block[span.start:span.end]
			replacement := rw.today
			if span.zone {
				replacement = rw.zone
			}
			if bytes.Equal(value, replacement) {
				continue
			}
			if span.zone {
				rw.stats.Timezones++
			} else {
				rw.stats.Dates++
			}
			rw.edits = append(rw.edits, codexEnvironmentEdit{
				start:       rawOffset(blockStart + span.start),
				end:         rawOffset(blockStart + span.end),
				replacement: replacement,
			})
		}
		pos = blockEnd + len(environmentContextCloseTag)
	}
}

type codexEnvironmentSpan struct {
	start, end int
	zone       bool
}

func mergeCodexEnvironmentSpans(zones, dates [][2]int) []codexEnvironmentSpan {
	spans := make([]codexEnvironmentSpan, 0, len(zones)+len(dates))
	i, j := 0, 0
	for i < len(zones) || j < len(dates) {
		if j >= len(dates) || (i < len(zones) && zones[i][0] < dates[j][0]) {
			spans = append(spans, codexEnvironmentSpan{start: zones[i][0], end: zones[i][1], zone: true})
			i++
			continue
		}
		spans = append(spans, codexEnvironmentSpan{start: dates[j][0], end: dates[j][1]})
		j++
	}
	return spans
}

// codexEnvironmentTagValues returns the value spans of every open/close tag
// pair in block. An opening tag without its closing tag, a closing tag
// without an opening tag, or a nested opening tag is malformed.
func codexEnvironmentTagValues(block, openTag, closeTag []byte) ([][2]int, error) {
	var spans [][2]int
	for pos := 0; ; {
		open := bytes.Index(block[pos:], openTag)
		closeAt := bytes.Index(block[pos:], closeTag)
		if open < 0 {
			if closeAt >= 0 {
				return nil, ErrCodexEnvironmentMalformed
			}
			return spans, nil
		}
		if closeAt >= 0 && closeAt < open {
			return nil, ErrCodexEnvironmentMalformed
		}
		valueStart := pos + open + len(openTag)
		valueLen := bytes.Index(block[valueStart:], closeTag)
		if valueLen < 0 {
			return nil, ErrCodexEnvironmentMalformed
		}
		valueEnd := valueStart + valueLen
		// A value never contains markup; '<' means a nested or crossing tag.
		if bytes.IndexByte(block[valueStart:valueEnd], '<') >= 0 {
			return nil, ErrCodexEnvironmentMalformed
		}
		spans = append(spans, [2]int{valueStart, valueEnd})
		pos = valueEnd + len(closeTag)
	}
}

var (
	timezoneTagPrefix         = []byte("<timezone")
	timezoneClosePrefix       = []byte("</timezone")
	currentDateTagPrefix      = []byte("<current_date")
	currentDateClosePrefix    = []byte("</current_date")
	codexEnvironmentTagChecks = []struct {
		prefix, exact []byte
		selfClosing   bool
	}{
		{timezoneTagPrefix, timezoneOpenTag, true},
		{timezoneClosePrefix, timezoneCloseTag, false},
		{currentDateTagPrefix, currentDateOpenTag, true},
		{currentDateClosePrefix, currentDateCloseTag, false},
	}
)

// validateCodexEnvironmentTags requires every `<timezone…`/`<current_date…`
// (and closing) occurrence in block to be an exact tag, or for opening tags a
// value-less self-closing tag such as `<current_date status="unavailable" />`.
// Anything else (`<timezone >`, a truncated `<current_date`) is malformed.
func validateCodexEnvironmentTags(block []byte) error {
	for _, check := range codexEnvironmentTagChecks {
		for pos := 0; ; {
			at := bytes.Index(block[pos:], check.prefix)
			if at < 0 {
				break
			}
			at += pos
			rest := block[at:]
			switch {
			case bytes.HasPrefix(rest, check.exact):
				pos = at + len(check.exact)
			case check.selfClosing && len(rest) > len(check.prefix) && rest[len(check.prefix)] == ' ':
				size, ok := selfClosingTagSize(rest, len(check.prefix))
				if !ok {
					return ErrCodexEnvironmentMalformed
				}
				pos = at + size
			default:
				return ErrCodexEnvironmentMalformed
			}
		}
	}
	return nil
}

// selfClosingTagSize parses ` name="value"`… `/>` (spaces allowed before
// "/>") starting at tag[i] and returns the tag's total length.
func selfClosingTagSize(tag []byte, i int) (int, bool) {
	for {
		start := i
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		if i+1 < len(tag) && tag[i] == '/' && tag[i+1] == '>' {
			return i + 2, true
		}
		if i == start {
			return 0, false // attributes must be space separated
		}
		nameStart := i
		for i < len(tag) && (tag[i] == '_' || tag[i] == '-' || (tag[i] >= 'a' && tag[i] <= 'z') || (tag[i] >= 'A' && tag[i] <= 'Z')) {
			i++
		}
		if i == nameStart || i+1 >= len(tag) || tag[i] != '=' || tag[i+1] != '"' {
			return 0, false
		}
		end := bytes.IndexByte(tag[i+2:], '"')
		if end < 0 || bytes.ContainsAny(tag[i+2:i+2+end], "<>") {
			return 0, false
		}
		i += 2 + end + 1
	}
}

// apply builds the output from body and the ascending, non-overlapping edits.
func (rw *codexEnvironmentRewriter) apply(body []byte) []byte {
	size := len(body)
	for _, edit := range rw.edits {
		size += len(edit.replacement) - (edit.end - edit.start)
	}
	out := make([]byte, 0, size)
	last := 0
	for _, edit := range rw.edits {
		out = append(out, body[last:edit.start]...)
		out = append(out, edit.replacement...)
		last = edit.end
	}
	return append(out, body[last:]...)
}

// appendUnescapedJSONStringWithOffsets decodes like appendUnescapedJSONString
// and appends to offs, for every decoded byte, the raw offset of the literal
// byte or escape sequence that produced it, followed by len(raw).
func appendUnescapedJSONStringWithOffsets(dst []byte, offs []int, raw []byte) ([]byte, []int) {
	for i := 0; i < len(raw); {
		unit := i
		if raw[i] != '\\' || i+1 >= len(raw) {
			dst = append(dst, raw[i])
			offs = append(offs, unit)
			i++
			continue
		}
		end := i + 2
		if raw[i+1] == 'u' {
			if r, ok := parseJSONHex4(raw, end); ok {
				end += 4
				if isHighSurrogate(r) && end+1 < len(raw) && raw[end] == '\\' && raw[end+1] == 'u' {
					if r2, ok := parseJSONHex4(raw, end+2); ok && isLowSurrogate(r2) {
						end += 6
					}
				}
			}
		}
		before := len(dst)
		dst = appendUnescapedJSONString(dst, raw[i:end])
		for range len(dst) - before {
			offs = append(offs, unit)
		}
		i = end
	}
	return dst, append(offs, len(raw))
}

func isHighSurrogate(r rune) bool { return r >= 0xD800 && r < 0xDC00 }
func isLowSurrogate(r rune) bool  { return r >= 0xDC00 && r < 0xE000 }
