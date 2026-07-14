package service

import (
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Gin context keys used by Ops error logger for capturing upstream error details.
// These keys are set by gateway services and consumed by handler/ops_error_logger.go.
const (
	OpsUpstreamStatusCodeKey   = "ops_upstream_status_code"
	OpsUpstreamErrorMessageKey = "ops_upstream_error_message"
	OpsUpstreamErrorDetailKey  = "ops_upstream_error_detail"
	OpsUpstreamErrorsKey       = "ops_upstream_errors"

	// Best-effort capture of the current upstream request body so ops can
	// retry the specific upstream attempt (not just the client request).
	// This value is sanitized+trimmed before being persisted.
	OpsUpstreamRequestBodyKey = "ops_upstream_request_body"

	// Optional stage latencies (milliseconds) for troubleshooting and alerting.
	OpsAuthLatencyMsKey      = "ops_auth_latency_ms"
	OpsRoutingLatencyMsKey   = "ops_routing_latency_ms"
	OpsUpstreamLatencyMsKey  = "ops_upstream_latency_ms"
	OpsResponseLatencyMsKey  = "ops_response_latency_ms"
	OpsTimeToFirstTokenMsKey = "ops_time_to_first_token_ms"
	// OpenAI WS 关键观测字段
	OpsOpenAIWSQueueWaitMsKey = "ops_openai_ws_queue_wait_ms"
	OpsOpenAIWSConnPickMsKey  = "ops_openai_ws_conn_pick_ms"
	OpsOpenAIWSConnReusedKey  = "ops_openai_ws_conn_reused"
	OpsOpenAIWSConnIDKey      = "ops_openai_ws_conn_id"

	// OpsSkipPassthroughKey 由 applyErrorPassthroughRule 在命中 skip_monitoring=true 的规则时设置。
	// ops_error_logger 中间件检查此 key，为 true 时跳过错误记录。
	OpsSkipPassthroughKey = "ops_skip_passthrough"

	// Success request-body capture is opt-in and API-key scoped. When enabled,
	// gateway services may attach a sanitized snapshot of the final upstream
	// request for post-rewrite debugging.
	OpsRequestBodyCaptureOptionsKey       = "ops_request_body_capture_options"
	OpsUpstreamForwardRequestSnapshotKey  = "ops_upstream_forward_request_snapshot"
	OpsUpstreamForwardResponseSnapshotKey = "ops_upstream_forward_response_snapshot"
	OpsFullRequestHeadersCaptureKey       = "ops_full_request_headers_capture"
)

func setOpsUpstreamRequestBody(c *gin.Context, body []byte) {
	if c == nil || len(body) == 0 {
		return
	}
	// 热路径避免 string(body) 额外分配，按需在落库前再转换。
	c.Set(OpsUpstreamRequestBodyKey, body)
}

type OpsRequestBodyCaptureOptions struct {
	MaxBytes        int  `json:"max_bytes"`
	CaptureSuccess  bool `json:"capture_success"`
	CaptureUpstream bool `json:"capture_upstream"`
}

type OpsUpstreamForwardRequestSnapshot struct {
	Method  string                  `json:"method,omitempty"`
	URL     string                  `json:"url,omitempty"`
	Headers []OpsCapturedHeaderLine `json:"headers,omitempty"`
	Body    []byte                  `json:"-"`
}

type OpsUpstreamForwardResponseSnapshot struct {
	StatusCode int                     `json:"status_code,omitempty"`
	Headers    []OpsCapturedHeaderLine `json:"headers,omitempty"`
	Body       []byte                  `json:"-"`
}

type OpsCapturedHeaderLine struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

func CaptureOpsRequestHeaders(req *http.Request) []OpsCapturedHeaderLine {
	if req == nil {
		return nil
	}
	return captureOpsRequestHeaders(req, true)
}

func SetOpsRequestBodyCaptureOptions(c *gin.Context, opts OpsRequestBodyCaptureOptions) {
	if c == nil || opts.MaxBytes <= 0 {
		return
	}
	c.Set(OpsRequestBodyCaptureOptionsKey, opts)
}

func ClearOpsRequestBodyCaptureOptions(c *gin.Context) {
	if c == nil || c.Keys == nil {
		return
	}
	delete(c.Keys, OpsRequestBodyCaptureOptionsKey)
	delete(c.Keys, OpsUpstreamForwardRequestSnapshotKey)
	delete(c.Keys, OpsUpstreamForwardResponseSnapshotKey)
}

func SetOpsFullRequestHeadersCapture(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(OpsFullRequestHeadersCaptureKey, true)
}

func ShouldCaptureFullOpsRequestHeaders(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, ok := c.Get(OpsFullRequestHeadersCaptureKey)
	if !ok {
		return false
	}
	enabled, _ := v.(bool)
	return enabled
}

func GetOpsRequestBodyCaptureOptions(c *gin.Context) (OpsRequestBodyCaptureOptions, bool) {
	if c == nil {
		return OpsRequestBodyCaptureOptions{}, false
	}
	v, ok := c.Get(OpsRequestBodyCaptureOptionsKey)
	if !ok {
		return OpsRequestBodyCaptureOptions{}, false
	}
	opts, ok := v.(OpsRequestBodyCaptureOptions)
	if !ok || opts.MaxBytes <= 0 {
		return OpsRequestBodyCaptureOptions{}, false
	}
	return opts, true
}

func shouldCaptureOpsUpstreamForwardRequest(c *gin.Context) bool {
	opts, ok := GetOpsRequestBodyCaptureOptions(c)
	return ok && opts.CaptureUpstream
}

func SetOpsUpstreamForwardRequestSnapshot(c *gin.Context, req *http.Request, body []byte) {
	if c == nil || req == nil || !shouldCaptureOpsUpstreamForwardRequest(c) {
		return
	}
	snapshot := &OpsUpstreamForwardRequestSnapshot{
		Method:  strings.TrimSpace(req.Method),
		URL:     strings.TrimSpace(req.URL.String()),
		Headers: captureOpsRequestHeaders(req, false),
	}
	if len(body) > 0 {
		snapshot.Body = append([]byte(nil), body...)
	}
	c.Set(OpsUpstreamForwardRequestSnapshotKey, snapshot)
}

func GetOpsUpstreamForwardRequestSnapshot(c *gin.Context) (*OpsUpstreamForwardRequestSnapshot, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Get(OpsUpstreamForwardRequestSnapshotKey)
	if !ok {
		return nil, false
	}
	snapshot, ok := v.(*OpsUpstreamForwardRequestSnapshot)
	if !ok || snapshot == nil {
		return nil, false
	}
	return snapshot, true
}

func SetOpsUpstreamForwardResponseSnapshot(c *gin.Context, statusCode int, headers http.Header, body []byte) {
	if c == nil || !shouldCaptureOpsUpstreamForwardRequest(c) {
		return
	}
	snapshot := &OpsUpstreamForwardResponseSnapshot{
		StatusCode: statusCode,
		Headers:    captureOpsHeadersFromMap(headers),
	}
	if len(body) > 0 {
		snapshot.Body = append([]byte(nil), body...)
	}
	c.Set(OpsUpstreamForwardResponseSnapshotKey, snapshot)
}

func GetOpsUpstreamForwardResponseSnapshot(c *gin.Context) (*OpsUpstreamForwardResponseSnapshot, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Get(OpsUpstreamForwardResponseSnapshotKey)
	if !ok {
		return nil, false
	}
	snapshot, ok := v.(*OpsUpstreamForwardResponseSnapshot)
	if !ok || snapshot == nil {
		return nil, false
	}
	return snapshot, true
}

func captureOpsRequestHeaders(req *http.Request, inbound bool) []OpsCapturedHeaderLine {
	if req == nil {
		return nil
	}
	var (
		dump []byte
		err  error
	)
	if inbound {
		dump, err = httputil.DumpRequest(req, false)
	} else {
		dump, err = httputil.DumpRequestOut(req, false)
	}
	if err != nil || len(dump) == 0 {
		return captureOpsHeadersFromMap(req.Header)
	}

	lines := strings.Split(strings.ReplaceAll(string(dump), "\r\n", "\n"), "\n")
	out := make([]OpsCapturedHeaderLine, 0, len(lines))
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		value = strings.TrimLeft(value, " \t")
		if isSensitiveOpsUpstreamHeader(name) {
			value = "[REDACTED]"
		}
		out = append(out, OpsCapturedHeaderLine{
			Index: len(out) + 1,
			Name:  name,
			Value: value,
		})
	}
	return out
}

