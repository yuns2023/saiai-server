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
	// Embedded counts the Blocks found inside a larger string (tool output,
	// pasted text); EmbeddedSkipped counts embedded candidates left untouched
	// because they are not well formed (the leak net still sees them).
	Embedded        int
	EmbeddedSkipped int
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
// The date of a block is the CodexRewriteTimezone date of the enclosing
// `input` item's `internal_chat_message_metadata_passthrough.create_time`
// (the item object's own key, before or after its content) when it is a
// JSON number of epoch seconds within [2020-01-01, now+24h]; otherwise, and
// outside input items, it is now's date. now is the caller's frozen
// per-request/turn clock. Strings inside the item metadata, create_time
// included, are never modified.
//
// The edit is a raw span replacement: every byte outside the replaced value
// spans is identical to body (escapes, key order and whitespace included),
// and the replacement text is plain ASCII. Strings are decoded once with a
// decoded→raw offset map, so `<`, `\/` and any other escape are handled
// without double-decoding. body is never modified; when nothing changes the
// same slice is returned. A body without "environment_context" and without
// a `\u` escape returns immediately.
//
// Two string shapes are recognised. A string whose content, trimmed of ASCII
// whitespace, is exactly one block (how Codex sends it) fails closed when the
// block is malformed. In any other string (tool output such as a Codex rollout
// line, pasted text) every well-formed block is rewritten as well, while
// unclosed, nested or otherwise malformed mentions are left untouched and never
// rejected; the caller's leak net still catches a CN zone left in them.
//
// An invalid JSON body, or an unclosed or nested block, or an unpaired/nested
// tag inside a whole-string block returns an error matching
// ErrCodexEnvironmentMalformed (see CodexEnvironmentMalformedOffset); a zone
// load failure returns ErrCodexRewriteClock. The function holds no state and
// is idempotent for a fixed now.
func RewriteCodexEnvironmentTime(body []byte, now time.Time) ([]byte, CodexEnvironmentRewriteStats, error) {
	var stats CodexEnvironmentRewriteStats
	if !bytes.Contains(body, environmentContextWord) && !bytes.Contains(body, jsonUnicodeEsc) {
		return body, stats, nil
	}
	if !gjson.ValidBytes(body) {
		return body, stats, &codexEnvironmentMalformedError{offset: -1}
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
	// follow the content: value is the current value and item the body
	// offset of the enclosing input item (-1 outside input items).
	date  bool
	value []byte
	item  int
}

type codexJSONFrame struct {
	object    bool
	start     int
	inputNext bool   // root object: the next value is the input array
	isInput   bool   // the root object's input array
	isItem    bool   // an object element of the input array
	item      int    // start of the enclosing input item, -1 outside items
	metaNext  bool   // item: the next value is its metadata value
	inMeta    bool   // inside an item's metadata value
	hasMeta   bool   // item: has the metadata key
	metaSpan  [2]int // item: body span of an object metadata value; -1 otherwise
	metaValue bool   // this frame is an item's metadata object
}

type codexEnvironmentRewriter struct {
	zone      []byte
	today     []byte
	now       time.Time
	loc       *time.Location
	edits     []codexEnvironmentEdit
	stats     CodexEnvironmentRewriteStats
	buf       []byte
	offs      []int
	frames    []codexJSONFrame
	metas     map[int][2]int // item start → metadata span
	itemDates map[int][]byte // item start → resolved date (nil: fall back)
	noEdit    bool           // validate blocks without recording edits
}

// scan walks the JSON tokens of a valid body, tracking whether each string
// is an object key, which input items carry item metadata, and records
// edits for string values. Strings inside an item's metadata are never
// edited.
func (rw *codexEnvironmentRewriter) scan(body []byte) error {
	rw.frames = make([]codexJSONFrame, 0, 16)
	expectKey := false
	top := func() *codexJSONFrame {
		if len(rw.frames) == 0 {
			return nil
		}
		return &rw.frames[len(rw.frames)-1]
	}
	// takeValue consumes the pending markers for the value that starts now
	// and reports whether it is the input array or an item metadata value.
	takeValue := func() (input, meta bool) {
		frame := top()
		if frame == nil {
			return false, false
		}
		input, meta = frame.inputNext, frame.metaNext
		frame.inputNext, frame.metaNext = false, false
		return input, meta
	}
	for i := 0; i < len(body); {
		switch c := body[i]; c {
		case '"':
			start := i + 1
			end, escaped := jsonStringEnd(body, start)
			if end < 0 {
				return &codexEnvironmentMalformedError{offset: start}
			}
			frame := top()
			isKey := expectKey && frame != nil && frame.object
			expectKey = false
			if isKey {
				switch {
				case len(rw.frames) == 1 && codexJSONStringEquals(body[start:end], escaped, "input"):
					frame.inputNext = true
				case frame.isItem && codexJSONStringEquals(body[start:end], escaped, codexItemMetadataKey):
					if frame.hasMeta {
						return &codexEnvironmentMalformedError{offset: start} // duplicate key: ambiguous
					}
					frame.hasMeta, frame.metaNext, frame.metaSpan = true, true, [2]int{-1, -1}
				}
			} else {
				_, meta := takeValue()
				// Metadata strings are validated (fail closed) but never edited.
				rw.noEdit = meta || (frame != nil && frame.inMeta)
				err := rw.stringValue(body, start, end, escaped)
				rw.noEdit = false
				if err != nil {
					return &codexEnvironmentMalformedError{offset: start}
				}
			}
			i = end + 1
			continue
		case '{', '[':
			parent := top()
			input, meta := takeValue()
			next := codexJSONFrame{object: c == '{', start: i}
			next.isInput = input && c == '['
			next.metaValue = meta && c == '{'
			next.inMeta = meta || (parent != nil && parent.inMeta)
			next.isItem = c == '{' && parent != nil && parent.isInput
			next.item = -1
			if next.isItem {
				next.item = i
			} else if parent != nil {
				next.item = parent.item
			}
			rw.frames = append(rw.frames, next)
			expectKey = c == '{'
		case '}', ']':
			frame := top()
			if frame == nil || frame.object != (c == '}') {
				return &codexEnvironmentMalformedError{offset: -1}
			}
			closed := *frame
			rw.frames = rw.frames[:len(rw.frames)-1]
			if parent := top(); closed.metaValue && parent != nil {
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
			takeValue() // a scalar value: no input array, no create_time
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

// enclosingItem returns the body offset of the open input item, or -1.
func (rw *codexEnvironmentRewriter) enclosingItem() int {
	if len(rw.frames) == 0 {
		return -1
	}
	return rw.frames[len(rw.frames)-1].item
}

// resolveDates sets each date edit's replacement from its item create_time
// or now, and drops edits whose value already matches.
func (rw *codexEnvironmentRewriter) resolveDates(body []byte) {
	kept := rw.edits[:0]
	for _, edit := range rw.edits {
		if edit.date {
			edit.replacement = rw.today
			if date := rw.itemDate(body, edit.item); date != nil {
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

// itemDate returns the LA date of the item's metadata create_time, or nil
// when the item has none or it is not sane. Each item is resolved once.
func (rw *codexEnvironmentRewriter) itemDate(body []byte, item int) []byte {
	if item < 0 {
		return nil
	}
	if date, ok := rw.itemDates[item]; ok {
		return date
	}
	var date []byte
	if span, ok := rw.metas[item]; ok && span[0] >= 0 {
		createTime := gjson.GetBytes(body[span[0]:span[1]], "create_time")
		if createTime.Type == gjson.Number {
			seconds := createTime.Float()
			// Bound before converting so huge values cannot overflow.
			if seconds >= float64(codexCreateTimeFloor.Unix()) && seconds <= float64(rw.now.Unix()+2*86400) {
				whole := int64(seconds)
				created := time.Unix(whole, int64((seconds-float64(whole))*1e9))
				if !created.After(rw.now.Add(24 * time.Hour)) {
					date = []byte(created.In(rw.loc).Format("2006-01-02"))
				}
			}
		}
	}
	if rw.itemDates == nil {
		rw.itemDates = make(map[int][]byte)
	}
	rw.itemDates[item] = date
	return date
}

// stringValue rewrites the environment blocks of one string value whose raw
// content is body[start:end]. Codex renders each environment context as a
// dedicated content item whose text is exactly
// `<environment_context>`BODY`</environment_context>` (BODY XML-escapes
// values, so it never contains either tag). A string whose decoded content,
// trimmed of ASCII whitespace, starts with the open tag and ends with the
// close tag is such a block and fails closed when malformed. Any other string
// is content (user text, tool output): its well-formed embedded blocks are
// rewritten too (see rewriteEmbeddedText), anything else in it is never
// edited and never rejected. Metadata strings (noEdit) are only validated.
func (rw *codexEnvironmentRewriter) stringValue(body []byte, start, end int, escaped bool) error {
	raw := body[start:end]
	if !bytes.Contains(raw, environmentContextWord) && (!escaped || !bytes.Contains(raw, jsonUnicodeEsc)) {
		return nil
	}
	var whole bool
	var err error
	if escaped {
		whole, err = rw.rewriteEscapedText(raw, start)
	} else {
		whole, err = rw.rewriteText(raw, start)
	}
	if err != nil || whole || rw.noEdit {
		return err
	}
	if escaped {
		rw.rewriteEmbeddedEscapedText(raw, start)
	} else {
		rw.rewriteEmbeddedText(raw, start)
	}
	return nil
}

// codexEnvironmentBlockMaxBytes bounds one decoded block (Codex renders well
// under 4 KiB); a larger block is malformed. It also bounds the memory used
// to decode an escaped string.
const codexEnvironmentBlockMaxBytes = 64 << 10

func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v'
}

// rewriteText handles an unescaped string in place; rawBase is the body
// offset of the string content. whole reports whether the string is a
// whole-string block.
func (rw *codexEnvironmentRewriter) rewriteText(text []byte, rawBase int) (whole bool, err error) {
	first, last := 0, len(text)
	for first < last && isASCIISpace(text[first]) {
		first++
	}
	for last > first && isASCIISpace(text[last-1]) {
		last--
	}
	trimmed := text[first:last]
	if len(trimmed) < len(environmentContextOpenTag)+len(environmentContextCloseTag) ||
		!bytes.HasPrefix(trimmed, environmentContextOpenTag) || !bytes.HasSuffix(trimmed, environmentContextCloseTag) {
		return false, nil
	}
	blockStart := first + len(environmentContextOpenTag)
	block := text[blockStart : last-len(environmentContextCloseTag)]
	base := rawBase + blockStart
	return true, rw.rewriteBlock(block, func(i int) int { return base + i })
}

// decodeJSONUnit decodes the escape unit (one byte, `\x`, `\uXXXX` or a
// surrogate pair) at raw[i] into dst and returns the index after it.
func decodeJSONUnit(dst, raw []byte, i int) ([]byte, int) {
	if raw[i] != '\\' || i+1 >= len(raw) {
		return append(dst, raw[i]), i + 1
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
	return appendUnescapedJSONString(dst, raw[i:end]), end
}

// rewriteEscapedText decodes an escaped string unit by unit without
// materializing it. Leading whitespace is skipped and the open tag must
// follow, otherwise the string is not a whole-string block and decoding stops.
// After the open tag, content is buffered with the raw offset of each decoded
// byte up to the block limit (beyond it only a 0/1 overflow flag is kept), and
// the last bytes are tracked to learn whether the trimmed string ends with the
// close tag. Extra memory is O(block limit), independent of the string length.
func (rw *codexEnvironmentRewriter) rewriteEscapedText(raw []byte, rawBase int) (whole bool, err error) {
	var unit [8]byte
	const bufferLimit = codexEnvironmentBlockMaxBytes + 64
	matched := 0      // bytes of the open tag matched so far
	contentLen := 0   // buffered bytes up to the last non-whitespace byte
	overflow := false // content (not just trailing whitespace) beyond the buffer
	endsWithClose := false
	var recent [64]byte // last decoded bytes after the open tag
	recentLen := 0
	rw.buf, rw.offs = rw.buf[:0], rw.offs[:0]
	for i := 0; i < len(raw); {
		unitStart := i
		var decoded []byte
		decoded, i = decodeJSONUnit(unit[:0], raw, i)
		for _, b := range decoded {
			if matched < len(environmentContextOpenTag) {
				switch {
				case matched == 0 && isASCIISpace(b):
				case b == environmentContextOpenTag[matched]:
					matched++
				default:
					return false, nil // not a whole-string block
				}
				continue
			}
			if recentLen == len(recent) {
				copy(recent[:], recent[len(recent)-len(environmentContextCloseTag):])
				recentLen = len(environmentContextCloseTag)
			}
			recent[recentLen] = b
			recentLen++
			space := isASCIISpace(b)
			if !space {
				endsWithClose = bytes.HasSuffix(recent[:recentLen], environmentContextCloseTag)
			}
			if len(rw.buf) >= bufferLimit {
				if !space {
					overflow = true
				}
				continue
			}
			rw.buf = append(rw.buf, b)
			rw.offs = append(rw.offs, rawBase+unitStart)
			if !space {
				contentLen = len(rw.buf)
			}
		}
	}
	if matched < len(environmentContextOpenTag) || !endsWithClose {
		return false, nil // not a whole-string block
	}
	if overflow || contentLen < len(environmentContextCloseTag) {
		return true, ErrCodexEnvironmentMalformed
	}
	blockLen := contentLen - len(environmentContextCloseTag)
	offs := rw.offs
	return true, rw.rewriteBlock(rw.buf[:blockLen], func(i int) int { return offs[i] })
}

// Embedded blocks. In a string that is not a whole-string block, the
// environment_context tags are read in order: a close tag pairs with the
// open tag right before it, and earlier open tags without a close are
// mentions. A string with a stray close tag (one not directly preceded by an
// open tag, e.g. the outer close of a nested pair, or a mention after a block)
// is ambiguous: nothing in it is rewritten. Otherwise each block that passes
// the block rules is rewritten; one that does not (malformed tags, over the
// size limit) is left untouched. Neither case is an error; the leak net still
// sees any zone left.

// embeddedScan snapshots the rewriter state at the start of one string so an
// ambiguous string can be rolled back.
type embeddedScan struct {
	edits int
	stats CodexEnvironmentRewriteStats
}

func (rw *codexEnvironmentRewriter) beginEmbedded() embeddedScan {
	return embeddedScan{edits: len(rw.edits), stats: rw.stats}
}

// rollbackEmbedded drops every edit of the string and counts each of its
// candidates seen so far (at least one) as skipped.
func (rw *codexEnvironmentRewriter) rollbackEmbedded(scan embeddedScan) {
	skipped := max(rw.stats.Embedded-scan.stats.Embedded+rw.stats.EmbeddedSkipped-scan.stats.EmbeddedSkipped, 1)
	rw.edits = rw.edits[:scan.edits]
	rw.stats = scan.stats
	rw.stats.EmbeddedSkipped += skipped
}

// rewriteEmbeddedText scans an unescaped string in place.
func (rw *codexEnvironmentRewriter) rewriteEmbeddedText(text []byte, rawBase int) {
	scan := rw.beginEmbedded()
	nextOpen, nextClose := -2, -2 // cached positions (-1: none, -2: unknown)
	find := func(cached *int, tag []byte, pos int) int {
		if *cached == -2 || (*cached >= 0 && *cached < pos) {
			if at := bytes.Index(text[pos:], tag); at >= 0 {
				*cached = pos + at
			} else {
				*cached = -1
			}
		}
		return *cached
	}
	blockStart := -1 // content start of the open candidate
	for pos := 0; ; {
		closeAt := find(&nextClose, environmentContextCloseTag, pos)
		if closeAt < 0 {
			return
		}
		if open := find(&nextOpen, environmentContextOpenTag, pos); open >= 0 && open < closeAt {
			blockStart = open + len(environmentContextOpenTag)
			pos = blockStart
			continue
		}
		if blockStart < 0 {
			rw.rollbackEmbedded(scan) // stray close: ambiguous structure
			return
		}
		base := rawBase + blockStart
		rw.embeddedBlock(text[blockStart:closeAt], func(i int) int { return base + i })
		blockStart = -1
		pos = closeAt + len(environmentContextCloseTag)
	}
}

// rewriteEmbeddedEscapedText scans an escaped string unit by unit without
// materializing it. Between tags it only skips to the next '<' or `\u`
// escape and runs the tag matchers; inside a candidate it buffers the decoded
// bytes and their raw offsets up to the block limit plus the close tag, and
// beyond that only the tag matchers run (the candidate is then skipped).
// Extra memory is O(block limit) (rw.buf/rw.offs, reused) however long the
// string is or however many candidates it holds; time is linear in the
// string.
func (rw *codexEnvironmentRewriter) rewriteEmbeddedEscapedText(raw []byte, rawBase int) {
	scan := rw.beginEmbedded()
	var unit [8]byte
	const bufferLimit = codexEnvironmentBlockMaxBytes + len("</environment_context>")
	openMatched, closeMatched := 0, 0
	inBlock, overflow := false, false
	rw.buf, rw.offs = rw.buf[:0], rw.offs[:0]
	for i := 0; i < len(raw); {
		if openMatched == 0 && closeMatched == 0 && !inBlock {
			// Only '<' or a `\u` escape can start a tag. Other units are
			// skipped whole (a plain byte, or a two-byte escape), which keeps
			// unit alignment.
			for i < len(raw) && raw[i] != '<' {
				if raw[i] == '\\' && i+1 < len(raw) {
					if raw[i+1] == 'u' {
						break
					}
					i += 2
					continue
				}
				i++
			}
			if i == len(raw) {
				return
			}
		}
		unitStart := i
		var decoded []byte
		decoded, i = decodeJSONUnit(unit[:0], raw, i)
		for _, b := range decoded {
			openMatched = advanceTagMatch(environmentContextOpenTag, openMatched, b)
			closeMatched = advanceTagMatch(environmentContextCloseTag, closeMatched, b)
			if inBlock && !overflow {
				if len(rw.buf) < bufferLimit {
					rw.buf = append(rw.buf, b)
					rw.offs = append(rw.offs, rawBase+unitStart)
				} else {
					overflow = true
				}
			}
			switch {
			case openMatched == len(environmentContextOpenTag):
				// A (new) candidate starts; an earlier one was a mention.
				openMatched, closeMatched = 0, 0
				inBlock, overflow = true, false
				rw.buf, rw.offs = rw.buf[:0], rw.offs[:0]
			case closeMatched == len(environmentContextCloseTag):
				openMatched, closeMatched = 0, 0
				if !inBlock {
					rw.rollbackEmbedded(scan) // stray close: ambiguous structure
					return
				}
				inBlock = false
				if overflow {
					rw.stats.EmbeddedSkipped++
					continue
				}
				offs := rw.offs
				rw.embeddedBlock(rw.buf[:len(rw.buf)-len(environmentContextCloseTag)], func(i int) int { return offs[i] })
			}
		}
	}
}

// advanceTagMatch advances a matcher for tag, whose first byte '<' does not
// occur again in it, by one byte.
func advanceTagMatch(tag []byte, matched int, b byte) int {
	switch {
	case matched < len(tag) && b == tag[matched]:
		return matched + 1
	case b == tag[0]:
		return 1
	default:
		return 0
	}
}

// embeddedBlock rewrites one embedded block, or leaves it untouched when it
// does not pass the block rules.
func (rw *codexEnvironmentRewriter) embeddedBlock(block []byte, rawOffset func(int) int) {
	zones, dates, err := rw.checkBlock(block, true)
	if err != nil {
		rw.stats.EmbeddedSkipped++
		return
	}
	rw.stats.Blocks++
	rw.stats.Embedded++
	rw.recordBlock(block, zones, dates, rawOffset)
}

// rewriteBlock validates one decoded whole-string block and records its
// edits. rawOffset maps a block index (up to len(block)) to the body offset
// of the unit that produced it.
func (rw *codexEnvironmentRewriter) rewriteBlock(block []byte, rawOffset func(int) int) error {
	zones, dates, err := rw.checkBlock(block, false)
	if err != nil {
		return err
	}
	if rw.noEdit {
		return nil
	}
	rw.stats.Blocks++
	rw.recordBlock(block, zones, dates, rawOffset)
	return nil
}

// checkBlock validates a decoded block and returns its timezone and date
// value spans. embedded allows `\"` attribute quotes (see selfClosingTagSize).
func (rw *codexEnvironmentRewriter) checkBlock(block []byte, embedded bool) (zones, dates [][2]int, err error) {
	if len(block) > codexEnvironmentBlockMaxBytes || bytes.Contains(block, environmentContextOpenTag) ||
		bytes.Contains(block, environmentContextCloseTag) {
		return nil, nil, ErrCodexEnvironmentMalformed
	}
	if err := validateCodexEnvironmentTags(block, embedded); err != nil {
		return nil, nil, err
	}
	zones, err = codexEnvironmentTagValues(block, timezoneOpenTag, timezoneCloseTag)
	if err != nil {
		return nil, nil, err
	}
	dates, err = codexEnvironmentTagValues(block, currentDateOpenTag, currentDateCloseTag)
	if err != nil {
		return nil, nil, err
	}
	// The rewritten block must stay within the limit too, so a rewrite is
	// accepted again on the next pass (idempotence).
	rewrittenLen := len(block)
	for _, span := range zones {
		if !bytes.Equal(block[span[0]:span[1]], rw.zone) {
			rewrittenLen += len(rw.zone) - (span[1] - span[0])
		}
	}
	for _, span := range dates {
		rewrittenLen += len("2006-01-02") - (span[1] - span[0])
	}
	if rewrittenLen > codexEnvironmentBlockMaxBytes {
		return nil, nil, ErrCodexEnvironmentMalformed
	}
	return zones, dates, nil
}

// recordBlock records the edits of a validated block.
func (rw *codexEnvironmentRewriter) recordBlock(block []byte, zones, dates [][2]int, rawOffset func(int) int) {
	item := rw.enclosingItem()
	// Tags never overlap (each value is closed before the next opening tag),
	// but zone and date spans interleave; keep edits sorted.
	for _, span := range mergeCodexEnvironmentSpans(zones, dates) {
		value := block[span.start:span.end]
		edit := codexEnvironmentEdit{start: rawOffset(span.start), end: rawOffset(span.end)}
		if span.zone {
			if bytes.Equal(value, rw.zone) {
				continue
			}
			rw.stats.Timezones++
			edit.replacement = rw.zone
		} else {
			edit.date, edit.value, edit.item = true, append([]byte(nil), value...), item
		}
		rw.edits = append(rw.edits, edit)
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
func validateCodexEnvironmentTags(block []byte, escapedQuotes bool) error {
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
				size, ok := selfClosingTagSize(rest, len(check.prefix), escapedQuotes)
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
// "/>") starting at tag[i] and returns the tag's total length. With
// escapedQuotes (embedded blocks only) the quotes may also be `\"` pairs: a
// block embedded in JSON text that is itself inside a JSON string (e.g. a
// Codex rollout line read by a tool) keeps one level of escaping after the
// body is decoded. Whole-string blocks never accept them.
func selfClosingTagSize(tag []byte, i int, escapedQuotes bool) (int, bool) {
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
		if i == nameStart || i+1 >= len(tag) || tag[i] != '=' {
			return 0, false
		}
		quote := `"`
		if escapedQuotes && tag[i+1] == '\\' {
			quote = `\"`
		}
		if !bytes.HasPrefix(tag[i+1:], []byte(quote)) {
			return 0, false
		}
		valueStart := i + 1 + len(quote)
		end := bytes.Index(tag[valueStart:], []byte(quote))
		if end < 0 || bytes.ContainsAny(tag[valueStart:valueStart+end], "<>\"") {
			return 0, false
		}
		i = valueStart + end + len(quote)
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

func isHighSurrogate(r rune) bool { return r >= 0xD800 && r < 0xDC00 }
func isLowSurrogate(r rune) bool  { return r >= 0xDC00 && r < 0xE000 }

// codexEnvironmentMalformedError is ErrCodexEnvironmentMalformed with the
// body offset of the offending JSON string content (-1 when unknown).
type codexEnvironmentMalformedError struct{ offset int }

func (e *codexEnvironmentMalformedError) Error() string { return ErrCodexEnvironmentMalformed.Error() }
func (e *codexEnvironmentMalformedError) Unwrap() error { return ErrCodexEnvironmentMalformed }

// CodexEnvironmentMalformedOffset returns the body offset of the content of
// the JSON string that made RewriteCodexEnvironmentTime fail (suitable for
// LocateCodexJSONString), or false when err carries none.
func CodexEnvironmentMalformedOffset(err error) (int, bool) {
	var malformed *codexEnvironmentMalformedError
	if !errors.As(err, &malformed) || malformed.offset < 0 {
		return 0, false
	}
	return malformed.offset, true
}
