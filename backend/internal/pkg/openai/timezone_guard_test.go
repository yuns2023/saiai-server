package openai

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func codexEnvironmentContext(zone string) string {
	return "<environment_context>\n  <cwd>/work</cwd>\n  <shell>zsh</shell>\n  <current_date>2026-10-25</current_date>\n  <timezone>" + zone + "</timezone>\n</environment_context>"
}

func responsesBody(t *testing.T, texts ...string) []byte {
	t.Helper()
	input := make([]map[string]any, 0, len(texts))
	for _, text := range texts {
		input = append(input, map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": text}},
		})
	}
	body, err := json.Marshal(map[string]any{"model": "gpt-5.1", "input": input})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestFindChinaTimezoneZoneSet(t *testing.T) {
	for _, zone := range []string{
		"Asia/Shanghai", "Asia/Urumqi", "Asia/Chongqing", "Asia/Chungking", "Asia/Harbin",
		"Asia/Kashgar", "PRC", "Asia/Hong_Kong", "Hongkong", "Asia/Macau", "Asia/Macao",
	} {
		got, found := FindChinaTimezone(responsesBody(t, codexEnvironmentContext(zone)))
		if !found || got != zone {
			t.Fatalf("zone %q: got %q found=%v", zone, got, found)
		}
	}
	for _, zone := range []string{
		"Asia/Taipei", "Asia/Tokyo", "America/Los_Angeles", "UTC", "Etc/UTC", "asia/shanghai",
		"ASIA/SHANGHAI", "Asia/Shanghai2", "Asia/ShanghaiX", "Asia", "", "Asia/Singapore",
	} {
		if got, found := FindChinaTimezone(responsesBody(t, codexEnvironmentContext(zone))); found {
			t.Fatalf("zone %q must not match, got %q", zone, got)
		}
	}
}

func TestFindChinaTimezoneTrimsValue(t *testing.T) {
	got, found := FindChinaTimezone(responsesBody(t, "<timezone>\n   Asia/Shanghai \t</timezone>"))
	if !found || got != "Asia/Shanghai" {
		t.Fatalf("got %q found=%v", got, found)
	}
}

func TestFindChinaTimezoneEscapedForms(t *testing.T) {
	cases := map[string]string{
		"unicode lt/gt":     `{"input":"\u003ctimezone\u003eAsia/Shanghai\u003c/timezone\u003e"}`,
		"escaped slash":     `{"input":"<timezone>Asia\/Hong_Kong<\/timezone>"}`,
		"escaped letters":   `{"input":"<\u0074imezone>Asia/\u004dacau</timezone>"}`,
		"uppercase hex":     `{"input":"\u003Ctimezone\u003EPRC\u003C/timezone\u003E"}`,
		"newline escapes":   `{"input":"<timezone>\n  Asia/Urumqi\n</timezone>"}`,
		"with other escape": `{"input":"say \"hi\"\t<timezone>Asia/Harbin</timezone>"}`,
	}
	for name, body := range cases {
		if !json.Valid([]byte(body)) {
			t.Fatalf("%s: invalid fixture", name)
		}
		if _, found := FindChinaTimezone([]byte(body)); !found {
			t.Fatalf("%s: expected match", name)
		}
	}
	// A backslash before the quote that is itself escaped must not hide the
	// end of the string, nor the tag in the next string.
	body := `{"a":"x\\","b":"<timezone>Asia/Shanghai</timezone>"}`
	if _, found := FindChinaTimezone([]byte(body)); !found {
		t.Fatal("expected match after escaped backslash")
	}
	// The tag must be within one JSON string: splitting it across strings or
	// placing it in raw structure does not match.
	split := `{"a":"<timezone>Asia/Shanghai","b":"</timezone>"}`
	if _, found := FindChinaTimezone([]byte(split)); found {
		t.Fatal("tag split across strings must not match")
	}
}

func TestFindChinaTimezoneMultipleAndBrokenTags(t *testing.T) {
	cases := map[string]string{
		"second tag matches":     "<timezone>America/New_York</timezone> later <timezone>Asia/Shanghai</timezone>",
		"overlong first tag":     "<timezone>" + strings.Repeat("x", 300) + "</timezone><timezone>Asia/Shanghai</timezone>",
		"unterminated first tag": "<timezone>Asia/Shanghai and more text <timezone>Asia/Hong_Kong</timezone>",
		"nested open tag":        "<timezone><timezone>Asia/Macao</timezone>",
	}
	for name, text := range cases {
		if _, found := FindChinaTimezone(responsesBody(t, text)); !found {
			t.Fatalf("%s: expected match", name)
		}
	}
	history := responsesBody(t, codexEnvironmentContext("Asia/Shanghai"), "next turn without context")
	if _, found := FindChinaTimezone(history); !found {
		t.Fatal("history item must be scanned")
	}
	for name, text := range map[string]string{
		"incomplete only":  "<timezone>Asia/Shanghai",
		"close only":       "Asia/Shanghai</timezone>",
		"overlong value":   "<timezone>" + strings.Repeat(" ", 100) + "Asia/Shanghai" + strings.Repeat("y", 70) + "</timezone>",
		"other tag":        "<tz>Asia/Shanghai</tz>",
		"mention in prose": "my timezone is Asia/Shanghai",
		"bare zone":        "Asia/Shanghai",
	} {
		if got, found := FindChinaTimezone(responsesBody(t, text)); found {
			t.Fatalf("%s: must not match, got %q", name, got)
		}
	}
}

