package openai

import (
	"sort"
	"unicode"
	"unicode/utf8"
)

// Loose tags. Codex clients also send the client timezone outside
// `<environment_context>`: Codex Desktop adds a developer message
// `<codex_apps_client_time_context><timezone>ZONE</timezone>\n<current_date>DATE</current_date>\n…`
// to every turn from the OS timezone, and later clients may add other
// wrappers. So in a string value that is not a whole-string environment
// block, every `<timezone>` whose value the leak net would flag (see
// findChinaTimezoneTag: an opening tag pairs with the first closing tag after
// it, the value is trimmed of Unicode whitespace, at most
// timezoneMaxValueBytes, and in chinaTimezones) is rewritten as well, unless
// an environment block edit of the same string already covers it.
//
// When a string gets at least one such zone edit, every `<current_date>`
// value in that string that is exactly a YYYY-MM-DD date is rewritten too
// (to the item's or now's LA date, as in blocks). This is a same-string
// heuristic: the zone and date of one client context are sent together, but
// a date in another string is never touched, and a string whose zones are
// already rewritten does not trigger date edits again (idempotence).
//
// A CN value contains no '<', so the first '<' after an opening tag must
// start its closing tag; anything else means the value is not a CN zone.
// That keeps the scan streaming: only the trimmed value (bounded) is kept,
// surrounding whitespace of any length is skipped.
//
// CN zones of Codex Desktop timezone sentences (see timezone_sentence.go) in
// the same strings are rewritten as well, without date edits.

var (
	looseTagOpen  = [2][]byte{timezoneOpenTag, currentDateOpenTag}
	looseTagClose = [2][]byte{timezoneCloseTag, currentDateCloseTag}
)

const (
	looseKindZone = 0
	looseKindDate = 1
	looseNone     = -1
)

const (
	loosePhaseLead  = iota // whitespace before the value
	loosePhaseValue        // inside the value
	loosePhaseTrail        // whitespace after the value
	loosePhaseClose        // '<' seen: the closing tag must follow directly
)

// looseSpan is a trimmed tag value: decoded bytes and the raw body span.
type looseSpan struct {
	start, end int
	value      []byte
}

// looseScanner streams one string's decoded bytes and reports each complete
// value of tags of kind want.
type looseScanner struct {
	want      int
	kind      int // kind of the open candidate, or looseNone
	phase     int
	value     [timezoneMaxValueBytes]byte
	valueLen  int
	start     int // raw offset of the value's first byte
	end       int // raw offset after the value
	closeLen  int // bytes of the closing tag matched in loosePhaseClose
	openMatch [2]int
	rune      [utf8.UTFMax]byte
	runeLen   int
	runeAt    int // raw offset of the pending rune's first byte
	emit      func(looseSpan)
}

// feed consumes one decoded byte produced by the unit at raw offset at.
func (s *looseScanner) feed(b byte, at int) {
	// Opening tags restart the candidate wherever they occur: an earlier
	// candidate whose value now contains '<' cannot be a CN zone or a date.
	for kind := range looseTagOpen {
		s.openMatch[kind] = advanceTagMatch(looseTagOpen[kind], s.openMatch[kind], b)
		if s.openMatch[kind] == len(looseTagOpen[kind]) {
			s.openMatch = [2]int{}
			s.kind, s.phase, s.valueLen, s.runeLen = looseNone, loosePhaseLead, 0, 0
			if kind == s.want {
				s.kind = kind
			}
			return
		}
	}
	if s.kind == looseNone {
		return
	}
	if s.phase == loosePhaseClose {
		tag := looseTagClose[s.kind]
		if b != tag[s.closeLen] {
			s.kind = looseNone
			return
		}
		if s.closeLen++; s.closeLen == len(tag) {
			if s.valueLen > 0 {
				s.emit(looseSpan{start: s.start, end: s.end, value: s.value[:s.valueLen]})
			}
			s.kind = looseNone
		}
		return
	}
	if s.runeLen == 0 {
		s.runeAt = at
	}
	s.rune[s.runeLen] = b
	s.runeLen++
	if !utf8.FullRune(s.rune[:s.runeLen]) && s.runeLen < utf8.UTFMax {
		return
	}
	r, size := utf8.DecodeRune(s.rune[:s.runeLen])
	if size != s.runeLen {
		s.kind = looseNone // invalid UTF-8 inside a candidate: not a zone or date
		return
	}
	s.runeLen = 0
	switch {
	case r == '<':
		if s.phase == loosePhaseValue {
			s.end = s.runeAt
		}
		s.phase, s.closeLen = loosePhaseClose, 1
	case unicode.IsSpace(r):
		if s.phase == loosePhaseValue {
			s.end, s.phase = s.runeAt, loosePhaseTrail
		}
	case s.phase == loosePhaseTrail || s.valueLen+size > len(s.value):
		s.kind = looseNone // inner whitespace or an overlong value
	default:
		if s.phase == loosePhaseLead {
			s.start, s.phase = s.runeAt, loosePhaseValue
		}
		s.valueLen += copy(s.value[s.valueLen:], s.rune[:size])
	}
}

