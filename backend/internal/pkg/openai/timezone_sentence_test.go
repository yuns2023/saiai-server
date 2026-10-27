package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// desktopCollaborationMode is the developer message app-server 0.164 sends in
// every request of a Codex Desktop aeon thread (instructions from the test
// harness; the sentence and deadline are Desktop 26.1021's).
func desktopCollaborationMode(zone string) string {
	return "<collaboration_mode>You are working on a long task.\n\nThe user's timezone is " + zone +
		"; use it for dates, times, and recurring schedules unless specified otherwise.\n\n" +
		"The task deadline is 2027-10-27 02:00:00 UTC. Do not infer early expiration.</collaboration_mode>"
}

func TestRewriteCodexDesktopTimezoneSentence(t *testing.T) {
	text := desktopCollaborationMode("Asia/Shanghai")
	if len(text) != 266 {
		t.Fatalf("fixture drifted from the captured shape: len=%d", len(text))
	}
	want := desktopCollaborationMode(CodexRewriteTimezone)
	for name, render := range map[string]func(string) []byte{
		"serde": func(s string) []byte { return serdeBody(s) },
		"html":  func(s string) []byte { return responsesBody(t, s) },
	} {
		body := render(text)
		if zone, found := FindChinaTimezone(body); !found || zone != "Asia/Shanghai" {
			t.Fatalf("%s: leak net = %q, %v", name, zone, found)
		}
		out, stats := mustRewrite(t, body)
		if stats.Timezones != 1 || stats.SentenceTimezones != 1 || stats.LooseTimezones != 0 || stats.Dates != 0 {
			t.Fatalf("%s: stats = %+v", name, stats)
		}
		if !bytes.Equal(out, render(want)) {
			t.Fatalf("%s: got %s", name, out)
		}
		if _, found := FindChinaTimezone(out); found {
			t.Fatalf("%s: leak net still matches", name)
		}
		again, againStats := mustRewrite(t, out)
		if againStats.Changed() || !bytes.Equal(again, out) {
			t.Fatalf("%s: not idempotent: %+v", name, againStats)
		}
	}
	// Other zones are untouched and not flagged.
	for _, zone := range []string{"Asia/Tokyo", "America/New_York", "Etc/GMT-8", "UTC"} {
		body := serdeBody(desktopCollaborationMode(zone))
		if _, found := FindChinaTimezone(body); found {
			t.Fatalf("%s flagged", zone)
		}
		if out, stats := mustRewrite(t, body); stats.Changed() || !bytes.Equal(out, body) {
			t.Fatalf("%s rewritten", zone)
		}
	}
}

func TestRewriteCodexTimezoneSentenceShapes(t *testing.T) {
	la := CodexRewriteTimezone
	const a = "The user's timezone is "
	cases := []struct{ in, want string }{
		{a + "PRC", a + la},
		{a + "Asia/Hong_Kong.", a + la + "."},
		{a + "Asia/Macau\n" + a + "Hongkong;", a + la + "\n" + a + la + ";"},
		// An anchor right after a value is still found.
		{a + "XThe user's timezone is PRC", a + "XThe user's timezone is " + la},
		{a + a + "PRC", a + a + la},
		// With a Desktop client time context in the same string.
		{desktopTimeContext("Asia/Shanghai", "2026-10-25") + a + "PRC;", desktopTimeContext(la, rewriteToday) + a + la + ";"},
		// Untouched: other zones, longer names, other spellings, overlong runs.
		{a + "Asia/Shanghai2", ""},
		{a + "PRCX", ""},
		{a + " PRC", ""},
		{"the user's timezone is PRC", ""},
		{"The user’s timezone is PRC", ""},
		{"The user's time zone is PRC", ""},
		{a + "Asia/Shanghai" + strings.Repeat("x", 60), ""},
		{"The user's timezone is", ""},
	}
	for _, c := range cases {
		if c.want == "" {
			c.want = c.in
		}
		for _, render := range []func(string) []byte{func(s string) []byte { return serdeBody(s) }, func(s string) []byte { return responsesBody(t, s) }} {
			out, _ := mustRewrite(t, render(c.in))
			if !bytes.Equal(out, render(c.want)) {
				t.Fatalf("%.80q:\ngot  %.300s\nwant %.300s", c.in, out, render(c.want))
			}
			_, found := FindChinaTimezone(render(c.in))
			if found != (c.want != c.in) {
				t.Fatalf("%.80q: leak net = %v", c.in, found)
			}
		}
	}
}

