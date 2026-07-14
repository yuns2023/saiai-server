package middleware

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httptrace"
	"github.com/gin-gonic/gin"
)

// HTTPTrace 在 SAIAI_HTTP_TRACE=1 时把每个 inbound request + response 全量落盘到
// http-trace.jsonl；未启用时返回一个零开销 pass-through handler。
//
// 顺序要求：必须在 RequestLogger 之后 —— 依赖 ctx 里的 ClientRequestID 作 trace_id。
func HTTPTrace() gin.HandlerFunc {
	tracer := httptrace.Default()
	if !tracer.Enabled() {
		return func(c *gin.Context) { c.Next() }
	}

	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}

		traceID := httptrace.TraceIDFromRequest(c.Request)
		start := time.Now()

		var body []byte
		if c.Request.Body != nil {
			body, _ = io.ReadAll(c.Request.Body)
			_ = c.Request.Body.Close()
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
		}

		tracer.WritePhase(traceID, "inbound_request", map[string]any{
			"method":   c.Request.Method,
			"url":      c.Request.URL.RequestURI(),
			"headers":  httptrace.CloneHeader(c.Request.Header),
			"body_b64": base64.StdEncoding.EncodeToString(body),
		})

		tw := &traceResponseWriter{
			ResponseWriter: c.Writer,
			tracer:         tracer,
			traceID:        traceID,
			start:          start,
		}
		c.Writer = tw
		c.Next()
		tw.finish()
	}
}

type traceResponseWriter struct {
	gin.ResponseWriter
	tracer       *httptrace.Writer
	traceID      string
	start        time.Time
	headerLogged bool
	finished     bool
	totalBytes   int64
	chunks       int
}

func (w *traceResponseWriter) WriteHeader(code int) {
	w.logHeaders(code)
	w.ResponseWriter.WriteHeader(code)
}

func (w *traceResponseWriter) WriteHeaderNow() {
	status := w.Status()
	if status == 0 {
		status = http.StatusOK
	}
	w.logHeaders(status)
	w.ResponseWriter.WriteHeaderNow()
}

func (w *traceResponseWriter) Write(data []byte) (int, error) {
	if !w.headerLogged {
		status := w.Status()
		if status == 0 {
			status = http.StatusOK
		}
		w.logHeaders(status)
	}
	n, err := w.ResponseWriter.Write(data)
	if n > 0 {
		index := w.chunks
		w.chunks++
		w.totalBytes += int64(n)
		w.tracer.WritePhase(w.traceID, "inbound_response_chunk", map[string]any{
			"index":    index,
			"body_b64": base64.StdEncoding.EncodeToString(data[:n]),
		})
	}
	return n, err
}

func (w *traceResponseWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *traceResponseWriter) logHeaders(status int) {
	if w.headerLogged {
		return
	}
	w.headerLogged = true
	w.tracer.WritePhase(w.traceID, "inbound_response_headers", map[string]any{
		"status":  status,
		"headers": httptrace.CloneHeader(w.Header()),
	})
}

func (w *traceResponseWriter) finish() {
	if w.finished {
		return
	}
	w.finished = true

	status := w.Status()
	if status == 0 {
		status = http.StatusOK
	}
	w.logHeaders(status)
	w.tracer.WritePhase(w.traceID, "inbound_response_end", map[string]any{
		"total_bytes": w.totalBytes,
		"chunks":      w.chunks,
		"elapsed_ms":  time.Since(w.start).Milliseconds(),
	})
}
