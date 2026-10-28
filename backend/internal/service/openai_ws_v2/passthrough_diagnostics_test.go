package openai_ws_v2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestRelay_InterruptedTurnDiagnosticsRecordClientInterrupt(t *testing.T) {
	client := &passthroughTestFrameConn{readCh: make(chan passthroughTestFrame, 8)}
	upstream := &passthroughTestFrameConn{readCh: make(chan passthroughTestFrame, 8)}
	turns := make(chan RelayTurnResult, 4)
	done := make(chan RelayResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		result, _ := Relay(ctx, client, upstream, []byte(`{"type":"response.create","model":"MOCK_ONLY","input":"TEST_ONLY_PRIVATE_CONTENT"}`), RelayOptions{
			OnTurnComplete: func(turn RelayTurnResult) { turns <- turn },
		})
		done <- result
	}()
	waitWrites := func(conn *passthroughTestFrameConn, n int) {
		require.Eventually(t, func() bool { return len(conn.Writes()) >= n }, 2*time.Second, time.Millisecond)
	}
	text := func(raw string) passthroughTestFrame {
		return passthroughTestFrame{msgType: coderws.MessageText, payload: []byte(raw)}
	}
	waitWrites(upstream, 1)
	upstream.readCh <- text(`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`)
	upstream.readCh <- text(`{"type":"response.output_text.delta","response_id":"resp_1","delta":"TEST_ONLY_PRIVATE_CONTENT"}`)
	waitWrites(client, 2)
	client.readCh <- text(`{"type":"response.interrupt","response_id":"resp_1","discard_partial_items":true,"note":"TEST_ONLY_PRIVATE_CONTENT"}`)
	waitWrites(upstream, 2)
	upstream.readCh <- text(`{"type":"response.incomplete","response":{"id":"resp_1","incomplete_details":{"reason":"interrupted"},"output":[{"type":"message","content":"TEST_ONLY_PRIVATE_CONTENT"}],"usage":{"input_tokens":120,"output_tokens":7,"input_tokens_details":{"cached_tokens":100},"output_tokens_details":{"reasoning_tokens":3}}}}`)

	turn := <-turns
	close(client.readCh)
	close(upstream.readCh)
	result := <-done

	require.NotNil(t, turn.Failure)
	require.Equal(t, "interrupted", turn.Failure.IncompleteReason)
	require.Equal(t, Usage{}, turn.Usage, "failure usage stays unbilled")
	require.Equal(t, Usage{}, result.Usage)
	diagnostics := turn.Diagnostics
	require.NotNil(t, diagnostics)
	require.Equal(t, "response_id", diagnostics.Attribution)
	require.NotNil(t, diagnostics.MsSinceSent)
	require.NotNil(t, diagnostics.MsSinceResponseCreated)
	require.NotNil(t, diagnostics.MsSinceFirstResponseEvent)
	require.NotNil(t, diagnostics.MsSincePreviousEvent)
	require.Equal(t, 2, diagnostics.UpstreamEvents)
	require.Equal(t, "response.output_text.delta", diagnostics.PreviousEventType)
	require.Zero(t, diagnostics.LaterClientTurns)
	require.Nil(t, diagnostics.ClientEnd)
	require.Len(t, diagnostics.ClientControl, 1)
	control := diagnostics.ClientControl[0]
	require.Equal(t, "response.interrupt", control.Type)
	require.Equal(t, "current", control.Target)
	require.NotNil(t, control.Forwarded)
	require.True(t, *control.Forwarded)
	require.Equal(t, 1, diagnostics.ClientInterrupts)
	require.Zero(t, diagnostics.ClientCancels)
	require.NotNil(t, control.DiscardPartialItems)
	require.True(t, *control.DiscardPartialItems)
	require.Equal(t, int64(7), *diagnostics.ReportedUsage.OutputTokens)
	require.Equal(t, int64(100), *diagnostics.ReportedUsage.CachedTokens)
	require.Equal(t, int64(3), *diagnostics.ReportedUsage.ReasoningTokens)
	require.Equal(t, 1, *diagnostics.OutputItems)
	encoded, err := json.Marshal(diagnostics)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "TEST_ONLY_PRIVATE_CONTENT")
	require.NotContains(t, string(encoded), "resp_1", "the target response id itself is not recorded")
}

