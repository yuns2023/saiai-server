package openai_ws_v2

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/tidwall/gjson"
)

// RelayFailureDiagnostics is content-free metadata about what the relay
// observed around a failed turn. It exists to tell a client-initiated stop
// (for example a client response.interrupt) from a provider-side termination.
// Every value is a count, a duration, a boolean or a fixed protocol label; no
// request or response text is ever copied. All times are gateway observation
// order, not provider-internal order.
type RelayFailureDiagnostics struct {
	// Attribution tells how the failure was matched to its turn:
	// "response_id" or "single_outstanding".
	Attribution string `json:"attribution"`
	RelayAgeMs  int64  `json:"relay_age_ms"`
	// MsSinceSent counts from the moment the relay started writing the
	// turn's response.create upstream.
	MsSinceSent *int64 `json:"ms_since_sent,omitempty"`
	// Event fields are omitted when no upstream event carrying this
	// response id preceded the failure.
	MsSinceFirstResponseEvent *int64 `json:"ms_since_first_response_event,omitempty"`
	MsSinceResponseCreated    *int64 `json:"ms_since_response_created,omitempty"`
	MsSincePreviousEvent      *int64 `json:"ms_since_previous_event,omitempty"`
	PreviousEventType         string `json:"previous_event_type,omitempty"`
	UpstreamEvents            int    `json:"upstream_events"`
	// FirstTokenMsAfterFirstEvent keeps the relay's existing per-turn
	// first-token semantics (any delta/output event after the first event).
	FirstTokenMsAfterFirstEvent *int `json:"first_token_ms_after_first_event,omitempty"`
	// LaterClientTurns counts response.create frames received after this
	// turn and before the failure was observed.
	LaterClientTurns       int                            `json:"later_client_turns"`
	ClientControl          []RelayControlFrameDiagnostics `json:"client_control,omitempty"`
	ClientControlTruncated bool                           `json:"client_control_truncated,omitempty"`
	ClientEnd              *RelayClientEndDiagnostics     `json:"client_end,omitempty"`
	// DownstreamWriteFailedMsBefore is set when a write to the client
	// failed before the failure was observed (the relay was draining).
	DownstreamWriteFailedMsBefore *int64              `json:"downstream_write_failed_ms_before,omitempty"`
	ReportedUsage                 *RelayReportedUsage `json:"reported_usage,omitempty"`
	OutputItems                   *int                `json:"output_items,omitempty"`
}

// RelayControlFrameDiagnostics is a client application frame other than
// response.create received while the failed turn was in flight.
type RelayControlFrameDiagnostics struct {
	Type            string `json:"type"`
	MsAfterSent     int64  `json:"ms_after_sent"`
	MsBeforeFailure int64  `json:"ms_before_failure"`
	// Target is "current", "other" or "unspecified" relative to the failed
	// response id; the id itself is not recorded.
	Target              string `json:"target"`
	DiscardPartialItems *bool  `json:"discard_partial_items,omitempty"`
	Forwarded           bool   `json:"forwarded"`
}

// RelayClientEndDiagnostics records that the client side of the relay had
// already ended (the relay was draining) when the failure was observed.
type RelayClientEndDiagnostics struct {
	Kind            string `json:"kind"`
	CloseCode       int    `json:"close_code,omitempty"`
	MsBeforeFailure int64  `json:"ms_before_failure"`
}

// RelayReportedUsage is usage reported on a failure event. It is diagnostic
// only and never billed; a missing field stays nil rather than zero.
type RelayReportedUsage struct {
	InputTokens     *int64 `json:"input_tokens,omitempty"`
	OutputTokens    *int64 `json:"output_tokens,omitempty"`
	CachedTokens    *int64 `json:"cached_tokens,omitempty"`
	ReasoningTokens *int64 `json:"reasoning_tokens,omitempty"`
}

const (
	relayControlRingSize  = 16
	relayControlOutputMax = 8
	relayMaxReportedCount = 1 << 40
)

// Upstream event types are provider protocol identifiers. Anything else is
// reported as "other" so a malformed frame cannot smuggle text into the log.
var relayEventTypeLabelPattern = regexp.MustCompile(`^(response|codex|error|rate_limits)[a-z0-9_.]{0,80}$`)

