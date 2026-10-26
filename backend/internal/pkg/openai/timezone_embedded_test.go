package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

// compactBlock has no newline, so serdeBody keeps its string unescaped.
func compactBlock(date, zone string) string {
	return "<environment_context><cwd>/w</cwd><current_date>" + date + "</current_date><timezone>" + zone + "</timezone></environment_context>"
}

// Blocks embedded in a larger string (tool output, pasted text) are
// rewritten like whole-string blocks; mentions around them are untouched.
// Each case runs on the unescaped path (serdeBody of compact text), the
// serde-escaped path (envBlock has newlines) and the HTML-escaped path
// (encoding/json), and the output must equal the same rendering of the
// expected text byte for byte (all bytes outside the value spans preserved).
func TestRewriteCodexEnvironmentTimeEmbeddedBlocks(t *testing.T) {
	type tc struct {
		in, want string
		embedded int
		skipped  int
	}
	cases := map[string]func(block func(date, zone string) string) tc{
		"prefix text": func(b func(string, string) string) tc {
			return tc{"note: " + b("2026-10-25", "Asia/Shanghai"), "note: " + b(rewriteToday, CodexRewriteTimezone), 1, 0}
		},
		"suffix text": func(b func(string, string) string) tc {
			return tc{b("2026-10-25", "Asia/Shanghai") + " was the context", b(rewriteToday, CodexRewriteTimezone) + " was the context", 1, 0}
		},
		"multiple blocks": func(b func(string, string) string) tc {
			return tc{"a " + b("2026-10-20", "Asia/Shanghai") + " b " + b("2026-10-21", "Asia/Macau") + " c",
				"a " + b(rewriteToday, CodexRewriteTimezone) + " b " + b(rewriteToday, CodexRewriteTimezone) + " c", 2, 0}
		},
		"mention before block": func(b func(string, string) string) tc {
			return tc{"what is <environment_context>? " + b("2026-10-25", "PRC"), "what is <environment_context>? " + b(rewriteToday, CodexRewriteTimezone), 1, 0}
		},
		// A close tag with no dropped open before it is a plain mention.
		"stray close after block": func(b func(string, string) string) tc {
			return tc{"x " + b("2026-10-25", "PRC") + " </environment_context>", "x " + b(rewriteToday, CodexRewriteTimezone) + " </environment_context>", 1, 0}
		},
		"leading close mention": func(b func(string, string) string) tc {
			return tc{"grep: </environment_context> hit\n" + b("2026-10-25", "PRC"), "grep: </environment_context> hit\n" + b(rewriteToday, CodexRewriteTimezone), 1, 0}
		},
		// A nested pair is ambiguous: the inner block stays untouched.
		"nested pair": func(b func(string, string) string) tc {
			in := "x <environment_context>" + b("2026-10-25", "PRC") + "</environment_context>"
			return tc{in, in, 0, 1}
		},
		"nested pair with text": func(b func(string, string) string) tc {
			in := "x <environment_context> a " + b("2026-10-25", "PRC") + " b </environment_context> c"
			return tc{in, in, 0, 1}
		},
		"deeply nested": func(b func(string, string) string) tc {
			in := "x <environment_context><environment_context>" + b("2026-10-25", "PRC") + "</environment_context></environment_context>"
			return tc{in, in, 0, 2}
		},
		// Only the ambiguous region is left: independent blocks before and
		// after a nested pair are still rewritten.
		"good block then nested pair": func(b func(string, string) string) tc {
			nested := " <environment_context>" + b("2026-10-25", "PRC") + "</environment_context> "
			return tc{"x " + b("2026-10-25", "Asia/Macau") + nested + b("2026-10-25", "Asia/Urumqi"),
				"x " + b(rewriteToday, CodexRewriteTimezone) + nested + b(rewriteToday, CodexRewriteTimezone), 2, 1}
		},
		"good CN block then CN-free nested segment": func(b func(string, string) string) tc {
			rest := "\ny <environment_context><environment_context></environment_context></environment_context>"
			return tc{"x " + b("2026-10-25", "PRC") + rest, "x " + b(rewriteToday, CodexRewriteTimezone) + rest, 1, 1}
		},
		"mention, block, mention": func(b func(string, string) string) tc {
			return tc{"<environment_context> a " + b("2026-10-25", "PRC") + " <environment_context> z",
				"<environment_context> a " + b(rewriteToday, CodexRewriteTimezone) + " <environment_context> z", 1, 0}
		},
		"malformed then good": func(b func(string, string) string) tc {
			bad := "<environment_context><timezone>PRC</environment_context>"
			return tc{"x " + bad + " y " + b("2026-10-25", "Asia/Urumqi"), "x " + bad + " y " + b(rewriteToday, CodexRewriteTimezone), 1, 1}
		},
		"malformed only": func(b func(string, string) string) tc {
			bad := "x <environment_context><timezone>Asia/Shanghai</timezone><current_date</environment_context>"
			return tc{bad, bad, 0, 1}
		},
		"target already": func(b func(string, string) string) tc {
			return tc{"x " + b(rewriteToday, CodexRewriteTimezone), "x " + b(rewriteToday, CodexRewriteTimezone), 1, 0}
		},
	}
	renders := map[string]struct {
		block func(date, zone string) string
		body  func(text string) []byte
	}{
		"unescaped":    {compactBlock, func(text string) []byte { return serdeBody(text) }},
		"serde":        {envBlock, func(text string) []byte { return serdeBody(text) }},
		"html escaped": {envBlock, func(text string) []byte { return responsesBody(t, text) }},
	}
	for name, mk := range cases {
		for renderName, render := range renders {
			t.Run(name+"/"+renderName, func(t *testing.T) {
				c := mk(render.block)
				body := render.body(c.in)
				out, stats := mustRewrite(t, body)
				if want := render.body(c.want); !bytes.Equal(out, want) {
					t.Fatalf("got  %s\nwant %s", out, want)
				}
				if stats.Embedded != c.embedded || stats.EmbeddedSkipped != c.skipped || stats.Blocks != c.embedded {
					t.Fatalf("stats = %+v", stats)
				}
				if c.in == c.want && &out[0] != &body[0] {
					t.Fatal("unchanged body must be returned as-is")
				}
				again, againStats := mustRewrite(t, out)
				if againStats.Changed() || !bytes.Equal(again, out) {
					t.Fatalf("not idempotent: %+v", againStats)
				}
			})
		}
	}
}

