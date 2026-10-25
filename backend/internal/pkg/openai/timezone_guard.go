package openai

import (
	"bytes"
	"unicode/utf16"
	"unicode/utf8"
)

// chinaTimezones is the China mainland / Hong Kong / Macau IANA zone set
// (canonical names plus their tzdata backward-compatible aliases). Matching
// is case-sensitive because Codex emits the canonical IANA spelling.
var chinaTimezones = map[string]struct{}{
	"Asia/Shanghai":  {},
	"Asia/Urumqi":    {},
	"Asia/Chongqing": {},
	"Asia/Chungking": {},
	"Asia/Harbin":    {},
	"Asia/Kashgar":   {},
	"PRC":            {},
	"Asia/Hong_Kong": {},
	"Hongkong":       {},
	"Asia/Macau":     {},
	"Asia/Macao":     {},
}

var (
	timezoneOpenTag  = []byte("<timezone>")
	timezoneCloseTag = []byte("</timezone>")
	timezoneWord     = []byte("timezone")
	jsonUnicodeEsc   = []byte(`\u`)
)

const (
	// timezoneMaxValueBytes bounds the trimmed tag value.
	timezoneMaxValueBytes = 64
	// timezoneMaxRawValueBytes bounds how far past an opening tag the closing
	// tag is searched for, so many unterminated tags cannot make the scan
	// quadratic. It leaves room for whitespace around a 64-byte value.
	timezoneMaxRawValueBytes = 128
)

// FindChinaTimezone reports whether any JSON string in body (keys included)
// contains `<timezone>ZONE</timezone>` whose trimmed ZONE is a China mainland,
// Hong Kong or Macau zone. Strings are matched after JSON unescaping, so
// `<timezone>` and `<\/timezone>` forms are found too.
//
// body must be valid JSON (callers validate first); the scan is read-only and
// never modifies body. Bodies containing neither "timezone" nor a `\u` escape
// return immediately. Strings without escapes are searched in place; only
// strings that contain an escape and could hold the tag are decoded, into one
// reused buffer.
func FindChinaTimezone(body []byte) (string, bool) {
	if !bytes.Contains(body, timezoneWord) && !bytes.Contains(body, jsonUnicodeEsc) {
		return "", false
	}
	var buf []byte
	for i := 0; i < len(body); {
		start := bytes.IndexByte(body[i:], '"')
		if start < 0 {
			return "", false
		}
		start += i + 1
		end, escaped := jsonStringEnd(body, start)
		if end < 0 {
			return "", false
		}
		raw := body[start:end]
		i = end + 1
		if !bytes.Contains(raw, timezoneWord) && !bytes.Contains(raw, jsonUnicodeEsc) {
			continue
		}
		text := raw
		if escaped {
			buf = appendUnescapedJSONString(buf[:0], raw)
			text = buf
		}
		if zone, ok := findChinaTimezoneTag(text); ok {
			return zone, true
		}
	}
	return "", false
}

// jsonStringEnd returns the index of the closing quote of the JSON string
// whose content starts at start, and whether the content contains escapes.
func jsonStringEnd(body []byte, start int) (int, bool) {
	escaped := false
	for i := start; i < len(body); {
		q := bytes.IndexByte(body[i:], '"')
		if q < 0 {
			return -1, escaped
		}
		q += i
		escaped = escaped || bytes.IndexByte(body[i:q], '\\') >= 0
		backslashes := 0
		for j := q - 1; j >= start && body[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return q, escaped
		}
		i = q + 1
	}
	return -1, escaped
}

// findChinaTimezoneTag checks every `<timezone>` occurrence in text; a
// non-matching, overlong or unterminated tag does not stop the search.
func findChinaTimezoneTag(text []byte) (string, bool) {
	for {
		open := bytes.Index(text, timezoneOpenTag)
		if open < 0 {
			return "", false
		}
		rest := text[open+len(timezoneOpenTag):]
		window := rest
		if limit := timezoneMaxRawValueBytes + len(timezoneCloseTag); len(window) > limit {
			window = window[:limit]
		}
		if closeAt := bytes.Index(window, timezoneCloseTag); closeAt >= 0 {
			value := bytes.TrimSpace(rest[:closeAt])
			if len(value) <= timezoneMaxValueBytes {
				if _, ok := chinaTimezones[string(value)]; ok {
					return string(value), true
				}
			}
		}
		text = rest
	}
}

// appendUnescapedJSONString decodes the content of a valid JSON string. An
// invalid escape is kept literally; a lone surrogate becomes U+FFFD (as in
// encoding/json).
func appendUnescapedJSONString(dst, raw []byte) []byte {
	for i := 0; i < len(raw); {
		b := raw[i]
		if b != '\\' || i+1 >= len(raw) {
			next := bytes.IndexByte(raw[i+1:], '\\')
			if next < 0 {
				return append(dst, raw[i:]...)
			}
			dst = append(dst, raw[i:i+1+next]...)
			i += 1 + next
			continue
		}
		switch raw[i+1] {
		case '"', '\\', '/':
			dst = append(dst, raw[i+1])
		case 'b':
			dst = append(dst, '\b')
		case 'f':
			dst = append(dst, '\f')
		case 'n':
			dst = append(dst, '\n')
		case 'r':
			dst = append(dst, '\r')
		case 't':
			dst = append(dst, '\t')
		case 'u':
			r, ok := parseJSONHex4(raw, i+2)
			if !ok {
				dst = append(dst, raw[i:i+2]...)
				i += 2
				continue
			}
			i += 6
			if utf16.IsSurrogate(r) {
				if i+1 < len(raw) && raw[i] == '\\' && raw[i+1] == 'u' {
					if r2, ok := parseJSONHex4(raw, i+2); ok {
						if combined := utf16.DecodeRune(r, r2); combined != utf8.RuneError {
							dst = utf8.AppendRune(dst, combined)
							i += 6
							continue
						}
					}
				}
				r = utf8.RuneError
			}
			dst = utf8.AppendRune(dst, r)
			continue
		default:
			dst = append(dst, raw[i:i+2]...)
		}
		i += 2
	}
	return dst
}

func parseJSONHex4(raw []byte, at int) (rune, bool) {
	if at+4 > len(raw) {
		return 0, false
	}
	var r rune
	for _, c := range raw[at : at+4] {
		switch {
		case c >= '0' && c <= '9':
			c -= '0'
		case c >= 'a' && c <= 'f':
			c = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			c = c - 'A' + 10
		default:
			return 0, false
		}
		r = r<<4 | rune(c)
	}
	return r, true
}