func TestRelayFailureDiagnostics_ControlWindowAndTruncation(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	diagnostics := &relayClientDiagnostics{}
	diagnostics.recordControl([]byte(`{"type":"response.cancel"}`), base.Add(-time.Second))
	for index := 0; index < relayControlOutputMax+1; index++ {
		seq := diagnostics.recordControl([]byte(fmt.Sprintf(`{"type":"TEST_ONLY_PRIVATE_CONTENT_%d","response_id":"resp_other"}`, index)), base.Add(time.Duration(index+1)*time.Millisecond))
		diagnostics.markControlWritten(seq, true, base.Add(time.Duration(index+1)*time.Millisecond))
	}
	interrupt := diagnostics.recordControl([]byte(`{"type":"response.interrupt","response_id":"resp_current","discard_partial_items":false}`), base.Add(100*time.Millisecond))
	diagnostics.recordControl([]byte(`{"type":"response.interrupt"}`), base.Add(time.Hour))
	out := &RelayFailureDiagnostics{}
	diagnostics.fill(out, base, base.Add(time.Second), "resp_current")
	require.Len(t, out.ClientControl, relayControlOutputMax)
	require.True(t, out.ClientControlTruncated)
	require.Equal(t, 1, out.ClientInterrupts, "the frame after the failure is excluded")
	last := out.ClientControl[relayControlOutputMax-1]
	require.Equal(t, "response.interrupt", last.Type, "the most recent frames are kept")
	require.Equal(t, "current", last.Target)
	require.Nil(t, last.Forwarded, "write not finished before the failure")
	require.False(t, *last.DiscardPartialItems)
	for _, control := range out.ClientControl[:relayControlOutputMax-1] {
		require.Equal(t, "other", control.Type, "unknown client types are not copied")
		require.Equal(t, "other", control.Target)
		require.True(t, *control.Forwarded)
	}
	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "TEST_ONLY_PRIVATE_CONTENT")

	// A write that returns after the failure stays unknown.
	diagnostics.markControlWritten(interrupt, true, base.Add(2*time.Second))
	out = &RelayFailureDiagnostics{}
	diagnostics.fill(out, base, base.Add(time.Second), "resp_current")
	require.Nil(t, out.ClientControl[relayControlOutputMax-1].Forwarded)
	out = &RelayFailureDiagnostics{}
	diagnostics.fill(out, base, base.Add(3*time.Second), "")
	require.Equal(t, "unknown", out.ClientControl[relayControlOutputMax-1].Target, "a failure without response id cannot be compared")
	require.True(t, *out.ClientControl[relayControlOutputMax-1].Forwarded)
	out = &RelayFailureDiagnostics{}
	diagnostics.fill(out, time.Time{}, base.Add(time.Second), "resp_current")
	require.Empty(t, out.ClientControl, "no turn window without a send time")

	// Counts cover the whole retained window, not only the listed frames.
	early := &relayClientDiagnostics{}
	early.recordControl([]byte(`{"type":"response.interrupt"}`), base)
	for index := 0; index < relayControlOutputMax; index++ {
		early.recordControl([]byte(`{"type":"session.update"}`), base.Add(time.Millisecond))
	}
	out = &RelayFailureDiagnostics{}
	early.fill(out, base, base.Add(time.Second), "resp_current")
	require.Equal(t, 1, out.ClientInterrupts)
	require.True(t, out.ClientControlTruncated)
	require.Len(t, out.ClientControl, relayControlOutputMax)

	targets := &relayClientDiagnostics{}
	targets.recordControl([]byte(`{"type":"response.cancel","response_id":42}`), base)
	targets.recordControl([]byte(`{"type":"response.cancel"}`), base)
	out = &RelayFailureDiagnostics{}
	targets.fill(out, base, base, "resp_current")
	require.Equal(t, "invalid", out.ClientControl[0].Target)
	require.Equal(t, "unspecified", out.ClientControl[1].Target)
	require.Equal(t, 2, out.ClientCancels)

	// A ring that evicted frames newer than the turn start is incomplete.
	evicted := &relayClientDiagnostics{}
	for index := 0; index < relayControlRingSize+1; index++ {
		evicted.recordControl([]byte(`{"type":"response.cancel"}`), base.Add(time.Duration(index)*time.Millisecond))
	}
	evicted.markControlWritten(0, true, base) // evicted sequence: ignored
	out = &RelayFailureDiagnostics{}
	evicted.fill(out, base.Add(-time.Second), base.Add(time.Second), "resp_current")
	require.True(t, out.ClientControlTruncated)
	out = &RelayFailureDiagnostics{}
	evicted.fill(out, base.Add(10*time.Millisecond), base.Add(time.Second), "resp_current")
	require.False(t, out.ClientControlTruncated, "evicted frames predate this turn")
	require.Len(t, out.ClientControl, 7)
	require.Nil(t, out.ClientControl[0].Forwarded)
}

