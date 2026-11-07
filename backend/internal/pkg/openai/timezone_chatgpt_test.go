package openai

import (
	"errors"
	"testing"
	"time"
)

func TestRewriteChatGPTTimezone(t *testing.T) {
	winter := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	summer := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		body    string
		now     time.Time
		want    string
		changed bool
	}{
		{"init zone and offset, winter",
			`{"conversation_id":"c1","timezone":"Asia/Shanghai","timezone_offset_min":-480,"system_hints":[]}`, winter,
			`{"conversation_id":"c1","timezone":"America/Los_Angeles","timezone_offset_min":480,"system_hints":[]}`, true},
		{"summer offset", `{"timezone":"Asia/Hong_Kong","timezone_offset_min":-480}`, summer,
			`{"timezone":"America/Los_Angeles","timezone_offset_min":420}`, true},
		{"zone without offset", `{"timezone":"PRC"}`, winter, `{"timezone":"America/Los_Angeles"}`, true},
		{"upload offset only", `{"file_name":"a.png","timezone_offset_min":-480}`, winter,
			`{"file_name":"a.png","timezone_offset_min":480}`, true},
		{"other zone keeps UTC+8 offset", `{"timezone":"Asia/Singapore","timezone_offset_min":-480}`, winter,
			`{"timezone":"Asia/Singapore","timezone_offset_min":-480}`, false},
		{"other offset only", `{"timezone_offset_min":-540}`, winter, `{"timezone_offset_min":-540}`, false},
		{"nested fields untouched", `{"messages":[{"timezone":"Asia/Shanghai","timezone_offset_min":-480}]}`, winter,
			`{"messages":[{"timezone":"Asia/Shanghai","timezone_offset_min":-480}]}`, false},
		{"non-string zone", `{"timezone":8,"timezone_offset_min":-480}`, winter, `{"timezone":8,"timezone_offset_min":-480}`, false},
		{"not an object", `["Asia/Shanghai"]`, winter, `["Asia/Shanghai"]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, changed, err := RewriteChatGPTTimezone([]byte(tc.body), tc.now)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if string(out) != tc.want || changed != tc.changed {
				t.Fatalf("got %s changed=%v, want %s changed=%v", out, changed, tc.want, tc.changed)
			}
		})
	}
}

func TestRewriteChatGPTTimezoneRejectsRepeatedFields(t *testing.T) {
	for _, body := range []string{
		`{"timezone":"America/Los_Angeles","timezone":"Asia/Shanghai"}`,
		`{"timezone_offset_min":-480,"timezone_offset_min":-480}`,
	} {
		out, changed, err := RewriteChatGPTTimezone([]byte(body), time.Now())
		if !errors.Is(err, ErrChatGPTTimezoneAmbiguous) || changed || string(out) != body {
			t.Fatalf("%s: got %s changed=%v err=%v", body, out, changed, err)
		}
	}
}
