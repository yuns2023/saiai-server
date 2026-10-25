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

// ErrCodexRewriteClock reports that the rewrite zone could not be loaded.
// Callers must fail closed; there is no UTC fallback.
var ErrCodexRewriteClock = errors.New("codex rewrite timezone unavailable")

// codexItemMetadataKey is the per-input-item metadata Codex 0.164+ sends
// (HTTP and WebSocket); its create_time is the item's creation time in epoch
// seconds (a JSON number, usually fractional).
const codexItemMetadataKey = "internal_chat_message_metadata_passthrough"

// codexCreateTimeFloor is the earliest create_time accepted as sane.
var codexCreateTimeFloor = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

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
	loc, err := codexRewriteZone()
	if err != nil {
		return "", err
	}
	return now.In(loc).Format("2006-01-02"), nil
}

func codexRewriteZone() (*time.Location, error) {
	codexRewriteLocationOnce.Do(func() {
		codexRewriteLocation, codexRewriteLocationErr = time.LoadLocation(CodexRewriteTimezone)
	})
	if codexRewriteLocationErr != nil {
		return nil, errors.Join(ErrCodexRewriteClock, codexRewriteLocationErr)
	}
	return codexRewriteLocation, nil
}

// RewriteCodexEnvironmentTime replaces, inside every
// `<environment_context>`…`</environment_context>` block found in a JSON
// string value (object keys are ignored), each `<timezone>` value that is not
// CodexRewriteTimezone with CodexRewriteTimezone and each `<current_date>`
// value with a Los Angeles date. Tags outside blocks and
// `<current_date status=… />` style tags are untouched.
//
// The date of a block is the CodexRewriteTimezone date of the enclosing input
// item's `internal_chat_message_metadata_passthrough.create_time` (the
// nearest enclosing object carrying that key, before or after the content)
// when it is a JSON number of epoch seconds within [2020-01-01, now+24h];
// otherwise it is now's date. now is the caller's frozen per-request/turn
// clock. create_time itself is never modified.
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
// tag inside a block returns ErrCodexEnvironmentMalformed; a zone load
// failure returns ErrCodexRewriteClock. The function holds no state and is
// idempotent for a fixed now.
func RewriteCodexEnvironmentTime(body []byte, now time.Time) ([]byte, CodexEnvironmentRewriteStats, error) {
	var stats CodexEnvironmentRewriteStats
	if !bytes.Contains(body, environmentContextWord) && !bytes.Contains(body, jsonUnicodeEsc) {
		return body, stats, nil
	}
	if !gjson.ValidBytes(body) {
		return body, stats, ErrCodexEnvironmentMalformed
	}
	loc, err := codexRewriteZone()
	if err != nil {
		return body, stats, err
	}
	rw := codexEnvironmentRewriter{
		zone:  []byte(CodexRewriteTimezone),
		today: []byte(now.In(loc).Format("2006-01-02")),
		now:   now,
		loc:   loc,
	}
	if err := rw.scan(body); err != nil {
		return body, CodexEnvironmentRewriteStats{}, err
	}
	rw.resolveDates(body)
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
	// Date edits are resolved after the scan, because item metadata may
	// follow the content: value is the current value and items the body
	// offsets of the enclosing objects (innermost last).
	date  bool
	value []byte
	items []int
}

type codexJSONFrame struct {
	object   bool
	start    int
	metaNext bool   // the next value is this object's metadata value
	isMeta   bool   // this frame is a metadata value
	hasMeta  bool   // this object has the metadata key
	metaSpan [2]int // body span of an object metadata value; -1 otherwise
}

type codexEnvironmentRewriter struct {
	zone   []byte
	today  []byte
	now    time.Time
	loc    *time.Location
	edits  []codexEnvironmentEdit
	stats  CodexEnvironmentRewriteStats
	buf    []byte
	offs   []int
	frames []codexJSONFrame
	metas  map[int][2]int // object start → metadata span
}