func TestRelayFailureDiagnostics_ClientEndAndDownstreamFailure(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	diagnostics := &relayClientDiagnostics{}
	diagnostics.recordClientEnd(coderws.CloseError{Code: coderws.StatusGoingAway, Reason: "TEST_ONLY_PRIVATE_CONTENT"}, base)
	diagnostics.recordClientEnd(io.EOF, base.Add(time.Millisecond))
	diagnostics.recordDownstreamWriteFailed(base.Add(2 * time.Millisecond))
	out := &RelayFailureDiagnostics{}
	diagnostics.fill(out, time.Time{}, base.Add(time.Second), "")
	require.Equal(t, &RelayClientEndDiagnostics{Kind: "closed", CloseCode: int(coderws.StatusGoingAway), MsBeforeFailure: 1000}, out.ClientEnd)
	require.Equal(t, int64(998), *out.DownstreamWriteFailedMsBefore)
	out = &RelayFailureDiagnostics{}
	diagnostics.fill(out, time.Time{}, base.Add(-time.Second), "")
	require.Nil(t, out.ClientEnd, "a later disconnect cannot explain an earlier failure")
	require.Nil(t, out.DownstreamWriteFailedMsBefore)

	for _, tc := range []struct {
		err  error
		kind string
	}{
		{err: context.Canceled, kind: "context_done"},
		{err: io.EOF, kind: "disconnected"},
		{err: errors.New("TEST_ONLY_PRIVATE_CONTENT"), kind: "read_error"},
		{kind: "frame_rejected"},
	} {
		diagnostics := &relayClientDiagnostics{}
		if tc.err == nil {
			diagnostics.recordClientFrameRejected(base)
		} else {
			diagnostics.recordClientEnd(tc.err, base)
		}
		out := &RelayFailureDiagnostics{}
		diagnostics.fill(out, time.Time{}, base, "")
		require.Equal(t, tc.kind, out.ClientEnd.Kind)
	}
}

