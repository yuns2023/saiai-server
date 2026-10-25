package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const rewriteToday = "2026-10-24"

// serdeBody renders a Responses body the way Codex (serde_json) does: no HTML
// escaping, `\n` for newlines.
func serdeBody(texts ...string) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"gpt-5.1","input":[`)
	for i, text := range texts {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"`)
		b.WriteString(strings.ReplaceAll(text, "\n", `\n`))
		b.WriteString(`"}]}`)
	}
	b.WriteString(`],"stream":true}`)
	return []byte(b.String())
}

func envBlock(date, zone string) string {
	return "<environment_context>\n  <cwd>/work</cwd>\n  <shell>zsh</shell>\n  <current_date>" + date + "</current_date>\n  <timezone>" + zone + "</timezone>\n</environment_context>"
}

func mustRewrite(t *testing.T, body []byte) ([]byte, CodexEnvironmentRewriteStats) {
	t.Helper()
	out, stats, err := RewriteCodexEnvironmentTime(body, rewriteToday)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	return out, stats
}

func decodedTexts(t *testing.T, body []byte) []string {
	t.Helper()
	var parsed struct {
		Input []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	var texts []string
	for _, item := range parsed.Input {
		for _, part := range item.Content {
			texts = append(texts, part.Text)
		}
	}
	return texts
}

func TestRewriteCodexEnvironmentTimeSerdeBody(t *testing.T) {
	body := serdeBody("hello " + envBlock("2026-10-25", "Asia/Shanghai") + " bye")
	out, stats := mustRewrite(t, body)
	want := serdeBody("hello " + envBlock(rewriteToday, CodexRewriteTimezone) + " bye")
	if !bytes.Equal(out, want) {
		t.Fatalf("got  %s\nwant %s", out, want)
	}
	if stats != (CodexEnvironmentRewriteStats{Blocks: 1, Timezones: 1, Dates: 1}) || !stats.Changed() {
		t.Fatalf("stats = %+v", stats)
	}
	if bytes.Contains(body, []byte(CodexRewriteTimezone)) {
		t.Fatal("input was modified")
	}
}

func TestRewriteCodexEnvironmentTimeHTMLEscapedBody(t *testing.T) {
	// encoding/json escapes < and > as < / >; every escape outside the
	// replaced values must stay byte-identical.
	body := responsesBody(t, envBlock("2026-10-25", "Asia/Hong_Kong"), "a<b> & c")
	out, stats := mustRewrite(t, body)
	if stats.Timezones != 1 || stats.Dates != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	want := bytes.Replace(body, []byte("Asia/Hong_Kong"), []byte(CodexRewriteTimezone), 1)
	want = bytes.Replace(want, []byte("2026-10-25"), []byte(rewriteToday), 1)
	if !bytes.Equal(out, want) {
		t.Fatalf("got  %s\nwant %s", out, want)
	}
	texts := decodedTexts(t, out)
	if texts[0] != envBlock(rewriteToday, CodexRewriteTimezone) || texts[1] != "a<b> & c" {
		t.Fatalf("decoded = %q", texts)
	}
}

func TestRewriteCodexEnvironmentTimeEscapeForms(t *testing.T) {
	cases := map[string]string{
		"upper hex":        `\u003Cenvironment_context\u003E\u003Ctimezone\u003EAsia/Shanghai\u003C\/timezone\u003E\u003C/environment_context\u003E`,
		"lower hex":        `\u003cenvironment_context\u003e\u003ctimezone\u003eAsia/Shanghai\u003c/timezone\u003e\u003c/environment_context\u003e`,
		"escaped slash":    `<environment_context><timezone>Asia\/Shanghai<\/timezone><\/environment_context>`,
		"escaped word":     `<environment\u005fcontext><timezone>Asia/Shanghai</timezone></\u0065nvironment_context>`,
		"escaped value":    `<environment_context><timezone>\u0041sia/Shangha\u0069</timezone></environment_context>`,
		"unicode before":   `caf\u00e9 \ud83d\ude00 \u2028 \"q\" <environment_context><timezone>Asia/Shanghai</timezone></environment_context> \u00e9`,
		"whitespace value": `<environment_context><timezone>\n  Asia/Shanghai \t</timezone></environment_context>`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"input":[{"type":"message","content":[{"type":"input_text","text":"` + raw + `"}]}]}`)
			out, stats := mustRewrite(t, body)
			if stats.Timezones != 1 {
				t.Fatalf("stats = %+v", stats)
			}
			// Exactly one raw span differs and it now holds plain ASCII.
			prefix := commonPrefix(body, out)
			suffix := commonSuffix(body[prefix:], out[prefix:])
			if got := string(out[prefix : len(out)-suffix]); !strings.Contains(CodexRewriteTimezone, got) {
				t.Fatalf("changed span %q is not part of the target", got)
			}
			if old := body[prefix : len(body)-suffix]; bytes.ContainsAny(old, `<>"`) || bytes.Contains(old, []byte("u003")) {
				t.Fatalf("changed span %q reaches outside the value", old)
			}
			var decoded string
			texts := decodedTexts(t, out)
			decoded = texts[0]
			if !strings.Contains(decoded, "<timezone>"+CodexRewriteTimezone+"</timezone>") {
				t.Fatalf("decoded = %q", decoded)
			}
			again, againStats := mustRewrite(t, out)
			if againStats.Changed() || !bytes.Equal(again, out) {
				t.Fatalf("not idempotent: %+v", againStats)
			}
		})
	}
}