func captureOpsHeadersFromMap(headers http.Header) []OpsCapturedHeaderLine {
	if len(headers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	out := make([]OpsCapturedHeaderLine, 0, len(headers))
	for _, key := range keys {
		values := headers.Values(key)
		if len(values) == 0 {
			values = []string{""}
		}
		for _, value := range values {
			if isSensitiveOpsUpstreamHeader(key) {
				value = "[REDACTED]"
			}
			out = append(out, OpsCapturedHeaderLine{
				Index: len(out) + 1,
				Name:  key,
				Value: value,
			})
		}
	}
	return out
}

func isSensitiveOpsUpstreamHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization",
		"proxy-authorization",
		"x-api-key",
		"x-goog-api-key",
		"api-key",
		"openai-api-key",
		"anthropic-auth-token",
		"claude-code-oauth-token",
		"cookie",
		"set-cookie":
		return true
	default:
		return false
	}
}

func SetOpsLatencyMs(c *gin.Context, key string, value int64) {
	if c == nil || strings.TrimSpace(key) == "" || value < 0 {
		return
	}
	c.Set(key, value)
}

// SetOpsUpstreamError is the exported wrapper for setOpsUpstreamError, used by
// handler-layer code (e.g. failover-exhausted paths) that needs to record the
// original upstream status code before mapping it to a client-facing code.
func SetOpsUpstreamError(c *gin.Context, upstreamStatusCode int, upstreamMessage, upstreamDetail string) {
	setOpsUpstreamError(c, upstreamStatusCode, upstreamMessage, upstreamDetail)
}