// Escapes inside an embedded block change exactly one raw span.
func TestRewriteCodexEnvironmentTimeEmbeddedEscapeForms(t *testing.T) {
	for name, raw := range map[string]string{
		"unicode tags":  `see \u003cenvironment_context\u003e\u003Ctimezone\u003EAsia/Shanghai\u003c/timezone\u003e\u003c/environment_context\u003e done`,
		"escaped slash": `see <environment_context><timezone>Asia\/Shanghai<\/timezone><\/environment_context> done`,
		"escaped value": `see <environment_context><timezone>\u0041sia/Shangha\u0069</timezone></environment_context> done`,
		"ansi around":   `\u001b[0m<environment_context><timezone>Asia/Shanghai</timezone></environment_context>\u001b[0m`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"input":[{"type":"function_call_output","output":"` + raw + `"}]}`)
			out, stats := mustRewrite(t, body)
			if stats.Timezones != 1 || stats.Embedded != 1 {
				t.Fatalf("stats = %+v", stats)
			}
			prefix := commonPrefix(body, out)
			suffix := commonSuffix(body[prefix:], out[prefix:])
			if got := string(out[prefix : len(out)-suffix]); !strings.Contains(CodexRewriteTimezone, got) {
				t.Fatalf("changed span %q is not part of the target", got)
			}
			if old := body[prefix : len(body)-suffix]; bytes.ContainsAny(old, `<>"`) || bytes.Contains(old, []byte("u003")) {
				t.Fatalf("changed span %q reaches outside the value", old)
			}
			if _, found := FindChinaTimezone(out); found {
				t.Fatal("leak net still matches")
			}
		})
	}
}

