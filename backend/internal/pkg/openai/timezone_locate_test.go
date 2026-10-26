package openai

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// locationText renders every string field of loc for "no content" checks.
func locationText(loc CodexTimezoneLocation) string {
	return fmt.Sprintf("%s|%s|%s|%s", loc.Path, loc.ItemType, loc.ItemRole, strings.Join(loc.NearbyTags, ","))
}

func TestLocateChinaTimezoneInToolOutput(t *testing.T) {
	output := "SECRET_PREFIX <environment_context>\n  <cwd>/home/SECRET_USER</cwd>\n  <timezone>Asia/Shanghai</timezone>\n</environment_context> <Foo> <a1> <" + strings.Repeat("z", 41) + ">"
	body := mustMarshal(t, map[string]any{"model": "m", "input": []any{
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
		map[string]any{"type": "function_call", "call_id": "c1", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": output},
	}})
	zone, loc, found := LocateChinaTimezone(body)
	if !found || zone != "Asia/Shanghai" {
		t.Fatalf("zone=%q found=%v", zone, found)
	}
	want := CodexTimezoneLocation{
		Path:           "input[2].output",
		ItemType:       "function_call_output",
		StringBytes:    len(output),
		TagOffset:      strings.Index(output, "<timezone>"),
		EnvOpenCount:   1,
		EnvCloseCount:  1,
		EnvOpenOffset:  strings.Index(output, "<environment_context>"),
		EnvCloseOffset: strings.Index(output, "</environment_context>"),
		NearbyTags:     []string{"environment_context", "cwd", "/cwd", "timezone", "/timezone", "/environment_context"},
	}
	if !reflect.DeepEqual(loc, want) {
		t.Fatalf("got  %+v\nwant %+v", loc, want)
	}
	if text := locationText(loc); strings.Contains(text, "SECRET") || strings.Contains(text, "Asia") {
		t.Fatalf("location leaks content: %s", text)
	}
}

func TestLocateChinaTimezonePaths(t *testing.T) {
	cases := map[string]struct {
		body string
		want CodexTimezoneLocation
	}{
		"content text": {
			`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"a"},{"type":"input_text","text":"<timezone>PRC</timezone>"}]}]}`,
			CodexTimezoneLocation{Path: "input[0].content[1].text", ItemType: "message", ItemRole: "user", StringBytes: 24, TagOffset: 0,
				EnvOpenOffset: -1, EnvCloseOffset: -1, NearbyTags: []string{"timezone", "/timezone"}},
		},
		"root string input": {
			`{"input":"x <timezone>PRC</timezone>"}`,
			CodexTimezoneLocation{Path: "input", StringBytes: 26, TagOffset: 2, EnvOpenOffset: -1, EnvCloseOffset: -1, NearbyTags: []string{"timezone", "/timezone"}},
		},
		"key with odd name": {
			`{"input":[{"role":"Some Role","type":"message","weird key!":{"<timezone>PRC</timezone>":1}}]}`,
			CodexTimezoneLocation{Path: "input[0].?", InKey: true, ItemType: "message", ItemRole: "?", StringBytes: 24, TagOffset: 0,
				EnvOpenOffset: -1, EnvCloseOffset: -1, NearbyTags: []string{"timezone", "/timezone"}},
		},
		"escaped and nested": {
			`{"instructions":"x","tools":[[1,{"description":"<timezone>Asia/Macau</timezone>"}]]}`,
			CodexTimezoneLocation{Path: "tools[0][1].description", StringBytes: 31, TagOffset: 0, EnvOpenOffset: -1, EnvCloseOffset: -1, NearbyTags: []string{"timezone", "/timezone"}},
		},
		"root string": {
			`"<timezone>PRC</timezone>"`,
			CodexTimezoneLocation{Path: "$", StringBytes: 24, TagOffset: 0, EnvOpenOffset: -1, EnvCloseOffset: -1, NearbyTags: []string{"timezone", "/timezone"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, loc, found := LocateChinaTimezone([]byte(tc.body))
			if !found || !reflect.DeepEqual(loc, tc.want) {
				t.Fatalf("found=%v\ngot  %+v\nwant %+v", found, loc, tc.want)
			}
		})
	}
	if _, _, found := LocateChinaTimezone([]byte(`{"input":"<timezone>Europe/Berlin</timezone>"}`)); found {
		t.Fatal("non-CN zone located")
	}
}

func TestLocateCodexJSONStringForMalformedBlock(t *testing.T) {
	body := serdeBody("hello", "<environment_context>\n<timezone>PRC</environment_context>")
	_, _, err := RewriteCodexEnvironmentTime(body, rewriteNow)
	offset, ok := CodexEnvironmentMalformedOffset(err)
	if !ok {
		t.Fatalf("err = %v carries no offset", err)
	}
	loc, ok := LocateCodexJSONString(body, offset)
	want := CodexTimezoneLocation{Path: "input[1].content[0].text", ItemType: "message", ItemRole: "user", StringBytes: 57, TagOffset: -1,
		StartsWithEnvOpen: true, EnvOpenCount: 1, EnvCloseCount: 1, EnvOpenOffset: 0, EnvCloseOffset: 35,
		NearbyTags: []string{"environment_context", "timezone", "/environment_context"}}
	if !ok || !reflect.DeepEqual(loc, want) {
		t.Fatalf("got  %+v\nwant %+v", loc, want)
	}
	if _, ok := LocateCodexJSONString(body, 3); ok {
		t.Fatal("offset that is not a string start must not locate")
	}
	if _, ok := CodexEnvironmentMalformedOffset(ErrCodexEnvironmentMalformed); ok {
		t.Fatal("bare sentinel has no offset")
	}
	_, _, err = RewriteCodexEnvironmentTime([]byte(`{"input":`), rewriteNow)
	if _, ok := CodexEnvironmentMalformedOffset(err); ok {
		t.Fatal("invalid JSON has no string offset")
	}
}

// Identifiers chosen by users or tools (JSON keys, enum-looking values, tag
// names) are never logged verbatim: only allowlisted structural names pass.
func TestLocateChinaTimezoneRedactsUnknownIdentifiers(t *testing.T) {
	output := "<private_customer_alpha> <cwd>/x</cwd> <timezone>Asia/Shanghai</timezone> </private_customer_alpha> <acme_secret_project/> <shell>"
	body := mustMarshal(t, map[string]any{"input": []any{
		map[string]any{"type": "private_customer_alpha", "role": "acme_secret_project", "private_customer_alpha": map[string]any{"acme_secret_project": []any{output}}},
	}})
	_, loc, found := LocateChinaTimezone(body)
	if !found {
		t.Fatal("not found")
	}
	want := CodexTimezoneLocation{
		Path: "input[0].?.?[0]", ItemType: "?", ItemRole: "?", StringBytes: len(output), TagOffset: strings.Index(output, "<timezone>"),
		EnvOpenOffset: -1, EnvCloseOffset: -1, NearbyTags: []string{"?", "cwd", "/cwd", "timezone", "/timezone", "shell"},
	}
	if !reflect.DeepEqual(loc, want) {
		t.Fatalf("got  %+v\nwant %+v", loc, want)
	}
	if text := locationText(loc); strings.Contains(text, "private") || strings.Contains(text, "acme") {
		t.Fatalf("location leaks identifiers: %s", text)
	}
}
