// Package httptrace 提供一个可按环境变量开关的 HTTP 全量流量调试记录器。
//
// 设计目标是在日常生产下零开销（SAIAI_HTTP_TRACE 未设置时所有 wrapper 走 no-op
// 快路径），而在需要调试上游通信时无需改代码、无需重新 build，仅开一个 env
// 就能把 gateway 的 inbound/outbound 全部字节落盘成 JSONL 方便离线分析。
//
// 落盘文件：SAIAI_HTTP_TRACE_PATH；未设置时为当前工作目录下的
// data/logs/http-trace.jsonl（0600 权限，等同凭证）。
// 轮转：单文件达到 SAIAI_HTTP_TRACE_MAX_FILE_MB 后重命名为 .1/.2/... 后缀，
//
//	保留最近 SAIAI_HTTP_TRACE_KEEP 个。
//
// Phase 记录以 JSONL 追加写入：
//
//	inbound_request              客户端进来的请求（method、url、headers、body）
//	outbound_request             gateway 发往上游的请求
//	outbound_response_headers    上游响应 header
//	outbound_response_chunk      上游响应 body 的一块（SSE 每 Read 一条；非 SSE 最终一条）
//	outbound_response_end        上游响应结束（total_bytes / chunks / elapsed_ms）
//	inbound_response_headers     gateway 返回给客户端的 header
//	inbound_response_chunk       gateway 返回给客户端的 body 块
//	inbound_response_end         gateway 返回结束
//
// 所有 body 内容 base64 编码（`body_b64` 字段），不截断不脱敏 —— 调试用途，
// 确保字节完整可还原；文件本身必须按凭证对待。
package httptrace

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

const (
	envEnabled   = "SAIAI_HTTP_TRACE"
	envPath      = "SAIAI_HTTP_TRACE_PATH"
	envMaxFileMB = "SAIAI_HTTP_TRACE_MAX_FILE_MB"
	envKeep      = "SAIAI_HTTP_TRACE_KEEP"

	defaultPath      = "data/logs/http-trace.jsonl"
	defaultMaxFileMB = 1024
	defaultKeep      = 10
)

var (
	defaultOnce   sync.Once
	defaultWriter *Writer
)

// Writer 是 HTTP trace 落盘器。未启用时 Enabled() 返回 false，所有写入都走 no-op。
type Writer struct {
	enabled  bool
	path     string
	maxBytes int64
	keep     int

	mu   sync.Mutex
	file *os.File
	size int64
}

// Default 返回进程单例 Writer。首次调用时读取环境变量决定是否启用。
func Default() *Writer {
	defaultOnce.Do(func() {
		defaultWriter = newFromEnv()
	})
	return defaultWriter
}

func newFromEnv() *Writer {
	if strings.TrimSpace(os.Getenv(envEnabled)) != "1" {
		return &Writer{}
	}

	maxFileMB := defaultMaxFileMB
	if v := strings.TrimSpace(os.Getenv(envMaxFileMB)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxFileMB = n
		}
	}

	keep := defaultKeep
	if v := strings.TrimSpace(os.Getenv(envKeep)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			keep = n
		}
	}

	tracePath := strings.TrimSpace(os.Getenv(envPath))
	if tracePath == "" {
		tracePath = defaultPath
	}
	tracePath = filepath.Clean(tracePath)

	if err := os.MkdirAll(filepath.Dir(tracePath), 0o700); err != nil {
		log.Printf("httptrace: mkdir failed: %v", err)
		return &Writer{}
	}

	f, err := os.OpenFile(tracePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("httptrace: open failed: %v", err)
		return &Writer{}
	}
	_ = f.Chmod(0o600)

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		log.Printf("httptrace: stat failed: %v", err)
		return &Writer{}
	}

	log.Printf("httptrace: enabled path=%s max_mb=%d keep=%d", tracePath, maxFileMB, keep)
	return &Writer{
		enabled:  true,
		path:     tracePath,
		maxBytes: int64(maxFileMB) * 1024 * 1024,
		keep:     keep,
		file:     f,
		size:     info.Size(),
	}
}

