package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

const rewriteToday = "2026-10-24"

// rewriteNow is 2026-10-24 13:00 in Los Angeles.
var rewriteNow = time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC)

// serdeBody renders a Responses body the way Codex (serde_json) does: no HTML
// escaping, `\n` for newlines.
func serdeBody(texts ...string) []byte {
	items := make([]string, 0, len(texts))
	for _, text := range texts {
		items = append(items, `{"type":"message","role":"user","content":[{"type":"input_text","text":"`+
			strings.ReplaceAll(text, "\n", `\n`)+`"}]}`)
	}
	return []byte(`{"model":"gpt-5.1","input":[` + strings.Join(items, ",") + `],"stream":true}`)
}

func envBlock(date, zone string) string {
	return "<environment_context>\n  <cwd>/work</cwd>\n  <shell>zsh</shell>\n  <current_date>" + date + "</current_date>\n  <timezone>" + zone + "</timezone>\n</environment_context>"
}

func mustRewrite(t *testing.T, body []byte) ([]byte, CodexEnvironmentRewriteStats) {
	t.Helper()
	out, stats, err := RewriteCodexEnvironmentTime(body, rewriteNow)
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
	body := serdeBody(envBlock("2026-10-25", "Asia/Shanghai"))
	out, stats := mustRewrite(t, body)
	want := serdeBody(envBlock(rewriteToday, CodexRewriteTimezone))
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
		"unicode inside":   `<environment_context><cwd>caf\u00e9 \ud83d\ude00 \u2028 \"q\"</cwd><timezone>Asia/Shanghai</timezone></environment_context>`,
		"surrounding ws":   `\n \t<environment_context><timezone>Asia/Shanghai</timezone></environment_context>\r\n`,
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
	// Keys are never edited; loose CN tags in values are (with the dates of
	// the same string).
	body := []byte(`{` + string(key) + `:"<timezone>Asia/Shanghai</timezone> <current_date>2026-10-25</current_date>","list":["<timezone>PRC</timezone>"]}`)
	out, stats := mustRewrite(t, body)
	if stats.Blocks != 0 || stats.Timezones != 2 || stats.LooseTimezones != 2 || stats.Dates != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	want := `{` + string(key) + `:"<timezone>` + CodexRewriteTimezone + `</timezone> <current_date>` + rewriteToday + `</current_date>","list":["<timezone>` + CodexRewriteTimezone + `</timezone>"]}`
	if string(out) != want {
		t.Fatalf("got  %s\nwant %s", out, want)
	}

	// A block string and a loose tag in another string are both rewritten.
	body = serdeBody(block, "<timezone>Asia/Urumqi</timezone>")
	out, stats = mustRewrite(t, body)
	if stats.Timezones != 2 || stats.LooseTimezones != 1 || stats.Dates != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	texts := decodedTexts(t, out)
	if texts[0] != envBlock(rewriteToday, CodexRewriteTimezone) || texts[1] != "<timezone>"+CodexRewriteTimezone+"</timezone>" {
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
		"instructions": first,
		"input": []any{
			map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": first}, map[string]any{"type": "input_text", "text": second}}},
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
		"two blocks in one text": serdeBody(envBlock("2026-10-20", "PRC") + "\n" + envBlock("2026-10-21", "PRC")),
		"inner close then text":  serdeBody("<environment_context><timezone>PRC</timezone></environment_context> hi </environment_context>"),
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
		"escaped nested block":   serdeBody(`\u003cenvironment_context\u003e\u003cenvironment_context>\n</environment_context>`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			out, stats, err := RewriteCodexEnvironmentTime(body, rewriteNow)
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
	// The tag itself is a loose tag; the escaped backslashes stay.
	body := []byte(`{"input":"\\u003cenvironment_context\\u003e<timezone>PRC</timezone>\\u003c/environment_context\\u003e"}`)
	out, stats := mustRewrite(t, body)
	if stats.Blocks != 0 || stats.LooseTimezones != 1 ||
		string(out) != `{"input":"\\u003cenvironment_context\\u003e<timezone>`+CodexRewriteTimezone+`</timezone>\\u003c/environment_context\\u003e"}` {
		t.Fatalf("stats = %+v out = %s", stats, out)
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
		out, _, err := RewriteCodexEnvironmentTime(body, rewriteNow)
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

// codexItem renders a Codex 0.164 input item with item metadata placed
// before or after content; createTime is the raw JSON value ("" omits it).
func codexItem(text, createTime string, metadataFirst bool) string {
	content := `"content":[{"type":"input_text","text":"` + strings.ReplaceAll(text, "\n", `\n`) + `"}]`
	meta := `"internal_chat_message_metadata_passthrough":{"content_item_kinds":["environments.environment_context"],"content_item_metadata":[{"provenance":{"hook":false,"type":"harness"}}]`
	if createTime != "" {
		meta += `,"create_time":` + createTime
	}
	meta += `,"turn_id":"01a173e5-f37b-7662-9aa0-63aba7fbbb18"}`
	if metadataFirst {
		return `{"type":"message","role":"developer",` + meta + `,` + content + `}`
	}
	return `{"type":"message","role":"developer",` + content + `,` + meta + `}`
}

func codexItemsBody(prefix string, items ...string) []byte {
	return []byte(`{` + prefix + `"model":"gpt-5.5","input":[` + strings.Join(items, ",") + `]}`)
}

func epochSeconds(t time.Time) string {
	return fmt.Sprintf("%d.5886154", t.Unix())
}

func TestRewriteCodexEnvironmentTimeUsesItemCreateTime(t *testing.T) {
	block := envBlock("2026-10-01", "Asia/Shanghai")
	// 2026-10-23 23:30 in Los Angeles is already 2026-10-24 in UTC/Shanghai.
	yesterdayLA := time.Date(2026, 10, 24, 6, 30, 0, 0, time.UTC)
	for _, metadataFirst := range []bool{true, false} {
		createTime := epochSeconds(yesterdayLA)
		body := codexItemsBody("", codexItem(block, createTime, metadataFirst))
		out, stats := mustRewrite(t, body)
		want := codexItemsBody("", codexItem(envBlock("2026-10-23", CodexRewriteTimezone), createTime, metadataFirst))
		if !bytes.Equal(out, want) || stats.Dates != 1 || stats.Timezones != 1 {
			t.Fatalf("metadataFirst=%v stats=%+v\ngot  %s\nwant %s", metadataFirst, stats, out, want)
		}
		again, againStats := mustRewrite(t, out)
		if againStats.Changed() || !bytes.Equal(again, out) {
			t.Fatal("not idempotent")
		}
	}
}

func TestRewriteCodexEnvironmentTimeCreateTimeFallbacks(t *testing.T) {
	block := envBlock("2026-10-01", "Asia/Shanghai")
	for name, createTime := range map[string]string{
		"absent":         "",
		"string":         `"1792945812.5"`,
		"null":           "null",
		"object":         `{"seconds":1792945812}`,
		"before_2020":    "1500000000",
		"after_now_1day": epochSeconds(rewriteNow.Add(25 * time.Hour)),
		"milliseconds":   fmt.Sprint(rewriteNow.UnixMilli()),
		"negative":       "-1",
	} {
		t.Run(name, func(t *testing.T) {
			body := codexItemsBody("", codexItem(block, createTime, false))
			out, _ := mustRewrite(t, body)
			want := codexItemsBody("", codexItem(envBlock(rewriteToday, CodexRewriteTimezone), createTime, false))
			if !bytes.Equal(out, want) {
				t.Fatalf("got  %s\nwant %s", out, want)
			}
		})
	}
	// Within now+24h is accepted.
	soon := rewriteNow.Add(20 * time.Hour) // 2026-10-25 09:00 in LA
	body := codexItemsBody("", codexItem(block, epochSeconds(soon), true))
	out, _ := mustRewrite(t, body)
	if !bytes.Contains(out, []byte("<current_date>2026-10-25</current_date>")) {
		t.Fatalf("got %s", out)
	}
	// Metadata that is not an object, and duplicate metadata keys.
	body = []byte(`{"input":[{"internal_chat_message_metadata_passthrough":7,"content":"` + strings.ReplaceAll(block, "\n", `\n`) + `"}]}`)
	if out, _ = mustRewrite(t, body); !bytes.Contains(out, []byte("<current_date>"+rewriteToday)) {
		t.Fatalf("got %s", out)
	}
	body = []byte(`{"input":[{"internal_chat_message_metadata_passthrough":{},"internal_chat_message_metadata_passthrough":{},"content":"x"}],"environment_context":1}`)
	if _, _, err := RewriteCodexEnvironmentTime(body, rewriteNow); !errors.Is(err, ErrCodexEnvironmentMalformed) {
		t.Fatalf("duplicate metadata key: err = %v", err)
	}
}

func TestRewriteCodexEnvironmentTimeMultipleItemsAndWSFrame(t *testing.T) {
	first := time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)  // 2026-10-20 LA
	second := time.Date(2026, 10, 22, 18, 0, 0, 0, time.UTC) // 2026-10-22 LA
	items := []string{
		codexItem(envBlock("2026-10-21", "Asia/Shanghai"), epochSeconds(first), true),
		codexItem(envBlock("2026-10-23", "Asia/Shanghai"), epochSeconds(second), false),
		codexItem(envBlock("2026-10-23", "Asia/Shanghai"), "", false),
	}
	// A WebSocket response.create frame carries the same items.
	body := codexItemsBody(`"type":"response.create",`, items...)
	out, stats := mustRewrite(t, body)
	want := codexItemsBody(`"type":"response.create",`,
		codexItem(envBlock("2026-10-20", CodexRewriteTimezone), epochSeconds(first), true),
		codexItem(envBlock("2026-10-22", CodexRewriteTimezone), epochSeconds(second), false),
		codexItem(envBlock(rewriteToday, CodexRewriteTimezone), "", false),
	)
	if !bytes.Equal(out, want) || stats.Dates != 3 || stats.Timezones != 3 {
		t.Fatalf("stats=%+v\ngot  %s\nwant %s", stats, out, want)
	}
	// create_time bytes themselves are untouched.
	if !bytes.Contains(out, []byte(`"create_time":`+epochSeconds(first))) {
		t.Fatal("create_time changed")
	}
}

func TestRewriteCodexEnvironmentTimeCreateTimeScope(t *testing.T) {
	block := strings.ReplaceAll(envBlock("2026-10-01", "Asia/Shanghai"), "\n", `\n`)
	itemTime := epochSeconds(time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)) // 2026-10-20 LA
	otherTime := epochSeconds(time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC))
	cases := map[string]struct{ body, date string }{
		// A same-name key nested in content does not override the item's.
		"nested_same_key": {`{"input":[{"content":[{"internal_chat_message_metadata_passthrough":{"create_time":` + otherTime + `},"text":"` + block + `"}],"internal_chat_message_metadata_passthrough":{"create_time":` + itemTime + `}}]}`, "2026-10-20"},
		// An item without metadata does not borrow from the root object.
		"no_borrow_from_root": {`{"internal_chat_message_metadata_passthrough":{"create_time":` + itemTime + `},"input":[{"content":"` + block + `"}]}`, rewriteToday},
		// A non-input array is not an item list.
		"outside_input": {`{"other":[{"internal_chat_message_metadata_passthrough":{"create_time":` + itemTime + `},"content":"` + block + `"}]}`, rewriteToday},
		// An escaped metadata key is still recognised.
		"escaped_key": {`{"input":[{"content":"` + block + `","internal\u005fchat_message_metadata_passthrough":{"create_time":` + itemTime + `}}]}`, "2026-10-20"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, _ := mustRewrite(t, []byte(tc.body))
			if !bytes.Contains(out, []byte("<current_date>"+tc.date+"</current_date>")) {
				t.Fatalf("got %s", out)
			}
		})
	}

	// Strings inside the metadata (create_time included) are never edited.
	body := []byte(`{"input":[{"internal_chat_message_metadata_passthrough":{"create_time":"` + block + `","x":["` + block + `"]},"content":"` + block + `"}]}`)
	out, stats := mustRewrite(t, body)
	if stats.Dates != 1 || bytes.Count(out, []byte("Asia/Shanghai")) != 2 {
		t.Fatalf("stats=%+v got %s", stats, out)
	}

	// The upper bound keeps fractional seconds: frozen now has .9s and the
	// item is 24h+0.5s later, which is inside now+24h.
	now := time.Date(2026, 10, 24, 20, 0, 0, 900_000_000, time.UTC)
	createTime := fmt.Sprintf("%d.5", now.Add(24*time.Hour).Unix())
	body = codexItemsBody("", codexItem(envBlock("2026-10-01", "Asia/Shanghai"), createTime, true))
	out, _, err := RewriteCodexEnvironmentTime(body, now)
	if err != nil || !bytes.Contains(out, []byte("<current_date>2026-10-25</current_date>")) {
		t.Fatalf("err=%v got %s", err, out)
	}
}

func TestRewriteCodexEnvironmentTimeMalformedMetadataStringFailsClosed(t *testing.T) {
	body := []byte(`{"input":[{"internal_chat_message_metadata_passthrough":{"note":"<environment_context><current_date>x</environment_context>"},"content":"hi"}]}`)
	if _, _, err := RewriteCodexEnvironmentTime(body, rewriteNow); !errors.Is(err, ErrCodexEnvironmentMalformed) {
		t.Fatalf("err = %v", err)
	}
}

// A huge escaped string containing environment_context must not be
// materialized: extra memory is bounded by the block size, not the string.
func TestRewriteCodexEnvironmentTimeHugeEscapedStringMemory(t *testing.T) {
	block := strings.ReplaceAll(envBlock("2026-10-01", "Asia/Shanghai"), "\n", `\n`)
	block = strings.ReplaceAll(block, "<", `\u003c`)                                                    // escaped tags too
	filler := strings.Repeat(`log line with <environment_context> \"quoted\" and \u001b[0m\n`, 600_000) // ~33 MiB
	body := []byte(`{"input":[{"type":"function_call_output","output":"` + filler + block + filler + `"},` +
		`{"type":"message","content":[{"type":"input_text","text":"` + block + `"},{"type":"input_text","text":"` + block + `"}]}]}`)

	measure := func(fn func()) int64 {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		fn()
		runtime.ReadMemStats(&after)
		return int64(after.TotalAlloc - before.TotalAlloc)
	}
	var out []byte
	var stats CodexEnvironmentRewriteStats
	var err error
	allocated := measure(func() { out, stats, err = RewriteCodexEnvironmentTime(body, rewriteNow) })
	// The block embedded in the huge string is rewritten too (it used to be
	// skipped; the leak net then rejected the request), the unclosed
	// mentions around it are not.
	if err != nil || stats.Blocks != 3 || stats.Embedded != 1 || stats.Timezones != 3 || stats.Dates != 3 {
		t.Fatalf("err=%v stats=%+v", err, stats)
	}
	// The only O(len) allocation is the rewritten output itself.
	if extra := allocated - int64(cap(out)); extra > 4<<20 {
		t.Fatalf("extra allocation %d bytes for a %d byte body", extra, len(body))
	}
	head := len(`{"input":[{"type":"function_call_output","output":"`) + len(filler)
	grow := len(CodexRewriteTimezone) - len("Asia/Shanghai")
	if !bytes.Equal(out[:head], body[:head]) || !bytes.Equal(out[head+len(block)+grow:head+len(block)+grow+len(filler)], body[head+len(block):head+len(block)+len(filler)]) {
		t.Fatal("user content around the embedded block changed")
	}
	if bytes.Count(out, []byte(CodexRewriteTimezone)) != 3 {
		t.Fatalf("rewritten blocks = %d", bytes.Count(out, []byte(CodexRewriteTimezone)))
	}

	// A huge string that is shaped like a block fails closed, also bounded.
	huge := []byte(`{"input":"\u003cenvironment_context>` + filler + `</environment_context>"}`)
	allocated = measure(func() { _, _, err = RewriteCodexEnvironmentTime(huge, rewriteNow) })
	if !errors.Is(err, ErrCodexEnvironmentMalformed) || allocated > 4<<20 {
		t.Fatalf("err=%v allocated=%d", err, allocated)
	}
}

// User text that merely mentions the tag is user content: never edited and
// never rejected, whether the tag is closed or not. (A well-formed block
// embedded in user text is rewritten since the embedded-block change; see
// TestRewriteCodexEnvironmentTimeEmbeddedBlocks.)
func TestRewriteCodexEnvironmentTimeUserTextMentioningTag(t *testing.T) {
	for _, text := range []string{
		"please explain what <environment_context> means",
		"<environment_context> is unclosed here",
		"the block ends with </environment_context>",
		"x <environment_context><timezone>PRC</environment_context>",
	} {
		for _, body := range [][]byte{serdeBody(text), responsesBody(t, text)} {
			out, stats, err := RewriteCodexEnvironmentTime(body, rewriteNow)
			if err != nil || stats.Changed() || !bytes.Equal(out, body) {
				t.Fatalf("%q: err=%v stats=%+v", text, err, stats)
			}
		}
	}
	// Not blocks, never rejected, but their CN zone tags are loose tags.
	for _, text := range []string{
		"<environment_context><timezone>Asia/Shanghai</timezone>",
		"x </environment_context><timezone>PRC</timezone><environment_context>",
	} {
		for _, render := range []func(string) []byte{func(s string) []byte { return serdeBody(s) }, func(s string) []byte { return responsesBody(t, s) }} {
			out, stats, err := RewriteCodexEnvironmentTime(render(text), rewriteNow)
			want := render(regexp.MustCompile(`<timezone>[^<]*</timezone>`).ReplaceAllString(text, "<timezone>"+CodexRewriteTimezone+"</timezone>"))
			if err != nil || stats.LooseTimezones != 1 || stats.Blocks != 0 || !bytes.Equal(out, want) {
				t.Fatalf("%q: err=%v stats=%+v out=%s", text, err, stats, out)
			}
		}
	}
}

// Real Codex 0.164.0 environment strings (monitor captures) and the
// multi-environment / subagents renderings from codex-rs tests.
func TestRewriteCodexEnvironmentTimeRealCodexStrings(t *testing.T) {
	captured := "<environment_context>\n  <cwd>/tmp/lab/work</cwd>\n  <shell>sh</shell>\n  <current_date>2026-10-25</current_date>\n  <timezone>Asia/Shanghai</timezone>\n  <filesystem><workspace_roots><root>/tmp/lab/work</root></workspace_roots><permission_profile type=\"managed\"><file_system type=\"restricted\"><entry access=\"read\"><special>:root</special></entry></file_system></permission_profile></filesystem>\n</environment_context>"
	multi := "<environment_context>\n  <environments>\n    <environment id=\"local\" primary=\"true\">\n      <cwd>/repo/local</cwd>\n      <shell>bash</shell>\n    </environment>\n    <environment id=\"remote\" primary=\"false\">\n      <cwd>/repo/remote</cwd>\n      <shell>bash</shell>\n    </environment>\n  </environments>\n  <current_date>2026-02-26</current_date>\n  <timezone>Asia/Shanghai</timezone>\n</environment_context>"
	subagents := "<environment_context>\n  <cwd>/repo</cwd>\n  <shell>bash</shell>\n  <current_date>2026-02-26</current_date>\n  <timezone>Asia/Shanghai</timezone>\n  <subagents>\n    - agent-1: atlas\n    - agent-2\n  </subagents>\n</environment_context>"
	noTime := "<environment_context>\n  <cwd>/tmp/lab/work</cwd>\n  <shell>sh</shell>\n</environment_context>"
	for name, text := range map[string]string{"captured": captured, "multi": multi, "subagents": subagents} {
		for _, body := range [][]byte{serdeBody(strings.ReplaceAll(text, `"`, `\"`)), responsesBody(t, text)} {
			out, stats := mustRewrite(t, body)
			if stats.Blocks != 1 || stats.Timezones != 1 || stats.Dates != 1 {
				t.Fatalf("%s: stats = %+v", name, stats)
			}
			got := decodedTexts(t, out)[0]
			want := strings.Replace(text, "Asia/Shanghai", CodexRewriteTimezone, 1)
			want = regexp.MustCompile(`<current_date>[0-9-]+</current_date>`).ReplaceAllString(want, "<current_date>"+rewriteToday+"</current_date>")
			if got != want {
				t.Fatalf("%s:\ngot  %q\nwant %q", name, got, want)
			}
		}
	}
	out, stats := mustRewrite(t, serdeBody(noTime))
	if stats.Blocks != 1 || stats.Changed() || !bytes.Equal(out, serdeBody(noTime)) {
		t.Fatalf("time-less block: stats = %+v", stats)
	}
}

func TestRewriteCodexEnvironmentTimeOversizedBlockFailsClosed(t *testing.T) {
	text := "<environment_context>" + strings.Repeat("x", codexEnvironmentBlockMaxBytes+1) + "</environment_context>"
	escaped := "<environment_context>" + strings.Repeat("x", codexEnvironmentBlockMaxBytes) + "\n</environment_context>" // 64 KiB + 1, escaped path
	for _, body := range [][]byte{serdeBody(text), serdeBody(escaped)} {
		if _, _, err := RewriteCodexEnvironmentTime(body, rewriteNow); !errors.Is(err, ErrCodexEnvironmentMalformed) {
			t.Fatalf("err = %v", err)
		}
	}
}

// A block at the limit is rejected if its rewrite would exceed the limit, so
// every accepted rewrite is accepted again (idempotence).
func TestRewriteCodexEnvironmentTimeBlockLimitIsIdempotent(t *testing.T) {
	tags := "<timezone>PRC</timezone><current_date>1</current_date>"
	pad := strings.Repeat("x", codexEnvironmentBlockMaxBytes-len(tags))
	body := serdeBody("<environment_context>" + pad + tags + "</environment_context>")
	if _, _, err := RewriteCodexEnvironmentTime(body, rewriteNow); !errors.Is(err, ErrCodexEnvironmentMalformed) {
		t.Fatalf("growing past the limit: err = %v", err)
	}
	grow := len(CodexRewriteTimezone) - len("PRC") + len(rewriteToday) - len("1")
	body = serdeBody("<environment_context>" + pad[grow:] + tags + "</environment_context>")
	out, stats := mustRewrite(t, body)
	if stats.Timezones != 1 || stats.Dates != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	again, againStats := mustRewrite(t, out)
	if againStats.Changed() || !bytes.Equal(again, out) {
		t.Fatal("not idempotent at the limit")
	}
}

// Boundaries of the streaming (escaped) path: the recent-byte ring wraps,
// trailing whitespace exceeds the buffer, form feed / vertical tab count as
// whitespace, and both paths agree.
func TestRewriteCodexEnvironmentTimeBlockShapeBoundaries(t *testing.T) {
	block := `<environment_context>\n  <timezone>Asia/Shanghai</timezone>\n</environment_context>`
	rewritten := `<environment_context>\n  <timezone>America/Los_Angeles</timezone>\n</environment_context>`
	longWS := strings.Repeat(`\n`, codexEnvironmentBlockMaxBytes+100)
	cases := map[string]struct{ in, want string }{
		"ring wraps":                 {`<environment_context>` + strings.Repeat("y", 200) + `<timezone>PRC</timezone>\n</environment_context>`, `<environment_context>` + strings.Repeat("y", 200) + `<timezone>America/Los_Angeles</timezone>\n</environment_context>`},
		"long trailing whitespace":   {block + longWS, rewritten + longWS},
		"form feed and vertical tab": {`\f\u000b` + block + `\f`, `\f\u000b` + rewritten + `\f`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"input":"` + tc.in + `"}`)
			out, stats := mustRewrite(t, body)
			if string(out) != `{"input":"`+tc.want+`"}` || stats.Timezones != 1 {
				t.Fatalf("stats=%+v got %.200s", stats, out)
			}
			again, againStats := mustRewrite(t, out)
			if againStats.Changed() || !bytes.Equal(again, out) {
				t.Fatal("not idempotent")
			}
		})
	}
	// Overflowed content that does not end with the close tag is user content.
	huge := []byte(`{"input":"<environment_context>\n` + strings.Repeat("z", codexEnvironmentBlockMaxBytes+10) + `"}`)
	if out, stats, err := RewriteCodexEnvironmentTime(huge, rewriteNow); err != nil || stats.Changed() || !bytes.Equal(out, huge) {
		t.Fatalf("err=%v stats=%+v", err, stats)
	}
}