// scan walks the JSON tokens of a valid body, tracking whether each string
// is an object key and which objects carry item metadata, and records edits
// for string values.
func (rw *codexEnvironmentRewriter) scan(body []byte) error {
	rw.frames = make([]codexJSONFrame, 0, 16)
	expectKey := false
	top := func() *codexJSONFrame {
		if len(rw.frames) == 0 {
			return nil
		}
		return &rw.frames[len(rw.frames)-1]
	}
	// takeMeta consumes a pending metadata value marker.
	takeMeta := func() bool {
		if frame := top(); frame != nil && frame.metaNext {
			frame.metaNext = false
			return true
		}
		return false
	}
	for i := 0; i < len(body); {
		switch c := body[i]; c {
		case '"':
			start := i + 1
			end, escaped := jsonStringEnd(body, start)
			if end < 0 {
				return ErrCodexEnvironmentMalformed
			}
			frame := top()
			isKey := expectKey && frame != nil && frame.object
			expectKey = false
			if isKey {
				if codexJSONStringEquals(body[start:end], escaped, codexItemMetadataKey) {
					if frame.hasMeta {
						return ErrCodexEnvironmentMalformed // duplicate key: ambiguous
					}
					frame.hasMeta, frame.metaNext, frame.metaSpan = true, true, [2]int{-1, -1}
				}
			} else {
				takeMeta()
				if err := rw.stringValue(body, start, end, escaped); err != nil {
					return err
				}
			}
			i = end + 1
			continue
		case '{', '[':
			isMeta := takeMeta() && c == '{'
			rw.frames = append(rw.frames, codexJSONFrame{object: c == '{', start: i, isMeta: isMeta})
			expectKey = c == '{'
		case '}', ']':
			frame := top()
			if frame == nil || frame.object != (c == '}') {
				return ErrCodexEnvironmentMalformed
			}
			closed := *frame
			rw.frames = rw.frames[:len(rw.frames)-1]
			if parent := top(); closed.isMeta && parent != nil {
				parent.metaSpan = [2]int{closed.start, i + 1}
			}
			if closed.hasMeta {
				if rw.metas == nil {
					rw.metas = make(map[int][2]int)
				}
				rw.metas[closed.start] = closed.metaSpan
			}
			expectKey = false
		case ',':
			frame := top()
			expectKey = frame != nil && frame.object
		case ':':
			expectKey = false
		case ' ', '\t', '\n', '\r':
		default:
			takeMeta() // a scalar metadata value carries no create_time
		}
		i++
	}
	return nil
}

// codexJSONStringEquals compares raw JSON string content with an ASCII name.
func codexJSONStringEquals(raw []byte, escaped bool, name string) bool {
	if !escaped {
		return string(raw) == name
	}
	return string(appendUnescapedJSONString(nil, raw)) == name
}

// enclosingObjects returns the body offsets of the open objects.
func (rw *codexEnvironmentRewriter) enclosingObjects() []int {
	starts := make([]int, 0, len(rw.frames))
	for _, frame := range rw.frames {
		if frame.object {
			starts = append(starts, frame.start)
		}
	}
	return starts
}

// resolveDates sets each date edit's replacement from its item create_time
// or now, and drops edits whose value already matches.
func (rw *codexEnvironmentRewriter) resolveDates(body []byte) {
	kept := rw.edits[:0]
	for _, edit := range rw.edits {
		if edit.date {
			edit.replacement = rw.today
			if date := rw.itemDate(body, edit.items); date != nil {
				edit.replacement = date
			}
			if bytes.Equal(edit.value, edit.replacement) {
				continue
			}
			rw.stats.Dates++
		}
		kept = append(kept, edit)
	}
	rw.edits = kept
}

// itemDate returns the LA date of the nearest enclosing object's metadata
// create_time, or nil when that object has none or it is not sane.
func (rw *codexEnvironmentRewriter) itemDate(body []byte, items []int) []byte {
	for i := len(items) - 1; i >= 0; i-- {
		span, ok := rw.metas[items[i]]
		if !ok {
			continue
		}
		if span[0] < 0 {
			return nil
		}
		createTime := gjson.GetBytes(body[span[0]:span[1]], "create_time")
		if createTime.Type != gjson.Number {
			return nil
		}
		seconds := createTime.Float()
		if seconds < float64(codexCreateTimeFloor.Unix()) || seconds > float64(rw.now.Add(24*time.Hour).Unix()) {
			return nil
		}
		whole := int64(seconds)
		created := time.Unix(whole, int64((seconds-float64(whole))*1e9))
		return []byte(created.In(rw.loc).Format("2006-01-02"))
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
		var items []int
		for _, span := range mergeCodexEnvironmentSpans(zones, dates) {
			value := block[span.start:span.end]
			edit := codexEnvironmentEdit{
				start: rawOffset(blockStart + span.start),
				end:   rawOffset(blockStart + span.end),
			}
			if span.zone {
				if bytes.Equal(value, rw.zone) {
					continue
				}
				rw.stats.Timezones++
				edit.replacement = rw.zone
			} else {
				if items == nil {
					items = rw.enclosingObjects()
				}
				edit.date, edit.value, edit.items = true, append([]byte(nil), value...), items
			}
			rw.edits = append(rw.edits, edit)
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
