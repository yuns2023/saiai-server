package openai_ws_v2

import (
	"context"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// Each turn reports the client's own message size, measured before
// TransformClientFrame (which may grow or shrink the sent bytes). The first
// frame never measures itself: it reports FirstClientMessageBytes. A terminal
// event that matches no registered turn reports nil, never a neighbour's size.
func TestRelay_RequestBodyBytesPerTurnBeforeTransform(t *testing.T) {
	t.Parallel()

	secondRaw := []byte(`{"type":"response.create","model":"second-model","input":"TEST_ONLY"}`)
	clientConn := newPassthroughTestFrameConn([]passthroughTestFrame{{msgType: coderws.MessageText, payload: secondRaw}}, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_turn_1","usage":{"input_tokens":2,"output_tokens":1}}}`)},
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_turn_2","usage":{"input_tokens":3,"output_tokens":4}}}`)},
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_unmatched","usage":{"input_tokens":5,"output_tokens":6}}}`)},
	}, true)

	// The first frame is a rebuilt payload; the client's original was 999 bytes.
	firstPayload := []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`)
	firstOriginal := int64(999)
	padding := make([]byte, 4096)
	for i := range padding {
		padding[i] = 'x'
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var turns []RelayTurnResult
	_, relayExit := Relay(ctx, clientConn, &delayedReadFrameConn{base: upstreamConn, firstDelay: 20 * time.Millisecond}, firstPayload, RelayOptions{
		FirstClientMessageBytes: &firstOriginal,
		TransformClientFrame: func(_ int, _ coderws.MessageType, payload []byte) ([]byte, error) {
			// Grow every sent frame so a post-transform measurement is visible.
			return append(append([]byte(nil), payload[:len(payload)-1]...), []byte(`,"pad":"`+string(padding)+`"}`)...), nil
		},
		TurnMetadata: func(turn int, payload []byte) RelayTurnMetadata {
			// The callback only sees transformed bytes; its size is ignored.
			wrong := int64(len(payload))
			return RelayTurnMetadata{RequestPayloadHash: "hash", RequestBodyBytes: &wrong}
		},
		OnTurnComplete: func(turn RelayTurnResult) {
			turns = append(turns, turn)
		},
	})
	require.Nil(t, relayExit)
	require.Len(t, turns, 3)

	require.Equal(t, 1, turns[0].Turn)
	require.NotNil(t, turns[0].RequestBodyBytes)
	require.Equal(t, firstOriginal, *turns[0].RequestBodyBytes)

	require.Equal(t, 2, turns[1].Turn)
	require.NotNil(t, turns[1].RequestBodyBytes)
	require.Equal(t, int64(len(secondRaw)), *turns[1].RequestBodyBytes)
	require.Equal(t, "hash", turns[1].RequestPayloadHash, "hash semantics unchanged")

	require.Equal(t, "resp_unmatched", turns[2].RequestID)
	require.Zero(t, turns[2].Turn)
	require.Nil(t, turns[2].RequestBodyBytes)

	writes := upstreamConn.Writes()
	require.Len(t, writes, 2)
	require.Greater(t, len(writes[1].payload), len(secondRaw)+4000, "upstream got the transformed frame")
}

func TestRelay_RequestBodyBytesUnknownFirstTurnStaysNil(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":2,"output_tokens":1}}}`)},
	}, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var turns []RelayTurnResult
	_, relayExit := Relay(ctx, clientConn, upstreamConn, []byte(`{"type":"response.create","model":"m","input":[]}`), RelayOptions{
		OnTurnComplete: func(turn RelayTurnResult) { turns = append(turns, turn) },
	})
	require.Nil(t, relayExit)
	require.Len(t, turns, 1)
	require.Nil(t, turns[0].RequestBodyBytes, "the relay never measures the frame it was handed")
}