func TestRelayFailureDiagnostics_StateLevelAttributionAndLabels(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	now := base
	clientTurns := &atomic.Int32{}
	clientTurns.Store(1)
	state := &relayState{turns: newRelayTurnTracker(), startAt: base, clientTurns: clientTurns, clientDiagnostics: &relayClientDiagnostics{}}
	state.turns.register(RelayTurnMetadata{Turn: 1, SentAt: base})
	var turns []RelayTurnResult
	observe := func(raw string) {
		emitTurnComplete(func(turn RelayTurnResult) { turns = append(turns, turn) }, state, observeUpstreamMessage(state, []byte(raw), base, func() time.Time { return now }, nil))
	}
	now = base.Add(50 * time.Millisecond)
	observe(`{"type":"error","error":{"type":"server_error","message":"TEST_ONLY_PRIVATE_CONTENT"}}`)
	require.Len(t, turns, 1)
	require.Equal(t, "single_outstanding", turns[0].Diagnostics.Attribution)
	require.Equal(t, int64(50), *turns[0].Diagnostics.MsSinceSent)
	require.Equal(t, int64(50), turns[0].Diagnostics.RelayAgeMs)
	require.Nil(t, turns[0].Diagnostics.MsSinceFirstResponseEvent, "no event preceded the failure")
	require.Nil(t, turns[0].Diagnostics.ReportedUsage)
	require.Nil(t, turns[0].Diagnostics.OutputItems)

	state.turns.register(RelayTurnMetadata{Turn: 2, SentAt: now})
	now = now.Add(10 * time.Millisecond)
	observe(`{"type":"response.output_text.delta","response_id":"resp_2"}`)
	now = now.Add(5 * time.Millisecond)
	observe(`{"type":"response.TEST ONLY","response_id":"resp_2"}`)
	now = now.Add(5 * time.Millisecond)
	observe(`{"type":"response.completed","response":{"id":"resp_2","usage":{"input_tokens":1,"output_tokens":1}}}`)
	require.Len(t, turns, 2)
	require.Nil(t, turns[1].Diagnostics, "successful turns carry no failure diagnostics")

	clientTurns.Store(4) // turn 3 plus one later create received before the failure
	state.turns.register(RelayTurnMetadata{Turn: 3, SentAt: now})
	now = now.Add(10 * time.Millisecond)
	observe(`{"type":"response.output_text.delta","response_id":"resp_3"}`)
	observe(`{"type":"response.private_customer_text","response_id":"resp_3"}`)
	now = now.Add(20 * time.Millisecond)
	observe(`{"type":"response.incomplete","response":{"id":"resp_3","incomplete_details":{"reason":"interrupted"},"usage":{"input_tokens":1.5,"output_tokens":-1}}}`)
	require.Len(t, turns, 3)
	diagnostics := turns[2].Diagnostics
	require.Equal(t, "response_id", diagnostics.Attribution)
	require.Equal(t, 1, diagnostics.LaterClientTurns)
	require.Equal(t, 2, diagnostics.UpstreamEvents)
	require.Equal(t, "other", diagnostics.PreviousEventType)
	require.Equal(t, int64(20), *diagnostics.MsSincePreviousEvent)
	require.Equal(t, int64(20), *diagnostics.MsSinceFirstResponseEvent)
	require.Nil(t, diagnostics.MsSinceResponseCreated, "response.created was never observed")
	require.Nil(t, diagnostics.ReportedUsage, "non-integer or negative usage is not reported")
	require.Equal(t, "response.output_text.delta", relayEventTypeLabel("response.output_text.delta"))
	require.Equal(t, "other", relayEventTypeLabel("responseTEST_ONLY_PRIVATE_CONTENT"))
	require.Equal(t, 0, *diagnostics.FirstTokenMsAfterFirstEvent)
}