// A Codex rollout JSONL line read by a tool: the block sits in JSON text
// inside the tool output string, so after one decode it still contains
// `\n` and `\"` escapes. Tag values carry no escapes and are rewritten; the
// value-less `<current_date status=\"unavailable\" />` form is accepted.
func TestRewriteCodexEnvironmentTimeEmbeddedRolloutLine(t *testing.T) {
	serde := func(v any) string {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSuffix(buf.String(), "\n")
	}
	rollout := func(text string) string {
		return serde(map[string]any{"timestamp": "2026-10-25T01:02:03.000Z", "type": "response_item", "payload": map[string]any{
			"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": text}},
		}})
	}
	unavailable := "<environment_context>\n  <cwd>/w</cwd>\n  <current_date status=\"unavailable\" />\n  <timezone>Asia/Shanghai</timezone>\n</environment_context>"
	output := "$ head -3 ~/.codex/sessions/x.jsonl\n" + rollout(envBlock("2026-10-25", "Asia/Shanghai")) + "\n" + rollout(unavailable) + "\n"
	for name, body := range map[string][]byte{
		"serde outer": []byte(serde(map[string]any{"input": []any{map[string]any{"type": "function_call_output", "call_id": "c1", "output": output}}})),
		"json outer":  mustMarshal(t, map[string]any{"input": []any{map[string]any{"type": "function_call_output", "call_id": "c1", "output": output}}}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, found := FindChinaTimezone(body); !found {
				t.Fatal("precondition: the leak net sees the zone")
			}
			out, stats := mustRewrite(t, body)
			if stats.Embedded != 2 || stats.Timezones != 2 || stats.Dates != 1 || stats.EmbeddedSkipped != 0 {
				t.Fatalf("stats = %+v", stats)
			}
			if _, found := FindChinaTimezone(out); found {
				t.Fatal("leak net still matches after the rewrite")
			}
			var parsed struct {
				Input []struct {
					Output string `json:"output"`
				} `json:"input"`
			}
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatal(err)
			}
			want := "$ head -3 ~/.codex/sessions/x.jsonl\n" + rollout(envBlock(rewriteToday, CodexRewriteTimezone)) + "\n" +
				rollout(strings.Replace(unavailable, "Asia/Shanghai", CodexRewriteTimezone, 1)) + "\n"
			if parsed.Input[0].Output != want {
				t.Fatalf("got  %q\nwant %q", parsed.Input[0].Output, want)
			}
		})
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// A `<timezone>` tag outside any block is never edited, even in a string
// whose embedded block is rewritten; the leak net keeps seeing it.
func TestRewriteCodexEnvironmentTimeEmbeddedLeavesOutsideTags(t *testing.T) {
	text := "x " + compactBlock("2026-10-25", "Asia/Shanghai") + " then <timezone>Asia/Shanghai</timezone>"
	for _, body := range [][]byte{serdeBody(text), responsesBody(t, text)} {
		out, stats := mustRewrite(t, body)
		if stats.Embedded != 1 || stats.Timezones != 1 {
			t.Fatalf("stats = %+v", stats)
		}
		got := decodedTexts(t, out)[0]
		if got != "x "+compactBlock(rewriteToday, CodexRewriteTimezone)+" then <timezone>Asia/Shanghai</timezone>" {
			t.Fatalf("decoded = %q", got)
		}
		if zone, found := FindChinaTimezone(out); !found || zone != "Asia/Shanghai" {
			t.Fatal("the outside tag must still be caught")
		}
	}
}

