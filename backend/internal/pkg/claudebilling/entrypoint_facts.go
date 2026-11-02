package claudebilling

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// Body status values reported by ParseEntrypointFacts.
const (
	EntrypointBodyValid     = "valid"
	EntrypointBodyMissing   = "missing"
	EntrypointBodyMalformed = "malformed"
	EntrypointBodyAmbiguous = "ambiguous"
)

// EntrypointNone is the recorded/displayed value when no entrypoint was found.
const EntrypointNone = "<none>"

// EntrypointInvalid is the recorded/displayed value for an entrypoint that
// contains characters outside [a-z0-9._/-].
const EntrypointInvalid = "<invalid>"

// EntrypointAmbiguous is the recorded/displayed UA entrypoint when the
// request carries more than one User-Agent header value.
const EntrypointAmbiguous = "<ambiguous>"

const (
	billingHeaderMarker  = "x-anthropic-billing-header:"
	ccEntrypointMarker   = "cc_entrypoint="
	maxRecordedValueSize = 64
)

// uaEntrypointPattern matches "claude-cli/<semver><suffix> (<user-type>, <entrypoint>[, ...])".
// The entrypoint is the first token after the user-type token; the comment
// segment must be closed by ")".
var uaEntrypointPattern = regexp.MustCompile(`(?i)^claude-cli/(\d+\.\d+\.\d+)\S*\s+\(([^,()]+),\s*([^,()]+)(?:,[^()]*)?\)`)

// EntrypointFacts is what a /v1/messages request declares about its Claude
// client entrypoint. Values are client-declared, never verified.
type EntrypointFacts struct {
	// UAEntrypoint is the trimmed, lower-cased UA entrypoint, or "" when the
	// User-Agent is not a claude-cli UA with an entrypoint.
	UAEntrypoint string
	// UAAmbiguous is set when the request has more than one User-Agent
	// value; UAEntrypoint and UAVersion are then empty.
	UAAmbiguous bool
	// UAVersion is the claude-cli semver from the UA, or "".
	UAVersion string
	// BodyEntrypoint is the trimmed, lower-cased cc_entrypoint of the single
	// billing header in top-level system; set only when BodyStatus is valid.
	BodyEntrypoint string
	BodyStatus     string
}

// ParseUserAgentEntrypoint extracts the claude-cli version and entrypoint
// from a User-Agent. ok is false for anything that is not a claude-cli UA
// with a "(<user-type>, <entrypoint>" section.
func ParseUserAgentEntrypoint(userAgent string) (entrypoint, version string, ok bool) {
	m := uaEntrypointPattern.FindStringSubmatch(userAgent)
	if m == nil {
		return "", "", false
	}
	entrypoint = NormalizeEntrypoint(m[3])
	if entrypoint == "" {
		return "", "", false
	}
	return entrypoint, m[1], true
}

