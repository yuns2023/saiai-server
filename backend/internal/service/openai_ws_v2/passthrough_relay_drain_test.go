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
	require.Nil(t, relayExit, "a fully drained and billed turn is not a relay failure")
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
	upstreamBase := newPassthroughTestFrameConn([]passthroughTestFrame{{
		msgType: coderws.MessageText,
		payload: []byte(`{"type":"response.completed","response":{"id":"resp_slow","usage":{"input_tokens":3,"output_tokens":500}}}`),
	}}, false)
	upstreamConn := &delayedReadFrameConn{base: upstreamBase, firstDelay: 1500 * time.Millisecond}
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
	require.Len(t, upstreamBase.Writes(), 1, "the rejected frame must not be forwarded")
	select {
	case turn := <-completed:
		require.Equal(t, 500, turn.Usage.OutputTokens)
	default:
		t.Fatal("outstanding turn was not reported for billing")
	}
}

// writeFailingClientConn fails every write (a client that is gone without a
// clean close) while its reader blocks until the relay ends.
type writeFailingClientConn struct{}

func (writeFailingClientConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	<-ctx.Done()
	return coderws.MessageText, nil, ctx.Err()
}

func (writeFailingClientConn) WriteFrame(context.Context, coderws.MessageType, []byte) error {
	return errors.New("write: broken pipe")
}

func (writeFailingClientConn) Close() error { return nil }

// scriptedUpstreamConn returns frames with a per-frame delay.
type scriptedUpstreamConn struct {
	*passthroughTestFrameConn
	delays []time.Duration
	reads  int
}

func (c *scriptedUpstreamConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if c.reads < len(c.delays) && c.delays[c.reads] > 0 {
		timer := time.NewTimer(c.delays[c.reads])
		select {
		case <-ctx.Done():
			timer.Stop()
			return coderws.MessageText, nil, ctx.Err()
		case <-timer.C:
		}
	}
	c.reads++
	return c.passthroughTestFrameConn.ReadFrame(ctx)
}

// A client write failure mid-turn switches the relay to discard mode; the
// turn is still drained and billed even when it finishes long afterwards.
func TestRelay_ClientWriteFailureDrainsOutstandingTurn(t *testing.T) {
	t.Parallel()
	upstreamConn := &scriptedUpstreamConn{
		passthroughTestFrameConn: newPassthroughTestFrameConn([]passthroughTestFrame{
			{msgType: coderws.MessageText, payload: []byte(`{"type":"response.output_text.delta","response_id":"resp_wf","delta":"x"}`)},
			{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_wf","usage":{"input_tokens":4,"output_tokens":777}}}`)},
		}, false),
		delays: []time.Duration{0, 1500 * time.Millisecond},
	}
	completed := make(chan RelayTurnResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, relayExit := Relay(ctx, writeFailingClientConn{}, upstreamConn, []byte(`{"type":"response.create","model":"gpt-5.4"}`), RelayOptions{
		UpstreamDrainTimeout:        200 * time.Millisecond,
		OutstandingTurnDrainTimeout: 3 * time.Second,
		OnTurnComplete:              func(turn RelayTurnResult) { completed <- turn },
	})
	require.NotNil(t, relayExit)
	require.Equal(t, "write_client", relayExit.Stage)
	require.Equal(t, 777, result.Usage.OutputTokens)
	require.Empty(t, result.UnbilledResponseIDs)
	select {
	case turn := <-completed:
		require.Equal(t, "resp_wf", turn.RequestID)
		require.Equal(t, 777, turn.Usage.OutputTokens)
	default:
		t.Fatal("turn was not billed after the client write failure")
	}
}

// upstreamCloseFrameConn returns a provider close frame after a delay.
type upstreamCloseFrameConn struct {
	*passthroughTestFrameConn
	delay time.Duration
	code  coderws.StatusCode
}

func (c *upstreamCloseFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	select {
	case <-ctx.Done():
		return coderws.MessageText, nil, ctx.Err()
	case <-time.After(c.delay):
		return coderws.MessageText, nil, coderws.CloseError{Code: c.code}
	}
}

// The client leaves mid-turn and the provider then closes with 1012 during the
// drain: the session ends on the provider close, not on the client leaving.
func TestRelay_ClientDisconnectThenUpstreamCloseDuringDrain(t *testing.T) {
	t.Parallel()
	clientConn := newPassthroughTestFrameConn(nil, true)
	upstreamConn := &upstreamCloseFrameConn{
		passthroughTestFrameConn: newPassthroughTestFrameConn(nil, false),
		delay:                    100 * time.Millisecond,
		code:                     coderws.StatusServiceRestart,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, relayExit := Relay(ctx, clientConn, upstreamConn, []byte(`{"type":"response.create","model":"gpt-5.4"}`), RelayOptions{
		OutstandingTurnDrainTimeout: 2 * time.Second,
	})
	require.NotNil(t, relayExit)
	require.Equal(t, "read_upstream", relayExit.Stage)
	require.Equal(t, coderws.StatusServiceRestart, coderws.CloseStatus(relayExit.Err))
	require.Equal(t, 1, result.UnbilledPendingTurns)
}