type relayControlEvent struct {
	kind      string
	at        time.Time
	target    string
	discard   *bool
	forwarded bool
}

// relayClientDiagnostics is written by the client reader and read by the
// upstream reader, so every access holds mu. Its size is fixed.
type relayClientDiagnostics struct {
	mu                 sync.Mutex
	ring               [relayControlRingSize]relayControlEvent
	recorded           int
	clientEndKind      string
	clientEndCode      int
	clientEndAt        time.Time
	downstreamFailedAt time.Time
}

func relayControlKind(payload []byte) string {
	switch strings.TrimSpace(gjson.GetBytes(payload, "type").String()) {
	case "response.interrupt":
		return "response.interrupt"
	case "response.cancel":
		return "response.cancel"
	default:
		return "other"
	}
}

func (d *relayClientDiagnostics) recordControl(payload []byte, at time.Time, forwarded bool) {
	if d == nil {
		return
	}
	event := relayControlEvent{kind: relayControlKind(payload), at: at, forwarded: forwarded}
	// The target id stays in memory only, for comparison with the failed
	// response id.
	if target := gjson.GetBytes(payload, "response_id"); target.Type == gjson.String && len(target.String()) <= 128 {
		event.target = target.String()
	}
	if discard := gjson.GetBytes(payload, "discard_partial_items"); discard.IsBool() {
		value := discard.Bool()
		event.discard = &value
	}
	d.mu.Lock()
	d.ring[d.recorded%relayControlRingSize] = event
	d.recorded++
	d.mu.Unlock()
}

func (d *relayClientDiagnostics) recordClientEnd(err error, at time.Time) {
	if d == nil {
		return
	}
	kind := "read_error"
	code := 0
	if status := coderws.CloseStatus(err); status != -1 {
		kind, code = "closed", int(status)
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		kind = "context_done"
	} else if isDisconnectError(err) {
		kind = "disconnected"
	}
	d.mu.Lock()
	if d.clientEndAt.IsZero() {
		d.clientEndKind, d.clientEndCode, d.clientEndAt = kind, code, at
	}
	d.mu.Unlock()
}

func (d *relayClientDiagnostics) recordDownstreamWriteFailed(at time.Time) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.downstreamFailedAt.IsZero() {
		d.downstreamFailedAt = at
	}
	d.mu.Unlock()
}

// fill copies the client-side observations in [sentAt, failureAt] into out.
func (d *relayClientDiagnostics) fill(out *RelayFailureDiagnostics, sentAt, failureAt time.Time, responseID string) {
	if d == nil || out == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	retained := d.recorded
	if retained > relayControlRingSize {
		retained = relayControlRingSize
		// Evicted frames may have belonged to this turn's window.
		if !sentAt.IsZero() && !d.ring[d.recorded%relayControlRingSize].at.Before(sentAt) {
			out.ClientControlTruncated = true
		}
	}
	for index := d.recorded - retained; index < d.recorded; index++ {
		event := d.ring[index%relayControlRingSize]
		if (!sentAt.IsZero() && event.at.Before(sentAt)) || event.at.After(failureAt) {
			continue
		}
		if len(out.ClientControl) >= relayControlOutputMax {
			out.ClientControlTruncated = true
			continue
		}
		target := "unspecified"
		if event.target != "" {
			target = "other"
			if event.target == responseID {
				target = "current"
			}
		}
		frame := RelayControlFrameDiagnostics{
			Type: event.kind, MsBeforeFailure: relayElapsedMs(event.at, failureAt),
			Target: target, Forwarded: event.forwarded,
		}
		if !sentAt.IsZero() {
			frame.MsAfterSent = relayElapsedMs(sentAt, event.at)
		}
		if event.discard != nil {
			discard := *event.discard
			frame.DiscardPartialItems = &discard
		}
		out.ClientControl = append(out.ClientControl, frame)
	}
	if !d.clientEndAt.IsZero() && !d.clientEndAt.After(failureAt) {
		out.ClientEnd = &RelayClientEndDiagnostics{
			Kind: d.clientEndKind, CloseCode: d.clientEndCode,
			MsBeforeFailure: relayElapsedMs(d.clientEndAt, failureAt),
		}
	}
	if !d.downstreamFailedAt.IsZero() && !d.downstreamFailedAt.After(failureAt) {
		before := relayElapsedMs(d.downstreamFailedAt, failureAt)
		out.DownstreamWriteFailedMsBefore = &before
	}
}