// Embedded blocks take the enclosing item's create_time date like
// whole-string blocks; metadata strings are never edited.
func TestRewriteCodexEnvironmentTimeEmbeddedItemDateAndMetadata(t *testing.T) {
	created := epochSeconds(time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)) // 2026-10-20 LA
	body := []byte(`{"input":[{"type":"function_call_output","output":"log: ` + compactBlock("2026-10-01", "PRC") + `","internal_chat_message_metadata_passthrough":{"create_time":` + created + `}}]}`)
	out, _ := mustRewrite(t, body)
	if !bytes.Contains(out, []byte("log: "+compactBlock("2026-10-20", CodexRewriteTimezone))) {
		t.Fatalf("got %s", out)
	}
	meta := []byte(`{"input":[{"internal_chat_message_metadata_passthrough":{"note":"x ` + compactBlock("2026-10-01", "PRC") + `","bad":"x <environment_context><timezone>PRC</environment_context>"},"content":"hi"}]}`)
	out, stats, err := RewriteCodexEnvironmentTime(meta, rewriteNow)
	if err != nil || stats.Changed() || stats.Embedded != 0 || &out[0] != &meta[0] {
		t.Fatalf("err=%v stats=%+v", err, stats)
	}
}

// Size limits for embedded blocks: over the limit (or growing past it) the
// block is skipped, never rejected; at the limit it is rewritten and stays
// idempotent.
func TestRewriteCodexEnvironmentTimeEmbeddedSizeLimits(t *testing.T) {
	tags := "<timezone>PRC</timezone><current_date>1</current_date>"
	grow := len(CodexRewriteTimezone) - len("PRC") + len(rewriteToday) - len("1")
	over := "x <environment_context>" + strings.Repeat("y", codexEnvironmentBlockMaxBytes-len(tags)) + tags + "</environment_context>"
	fits := "x <environment_context>" + strings.Repeat("y", codexEnvironmentBlockMaxBytes-len(tags)-grow) + tags + "</environment_context>"
	huge := "x <environment_context>" + strings.Repeat("y", 3*codexEnvironmentBlockMaxBytes) + tags + "</environment_context>"
	for _, escape := range []bool{false, true} {
		render := func(text string) []byte {
			if escape {
				text = strings.ReplaceAll(text, "x ", `x\n`)
			}
			return []byte(`{"input":"` + text + `"}`)
		}
		for _, text := range []string{over, huge} {
			body := render(text)
			out, stats, err := RewriteCodexEnvironmentTime(body, rewriteNow)
			if err != nil || stats.Changed() || stats.EmbeddedSkipped != 1 || !bytes.Equal(out, body) {
				t.Fatalf("escape=%v len=%d: err=%v stats=%+v", escape, len(text), err, stats)
			}
		}
		out, stats := mustRewrite(t, render(fits))
		if stats.Embedded != 1 || stats.Timezones != 1 || stats.Dates != 1 {
			t.Fatalf("escape=%v: stats = %+v", escape, stats)
		}
		again, againStats := mustRewrite(t, out)
		if againStats.Changed() || !bytes.Equal(again, out) {
			t.Fatal("not idempotent at the limit")
		}
	}
}

// Many candidates and a long unclosed candidate in a multi-MB escaped string:
// memory stays bounded by the block limit and the scan is linear.
func TestRewriteCodexEnvironmentTimeEmbeddedHugeStringBounded(t *testing.T) {
	block := strings.ReplaceAll(envBlock("2026-10-01", "Asia/Shanghai"), "\n", `\n`)
	filler := strings.Repeat(`\u001b[0m line \"q\" <environment_context> mention\n`, 200_000) // ~10 MiB
	output := `<environment_context>` + filler + block + filler + block + ` <environment_context>`
	body := []byte(`{"input":[{"type":"function_call_output","output":"x ` + output + `"}]}`)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	out, stats, err := RewriteCodexEnvironmentTime(body, rewriteNow)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if err != nil || stats.Embedded != 2 || stats.Timezones != 2 || stats.EmbeddedSkipped != 0 {
		t.Fatalf("err=%v stats=%+v", err, stats)
	}
	if extra := int64(after.TotalAlloc-before.TotalAlloc) - int64(cap(out)); extra > 4<<20 {
		t.Fatalf("extra allocation %d bytes for a %d byte body", extra, len(body))
	}
	if elapsed > 5*time.Second {
		t.Fatalf("rewrite of %d bytes took %v", len(body), elapsed)
	}
	if bytes.Count(out, []byte(CodexRewriteTimezone)) != 2 || len(out) != len(body)+2*(len(CodexRewriteTimezone)-len("Asia/Shanghai")) {
		t.Fatal("unexpected output")
	}
}

