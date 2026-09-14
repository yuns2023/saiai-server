package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
)

// OpenAI WS tracing is deliberately opt-in. The output contains request
// payloads and must be treated like credentials while enabled.
type openAIWSTrace struct {
	mu   sync.Mutex
	file *os.File
}

func newOpenAIWSTraceFromEnv() *openAIWSTrace {
	if strings.TrimSpace(os.Getenv("SAIAI_OPENAI_WS_TRACE")) != "1" {
		return nil
	}
	path := strings.TrimSpace(os.Getenv("SAIAI_OPENAI_WS_TRACE_PATH"))
	if path == "" {
		path = "/tmp/saiai-openai-ws-trace.jsonl"
	}
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	_ = file.Chmod(0o600)
	return &openAIWSTrace{file: file}
}

func (t *openAIWSTrace) write(record map[string]any) {
	if t == nil || t.file == nil {
		return
	}
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	line = append(line, '\n')
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.file.Write(line)
}

func traceHeaders(headers http.Header) map[string][]string {
	out := make(map[string][]string)
	for name, values := range headers {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "proxy-authorization" || lower == "cookie" {
			out[lower] = []string{"<redacted>"}
			continue
		}
		out[lower] = append([]string(nil), values...)
	}
	return out
}

type openAIWSTracedConn struct {
	inner openAIWSClientConn
	trace *openAIWSTrace
}

func (c *openAIWSTracedConn) WriteJSON(ctx context.Context, value any) error {
	payload, _ := json.Marshal(value)
	c.trace.write(map[string]any{
		"event":     "frame",
		"direction": "to_openai",
		"kind":      "text",
		"body_b64":  base64.StdEncoding.EncodeToString(payload),
	})
	return c.inner.WriteJSON(ctx, value)
}

func (c *openAIWSTracedConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	c.trace.write(map[string]any{
		"event":     "frame",
		"direction": "to_openai",
		"kind":      strings.ToLower(msgType.String()),
		"body_b64":  base64.StdEncoding.EncodeToString(payload),
	})
	if writer, ok := c.inner.(interface {
		WriteFrame(context.Context, coderws.MessageType, []byte) error
	}); ok {
		return writer.WriteFrame(ctx, msgType, payload)
	}
	return c.inner.WriteJSON(ctx, json.RawMessage(payload))
}

func (c *openAIWSTracedConn) ReadMessage(ctx context.Context) ([]byte, error) {
	payload, err := c.inner.ReadMessage(ctx)
	if len(payload) > 0 {
		c.trace.write(map[string]any{
			"event":     "frame",
			"direction": "from_openai",
			"kind":      "text",
			"body_b64":  base64.StdEncoding.EncodeToString(payload),
		})
	}
	return payload, err
}

func (c *openAIWSTracedConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if reader, ok := c.inner.(openaiwsv2.FrameConn); ok {
		msgType, payload, err := reader.ReadFrame(ctx)
		if len(payload) > 0 {
			c.trace.write(map[string]any{
				"event":     "frame",
				"direction": "from_openai",
				"kind":      strings.ToLower(msgType.String()),
				"body_b64":  base64.StdEncoding.EncodeToString(payload),
			})
		}
		return msgType, payload, err
	}
	payload, err := c.ReadMessage(ctx)
	return coderws.MessageText, payload, err
}

func (c *openAIWSTracedConn) Ping(ctx context.Context) error {
	c.trace.write(map[string]any{"event": "ping", "direction": "to_openai"})
	return c.inner.Ping(ctx)
}

func (c *openAIWSTracedConn) Close() error {
	c.trace.write(map[string]any{"event": "close", "direction": "to_openai"})
	return c.inner.Close()
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

var _ openAIWSClientConn = (*openAIWSTracedConn)(nil)
var _ openaiwsv2.FrameConn = (*openAIWSTracedConn)(nil)
