package openai_ws_v2

import (
	"context"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestRelay_Provider400ThenDisconnectDoesNotCompleteUsage(t *testing.T) {
	client := newPassthroughTestFrameConn(nil, false)
	request := []byte(` {"type":"response.create","model":"test-luna","input":[],"tools":[{"type":"image_gen"}]} `)
	frame := []byte(` {"type":"error","status":400,"error":{"type":"invalid_request_error","code":"model_not_supported","message":"The 'test-luna' model is not supported when using Codex with a ChatGPT account."}} `)
	upstream := newPassthroughTestFrameConn([]passthroughTestFrame{{msgType: coderws.MessageText, payload: frame}}, true)
	var turns []RelayTurnResult
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, _ := Relay(ctx, client, upstream, request, RelayOptions{OnTurnComplete: func(turn RelayTurnResult) { turns = append(turns, turn) }})
	require.Equal(t, request, upstream.Writes()[0].payload)
	require.Equal(t, frame, client.Writes()[0].payload)
	require.Equal(t, int64(1), result.ClientToUpstreamFrames, "no retry")
	require.Len(t, turns, 1)
	require.Equal(t, 1, turns[0].Turn)
	require.Equal(t, "test-luna", turns[0].RequestModel)
	require.Equal(t, 400, turns[0].Failure.Status)
	require.Equal(t, "invalid_request_error", turns[0].Failure.ErrorType)
	require.Empty(t, turns[0].RequestID, "do not synthesize response.completed or a response id")
	require.Equal(t, Usage{}, turns[0].Usage)
}

func TestRelay_FailedTurnThenSuccessfulTurnKeepsMetadataAndRawFrames(t *testing.T) {
	state := &relayState{turns: newRelayTurnTracker()}
	state.turns.register(RelayTurnMetadata{Turn: 1, RequestModel: "unsupported"})
	start := time.Now()
	var turns []RelayTurnResult
	observe := func(raw string) {
		emitTurnComplete(func(turn RelayTurnResult) { turns = append(turns, turn) }, state, observeUpstreamMessage(state, []byte(raw), start, time.Now, nil))
	}
	observe(`{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"unsupported"}}`)
	observe(`{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"duplicate"}}`)
	require.Len(t, turns, 1)
	state.turns.register(RelayTurnMetadata{Turn: 2, RequestModel: "available"})
	observe(`{"type":"response.completed","response":{"id":"resp_success","usage":{"input_tokens":11,"output_tokens":2}}}`)
	observe(`{"type":"response.failed","response":{"id":"resp_success","error":{"message":"late old response error"}}}`)
	require.Len(t, turns, 2, "a duplicate/old failure must not be logged twice")
	require.Equal(t, 2, turns[1].Turn)
	require.Equal(t, "available", turns[1].RequestModel)
	require.Nil(t, turns[1].Failure)
	require.Equal(t, 11, turns[1].Usage.InputTokens)
}

func TestRelay_FailureEnvelopesAndUncorrelatedFrames(t *testing.T) {
	for _, raw := range []string{
		`{"type":"error","error":{"type":"server_error","message":"failed without status"}}`,
		`{"type":"response.failed","response":{"id":"resp_failure","error":{"type":"server_error","message":"failed"}}}`,
		`{"type":"response.incomplete","response":{"id":"resp_failure","incomplete_details":{"reason":"max_output_tokens"}}}`,
	} {
		state := &relayState{turns: newRelayTurnTracker()}
		state.turns.register(RelayTurnMetadata{Turn: 4, RequestModel: "m"})
		var turns []RelayTurnResult
		emitTurnComplete(func(turn RelayTurnResult) { turns = append(turns, turn) }, state, observeUpstreamMessage(state, []byte(raw), time.Now(), time.Now, nil))
		require.Len(t, turns, 1)
		require.NotNil(t, turns[0].Failure)
		require.Zero(t, turns[0].Failure.Status, "missing status is not a provider HTTP 500")
	}
	for _, raw := range []string{`{"type":"error"`, `{"type":"codex.rate_limits"}`, `{"type":"error","error":null}`} {
		state := &relayState{turns: newRelayTurnTracker()}
		state.turns.register(RelayTurnMetadata{Turn: 1})
		observed := observeUpstreamMessage(state, []byte(raw), time.Now(), time.Now, nil)
		require.Nil(t, observed.failure)
	}
	tracker := newRelayTurnTracker()
	tracker.register(RelayTurnMetadata{Turn: 1})
	tracker.register(RelayTurnMetadata{Turn: 2})
	_, matched := tracker.failUnbound()
	require.False(t, matched, "do not guess between multiple outstanding turns")
}
