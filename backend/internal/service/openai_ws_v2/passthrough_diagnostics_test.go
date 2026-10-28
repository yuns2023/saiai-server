package openai_ws_v2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	require.True(t, control.Forwarded)
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
	diagnostics.recordControl([]byte(`{"type":"response.cancel"}`), base.Add(-time.Second), true)
	for index := 0; index < relayControlOutputMax+2; index++ {
		diagnostics.recordControl([]byte(fmt.Sprintf(`{"type":"TEST_ONLY_PRIVATE_CONTENT_%d","response_id":"resp_other"}`, index)), base.Add(time.Duration(index+1)*time.Millisecond), true)
	}
	diagnostics.recordControl([]byte(`{"type":"response.interrupt"}`), base.Add(time.Hour), false)
	out := &RelayFailureDiagnostics{}
	diagnostics.fill(out, base, base.Add(time.Second), "resp_current")
	require.Len(t, out.ClientControl, relayControlOutputMax)
	require.True(t, out.ClientControlTruncated)
	for _, control := range out.ClientControl {
		require.Equal(t, "other", control.Type, "unknown client types are not copied")
		require.Equal(t, "other", control.Target)
		require.Nil(t, control.DiscardPartialItems)
	}
	require.Equal(t, int64(1), out.ClientControl[0].MsAfterSent)

	// A ring that evicted frames newer than the turn start is incomplete.
	evicted := &relayClientDiagnostics{}
	for index := 0; index < relayControlRingSize+1; index++ {
		evicted.recordControl([]byte(`{"type":"response.cancel"}`), base.Add(time.Duration(index)*time.Millisecond), true)
	}
	out = &RelayFailureDiagnostics{}
	evicted.fill(out, base.Add(-time.Second), base.Add(time.Second), "resp_current")
	require.True(t, out.ClientControlTruncated)
	out = &RelayFailureDiagnostics{}
	evicted.fill(out, base.Add(10*time.Millisecond), base.Add(time.Second), "resp_current")
	require.False(t, out.ClientControlTruncated, "evicted frames predate this turn")
	require.Len(t, out.ClientControl, 7)
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
	} {
		diagnostics := &relayClientDiagnostics{}
		diagnostics.recordClientEnd(tc.err, base)
		out := &RelayFailureDiagnostics{}
		diagnostics.fill(out, time.Time{}, base, "")
		require.Equal(t, tc.kind, out.ClientEnd.Kind)
	}
}

func TestRelayFailureDiagnostics_StateLevelAttributionAndLabels(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	now := base
	state := &relayState{turns: newRelayTurnTracker(), startAt: base, now: func() time.Time { return now }, clientDiagnostics: &relayClientDiagnostics{}}
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

	state.turns.register(RelayTurnMetadata{Turn: 3, SentAt: now})
	now = now.Add(10 * time.Millisecond)
	observe(`{"type":"response.output_text.delta","response_id":"resp_3"}`)
	observe(`{"type":"response.Bearer TEST_ONLY_SECRET","response_id":"resp_3"}`)
	now = now.Add(20 * time.Millisecond)
	observe(`{"type":"response.incomplete","response":{"id":"resp_3","incomplete_details":{"reason":"interrupted"},"usage":{"input_tokens":1.5,"output_tokens":-1}}}`)
	require.Len(t, turns, 3)
	diagnostics := turns[2].Diagnostics
	require.Equal(t, "response_id", diagnostics.Attribution)
	require.Equal(t, 2, diagnostics.UpstreamEvents)
	require.Equal(t, "other", diagnostics.PreviousEventType)
	require.Equal(t, int64(20), *diagnostics.MsSincePreviousEvent)
	require.Equal(t, int64(20), *diagnostics.MsSinceFirstResponseEvent)
	require.Nil(t, diagnostics.MsSinceResponseCreated, "response.created was never observed")
	require.Nil(t, diagnostics.ReportedUsage, "non-integer or negative usage is not reported")
	require.Equal(t, 0, *diagnostics.FirstTokenMsAfterFirstEvent)
}