// Enabled 当且仅当环境变量 SAIAI_HTTP_TRACE=1 且文件打开成功时为 true。
func (w *Writer) Enabled() bool {
	return w != nil && w.enabled
}

// CloneHeader 返回 header 的浅复制。JSON 序列化 http.Header 时输出
// {"Header-Name":["val1","val2"]} 格式。
func CloneHeader(h http.Header) http.Header {
	if h == nil {
		return http.Header{}
	}
	return h.Clone()
}

// TraceIDFromRequest 用于在 JSONL 里串联 inbound 与 outbound。
// 查找顺序：
//  1. ctxkey.RequestID（saiai RequestLogger 中间件写入，是主 trace key）
//  2. ctxkey.ClientRequestID（上游客户端自带的 trace id，非必有）
//  3. X-Request-ID / X-Client-Request-Id 响应/请求 header
//  4. 空字符串（健康检查等不过中间件链的路径）
func TraceIDFromRequest(req *http.Request) string {
	if req == nil {
		return ""
	}
	ctx := req.Context()
	for _, key := range []ctxkey.Key{ctxkey.RequestID, ctxkey.ClientRequestID} {
		if v := ctx.Value(key); v != nil {
			if s, ok := v.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					return s
				}
			}
		}
	}
	for _, key := range []string{"X-Request-ID", "X-Request-Id", "X-Client-Request-Id"} {
		if s := strings.TrimSpace(req.Header.Get(key)); s != "" {
			return s
		}
	}
	return ""
}

// WritePhase 落一条 JSONL 记录。未启用时 no-op。
func (w *Writer) WritePhase(traceID, phase string, fields map[string]any) {
	if !w.Enabled() {
		return
	}

	record := map[string]any{
		"ts":       time.Now().UTC().Format(time.RFC3339Nano),
		"trace_id": traceID,
		"phase":    phase,
	}
	for k, v := range fields {
		record[k] = v
	}

	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	line = append(line, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.enabled || w.file == nil {
		return
	}
	if err := w.rotateLocked(int64(len(line))); err != nil {
		log.Printf("httptrace: rotate failed: %v", err)
		// rotate 失败后尝试重开原 path，避免 trace 静默停写
		if w.file == nil {
			if f, rerr := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); rerr == nil {
				_ = f.Chmod(0o600)
				w.file = f
				if info, serr := f.Stat(); serr == nil {
					w.size = info.Size()
				}
			}
		}
		if w.file == nil {
			return
		}
	}
	n, err := w.file.Write(line)
	if err != nil {
		log.Printf("httptrace: write failed: %v", err)
		return
	}
	w.size += int64(n)
}

// rotateLocked 调用前必须已持有 w.mu。
func (w *Writer) rotateLocked(incoming int64) error {
	if w.maxBytes <= 0 || w.size+incoming <= w.maxBytes {
		return nil
	}

	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}

	// 先删最老的（如果存在），再把 i 推到 i+1，最后把当前 .jsonl 推到 .1.jsonl。
	_ = os.Remove(rotatedPath(w.path, w.keep))
	for i := w.keep - 1; i >= 1; i-- {
		src := rotatedPath(w.path, i)
		dst := rotatedPath(w.path, i+1)
		if _, err := os.Stat(src); err == nil {
			if err := os.Rename(src, dst); err != nil {
				return err
			}
		}
	}
	if _, err := os.Stat(w.path); err == nil {
		if err := os.Rename(w.path, rotatedPath(w.path, 1)); err != nil {
			return err
		}
	}

	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_ = f.Chmod(0o600)
	w.file = f
	w.size = 0
	return nil
}

func rotatedPath(path string, n int) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	return base + "." + strconv.Itoa(n) + ext
}

// OutboundTrace 包装单次上游 HTTP 请求的生命周期。
type OutboundTrace struct {
	writer  *Writer
	traceID string
	start   time.Time
}