func TestRelay_ClientInterruptAcceptedTurnIsBilledNotFailed(t *testing.T) {
	client := &passthroughTestFrameConn{readCh: make(chan passthroughTestFrame, 8)}
	upstream := &passthroughTestFrameConn{readCh: make(chan passthroughTestFrame, 8)}
	turns := make(chan RelayTurnResult, 4)
	done := make(chan RelayResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		result, _ := Relay(ctx, client, upstream, []byte(`{"type":"response.create","model":"MOCK_ONLY","input":[]}`), RelayOptions{
			OnTurnComplete: func(turn RelayTurnResult) { turns <- turn },
		})
		done <- result
	}()
	text := func(raw string) passthroughTestFrame {
		return passthroughTestFrame{msgType: coderws.MessageText, payload: []byte(raw)}
	}
	require.Eventually(t, func() bool { return len(upstream.Writes()) >= 1 }, 2*time.Second, time.Millisecond)
	upstream.readCh <- text(`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`)
	client.readCh <- text(`{"type":"response.interrupt","response_id":"resp_1","mode":"discard_partial_items"}`)
	require.Eventually(t, func() bool { return len(upstream.Writes()) >= 2 }, 2*time.Second, time.Millisecond)
	upstream.readCh <- text(`{"type":"response.interrupt.accepted","response_id":"resp_1","sequence_number":5}`)
	upstream.readCh <- text(`{"type":"response.output_item.interrupted","response_id":"resp_1"}`)
	upstream.readCh <- text(`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","service_tier":"default","incomplete_details":{"reason":"interrupted"},"usage":{"input_tokens":34,"output_tokens":156,"input_tokens_details":{"cached_tokens":4}}}}`)
	// A duplicate terminal must not bill twice.
	upstream.readCh <- text(`{"type":"response.incomplete","response":{"id":"resp_1","incomplete_details":{"reason":"interrupted"},"usage":{"input_tokens":34,"output_tokens":156}}}`)

	turn := <-turns
	require.Eventually(t, func() bool { return len(client.Writes()) >= 5 }, 2*time.Second, time.Millisecond)
	close(client.readCh)
	close(upstream.readCh)
	result := <-done
	require.Nil(t, turn.Failure)
	require.Nil(t, turn.Diagnostics)
	require.True(t, turn.ClientInterrupted)
	require.Equal(t, "resp_1", turn.RequestID)
	require.Equal(t, "response.incomplete", turn.TerminalEventType)
	require.Equal(t, Usage{InputTokens: 34, OutputTokens: 156, CacheReadInputTokens: 4, ServiceTier: "default"}, turn.Usage)
	require.Nil(t, turn.FirstTokenMs, "response.output_item.interrupted is not output")
	require.Len(t, turns, 0)
	require.Equal(t, Usage{InputTokens: 34, OutputTokens: 156, CacheReadInputTokens: 4, ServiceTier: "default"}, result.Usage)
	require.Empty(t, result.UnbilledResponseIDs)
}

func TestRelay_InterruptedWithoutAcceptedClientInterruptStaysFailure(t *testing.T) {
	incomplete := func(id, reason string) string {
		return `{"type":"response.incomplete","response":{"id":"` + id + `","incomplete_details":{"reason":"` + reason + `"},"usage":{"input_tokens":5,"output_tokens":3}}}`
	}
	for _, tc := range []struct {
		name   string
		frames []string
	}{
		{name: "no_ack", frames: []string{incomplete("resp_1", "interrupted")}},
		{name: "ack_failed", frames: []string{`{"type":"response.interrupt.failed","response_id":"resp_1","error":{"code":"interrupt_not_supported"}}`, incomplete("resp_1", "interrupted")}},
		{name: "accepted_then_failed", frames: []string{`{"type":"response.interrupt.accepted","response_id":"resp_1"}`, `{"type":"response.interrupt.failed","response_id":"resp_1"}`, incomplete("resp_1", "interrupted")}},
		{name: "other_reason", frames: []string{`{"type":"response.interrupt.accepted","response_id":"resp_1"}`, incomplete("resp_1", "max_output_tokens")}},
		{name: "ack_for_other_response", frames: []string{`{"type":"response.interrupt.accepted","response_id":"resp_other"}`, incomplete("resp_1", "interrupted")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &relayState{turns: newRelayTurnTracker(), clientDiagnostics: &relayClientDiagnostics{}}
			state.turns.register(RelayTurnMetadata{Turn: 1, SentAt: time.Now()})
			var turns []RelayTurnResult
			observe := func(raw string) {
				emitTurnComplete(func(turn RelayTurnResult) { turns = append(turns, turn) }, state, observeUpstreamMessage(state, []byte(raw), time.Now(), time.Now, nil))
			}
			observe(`{"type":"response.created","response":{"id":"resp_1"}}`)
			for _, frame := range tc.frames {
				observe(frame)
			}
			require.Len(t, turns, 1)
			require.NotNil(t, turns[0].Failure)
			require.False(t, turns[0].ClientInterrupted)
			require.Equal(t, Usage{}, turns[0].Usage)
			require.Equal(t, Usage{}, state.usage, "an unconfirmed interruption is not billed")
			require.Empty(t, state.acceptedInterrupts)
		})
	}

	// An acknowledgement for an unknown response neither binds a pending turn
	// nor survives.
	state := &relayState{turns: newRelayTurnTracker()}
	state.turns.register(RelayTurnMetadata{Turn: 1})
	observeUpstreamMessage(state, []byte(`{"type":"response.interrupt.accepted","response_id":"resp_unknown"}`), time.Now(), time.Now, nil)
	require.Empty(t, state.acceptedInterrupts)
	ids, pending := state.turns.outstanding()
	require.Empty(t, ids)
	require.Equal(t, 1, pending)
}