func setOpsUpstreamError(c *gin.Context, upstreamStatusCode int, upstreamMessage, upstreamDetail string) {
	if c == nil {
		return
	}
	if upstreamStatusCode > 0 {
		c.Set(OpsUpstreamStatusCodeKey, upstreamStatusCode)
	}
	if msg := strings.TrimSpace(upstreamMessage); msg != "" {
		c.Set(OpsUpstreamErrorMessageKey, msg)
	}
	if detail := strings.TrimSpace(upstreamDetail); detail != "" {
		c.Set(OpsUpstreamErrorDetailKey, detail)
	}
}

// OpsUpstreamErrorEvent describes one upstream error attempt during a single gateway request.
// It is stored in ops_error_logs.upstream_errors as a JSON array.
type OpsUpstreamErrorEvent struct {
	AtUnixMs int64 `json:"at_unix_ms,omitempty"`

	// Passthrough 表示本次请求是否命中“原样透传（仅替换认证）”分支。
	// 该字段用于排障与灰度评估；存入 JSON，不涉及 DB schema 变更。
	Passthrough bool `json:"passthrough,omitempty"`

	// Context
	Platform    string `json:"platform,omitempty"`
	AccountID   int64  `json:"account_id,omitempty"`
	AccountName string `json:"account_name,omitempty"`

	// Outcome
	UpstreamStatusCode int    `json:"upstream_status_code,omitempty"`
	UpstreamRequestID  string `json:"upstream_request_id,omitempty"`

	// Best-effort upstream request capture (sanitized+trimmed).
	// Required for retrying a specific upstream attempt.
	UpstreamRequestBody string `json:"upstream_request_body,omitempty"`

	// Best-effort upstream response capture (sanitized+trimmed).
	UpstreamResponseBody string `json:"upstream_response_body,omitempty"`

	// Kind: http_error | request_error | retry_exhausted | failover
	Kind string `json:"kind,omitempty"`

	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

func appendOpsUpstreamError(c *gin.Context, ev OpsUpstreamErrorEvent) {
	if c == nil {
		return
	}
	if ev.AtUnixMs <= 0 {
		ev.AtUnixMs = time.Now().UnixMilli()
	}
	ev.Platform = strings.TrimSpace(ev.Platform)
	ev.UpstreamRequestID = strings.TrimSpace(ev.UpstreamRequestID)
	ev.UpstreamRequestBody = strings.TrimSpace(ev.UpstreamRequestBody)
	ev.UpstreamResponseBody = strings.TrimSpace(ev.UpstreamResponseBody)
	ev.Kind = strings.TrimSpace(ev.Kind)
	ev.Message = strings.TrimSpace(ev.Message)
	ev.Detail = strings.TrimSpace(ev.Detail)
	if ev.Message != "" {
		ev.Message = sanitizeUpstreamErrorMessage(ev.Message)
	}

	// If the caller didn't explicitly pass upstream request body but the gateway
	// stored it on the context, attach it so ops can retry this specific attempt.
	if ev.UpstreamRequestBody == "" {
		if v, ok := c.Get(OpsUpstreamRequestBodyKey); ok {
			switch raw := v.(type) {
			case string:
				ev.UpstreamRequestBody = strings.TrimSpace(raw)
			case []byte:
				ev.UpstreamRequestBody = strings.TrimSpace(string(raw))
			}
		}
	}

	var existing []*OpsUpstreamErrorEvent
	if v, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if arr, ok := v.([]*OpsUpstreamErrorEvent); ok {
			existing = arr
		}
	}

	evCopy := ev
	existing = append(existing, &evCopy)
	c.Set(OpsUpstreamErrorsKey, existing)

	checkSkipMonitoringForUpstreamEvent(c, &evCopy)
}