func (t *relayTurnTiming) recordEvent(eventType string, at time.Time) {
	if t == nil {
		return
	}
	if eventType == "response.created" && t.createdAt.IsZero() {
		t.createdAt = at
	}
	t.events++
	t.lastEventAt = at
	t.lastEventType = relayEventTypeLabel(eventType)
}

func relayEventTypeLabel(eventType string) string {
	if relayEventTypeLabelPattern.MatchString(eventType) {
		return eventType
	}
	return "other"
}

func parseRelayReportedUsage(message []byte) *RelayReportedUsage {
	values := gjson.GetManyBytes(message,
		"response.usage.input_tokens",
		"response.usage.output_tokens",
		"response.usage.input_tokens_details.cached_tokens",
		"response.usage.output_tokens_details.reasoning_tokens",
	)
	usage := RelayReportedUsage{
		InputTokens:     relayReportedCount(values[0]),
		OutputTokens:    relayReportedCount(values[1]),
		CachedTokens:    relayReportedCount(values[2]),
		ReasoningTokens: relayReportedCount(values[3]),
	}
	if usage == (RelayReportedUsage{}) {
		return nil
	}
	return &usage
}

func relayReportedCount(value gjson.Result) *int64 {
	if value.Type != gjson.Number {
		return nil
	}
	number := value.Int()
	if float64(number) != value.Float() || number < 0 || number > relayMaxReportedCount {
		return nil
	}
	return &number
}

func relayOutputItemCount(message []byte) *int {
	output := gjson.GetBytes(message, "response.output")
	if !output.IsArray() {
		return nil
	}
	count := 0
	output.ForEach(func(_, _ gjson.Result) bool {
		count++
		return true
	})
	return &count
}

func buildRelayFailureDiagnostics(state *relayState, observed observedUpstreamEvent, metadata RelayTurnMetadata, attribution string) *RelayFailureDiagnostics {
	if state == nil || observed.at.IsZero() {
		return nil
	}
	at := observed.at
	diagnostics := &RelayFailureDiagnostics{
		Attribution:   attribution,
		ReportedUsage: observed.reportedUsage,
		OutputItems:   observed.outputItems,
	}
	if !state.startAt.IsZero() {
		diagnostics.RelayAgeMs = relayElapsedMs(state.startAt, at)
	}
	if !metadata.SentAt.IsZero() {
		sent := relayElapsedMs(metadata.SentAt, at)
		diagnostics.MsSinceSent = &sent
	}
	if timing := observed.timing; timing != nil && timing.events > 0 {
		first := relayElapsedMs(timing.startAt, at)
		previous := relayElapsedMs(timing.lastEventAt, at)
		diagnostics.MsSinceFirstResponseEvent = &first
		diagnostics.MsSincePreviousEvent = &previous
		diagnostics.PreviousEventType = timing.lastEventType
		diagnostics.UpstreamEvents = timing.events
		diagnostics.FirstTokenMsAfterFirstEvent = openAIWSRelayCloneIntPtr(timing.firstTokenMs)
		if !timing.createdAt.IsZero() {
			created := relayElapsedMs(timing.createdAt, at)
			diagnostics.MsSinceResponseCreated = &created
		}
	}
	if state.clientTurns != nil && metadata.Turn > 0 {
		if later := int(state.clientTurns.Load()) - metadata.Turn; later > 0 {
			diagnostics.LaterClientTurns = later
		}
	}
	state.clientDiagnostics.fill(diagnostics, metadata.SentAt, at, observed.responseID)
	return diagnostics
}

func relayElapsedMs(from, to time.Time) int64 {
	if elapsed := to.Sub(from).Milliseconds(); elapsed > 0 {
		return elapsed
	}
	return 0
}