func runRelayForBodyBytes(t *testing.T, first []byte, firstBytes *int64, clientFrames [][]byte, upstreamEvents []string) []RelayTurnResult {
	t.Helper()
	frames := make([]passthroughTestFrame, 0, len(clientFrames))
	for _, frame := range clientFrames {
		frames = append(frames, passthroughTestFrame{msgType: coderws.MessageText, payload: frame})
	}
	clientConn := newPassthroughTestFrameConn(frames, false)
	events := make([]passthroughTestFrame, 0, len(upstreamEvents))
	for _, event := range upstreamEvents {
		events = append(events, passthroughTestFrame{msgType: coderws.MessageText, payload: []byte(event)})
	}
	upstreamConn := newPassthroughTestFrameConn(events, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var turns []RelayTurnResult
	_, relayExit := Relay(ctx, clientConn, &delayedReadFrameConn{base: upstreamConn, firstDelay: 30 * time.Millisecond}, first, RelayOptions{
		FirstClientMessageBytes: firstBytes,
		OnTurnComplete:          func(turn RelayTurnResult) { turns = append(turns, turn) },
	})
	require.Nil(t, relayExit)
	return turns
}

// A non-turn first frame never consumes the caller's first-turn size: the
// first response.create read later reports its own raw size.
func TestRelay_RequestBodyBytesControlFirstFrame(t *testing.T) {
	t.Parallel()
	callerSize := int64(999)
	create := []byte(`{"type":"response.create","model":"m","input":"TEST_ONLY_CREATE_AFTER_CONTROL"}`)
	turns := runRelayForBodyBytes(t, []byte(`{"type":"session.update","session":{}}`), &callerSize, [][]byte{create}, []string{
		`{"type":"response.completed","response":{"id":"resp_after_control","usage":{"input_tokens":1,"output_tokens":1}}}`,
	})
	require.Len(t, turns, 1)
	require.Equal(t, 1, turns[0].Turn)
	require.NotNil(t, turns[0].RequestBodyBytes)
	require.Equal(t, int64(len(create)), *turns[0].RequestBodyBytes)
}

// Two overlapping turns of different sizes, both bound before completion,
// complete in reverse order: each keeps its own size.
func TestRelay_RequestBodyBytesOverlappingTurnsReverseCompletion(t *testing.T) {
	t.Parallel()
	firstSize := int64(111)
	second := []byte(`{"type":"response.create","model":"m","input":"TEST_ONLY_SECOND_TURN_WITH_A_DIFFERENT_SIZE"}`)
	turns := runRelayForBodyBytes(t, []byte(`{"type":"response.create","model":"m","input":[]}`), &firstSize, [][]byte{second}, []string{
		`{"type":"response.created","response":{"id":"resp_overlap_1"}}`,
		`{"type":"response.created","response":{"id":"resp_overlap_2"}}`,
		`{"type":"response.completed","response":{"id":"resp_overlap_2","usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"response.completed","response":{"id":"resp_overlap_1","usage":{"input_tokens":1,"output_tokens":1}}}`,
	})
	require.Len(t, turns, 2)
	require.Equal(t, "resp_overlap_2", turns[0].RequestID)
	require.Equal(t, 2, turns[0].Turn)
	require.Equal(t, int64(len(second)), *turns[0].RequestBodyBytes)
	require.Equal(t, "resp_overlap_1", turns[1].RequestID)
	require.Equal(t, 1, turns[1].Turn)
	require.Equal(t, firstSize, *turns[1].RequestBodyBytes)
}

// A duplicate terminal of turn 1 while turn 2 is outstanding is unmatched and
// records no size; it never borrows turn 2's.
func TestRelay_RequestBodyBytesDuplicateTerminalDoesNotBorrow(t *testing.T) {
	t.Parallel()
	firstSize := int64(222)
	second := []byte(`{"type":"response.create","model":"m","input":"TEST_ONLY_SECOND_TURN_DUPLICATE_CASE"}`)
	turns := runRelayForBodyBytes(t, []byte(`{"type":"response.create","model":"m","input":[]}`), &firstSize, [][]byte{second}, []string{
		`{"type":"response.created","response":{"id":"resp_dup_1"}}`,
		`{"type":"response.created","response":{"id":"resp_dup_2"}}`,
		`{"type":"response.completed","response":{"id":"resp_dup_1","usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"response.completed","response":{"id":"resp_dup_1","usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"response.completed","response":{"id":"resp_dup_2","usage":{"input_tokens":1,"output_tokens":1}}}`,
	})
	require.Len(t, turns, 3)
	require.Equal(t, firstSize, *turns[0].RequestBodyBytes)
	require.Equal(t, "resp_dup_1", turns[1].RequestID)
	require.Zero(t, turns[1].Turn)
	require.Nil(t, turns[1].RequestBodyBytes)
	require.Equal(t, "resp_dup_2", turns[2].RequestID)
	require.Equal(t, int64(len(second)), *turns[2].RequestBodyBytes)
}
