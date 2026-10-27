package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// tzAeonCollaborationMode is the developer message app-server sends in every
// request of a Codex Desktop aeon thread.
func tzAeonCollaborationMode(zone string) string {
	return "<collaboration_mode>You are working on a long task.\n\nThe user's timezone is " + zone +
		"; use it for dates, times, and recurring schedules unless specified otherwise.\n\n" +
		"The task deadline is 2027-10-27 02:00:00 UTC. Do not infer early expiration.</collaboration_mode>"
}

func tzAeonRequest(t *testing.T, zone string, extra map[string]any) []byte {
	t.Helper()
	root := map[string]any{"model": "TEST_ONLY_MODEL", "stream": true, "input": []any{
		map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": tzAeonCollaborationMode(zone)}}},
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
	}}
	for k, v := range extra {
		root[k] = v
	}
	return []byte(tzSerdeJSON(t, root))
}

// Codex Desktop aeon threads carry the OS zone in a sentence, not a tag:
// rewrite mode replaces only the zone (HTTP and WS), reject mode rejects it.
func TestCodexTimezoneGuardDesktopTimezoneSentence(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	rewrite := &service.Group{ID: 48, CodexCNTimezoneGuard: true, CodexTimezoneAction: service.CodexTimezoneActionRewrite}
	body := tzAeonRequest(t, "Asia/Shanghai", nil)
	want := tzAeonRequest(t, "America/Los_Angeles", nil)

	c, logs := tzObservedContext(t, http.MethodPost)
	sent, ok := h.applyCodexTimezoneGuardHTTP(c, rewrite, 1, body, &codexTimezoneFrozenDate{}, false)
	require.True(t, ok)
	require.Equal(t, string(want), string(sent))
	entries := logs.FilterMessage("openai.codex_timezone_rewritten").All()
	require.Len(t, entries, 1)
	require.EqualValues(t, 1, entries[0].ContextMap()["sentence_timezones"])
	require.EqualValues(t, 0, entries[0].ContextMap()["dates_rewritten"])

	frame := tzAeonRequest(t, "Asia/Shanghai", map[string]any{"type": "response.create"})
	wsc, _ := tzObservedContext(t, http.MethodGet)
	out, err := rewriteCodexTimezoneWS(context.Background(), wsc, nil, rewrite, 1, 1, frame, &codexTimezoneTurnDates{})
	require.NoError(t, err)
	require.Equal(t, tzAeonCollaborationMode("America/Los_Angeles"), gjson.GetBytes(out, "input.0.content.0.text").String())
	require.Equal(t, "response.create", gjson.GetBytes(out, "type").String())

	// Other zones pass both modes untouched.
	reject := &service.Group{ID: 2, CodexCNTimezoneGuard: true}
	tokyo := tzAeonRequest(t, "Asia/Tokyo", nil)
	for _, group := range []*service.Group{rewrite, reject} {
		c, _ = tzObservedContext(t, http.MethodPost)
		sent, ok = h.applyCodexTimezoneGuardHTTP(c, group, 1, tokyo, &codexTimezoneFrozenDate{}, false)
		require.True(t, ok)
		require.Equal(t, string(tokyo), string(sent))
	}

	c, logs = tzObservedContext(t, http.MethodPost)
	_, ok = h.applyCodexTimezoneGuardHTTP(c, reject, 1, body, &codexTimezoneFrozenDate{}, false)
	require.False(t, ok)
	entries = logs.FilterMessage("openai.codex_timezone_guard_rejected").All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	require.Equal(t, codexTimezoneRejectZone, fields["reason"])
	require.Equal(t, "Asia/Shanghai", fields["zone"])
	require.Equal(t, "input[0].content[0].text", fields["loc_path"])
	require.Equal(t, "developer", fields["loc_item_role"])
	require.EqualValues(t, strings.Index(tzAeonCollaborationMode("Asia/Shanghai"), "Asia/Shanghai"), fields["loc_tag_offset"])
	require.Equal(t, []any{"collaboration_mode", "/collaboration_mode"}, fields["loc_nearby_tags"])
	require.Equal(t, http.StatusBadRequest, c.Writer.Status())
}
