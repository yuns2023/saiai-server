package middleware

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httptrace"
	"github.com/gin-gonic/gin"
)

// HTTPTrace 在 SAIAI_HTTP_TRACE=1 时把每个 inbound request + response 全量落盘到
// http-trace.jsonl；未启用时返回一个零开销 pass-through handler。
//
// 顺序要求：必须在 RequestLogger 之后 —— 依赖 ctx 里的 ClientRequestID 作 trace_id。
func HTTPTrace() gin.HandlerFunc {
	return httpTrace(httptrace.Default())
}

func httpTrace(tracer *httptrace.Writer) gin.HandlerFunc {
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
		path := c.Request.URL.Path
		installCode := traceIsAITPInstallCodePath(path)

		fields := map[string]any{
			"method":  c.Request.Method,
			"url":     c.Request.URL.RequestURI(),
			"headers": httptrace.CloneHeader(c.Request.Header),
		}
		if installCode {
			// The code is in the URL; the bodies carry the code or the plaintext key.
			fields["url"] = RedactedLogPath(path)
			fields["body_omitted"] = true
		} else {
			var body []byte
			if c.Request.Body != nil {
				body, _ = io.ReadAll(c.Request.Body)
				_ = c.Request.Body.Close()
				c.Request.Body = io.NopCloser(bytes.NewReader(body))
			}
			fields["body_b64"] = base64.StdEncoding.EncodeToString(body)
		}
		tracer.WritePhase(traceID, "inbound_request", fields)

		tw := &traceResponseWriter{
			ResponseWriter: c.Writer,
			tracer:         tracer,
			traceID:        traceID,
			start:          start,
			omitBody:       installCode || traceOmitsResponseBody(path),
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
	omitBody     bool
}

// Decrypted moderation evidence must stay out of trace files, which are not
// covered by evidence retention, holds or user deletion.
func traceOmitsResponseBody(path string) bool {
	return strings.Contains(path, "/input-moderation-events/")
}

// traceIsAITPInstallCodePath: one-time install codes. /i/<code> answers with the plaintext
// API key, the create endpoint with a fresh code; neither body nor the code may be traced.
func traceIsAITPInstallCodePath(path string) bool {
	return isAITPInstallCodeRedeemPath(path) || isAITPInstallCodeCreatePath(path)
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
		fields := map[string]any{"index": index, "body_b64": base64.StdEncoding.EncodeToString(data[:n])}
		if w.omitBody {
			fields = map[string]any{"index": index, "body_omitted": n}
		}
		w.tracer.WritePhase(w.traceID, "inbound_response_chunk", fields)
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