func TestRewriteCodexTimezoneSentenceEscapes(t *testing.T) {
	la := CodexRewriteTimezone
	for raw, want := range map[string]string{
		`The user\u0027s timezone is Asia/Shanghai; x`:     `The user\u0027s timezone is ` + la + `; x`,
		`The user's timezone is Asia\/Shanghai\n`:          `The user's timezone is ` + la + `\n`,
		`The user's timezone is \u0050RC`:                  `The user's timezone is ` + la,
		`\u0054he user's timezone is PR\u0043\u00e9`:       `\u0054he user's timezone is ` + la + `\u00e9`,
		`The user's timezone is PRC\u00e9`:                 `The user's timezone is ` + la + `\u00e9`,
		`The user's timezone is PRC\ud83d\ude00`:           `The user's timezone is ` + la + `\ud83d\ude00`,
		`The user's timezone is \u00e4PRC`:                 ``,
		`The user's timezone is Asia/Shanghai\u0032 x \"y`: ``,
	} {
		body := []byte(`{"input":"` + raw + `"}`)
		if want == "" {
			want = raw
		}
		out, _ := mustRewrite(t, body)
		if string(out) != `{"input":"`+want+`"}` {
			t.Fatalf("%s: got %s", raw, out)
		}
		if _, found := FindChinaTimezone(body); found != (want != raw) {
			t.Fatalf("%s: leak net = %v", raw, found)
		}
	}
}

// A sentence in an object key or item metadata is never edited, so the leak
// net rejects it (fail closed).
func TestTimezoneSentenceInKeysAndMetadataIsLeft(t *testing.T) {
	const s = "The user's timezone is PRC"
	for _, body := range [][]byte{
		[]byte(`{"input":[{"` + s + `":1}]}`),
		codexItemsBody("", `{"type":"message","role":"developer","content":[],"internal_chat_message_metadata_passthrough":{"provenance":"`+s+`"}}`),
	} {
		out, stats := mustRewrite(t, body)
		if stats.Changed() || !bytes.Equal(out, body) {
			t.Fatalf("edited %s", body)
		}
		if _, found := FindChinaTimezone(out); !found {
			t.Fatalf("leak net misses %s", body)
		}
	}
}

// Whatever the leak net matches in a string value, the rewrite removes.
func TestRewriteCodexTimezoneSentencesMatchLeakNet(t *testing.T) {
	parts := []string{
		"The user's timezone is ", "The user's ", "timezone is ", "The ", "T", "<timezone>", "</timezone>",
		"<current_date>", "</current_date>", "<environment_context>", "</environment_context>",
		"PRC", "Asia/Shanghai", "Asia/Tokyo", "2026-10-25", ";", ".", " ", "\n", "x", "é", "\"", "\\",
	}
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 20000; i++ {
		var text strings.Builder
		for n := rng.Intn(14); n >= 0; n-- {
			_, _ = text.WriteString(parts[rng.Intn(len(parts))])
		}
		quoted, _ := json.Marshal(text.String())
		// Every rune as a \u escape (all parts are in the BMP).
		escaped := []byte(`{"input":"`)
		for _, r := range text.String() {
			escaped = fmt.Appendf(escaped, `\u%04x`, r)
		}
		escaped = append(escaped, `"}`...)
		for _, body := range [][]byte{responsesBody(t, text.String()), []byte(`{"input":` + string(quoted) + `}`), escaped} {
			out, _, err := RewriteCodexEnvironmentTime(body, rewriteNow)
			if err != nil {
				continue // malformed whole-string block: rejected (fail closed)
			}
			if !json.Valid(out) {
				t.Fatalf("%q: invalid JSON %s", text.String(), out)
			}
			if zone, found := FindChinaTimezone(out); found {
				t.Fatalf("%q: leak net still matches %s in %s", text.String(), zone, out)
			}
			again, _, err := RewriteCodexEnvironmentTime(out, rewriteNow)
			if err != nil || !bytes.Equal(again, out) {
				t.Fatalf("%q: not idempotent: %v", text.String(), err)
			}
		}
	}
}
