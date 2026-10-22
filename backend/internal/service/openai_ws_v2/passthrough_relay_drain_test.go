package openai_ws_v2

import (
	"context"
	"errors"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// A client that disconnects mid-turn must not avoid billing: the relay keeps
// draining past the short UpstreamDrainTimeout while the turn is outstanding.
func TestRelay_ClientDisconnectDrainsOutstandingTurnPastShortWindow(t *testing.T) {
	t.Parallel()
	clientConn := newPassthroughTestFrameConn(nil, true)
	upstreamConn := &delayedReadFrameConn{
		base: newPassthroughTestFrameConn([]passthroughTestFrame{{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_long","usage":{"input_tokens":11,"output_tokens":900}}}`),
		}}, false),
		firstDelay: 1500 * time.Millisecond,
	}
	completed := make(chan RelayTurnResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	started := time.Now()
	result, relayExit := Relay(ctx, clientConn, upstreamConn, []byte(`{"type":"response.create","model":"gpt-5.4"}`), RelayOptions{
		UpstreamDrainTimeout:        200 * time.Millisecond,
		OutstandingTurnDrainTimeout: 3 * time.Second,
		OnTurnComplete:              func(turn RelayTurnResult) { completed <- turn },
	})
	require.GreaterOrEqual(t, time.Since(started), 1400*time.Millisecond)
	require.NotNil(t, relayExit)
	require.Equal(t, "client_disconnected", relayExit.Stage)
	require.Equal(t, 900, result.Usage.OutputTokens)
	require.Empty(t, result.UnbilledResponseIDs)
	require.Zero(t, result.UnbilledPendingTurns)
	select {
	case turn := <-completed:
		require.Equal(t, "resp_long", turn.RequestID)
		require.Equal(t, 900, turn.Usage.OutputTokens)
	default:
		t.Fatal("outstanding turn was not reported for billing")
	}
}

// The drain is bounded; a turn that never finishes is reported as unbilled.
func TestRelay_ClientDisconnectDrainCapReportsUnbilledTurn(t *testing.T) {
	t.Parallel()
	clientConn := newPassthroughTestFrameConn(nil, true)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{{
		msgType: coderws.MessageText,
		payload: []byte(`{"type":"response.created","response":{"id":"resp_never_done"}}`),
	}}, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	started := time.Now()
	result, relayExit := Relay(ctx, clientConn, upstreamConn, []byte(`{"type":"response.create","model":"gpt-5.4"}`), RelayOptions{
		UpstreamDrainTimeout:        100 * time.Millisecond,
		OutstandingTurnDrainTimeout: 400 * time.Millisecond,
	})
	elapsed := time.Since(started)
	require.GreaterOrEqual(t, elapsed, 350*time.Millisecond)
	require.Less(t, elapsed, 3*time.Second)
	require.NotNil(t, relayExit)
	require.Equal(t, []string{"resp_never_done"}, result.UnbilledResponseIDs)
	require.Zero(t, result.UnbilledPendingTurns)
}

// A rejected frame while a long turn is running: the frame is not forwarded,
// and the turn is still billed even though it completes after more than the
// short drain window.
func TestRelay_RejectedFrameDrainsLongOutstandingTurn(t *testing.T) {
	t.Parallel()
	clientConn := newPassthroughTestFrameConn([]passthroughTestFrame{{
		msgType: coderws.MessageText,
		payload: []byte(`{"type":"response.create","type":"response.cancel"}`),
	}}, false)
	upstreamConn := &delayedReadFrameConn{
		base: newPassthroughTestFrameConn([]passthroughTestFrame{{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_slow","usage":{"input_tokens":3,"output_tokens":500}}}`),
		}}, false),
		firstDelay: 1500 * time.Millisecond,
	}
	rejection := errors.New("rejected")
	completed := make(chan RelayTurnResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	validated := 0
	result, relayExit := Relay(ctx, clientConn, upstreamConn, []byte(`{"type":"response.create","model":"gpt-5.4"}`), RelayOptions{
		UpstreamDrainTimeout:        200 * time.Millisecond,
		OutstandingTurnDrainTimeout: 3 * time.Second,
		ValidateClientFrame: func(_ coderws.MessageType, _ []byte) error {
			validated++
			if validated > 1 {
				return rejection
			}
			return nil
		},
		OnTurnComplete: func(turn RelayTurnResult) { completed <- turn },
	})
	require.NotNil(t, relayExit)
	require.Equal(t, relayStageClientTurnRejected, relayExit.Stage)
	require.ErrorIs(t, relayExit.Err, rejection)
	require.Equal(t, 500, result.Usage.OutputTokens)
	require.Len(t, upstreamConn.base.(*passthroughTestFrameConn).Writes(), 1, "the rejected frame must not be forwarded")
	select {
	case turn := <-completed:
		require.Equal(t, 500, turn.Usage.OutputTokens)
	default:
		t.Fatal("outstanding turn was not reported for billing")
	}
}