// StartOutbound 记录 outbound_request 并返回一个 handle 供 WrapResponse 调用。
// 如果 tracer 未启用直接返回 nil，调用方逻辑无需分支判断（WrapResponse 对 nil 友好）。
// 会把 req.Body 读出 → base64 → 重写回一个可重读的 NopCloser，同时补 req.GetBody
// 以便下游如果做 retry 也能重放 body。
func StartOutbound(w *Writer, req *http.Request) (*OutboundTrace, *http.Request) {
	if w == nil || !w.Enabled() || req == nil {
		return nil, req
	}

	body, req, err := cloneRequestBody(req)
	if err != nil {
		body = nil
	}

	traceID := TraceIDFromRequest(req)
	w.WritePhase(traceID, "outbound_request", map[string]any{
		"upstream_url": req.URL.String(),
		"method":       req.Method,
		"headers":      CloneHeader(req.Header),
		"body_b64":     base64.StdEncoding.EncodeToString(body),
	})

	return &OutboundTrace{writer: w, traceID: traceID, start: time.Now()}, req
}

// WrapResponse 记录 outbound_response_headers 并把 resp.Body 包装成
// 可流式追记 chunk 的 ReadCloser。对 nil resp / 未启用的 tracer 透传。
func (t *OutboundTrace) WrapResponse(resp *http.Response) *http.Response {
	if t == nil || resp == nil {
		return resp
	}
	t.writer.WritePhase(t.traceID, "outbound_response_headers", map[string]any{
		"status":  resp.StatusCode,
		"headers": CloneHeader(resp.Header),
	})

	isSSE := strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	resp.Body = &tracedResponseBody{
		rc:      resp.Body,
		writer:  t.writer,
		traceID: t.traceID,
		start:   t.start,
		isSSE:   isSSE,
	}
	return resp
}

type tracedResponseBody struct {
	rc      io.ReadCloser
	writer  *Writer
	traceID string
	start   time.Time
	isSSE   bool

	buf        bytes.Buffer
	totalBytes int64
	chunks     int
	finishOnce sync.Once
}

func (b *tracedResponseBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.totalBytes += int64(n)
		if b.isSSE {
			index := b.chunks
			b.chunks++
			b.writer.WritePhase(b.traceID, "outbound_response_chunk", map[string]any{
				"index":    index,
				"body_b64": base64.StdEncoding.EncodeToString(p[:n]),
			})
		} else {
			_, _ = b.buf.Write(p[:n])
		}
	}
	if err != nil {
		b.finish()
	}
	return n, err
}

func (b *tracedResponseBody) Close() error {
	err := b.rc.Close()
	b.finish()
	return err
}

// finish 通过 finishOnce 保证 outbound_response_end 只落一次 —— Close 和 Read(err)
// 可能被不同 goroutine 调用（stdlib 并不强制 Read/Close 串行，HTTP/2 取消场景尤其），
// 重复写会污染 JSONL 串联。counter 上可能仍有轻微 race 但只影响 end 记录的
// 计数字段，不影响关键字节流重建。非 SSE 路径若只 Close 从不 Read，buf 为空会
// 写一条只有 end、没有 chunk 的记录，符合"什么都没读到"的事实。
func (b *tracedResponseBody) finish() {
	b.finishOnce.Do(func() {
		if !b.isSSE && b.buf.Len() > 0 {
			b.chunks = 1
			b.writer.WritePhase(b.traceID, "outbound_response_chunk", map[string]any{
				"index":    0,
				"body_b64": base64.StdEncoding.EncodeToString(b.buf.Bytes()),
			})
		}
		b.writer.WritePhase(b.traceID, "outbound_response_end", map[string]any{
			"total_bytes": b.totalBytes,
			"chunks":      b.chunks,
			"elapsed_ms":  time.Since(b.start).Milliseconds(),
		})
	})
}

func cloneRequestBody(req *http.Request) ([]byte, *http.Request, error) {
	if req == nil {
		return nil, req, nil
	}
	if req.GetBody != nil {
		rc, err := req.GetBody()
		if err == nil {
			defer func() { _ = rc.Close() }()
			body, err := io.ReadAll(rc)
			return body, req, err
		}
	}
	if req.Body == nil {
		return nil, req, nil
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, req, err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return body, req, nil
}