// ParseBodyEntrypoint reads the cc_entrypoint of the billing header in the
// top-level "system" field. It never looks at messages, tools or the rest of
// the body, and never mutates body.
//
//   - more than one top-level "system" key → ambiguous
//   - system string → scanned; system array → every {"type":"text"} element's text
//   - zero billing headers → missing; more than one → ambiguous
//   - header without a non-empty "cc_entrypoint=<v>;" → malformed
//   - header with more than one cc_entrypoint → ambiguous
func ParseBodyEntrypoint(body []byte) (entrypoint, status string) {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return "", EntrypointBodyMissing
	}
	var system gjson.Result
	systemKeys := 0
	root.ForEach(func(key, value gjson.Result) bool {
		if key.String() == "system" {
			systemKeys++
			system = value
		}
		return true
	})
	if systemKeys > 1 {
		return "", EntrypointBodyAmbiguous
	}
	if systemKeys == 0 {
		return "", EntrypointBodyMissing
	}

	var texts []string
	switch {
	case system.Type == gjson.String:
		texts = append(texts, system.String())
	case system.IsArray():
		duplicateKey := false
		system.ForEach(func(_, block gjson.Result) bool {
			if !block.IsObject() {
				return true
			}
			// gjson reads the first of duplicate keys, encoding/json the
			// last: a block with two "type" or "text" keys is ambiguous.
			var blockType, text gjson.Result
			typeKeys, textKeys := 0, 0
			block.ForEach(func(key, value gjson.Result) bool {
				switch key.String() {
				case "type":
					typeKeys++
					blockType = value
				case "text":
					textKeys++
					text = value
				}
				return true
			})
			if typeKeys > 1 || textKeys > 1 {
				duplicateKey = true
				return false
			}
			if blockType.Type == gjson.String && blockType.String() == "text" && text.Type == gjson.String {
				texts = append(texts, text.String())
			}
			return true
		})
		if duplicateKey {
			return "", EntrypointBodyAmbiguous
		}
	default:
		return "", EntrypointBodyMissing
	}

	headerLine := ""
	headers := 0
	for _, text := range texts {
		lower := strings.ToLower(text)
		for offset := 0; ; {
			idx := strings.Index(lower[offset:], billingHeaderMarker)
			if idx < 0 {
				break
			}
			headers++
			if headers > 1 {
				return "", EntrypointBodyAmbiguous
			}
			start := offset + idx + len(billingHeaderMarker)
			line := lower[start:]
			if nl := strings.IndexByte(line, '\n'); nl >= 0 {
				line = line[:nl]
			}
			headerLine = line
			offset = start
		}
	}
	if headers == 0 {
		return "", EntrypointBodyMissing
	}

	// Fields are "name=value;" separated by ';'. Only a field named exactly
	// cc_entrypoint counts; a trailing field without ';' is unterminated.
	// Scanned in place (no Split) so a long run of ';' cannot amplify memory.
	found := 0
	value := ""
	unterminated := false
	for rest := headerLine; ; {
		field, tail, terminated := strings.Cut(rest, ";")
		if field = strings.TrimSpace(field); strings.HasPrefix(field, ccEntrypointMarker) {
			found++
			value = field[len(ccEntrypointMarker):]
			unterminated = !terminated
		}
		if !terminated {
			break
		}
		rest = tail
	}
	switch {
	case found == 0:
		return "", EntrypointBodyMalformed
	case found > 1:
		return "", EntrypointBodyAmbiguous
	case unterminated:
		return "", EntrypointBodyMalformed
	}
	entrypoint = NormalizeEntrypoint(value)
	if entrypoint == "" {
		return "", EntrypointBodyMalformed
	}
	return entrypoint, EntrypointBodyValid
}

// ParseEntrypointFacts combines ParseUserAgentEntrypoint and
// ParseBodyEntrypoint. userAgents are all User-Agent header values; more
// than one is ambiguous.
func ParseEntrypointFacts(userAgents []string, body []byte) EntrypointFacts {
	var facts EntrypointFacts
	switch {
	case len(userAgents) > 1:
		facts.UAAmbiguous = true
	case len(userAgents) == 1:
		facts.UAEntrypoint, facts.UAVersion, _ = ParseUserAgentEntrypoint(userAgents[0])
	}
	facts.BodyEntrypoint, facts.BodyStatus = ParseBodyEntrypoint(body)
	return facts
}

// RecordedUAEntrypoint is the sanitized UA entrypoint for storage/display.
func (f EntrypointFacts) RecordedUAEntrypoint() string {
	if f.UAAmbiguous {
		return EntrypointAmbiguous
	}
	return SanitizeEntrypointForRecord(f.UAEntrypoint)
}

// RecordedBodyEntrypoint is the sanitized body entrypoint ("<none>" unless
// the body status is valid).
func (f EntrypointFacts) RecordedBodyEntrypoint() string {
	if f.BodyStatus != EntrypointBodyValid {
		return EntrypointNone
	}
	return SanitizeEntrypointForRecord(f.BodyEntrypoint)
}

// NormalizeEntrypoint is the comparison form: trimmed and lower-cased.
func NormalizeEntrypoint(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// SanitizeEntrypointForRecord returns a value safe to store and display:
// "<none>" for empty, "<invalid>" when any character is outside
// [a-z0-9._/-], otherwise the normalized value truncated to 64 bytes.
func SanitizeEntrypointForRecord(value string) string {
	value = NormalizeEntrypoint(value)
	if value == "" {
		return EntrypointNone
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '/' || c == '-' {
			continue
		}
		return EntrypointInvalid
	}
	if len(value) > maxRecordedValueSize {
		value = value[:maxRecordedValueSize]
	}
	return value
}
