package service

import (
	"context"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// upstreamStopReasonMaxLen mirrors usage_logs.stop_reason VARCHAR(32).
const upstreamStopReasonMaxLen = 32

var upstreamStopReasonPattern = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// anthropicUpstreamObservation captures diagnostic facts observed on an
// Anthropic-native upstream response (direct OAuth/API key, API key
// passthrough, Bedrock). It is evidence only: it must never influence
// forwarding, retry, stream recovery, scheduling or billing.
//
// It is intentionally separate from the stream-recovery stop_reason tracking
// (processedAnthropicSSEEvent.finalStopReason / terminalCandidateStopReason),
// which has stricter eligibility rules.
type anthropicUpstreamObservation struct {
	// stopReason is the sanitized upstream stop_reason; nil when missing,
	// null, non-string or rejected by sanitization.
	stopReason *string
	// lastOutputTokens is the raw output_tokens as last reported by upstream:
	// the non-stream body usage, or for streams the LAST VALID numeric
	// output_tokens seen on a message_delta. It is not proof that the stream
	// completed (terminal completeness is decided elsewhere). nil means no
	// valid value was observed. Kept separate from the merged billing usage,
	// which ignores non-positive stream deltas.
	lastOutputTokens *int64
}

// numericOutputTokens accepts only whole, non-negative JSON numbers; anything
// else (fractions, negatives, strings) is treated as not reported.
func numericOutputTokens(value gjson.Result) *int64 {
	if value.Type != gjson.Number || value.Num < 0 || value.Num != float64(int64(value.Num)) {
		return nil
	}
	v := int64(value.Num)
	return &v
}

// sanitizeUpstreamStopReason accepts only JSON strings whose trimmed,
// lower-cased value is entirely [a-z0-9_]{1,32}. Anything else yields nil;
// characters are never stripped to manufacture a valid value. Unknown but
// well-formed values are kept so new upstream reasons remain visible.
func sanitizeUpstreamStopReason(value gjson.Result) *string {
	if value.Type != gjson.String {
		return nil
	}
	normalized := strings.ToLower(strings.TrimSpace(value.String()))
	if len(normalized) > upstreamStopReasonMaxLen || !upstreamStopReasonPattern.MatchString(normalized) {
		return nil
	}
	return &normalized
}

// observeAnthropicNonStreamBody reads the root stop_reason and the raw
// usage.output_tokens (if numeric) from a non-streaming
// Anthropic Messages response body. Parse problems yield an empty observation.
func observeAnthropicNonStreamBody(body []byte) anthropicUpstreamObservation {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return anthropicUpstreamObservation{}
	}
	parsed := gjson.ParseBytes(body)
	return anthropicUpstreamObservation{
		stopReason:       sanitizeUpstreamStopReason(parsed.Get("stop_reason")),
		lastOutputTokens: numericOutputTokens(parsed.Get("usage.output_tokens")),
	}
}

// observeStreamEvent records stop_reason / raw output_tokens from a
// message_delta event. Other event types (including message_start, whose
// usage is only the initial snapshot) are ignored. A later valid stop_reason
// overrides an earlier one; an invalid or null one never clears it.
func (o *anthropicUpstreamObservation) observeStreamEvent(parsed gjson.Result) {
	if o == nil || parsed.Get("type").String() != "message_delta" {
		return
	}
	if reason := sanitizeUpstreamStopReason(parsed.Get("delta.stop_reason")); reason != nil {
		o.stopReason = reason
	}
	if v := numericOutputTokens(parsed.Get("usage.output_tokens")); v != nil {
		o.lastOutputTokens = v
	}
}

// bedrockRawOutputTokens mirrors transformBedrockInvocationMetrics' source
// selection (a standard "usage" object wins, otherwise
// amazon-bedrock-invocationMetrics.outputTokenCount) but reads the RAW value
// with the strict diagnostic rule. The converter coerces fractions/strings to
// integers, so observing after conversion could fabricate an explicit 0.
func bedrockRawOutputTokens(event gjson.Result) *int64 {
	if event.Get("usage").Exists() {
		return numericOutputTokens(event.Get("usage.output_tokens"))
	}
	if metrics := event.Get("amazon-bedrock-invocationMetrics"); metrics.IsObject() {
		return numericOutputTokens(metrics.Get("outputTokenCount"))
	}
	return nil
}

