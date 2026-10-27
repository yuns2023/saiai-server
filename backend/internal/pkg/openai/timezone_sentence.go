package openai

import "bytes"

// Timezone sentences. Codex Desktop (26.1021+) appends
// `The user's timezone is ZONE; use it for dates, times, and recurring schedules unless specified otherwise.`
// (ZONE from the OS via Intl) to the collaboration-mode developer
// instructions of aeon threads; app-server sends them as a developer message
// `<collaboration_mode>…</collaboration_mode>` in every request of the thread.
// There is no `<timezone>` tag, so the sentence is matched by its fixed
// anchor: ZONE is the longest run of IANA name characters right after the
// anchor (at most timezoneMaxValueBytes). The leak net (findChinaTimezone)
// and the rewrite (rewriteLooseTags) use the same rule, so a rewritten string
// never matches the leak net again.

var timezoneSentenceAnchor = []byte("The user's timezone is ")

// isTimezoneNameByte reports whether b can be part of an IANA zone name.
func isTimezoneNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' ||
		b == '/' || b == '_' || b == '-' || b == '+'
}

// timezoneSentenceScanner streams one string's decoded bytes and reports the
// zone of each anchored sentence. The anchor's first byte occurs nowhere else
// in it, so advanceTagMatch's restart is exact.
type timezoneSentenceScanner struct {
	matched  int  // anchor bytes matched
	inValue  bool // right after a complete anchor
	start    int  // raw offset of the value's first byte
	value    [timezoneMaxValueBytes]byte
	valueLen int // may exceed len(value): the value is then overlong
	emit     func(looseSpan)
}

// feed consumes one decoded byte produced by the unit at raw offset at.
func (s *timezoneSentenceScanner) feed(b byte, at int) {
	if s.inValue {
		if isTimezoneNameByte(b) {
			if s.valueLen == 0 {
				s.start = at
			}
			if s.valueLen < len(s.value) {
				s.value[s.valueLen] = b
			}
			s.valueLen++
		} else {
			s.finish(at)
		}
	}
	// A value byte may also start the next anchor. The anchor ends with a
	// space, which is not a name byte, so a value is always finished before
	// the next anchor completes.
	s.matched = advanceTagMatch(timezoneSentenceAnchor, s.matched, b)
	if s.matched == len(timezoneSentenceAnchor) {
		s.matched, s.inValue, s.valueLen = 0, true, 0
	}
}

// finish ends the open value at raw offset end (the next unit, or the end of
// the string).
func (s *timezoneSentenceScanner) finish(end int) {
	if s.inValue && s.valueLen > 0 && s.valueLen <= len(s.value) {
		s.emit(looseSpan{start: s.start, end: end, value: s.value[:s.valueLen]})
	}
	s.inValue = false
}

// findChinaTimezoneSentence returns the first sentence zone in decoded text
// that is a China mainland / Hong Kong / Macau zone, and its offset. It
// matches exactly what the scanner reports, without allocating.
func findChinaTimezoneSentence(text []byte) (zone string, at int, found bool) {
	for pos := 0; ; {
		anchor := bytes.Index(text[pos:], timezoneSentenceAnchor)
		if anchor < 0 {
			return "", 0, false
		}
		start := pos + anchor + len(timezoneSentenceAnchor)
		end := start
		for end < len(text) && isTimezoneNameByte(text[end]) {
			end++
		}
		if end-start <= timezoneMaxValueBytes {
			if _, ok := chinaTimezones[string(text[start:end])]; ok {
				return string(text[start:end]), start, true
			}
		}
		pos = start
	}
}

// scanTimezoneSentences runs the scanner over one string value whose raw
// content is raw at body offset rawBase.
func scanTimezoneSentences(raw []byte, rawBase int, escaped bool, emit func(looseSpan)) {
	if !escaped && !bytes.Contains(raw, timezoneSentenceAnchor) {
		return
	}
	s := timezoneSentenceScanner{emit: emit}
	if !escaped {
		for i, b := range raw {
			s.feed(b, rawBase+i)
		}
	} else {
		var unit [8]byte
		for i := 0; i < len(raw); {
			at := rawBase + i
			var decoded []byte
			decoded, i = decodeJSONUnit(unit[:0], raw, i)
			for _, b := range decoded {
				s.feed(b, at)
			}
		}
	}
	s.finish(rawBase + len(raw))
}
