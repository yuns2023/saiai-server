package openai

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// desktopTimeContext is the developer message Codex Desktop (Electron front
// end, codex_apps_client_time_context) adds to every turn.
func desktopTimeContext(zone, date string) string {
	return "<codex_apps_client_time_context><timezone>" + zone + "</timezone>\n<current_date>" + date + "</current_date>\n" +
		"Use this client time context for user-facing dates, times, and schedules instead of the execution host's timezone and current date.</codex_apps_client_time_context>"
}

func TestRewriteCodexDesktopClientTimeContext(t *testing.T) {
	text := desktopTimeContext("Asia/Shanghai", "2026-10-25")
	if len(text) != 271 || strings.Index(text, "<timezone>") != 32 {
		t.Fatalf("fixture drifted from the production shape: len=%d", len(text))
	}
	want := desktopTimeContext(CodexRewriteTimezone, rewriteToday)
	for name, render := range map[string]func(string) []byte{
		"serde": func(s string) []byte { return serdeBody(s) },
		"html":  func(s string) []byte { return responsesBody(t, s) },
	} {
		body := render(text)
		out, stats := mustRewrite(t, body)
		if stats.Timezones != 1 || stats.LooseTimezones != 1 || stats.Dates != 1 || stats.Blocks != 0 {
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

	// As a developer message item with create_time, the item's LA date wins.
	created := epochSeconds(time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)) // 2026-10-20 LA
	item := func(text string) string {
		quoted, _ := json.Marshal(text)
		return `{"type":"message","role":"developer","content":[{"type":"input_text","text":` + string(quoted) +
			`}],"internal_chat_message_metadata_passthrough":{"create_time":` + created + `}}`
	}
	out, _ := mustRewrite(t, codexItemsBody("", item(text)))
	if !bytes.Equal(out, codexItemsBody("", item(desktopTimeContext(CodexRewriteTimezone, "2026-10-20")))) {
		t.Fatalf("got %s", out)
	}
}

func TestRewriteCodexLooseTagShapes(t *testing.T) {
	la := CodexRewriteTimezone
	cases := []struct{ in, want string }{
		// Whitespace of any length (Unicode included) is kept around the value.
		{"<timezone>" + strings.Repeat(" ", 5000) + "PRC\u3000\n</timezone>", "<timezone>" + strings.Repeat(" ", 5000) + la + "\u3000\n</timezone>"},
		{"a<timezone>Asia/Hong_Kong</timezone>b<timezone>Asia/Macau</timezone>c", "a<timezone>" + la + "</timezone>b<timezone>" + la + "</timezone>c"},
		// Date before the zone, and a second context in the same string.
		{"<current_date> 2026-10-25 </current_date><timezone>PRC</timezone>", "<current_date> " + rewriteToday + " </current_date><timezone>" + la + "</timezone>"},
		// An earlier opening tag whose value holds another tag is not a zone;
		// the inner pair is.
		{"<timezone>x<timezone>PRC</timezone>", "<timezone>x<timezone>" + la + "</timezone>"},
		{"<current_date><timezone>PRC</timezone></current_date>", "<current_date><timezone>" + la + "</timezone></current_date>"},
		// Untouched: other zones and their dates, non-dates, inner text,
		// comments, wrong case, overlong values, unclosed tags.
		{"<timezone>Asia/Tokyo</timezone><current_date>2026-10-25</current_date>", ""},
		{"<timezone>PRC</timezone><current_date>2026-10-25T01:00</current_date>", "<timezone>" + la + "</timezone><current_date>2026-10-25T01:00</current_date>"},
		{"<timezone>Asia/Shang hai</timezone>", ""},
		{"<timezone>PRC<!-- c --></timezone>", ""},
		{"<timezone>prc</timezone>", ""},
		{"<timezone>" + strings.Repeat("A", 65) + "</timezone>", ""},
		{"<timezone>PRC", ""},
		{"<timezone>PRC</timezone ", ""},
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
		}
	}
}

func TestRewriteCodexLooseTagEscapes(t *testing.T) {
	la := CodexRewriteTimezone
	for raw, want := range map[string]string{
		`\u003ctimezone\u003eAsia/Shanghai\u003c/timezone\u003e`:    `\u003ctimezone\u003e` + la + `\u003c/timezone\u003e`,
		`<timezone>Asia\/Shanghai<\/timezone>`:                      `<timezone>` + la + `<\/timezone>`,
		`<timezone>\u0041sia/Shangha\u0069</timezone>`:              `<timezone>` + la + `</timezone>`,
		`<timezone>\u3000PRC\t</timezone>`:                          `<timezone>\u3000` + la + `\t</timezone>`,
		`<timezone>\u00e4PRC</timezone>`:                            ``,
		`\u001b[0m<timezone>PRC</timezone>\u001b[0m x \ud83d\ude00`: `\u001b[0m<timezone>` + la + `</timezone>\u001b[0m x \ud83d\ude00`,
	} {
		body := []byte(`{"input":"` + raw + `"}`)
		if want == "" {
			want = raw
		}
		out, _ := mustRewrite(t, body)
		if string(out) != `{"input":"`+want+`"}` {
			t.Fatalf("%s: got %s", raw, out)
		}
	}
}

// Whatever the leak net matches in a string value, the rewrite removes.
func TestRewriteCodexLooseTagsMatchLeakNet(t *testing.T) {
	parts := []string{
		"<timezone>", "</timezone>", "<current_date>", "</current_date>", "<environment_context>", "</environment_context>",
		"PRC", "Asia/Shanghai", "Asia/Tokyo", "2026-10-25", " ", "\u3000", "\n", "<", ">", "/", "x", "\"", "\\", "é",
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		var text strings.Builder
		for n := rng.Intn(14); n >= 0; n-- {
			text.WriteString(parts[rng.Intn(len(parts))])
		}
		for _, body := range [][]byte{responsesBody(t, text.String()), serdeBody(strings.ReplaceAll(strings.ReplaceAll(text.String(), `\`, `\\`), `"`, `\"`))} {
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