// observeBedrockNonStreamBody observes a Bedrock InvokeModel body BEFORE
// transformBedrockInvocationMetrics rewrites it.
func observeBedrockNonStreamBody(raw []byte) anthropicUpstreamObservation {
	if len(raw) == 0 || !gjson.ValidBytes(raw) {
		return anthropicUpstreamObservation{}
	}
	parsed := gjson.ParseBytes(raw)
	return anthropicUpstreamObservation{
		stopReason:       sanitizeUpstreamStopReason(parsed.Get("stop_reason")),
		lastOutputTokens: bedrockRawOutputTokens(parsed),
	}
}

// observeBedrockStreamChunk observes a decoded Bedrock chunk (Anthropic SSE
// event JSON) BEFORE transformBedrockInvocationMetrics rewrites it. Like the
// direct path, only message_delta events are considered.
func (o *anthropicUpstreamObservation) observeBedrockStreamChunk(raw []byte) {
	if o == nil || len(raw) == 0 {
		return
	}
	parsed := gjson.ParseBytes(raw)
	if parsed.Get("type").String() != "message_delta" {
		return
	}
	if reason := sanitizeUpstreamStopReason(parsed.Get("delta.stop_reason")); reason != nil {
		o.stopReason = reason
	}
	if v := bedrockRawOutputTokens(parsed); v != nil {
		o.lastOutputTokens = v
	}
}

// observeStreamData is observeStreamEvent for a raw SSE data payload.
func (o *anthropicUpstreamObservation) observeStreamData(data string) {
	if o == nil || data == "" || data == "[DONE]" {
		return
	}
	o.observeStreamEvent(gjson.Parse(data))
}

func (o *anthropicUpstreamObservation) applyTo(result *ForwardResult) {
	if o == nil || result == nil {
		return
	}
	result.StopReason = o.stopReason
	result.UpstreamLastOutputTokens = o.lastOutputTokens
}

// isZeroOutputUsage reports whether the upstream explicitly said it generated
// zero output tokens while still consuming prompt/cache tokens. It must be
// evaluated on the upstream usage before billing conversions in RecordUsage.
func isZeroOutputUsage(result *ForwardResult) bool {
	if result == nil || result.UpstreamLastOutputTokens == nil || *result.UpstreamLastOutputTokens != 0 {
		return false
	}
	return result.Usage.InputTokens+result.Usage.CacheCreationInputTokens+result.Usage.CacheReadInputTokens > 0
}

// LogZeroOutputUsage emits one structured WARN per successful response when
// the upstream explicitly reported output_tokens=0 but consumed prompt/cache
// tokens. It is diagnostic only and has no billing/retry/scheduling effect.
// Call it before submitting the async usage task so the usage is still the
// raw upstream usage.
func LogZeroOutputUsage(ctx context.Context, account *Account, result *ForwardResult) {
	if account == nil || !isZeroOutputUsage(result) {
		return
	}
	stopReason := ""
	if result.StopReason != nil {
		stopReason = *result.StopReason
	}
	upstreamModel := result.UpstreamModel
	if upstreamModel == "" {
		upstreamModel = result.Model
	}
	// The request-scoped logger already carries request_id / client_request_id
	// (RequestLogger middleware). Correlation with the usage row is by
	// upstream_request_id: the async usage worker runs on a detached context
	// and resolves usage_logs.request_id from the upstream request id, which
	// Anthropic/Bedrock responses normally carry. Known gap: if upstream sends
	// no request id the worker stores a "generated:..." id that cannot be
	// joined from this log. Billing idempotency resolution is intentionally
	// left unchanged.
	logger.FromContext(ctx).Warn("gateway.zero_output_usage",
		zap.String("component", "service.gateway"),
		zap.String("platform", account.Platform),
		zap.Int64("account_id", account.ID),
		zap.String("model", result.Model),
		zap.String("upstream_model", upstreamModel),
		zap.String("upstream_request_id", result.RequestID),
		zap.String("stop_reason", stopReason),
		zap.Bool("stream", result.Stream),
		zap.Int64("output_tokens", *result.UpstreamLastOutputTokens),
		zap.Int("input_tokens", result.Usage.InputTokens),
		zap.Int("cache_creation_tokens", result.Usage.CacheCreationInputTokens),
		zap.Int("cache_read_tokens", result.Usage.CacheReadInputTokens),
		zap.Bool("client_disconnect", result.ClientDisconnect),
	)
}