// A whole-string block keeps failing closed when malformed; the embedded
// rules (including `\"` attribute quotes) only apply to other strings.
func TestRewriteCodexEnvironmentTimeWholeBlockStillFailsClosed(t *testing.T) {
	for _, text := range []string{
		"<environment_context><timezone>PRC</environment_context>",
		"  " + envBlock("2026-10-20", "PRC") + " text " + envBlock("2026-10-21", "PRC") + "\n",
		// Decoded text holds literal backslashes before the quotes.
		`<environment_context><current_date status=\\\"unavailable\\\" /><timezone>PRC</timezone></environment_context>`,
	} {
		body := serdeBody(text)
		if _, _, err := RewriteCodexEnvironmentTime(body, rewriteNow); !errors.Is(err, ErrCodexEnvironmentMalformed) {
			t.Fatalf("%q: err = %v", text, err)
		}
	}
	// The same `\"` quotes in an embedded block are accepted.
	embedded := serdeBody(`x <environment_context><current_date status=\\\"unavailable\\\" /><timezone>PRC</timezone></environment_context>`)
	if _, stats := mustRewrite(t, embedded); stats.Embedded != 1 || stats.Timezones != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func BenchmarkRewriteCodexEnvironmentTimeEmbedded5MB(b *testing.B) {
	block := strings.ReplaceAll(envBlock("2026-10-01", "Asia/Shanghai"), "\n", `\n`)
	var items []string
	for i := 0; i < 100; i++ {
		items = append(items, `{"type":"function_call_output","call_id":"c","output":"`+
			strings.Repeat(`{\"type\":\"exec\",\"line\":\"\u001b[32mok\u001b[0m some output text\"}\n`, 800)+block+`"}`)
	}
	body := []byte(`{"model":"m","input":[` + strings.Join(items, ",") + `]}`)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := RewriteCodexEnvironmentTime(body, rewriteNow); err != nil {
			b.Fatal(err)
		}
	}
}

// Alternating dropped opens and value-less blocks must not grow the nesting
// state with the string: runs are bounded by recorded edits.
func TestRewriteCodexEnvironmentTimeEmbeddedNestingBounded(t *testing.T) {
	const n = 60_000
	unit := "<environment_context><environment_context></environment_context>"
	tail := strings.Repeat("</environment_context>", n)
	block := compactBlock("2026-10-25", "PRC")
	for _, escape := range []bool{false, true} {
		text := "x " + strings.Repeat(unit, n) + tail + " " + block
		if escape {
			text = "x\\n" + text[2:]
		}
		body := []byte(`{"input":"` + text + `"}`)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		out, stats, err := RewriteCodexEnvironmentTime(body, rewriteNow)
		runtime.ReadMemStats(&after)
		if err != nil || stats.Timezones != 1 || stats.Dates != 1 {
			t.Fatalf("escape=%v err=%v stats=%+v", escape, err, stats)
		}
		if extra := int64(after.TotalAlloc-before.TotalAlloc) - int64(cap(out)); extra > 1<<20 {
			t.Fatalf("escape=%v: extra allocation %d bytes for a %d byte body", escape, extra, len(body))
		}
		if !bytes.HasSuffix(out, []byte(compactBlock(rewriteToday, CodexRewriteTimezone)+`"}`)) {
			t.Fatal("trailing block not rewritten")
		}
	}
}