func commonPrefix(a, b []byte) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func commonSuffix(a, b []byte) int {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	return n
}

func TestRewriteCodexEnvironmentTimeIgnoresKeysAndOutsideTags(t *testing.T) {
	block := envBlock("2026-10-25", "Asia/Shanghai")
	key, _ := json.Marshal(block)
	body := []byte(`{` + string(key) + `:"<timezone>Asia/Shanghai</timezone> <current_date>2026-10-25</current_date>","list":["<timezone>PRC</timezone>"]}`)
	out, stats := mustRewrite(t, body)
	if stats.Changed() || stats.Blocks != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if &out[0] != &body[0] {
		t.Fatal("unchanged body must be returned as-is")
	}

	// A block in one string does not reach tags in another string or after it.
	body = serdeBody(block+" <timezone>Asia/Shanghai</timezone>", "<timezone>Asia/Urumqi</timezone>")
	out, stats = mustRewrite(t, body)
	if stats.Timezones != 1 || stats.Dates != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	texts := decodedTexts(t, out)
	if texts[0] != envBlock(rewriteToday, CodexRewriteTimezone)+" <timezone>Asia/Shanghai</timezone>" || texts[1] != "<timezone>Asia/Urumqi</timezone>" {
		t.Fatalf("decoded = %q", texts)
	}
}

func TestRewriteCodexEnvironmentTimeUnavailableDateAndTargetZone(t *testing.T) {
	text := "<environment_context>\n  <current_date status=\"unavailable\" />\n  <timezone>Asia/Shanghai</timezone>\n</environment_context>"
	body, _ := json.Marshal(map[string]any{"input": []any{map[string]any{"content": []any{map[string]any{"text": text}}}}})
	out, stats := mustRewrite(t, body)
	if stats.Timezones != 1 || stats.Dates != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if got := decodedTexts(t, out)[0]; got != strings.Replace(text, "Asia/Shanghai", CodexRewriteTimezone, 1) {
		t.Fatalf("decoded = %q", got)
	}

	// Already the target zone and date: nothing changes.
	body = serdeBody(envBlock(rewriteToday, CodexRewriteTimezone))
	out, stats = mustRewrite(t, body)
	if stats.Changed() || stats.Blocks != 1 || &out[0] != &body[0] {
		t.Fatalf("stats = %+v", stats)
	}

	// A non-CN zone is rewritten too, only the date when the zone matches.
	out, stats = mustRewrite(t, serdeBody(envBlock("2026-01-01", "Europe/Berlin")))
	if stats.Timezones != 1 || stats.Dates != 1 || !bytes.Equal(out, serdeBody(envBlock(rewriteToday, CodexRewriteTimezone))) {
		t.Fatalf("stats = %+v out=%s", stats, out)
	}
	_, stats = mustRewrite(t, serdeBody(envBlock("2026-01-01", CodexRewriteTimezone)))
	if stats.Timezones != 0 || stats.Dates != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestRewriteCodexEnvironmentTimeMultipleBlocksAndRoles(t *testing.T) {
	first := envBlock("2026-10-20", "Asia/Shanghai")
	second := envBlock("2026-10-21", "Asia/Macau")
	body, _ := json.Marshal(map[string]any{
		"model":        "gpt-5.1",
		"instructions": "sys " + first,
		"input": []any{
			map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": first + "\n" + second}}},
			map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": second}}},
			map[string]any{"type": "function_call_output", "output": map[string]any{"nested": []any{[]any{first}}}},
		},
	})
	out, stats := mustRewrite(t, body)
	if stats.Blocks != 5 || stats.Timezones != 5 || stats.Dates != 5 {
		t.Fatalf("stats = %+v", stats)
	}
	if bytes.Contains(out, []byte("Asia/")) || bytes.Contains(out, []byte("2026-10-20")) || bytes.Contains(out, []byte("2026-10-21")) {
		t.Fatalf("leftover values: %s", out)
	}
	var before, after map[string]any
	_ = json.Unmarshal(body, &before)
	if err := json.Unmarshal(out, &after); err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("shape changed")
	}
}