func TestFindChinaTimezoneScansKeysAndNestedValues(t *testing.T) {
	body := `{"model":"m","tools":[{"x":{"y":["a",1,true,null,{"z":"<timezone>Asia/Kashgar</timezone>"}]}}]}`
	if got, found := FindChinaTimezone([]byte(body)); !found || got != "Asia/Kashgar" {
		t.Fatalf("got %q found=%v", got, found)
	}
	compact := `{"model":"m","input":[{"type":"compaction","encrypted_content":"opaque"},{"type":"message","role":"user","content":"<timezone>PRC</timezone>"}]}`
	if _, found := FindChinaTimezone([]byte(compact)); !found {
		t.Fatal("compact-shaped body must be scanned")
	}
}

func TestFindChinaTimezoneFastPathAndLargeBody(t *testing.T) {
	// No "timezone" and no \u: rejected by the fast path without walking.
	plain := []byte(`{"model":"m","input":"` + strings.Repeat("a", 1<<20) + `"}`)
	if _, found := FindChinaTimezone(plain); found {
		t.Fatal("unexpected match")
	}
	if allocs := testing.AllocsPerRun(5, func() { FindChinaTimezone(plain) }); allocs != 0 {
		t.Fatalf("fast path allocated %v times", allocs)
	}

	// A 50 MiB body with the tag at the very end, many strings and no escapes.
	var b bytes.Buffer
	_, _ = b.WriteString(`{"model":"m","input":[`)
	chunk := `{"type":"message","role":"user","content":"` + strings.Repeat("x", 1000) + ` timezone talk"},`
	for b.Len() < 50<<20 {
		_, _ = b.WriteString(chunk)
	}
	_, _ = b.WriteString(`{"type":"message","role":"user","content":"<timezone>Asia/Shanghai</timezone>"}]}`)
	large := b.Bytes()
	if !json.Valid(large) {
		t.Fatal("invalid large fixture")
	}
	before := append([]byte(nil), large[len(large)-200:]...)
	if allocs := testing.AllocsPerRun(1, func() {
		if _, found := FindChinaTimezone(large); !found {
			t.Fatal("expected match at end of large body")
		}
	}); allocs > 2 {
		t.Fatalf("large unescaped body allocated %v times", allocs)
	}
	if !bytes.Equal(before, large[len(large)-200:]) {
		t.Fatal("scanner modified the body")
	}
}

func TestFindChinaTimezoneDoesNotModifyEscapedBody(t *testing.T) {
	body := []byte(`{"input":"\u003ctimezone>Asia/Shanghai</timezone>","b":"\ud83d\ude00 \ud800 x"}`)
	original := append([]byte(nil), body...)
	if _, found := FindChinaTimezone(body); !found {
		t.Fatal("expected match")
	}
	if !bytes.Equal(body, original) {
		t.Fatal("scanner modified the body")
	}
}

func TestAppendUnescapedJSONStringMatchesEncodingJSON(t *testing.T) {
	for _, raw := range []string{
		`plain`, `a\"b\\c\/d\b\f\n\r\t`, `\u003c\u00e9\u4e2d`, `\ud83d\ude00`, `\ud800x`, `\udc00`, `\ud83dA`, `x\u0000y`,
	} {
		var want string
		if err := json.Unmarshal([]byte(`"`+raw+`"`), &want); err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if got := string(appendUnescapedJSONString(nil, []byte(raw))); got != want {
			t.Fatalf("%q: got %q want %q", raw, got, want)
		}
	}
}

func BenchmarkFindChinaTimezoneLargeBody(b *testing.B) {
	var buf bytes.Buffer
	_, _ = buf.WriteString(`{"input":[`)
	for buf.Len() < 8<<20 {
		_, _ = buf.WriteString(`{"content":"` + strings.Repeat("y", 4000) + ` timezone"},`)
	}
	_, _ = buf.WriteString(`{"content":"end"}]}`)
	body := buf.Bytes()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		FindChinaTimezone(body)
	}
}
