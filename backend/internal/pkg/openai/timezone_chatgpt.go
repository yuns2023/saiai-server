package openai

import (
	"errors"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ErrChatGPTTimezoneAmbiguous reports a body with more than one top-level
// "timezone" or "timezone_offset_min" key: upstream may honour a copy the
// rewrite did not touch, so callers reject it.
var ErrChatGPTTimezoneAmbiguous = errors.New("chatgpt body repeats a timezone field")

// chatGPTChinaOffsetMinutes is Date.getTimezoneOffset() in UTC+8.
const chatGPTChinaOffsetMinutes = -480

// RewriteChatGPTTimezone rewrites the browser time zone that ChatGPT web
// clients (Codex Desktop native Chat: conversation/init, f/conversation,
// file creation) put into top-level body fields. A China-region "timezone"
// becomes CodexRewriteTimezone and "timezone_offset_min" becomes that zone's
// Date.getTimezoneOffset() at now. Without a "timezone" key, only an offset
// of -480 (UTC+8) is replaced. Any other zone, nested fields and bodies that
// are not JSON objects are left unchanged. changed reports whether out
// differs from body; a zone load failure returns ErrCodexRewriteClock.
func RewriteChatGPTTimezone(body []byte, now time.Time) (out []byte, changed bool, err error) {
	root := gjson.ParseBytes(body)
	if !gjson.ValidBytes(body) || !root.IsObject() {
		return body, false, nil
	}
	var zone, offset gjson.Result
	zones, offsets := 0, 0
	root.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "timezone":
			zone, zones = value, zones+1
		case "timezone_offset_min":
			offset, offsets = value, offsets+1
		}
		return true
	})
	if zones > 1 || offsets > 1 {
		return body, false, ErrChatGPTTimezoneAmbiguous
	}
	_, chinaZone := chinaTimezones[zone.String()]
	rewriteZone := zone.Type == gjson.String && chinaZone
	rewriteOffset := offset.Type == gjson.Number &&
		(rewriteZone || (zones == 0 && offset.Num == chatGPTChinaOffsetMinutes))
	if !rewriteZone && !rewriteOffset {
		return body, false, nil
	}
	loc, err := codexRewriteZone()
	if err != nil {
		return body, false, err
	}
	out = body
	if rewriteZone {
		if out, err = sjson.SetBytes(out, "timezone", CodexRewriteTimezone); err != nil {
			return body, false, err
		}
	}
	if rewriteOffset {
		_, seconds := now.In(loc).Zone()
		if out, err = sjson.SetBytes(out, "timezone_offset_min", -seconds/60); err != nil {
			return body, false, err
		}
	}
	return out, true, nil
}