func TestRelayControl_ReadsCodexInterruptMode(t *testing.T) {
	diagnostics := &relayClientDiagnostics{}
	base := time.Unix(1_700_000_000, 0)
	diagnostics.recordControl([]byte(`{"type":"response.interrupt","response_id":"resp_1","mode":"discard_partial_items"}`), base)
	diagnostics.recordControl([]byte(`{"type":"response.interrupt","response_id":"resp_1","mode":"TEST_ONLY_PRIVATE_CONTENT"}`), base)
	out := &RelayFailureDiagnostics{}
	diagnostics.fill(out, base, base, "resp_1")
	require.True(t, *out.ClientControl[0].DiscardPartialItems)
	require.False(t, *out.ClientControl[1].DiscardPartialItems)
	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "TEST_ONLY_PRIVATE_CONTENT")
	usage, err := json.Marshal(RelayFailureDiagnostics{FirstTokenMsAfterFirstEvent: new(int), ReportedUsage: &RelayReportedUsage{CachedTokens: new(int64), ReasoningTokens: new(int64)}})
	require.NoError(t, err)
	for _, key := range []string{"first_output_ms_after_first_event", "cache_read_input_tokens", "reasoning_output"} {
		require.Contains(t, string(usage), key)
	}
}

func TestRelay_InterruptAcknowledgementEdgeCases(t *testing.T) {
	newState := func() (*relayState, *[]RelayTurnResult, func(string)) {
		state := &relayState{turns: newRelayTurnTracker()}
		state.turns.register(RelayTurnMetadata{Turn: 1})
		turns := &[]RelayTurnResult{}
		observe := func(raw string) {
			emitTurnComplete(func(turn RelayTurnResult) { *turns = append(*turns, turn) }, state, observeUpstreamMessage(state, []byte(raw), time.Now(), time.Now, nil))
		}
		return state, turns, observe
	}
	interrupted := `{"type":"response.incomplete","response":{"id":"resp_1","incomplete_details":{"reason":"interrupted"},"usage":{"input_tokens":5,"output_tokens":3}}}`

	// An acknowledgement before response.created is not trusted.
	state, turns, observe := newState()
	observe(`{"type":"response.interrupt.accepted","response_id":"resp_1"}`)
	observe(`{"type":"response.created","response":{"id":"resp_1"}}`)
	observe(interrupted)
	require.Len(t, *turns, 1)
	require.NotNil(t, (*turns)[0].Failure)
	require.Equal(t, Usage{}, state.usage)

	// A turn ended by an unattributed error cannot be billed later.
	state, turns, observe = newState()
	observe(`{"type":"response.created","response":{"id":"resp_1"}}`)
	observe(`{"type":"response.interrupt.accepted","response_id":"resp_1"}`)
	observe(`{"type":"error","error":{"type":"server_error","message":"failed"}}`)
	require.Empty(t, state.acceptedInterrupts)
	observe(interrupted)
	require.Len(t, *turns, 1, "only the error is reported")
	require.NotNil(t, (*turns)[0].Failure)
	require.Equal(t, Usage{}, state.usage)
}
