package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const tzEmbeddedCNBlock = "<environment_context>\n  <cwd>/Users/TEST_ONLY</cwd>\n  <shell>zsh</shell>\n  <current_date>2026-10-26</current_date>\n  <timezone>Asia/Shanghai</timezone>\n</environment_context>"

func tzSerdeJSON(t *testing.T, v any) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	require.NoError(t, enc.Encode(v))
	return strings.TrimSuffix(buf.String(), "\n")
}

// tzEmbeddedHistory renders a Codex request whose history has the session's
// own environment block plus a tool output that printed a Codex rollout line
// (the block is embedded in a larger string, one JSON level deeper) and a
// grep hit with the block inline. extra is merged into the root object.
func tzEmbeddedHistory(t *testing.T, serde bool, extra map[string]any) []byte {
	t.Helper()
	rollout := tzSerdeJSON(t, map[string]any{"type": "response_item", "payload": map[string]any{
		"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": tzEmbeddedCNBlock}},
	}})
	root := map[string]any{"model": "TEST_ONLY_MODEL", "stream": true, "input": []any{
		map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": tzEmbeddedCNBlock}}},
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "check my last session"}}},
		map[string]any{"type": "function_call", "name": "exec_command", "call_id": "c1", "arguments": `{"cmd":"tail -1 rollout.jsonl"}`},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": "Chunk ID: 1\nOutput:\n" + rollout + "\n"},
		map[string]any{"type": "function_call_output", "call_id": "c2", "output": "log.txt:12: " + strings.ReplaceAll(tzEmbeddedCNBlock, "\n", " ") + " (end)"},
	}}
	for k, v := range extra {
		root[k] = v
	}
	if serde {
		return []byte(tzSerdeJSON(t, root))
	}
	body, err := json.Marshal(root)
	require.NoError(t, err)
	return body
}

// requireTZEmbeddedRewritten checks the model-visible text of every block.
func requireTZEmbeddedRewritten(t *testing.T, sent []byte) {
	t.Helper()
	require.True(t, gjson.ValidBytes(sent))
	require.NotContains(t, gjson.GetBytes(sent, "input").Raw, "Asia")
	require.Contains(t, gjson.GetBytes(sent, "input.0.content.0.text").String(), "<timezone>America/Los_Angeles</timezone>")
	rollout := gjson.GetBytes(sent, "input.3.output").String()
	line := strings.Split(rollout, "\n")[2]
	inner := gjson.Get(line, "payload.content.0.text").String()
	require.Contains(t, inner, "<timezone>America/Los_Angeles</timezone>", "rollout line rewritten")
	require.NotContains(t, inner, "2026-10-26")
	require.Contains(t, gjson.GetBytes(sent, "input.4.output").String(), "<timezone>America/Los_Angeles</timezone> </environment_context> (end)")
	require.Equal(t, `{"cmd":"tail -1 rollout.jsonl"}`, gjson.GetBytes(sent, "input.2.arguments").String())
}

func tzObservedContext(t *testing.T, method string) (*gin.Context, *observer.ObservedLogs) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	core, logs := observer.New(zap.InfoLevel)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(method, "/v1/responses", nil)
	req.Header.Set("User-Agent", "Codex Desktop/0.164.0 (Mac OS 15.5.0; arm64) unknown")
	c.Request = req.WithContext(logger.IntoContext(req.Context(), zap.New(core)))
	return c, logs
}

// Prod 2026-10-26: rewrite-mode groups rejected sessions whose history held a
// tool output with a full CN environment block (cn_timezone_after_rewrite).
// Those requests now pass over HTTP and WS, and the bytes sent upstream carry
// Los Angeles everywhere.
func TestCodexTimezoneRewriteEmbeddedHistoryPasses(t *testing.T) {
	f := newTZRewriteFixture(t)
	for _, serde := range []bool{false, true} {
		rec := f.post("/v1/responses", string(tzEmbeddedHistory(t, serde, nil)))
		require.NotContains(t, rec.Body.String(), codexTimezoneGuardCode)
	}
	require.Equal(t, int32(2), f.slotCalls.Load(), "HTTP proceeds to scheduling")

	conn, ctx := dialTZGuardWS(t, f.router)
	frame := tzEmbeddedHistory(t, true, map[string]any{"type": "response.create"})
	require.NoError(t, conn.Write(ctx, coderws.MessageText, frame))
	_, _, err := conn.Read(ctx)
	require.Equal(t, coderws.StatusInternalError, coderws.CloseStatus(err), "WS proceeds to scheduling, no rejection frame")
	require.Equal(t, int32(3), f.slotCalls.Load())

	// Model-visible bytes.
	c, logs := tzObservedContext(t, http.MethodPost)
	h := &OpenAIGatewayHandler{}
	for _, serde := range []bool{false, true} {
		original := tzEmbeddedHistory(t, serde, nil)
		sent, ok := h.applyCodexTimezoneGuardHTTP(c, f.group, 1, original, &codexTimezoneFrozenDate{}, false)
		require.True(t, ok)
		requireTZEmbeddedRewritten(t, sent)
	}
	rewritten := logs.FilterMessage("openai.codex_timezone_rewritten").All()
	require.Len(t, rewritten, 2)
	require.EqualValues(t, 2, rewritten[0].ContextMap()["embedded_blocks"])
	require.EqualValues(t, 3, rewritten[0].ContextMap()["timezones_rewritten"])

	wsc, _ := tzObservedContext(t, http.MethodGet)
	out, err := rewriteCodexTimezoneWS(context.Background(), wsc, nil, f.group, 1, 4, frame, &codexTimezoneTurnDates{})
	require.NoError(t, err)
	requireTZEmbeddedRewritten(t, out)
}