// checkSkipMonitoringForUpstreamEvent checks whether the upstream error event
// matches a passthrough rule with skip_monitoring=true and, if so, sets the
// OpsSkipPassthroughKey on the context.  This ensures intermediate retry /
// failover errors (which never go through the final applyErrorPassthroughRule
// path) can still suppress ops_error_logs recording.
func checkSkipMonitoringForUpstreamEvent(c *gin.Context, ev *OpsUpstreamErrorEvent) {
	if ev.UpstreamStatusCode == 0 {
		return
	}

	svc := getBoundErrorPassthroughService(c)
	if svc == nil {
		return
	}

	// Use the best available body representation for keyword matching.
	// Even when body is empty, MatchRule can still match rules that only
	// specify ErrorCodes (no Keywords), so we always call it.
	body := ev.Detail
	if body == "" {
		body = ev.Message
	}

	rule := svc.MatchRule(ev.Platform, ev.UpstreamStatusCode, []byte(body))
	if rule != nil && rule.SkipMonitoring {
		c.Set(OpsSkipPassthroughKey, true)
	}
}

func marshalOpsUpstreamErrors(events []*OpsUpstreamErrorEvent) *string {
	if len(events) == 0 {
		return nil
	}
	// Ensure we always store a valid JSON value.
	raw, err := json.Marshal(events)
	if err != nil || len(raw) == 0 {
		return nil
	}
	s := string(raw)
	return &s
}

func ParseOpsUpstreamErrors(raw string) ([]*OpsUpstreamErrorEvent, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []*OpsUpstreamErrorEvent{}, nil
	}
	var out []*OpsUpstreamErrorEvent
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}