func TestRewriteCodexEnvironmentTimeMalformedFailsClosed(t *testing.T) {
	cases := map[string][]byte{
		"unclosed block":         serdeBody("<environment_context><timezone>Asia/Shanghai</timezone>"),
		"nested block":           serdeBody("<environment_context><environment_context></environment_context></environment_context>"),
		"unclosed timezone":      serdeBody("<environment_context><timezone>Asia/Shanghai</environment_context>"),
		"stray timezone close":   serdeBody("<environment_context>Asia/Shanghai</timezone></environment_context>"),
		"nested timezone":        serdeBody("<environment_context><timezone><timezone>PRC</timezone></timezone></environment_context>"),
		"unclosed current_date":  serdeBody("<environment_context><current_date>2026-10-25</environment_context>"),
		"invalid json":           []byte(`{"input":"<environment_context></environment_context>"`),
		"invalid json escape":    []byte(`{"input":"<environment_context>\u12"}`),
		"unterminated string":    []byte(`{"input":"<environment_context>`),
		"timezone wraps date":    serdeBody("<environment_context><timezone><current_date>x</current_date></timezone></environment_context>"),
		"date wraps target zone": serdeBody("<environment_context><current_date><timezone>America/Los_Angeles</timezone></current_date></environment_context>"),
		"crossing tags":          serdeBody("<environment_context><timezone>PRC<current_date></timezone>2026-10-25</current_date></environment_context>"),
		"truncated date tag":     serdeBody("<environment_context><timezone>Asia/Shanghai</timezone><current_date</environment_context>"),
		"spaced timezone tag":    serdeBody("<environment_context><timezone >Asia/Shanghai</timezone ></environment_context>"),
		"attribute timezone tag": serdeBody(`<environment_context><timezone a=\"1\">PRC</timezone></environment_context>`),
		"unclosed attribute":     serdeBody(`<environment_context><timezone status=\"Asia/Shanghai /></environment_context>`),
		"bare attribute":         serdeBody(`<environment_context><timezone unavailable /></environment_context>`),
		"escaped unclosed block": serdeBody(`\u003cenvironment_context\u003e<timezone>PRC</timezone>`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			out, stats, err := RewriteCodexEnvironmentTime(body, rewriteToday)
			if !errors.Is(err, ErrCodexEnvironmentMalformed) {
				t.Fatalf("err = %v", err)
			}
			if stats.Changed() || !bytes.Equal(out, body) {
				t.Fatal("malformed input must not produce a partial rewrite")
			}
		})
	}
}

func TestRewriteCodexEnvironmentTimeNoDoubleDecoding(t *testing.T) {
	// `\\u003c` is a literal backslash followed by "u003c", not "<".
	body := []byte(`{"input":"\\u003cenvironment_context\\u003e<timezone>PRC</timezone>\\u003c/environment_context\\u003e"}`)
	out, stats := mustRewrite(t, body)
	if stats.Blocks != 0 || &out[0] != &body[0] {
		t.Fatalf("stats = %+v", stats)
	}
	// Mentioning the word in prose is not a block.
	body = serdeBody("please explain environment_context handling")
	if _, stats = mustRewrite(t, body); stats.Blocks != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestRewriteCodexEnvironmentTimeFastPathAllocs(t *testing.T) {
	body := serdeBody("plain text without context", strings.Repeat("x", 4096))
	allocs := testing.AllocsPerRun(50, func() {
		out, _, err := RewriteCodexEnvironmentTime(body, rewriteToday)
		if err != nil || &out[0] != &body[0] {
			t.Fatal("fast path changed body")
		}
	})
	if allocs != 0 {
		t.Fatalf("fast path allocs = %v", allocs)
	}
}

func TestRewriteCodexEnvironmentTimeLargeBody(t *testing.T) {
	filler := strings.Repeat(`line with \"quotes\" and \u001b[0m escapes\n`, 400_000) // ~16 MiB raw
	body := []byte(`{"model":"gpt-5.1","input":[{"type":"function_call_output","output":"` + filler + `"},` +
		`{"type":"message","content":[{"type":"input_text","text":"` + strings.ReplaceAll(envBlock("2026-10-25", "Asia/Shanghai"), "\n", `\n`) + `"}]}]}`)
	start := time.Now()
	out, stats := mustRewrite(t, body)
	if stats.Timezones != 1 || stats.Dates != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if len(out) != len(body)+len(CodexRewriteTimezone)-len("Asia/Shanghai") {
		t.Fatalf("len = %d", len(out))
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("rewrite of %d bytes took %v", len(body), elapsed)
	}
	if !bytes.Equal(out[:len(filler)], body[:len(filler)]) {
		t.Fatal("filler changed")
	}
}

func TestCodexRewriteDate(t *testing.T) {
	cases := map[string]string{
		"2026-10-25T06:59:59Z": "2026-10-24", // PDT, UTC-7
		"2026-10-25T07:00:00Z": "2026-10-25",
		"2026-12-01T07:59:59Z": "2026-11-30", // PST, UTC-8
		"2026-12-01T08:00:00Z": "2026-12-01",
	}
	for in, want := range cases {
		now, _ := time.Parse(time.RFC3339, in)
		got, err := CodexRewriteDate(now.In(time.FixedZone("CST", 8*3600)))
		if err != nil || got != want {
			t.Fatalf("%s: got %q err=%v want %q", in, got, err, want)
		}
	}
}

func TestRewriteCodexEnvironmentTimeSelfClosingTags(t *testing.T) {
	text := `<environment_context><timezone status=\"unavailable\" /><current_date status=\"unavailable\"/></environment_context>`
	body := serdeBody(text)
	out, stats := mustRewrite(t, body)
	if stats.Changed() || stats.Blocks != 1 || !bytes.Equal(out, body) {
		t.Fatalf("stats = %+v", stats)
	}
}
