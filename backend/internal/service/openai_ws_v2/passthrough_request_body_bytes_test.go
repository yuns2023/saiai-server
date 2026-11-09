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
	result, relayExit := Relay(ctx, clientConn, &delayedReadFrameConn{base: upstreamConn, firstDelay: 20 * time.Millisecond}, firstPayload, RelayOptions{
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
	// The aggregate result follows the last completed (matched) turn.
	require.NotNil(t, result.RequestBodyBytes)
	require.Equal(t, int64(len(secondRaw)), *result.RequestBodyBytes)
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