// scanLooseTags runs a scanner of kind want over one string value whose raw
// content is raw at body offset rawBase.
func scanLooseTags(raw []byte, rawBase int, escaped bool, want int, emit func(looseSpan)) {
	s := looseScanner{want: want, kind: looseNone, emit: emit}
	var unit [8]byte
	for i := 0; i < len(raw); {
		if s.kind == looseNone && s.openMatch == [2]int{} {
			// Only '<' (or a `\u` escape) can start a tag; other units are
			// skipped whole, keeping escape alignment.
			for i < len(raw) && raw[i] != '<' {
				if escaped && raw[i] == '\\' && i+1 < len(raw) {
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
		at := rawBase + i
		if !escaped {
			s.feed(raw[i], at)
			i++
			continue
		}
		var decoded []byte
		decoded, i = decodeJSONUnit(unit[:0], raw, i)
		for _, b := range decoded {
			s.feed(b, at)
		}
	}
}

// rewriteLooseTags records the loose-tag and timezone-sentence edits of one
// string value. base is
// len(rw.edits) before the string's environment block edits; those edits are
// sorted and are never overlapped.
func (rw *codexEnvironmentRewriter) rewriteLooseTags(raw []byte, rawBase int, escaped bool, base int) {
	blockEdits := len(rw.edits)
	next := base
	free := func(span looseSpan) bool {
		for next < blockEdits && rw.edits[next].end <= span.start {
			next++
		}
		return next == blockEdits || rw.edits[next].start >= span.end
	}
	zones := 0
	scanLooseTags(raw, rawBase, escaped, looseKindZone, func(span looseSpan) {
		if _, cn := chinaTimezones[string(span.value)]; !cn || !free(span) {
			return
		}
		rw.edits = append(rw.edits, codexEnvironmentEdit{start: span.start, end: span.end, replacement: rw.zone})
		zones++
	})
	rw.stats.Timezones += zones
	rw.stats.LooseTimezones += zones
	if zones > 0 {
		item := rw.enclosingItem()
		next = base
		scanLooseTags(raw, rawBase, escaped, looseKindDate, func(span looseSpan) {
			if !isLooseDate(span.value) || !free(span) {
				return
			}
			rw.edits = append(rw.edits, codexEnvironmentEdit{
				start: span.start, end: span.end, date: true, value: append([]byte(nil), span.value...), item: item,
			})
		})
	}
	// Timezone sentences carry no date: only the zone is replaced.
	next = base
	scanTimezoneSentences(raw, rawBase, escaped, func(span looseSpan) {
		if _, cn := chinaTimezones[string(span.value)]; !cn || !free(span) {
			return
		}
		rw.edits = append(rw.edits, codexEnvironmentEdit{start: span.start, end: span.end, replacement: rw.zone})
		rw.stats.Timezones++
		rw.stats.SentenceTimezones++
	})
	added := rw.edits[base:]
	sort.SliceStable(added, func(i, j int) bool { return added[i].start < added[j].start })
}

// isLooseDate reports whether value is exactly YYYY-MM-DD (digits only).
func isLooseDate(value []byte) bool {
	if len(value) != len("2006-01-02") {
		return false
	}
	for i, c := range value {
		if i == 4 || i == 7 {
			if c != '-' {
				return false
			}
		} else if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