// A bare CN tag outside any block is still rejected; the rejection log says
// where it is (path, item type/role, offsets, nearby tag names) and carries
// no content.
func TestCodexTimezoneRewriteBareTagRejectedWithLocation(t *testing.T) {
	bare := "SECRET_TEXT run date: <timezone>Asia/Shanghai</timezone> <cwd>"
	body := tzEmbeddedHistory(t, false, nil)
	var root map[string]any
	require.NoError(t, json.Unmarshal(body, &root))
	input, ok := root["input"].([]any)
	require.True(t, ok)
	root["input"] = append(input, map[string]any{"type": "function_call_output", "call_id": "c3", "output": bare})
	body, err := json.Marshal(root)
	require.NoError(t, err)

	requireLocated := func(t *testing.T, entry observer.LoggedEntry, reason, transport string) {
		t.Helper()
		fields := entry.ContextMap()
		require.Equal(t, reason, fields["reason"])
		require.Equal(t, transport, fields["transport"])
		require.Equal(t, "Asia/Shanghai", fields["zone"])
		require.Equal(t, "input[5].output", fields["loc_path"])
		require.Equal(t, "function_call_output", fields["loc_item_type"])
		require.Equal(t, "", fields["loc_item_role"])
		require.EqualValues(t, len(bare), fields["loc_string_bytes"])
		require.EqualValues(t, strings.Index(bare, "<timezone>"), fields["loc_tag_offset"])
		require.Equal(t, false, fields["loc_starts_with_env_open"])
		require.EqualValues(t, 0, fields["loc_env_open_count"])
		require.EqualValues(t, -1, fields["loc_env_open_offset"])
		require.EqualValues(t, -1, fields["loc_env_close_offset"])
		require.Equal(t, []any{"timezone", "/timezone", "cwd"}, fields["loc_nearby_tags"])
		for _, value := range fields {
			if s, ok := value.(string); ok {
				require.NotContains(t, s, "SECRET")
			}
		}
	}

	rewrite := &service.Group{ID: 48, CodexCNTimezoneGuard: true, CodexTimezoneAction: service.CodexTimezoneActionRewrite}
	c, logs := tzObservedContext(t, http.MethodPost)
	h := &OpenAIGatewayHandler{}
	sent, ok := h.applyCodexTimezoneGuardHTTP(c, rewrite, 1, body, &codexTimezoneFrozenDate{}, false)
	require.False(t, ok)
	require.Nil(t, sent)
	entries := logs.FilterMessage("openai.codex_timezone_guard_rejected").All()
	require.Len(t, entries, 1)
	requireLocated(t, entries[0], codexTimezoneRejectAfterRewrite, "http")

	wsc, wsLogs := tzObservedContext(t, http.MethodGet)
	out, err := rewriteCodexTimezoneWS(context.Background(), wsc, nil, rewrite, 1, 2, body, &codexTimezoneTurnDates{})
	require.Nil(t, out)
	require.Error(t, err)
	entries = wsLogs.FilterMessage("openai.codex_timezone_guard_rejected").All()
	require.Len(t, entries, 1)
	requireLocated(t, entries[0], codexTimezoneRejectAfterRewrite, "ws")

	// Reject mode locates its first match the same way: here the session's
	// own block at input[0].
	reject := &service.Group{ID: 2, CodexCNTimezoneGuard: true}
	c, logs = tzObservedContext(t, http.MethodPost)
	_, ok = h.applyCodexTimezoneGuardHTTP(c, reject, 1, body, &codexTimezoneFrozenDate{}, false)
	require.False(t, ok)
	entries = logs.FilterMessage("openai.codex_timezone_guard_rejected").All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	require.Equal(t, codexTimezoneRejectZone, fields["reason"])
	require.Equal(t, "input[0].content[0].text", fields["loc_path"])
	require.Equal(t, "message", fields["loc_item_type"])
	require.Equal(t, "developer", fields["loc_item_role"])
	require.Equal(t, true, fields["loc_starts_with_env_open"])
	require.EqualValues(t, 0, fields["loc_env_open_offset"])

	// A malformed whole-string block is located too.
	malformed := []byte(`{"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"<environment_context><timezone>PRC</environment_context>"}]}]}`)
	c, logs = tzObservedContext(t, http.MethodPost)
	_, ok = h.applyCodexTimezoneGuardHTTP(c, rewrite, 1, malformed, &codexTimezoneFrozenDate{}, false)
	require.False(t, ok)
	entries = logs.FilterMessage("openai.codex_timezone_guard_rejected").All()
	require.Len(t, entries, 1)
	fields = entries[0].ContextMap()
	require.Equal(t, codexTimezoneRejectMalformed, fields["reason"])
	require.Equal(t, "input[0].content[0].text", fields["loc_path"])
	require.EqualValues(t, -1, fields["loc_tag_offset"])
	require.Equal(t, true, fields["loc_starts_with_env_open"])
}
