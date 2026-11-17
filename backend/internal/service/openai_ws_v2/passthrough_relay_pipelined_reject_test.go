package openai_ws_v2

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// Pipelined turns A and B are upstream; A settles, then the client's turn C
// is rejected by OnClientTurn (e.g. a per-turn billing check). Locks the
// semantics shared by every mid-session rejection: C never reaches upstream,
// A and B are each reported for billing exactly once, the relay returns (and
// the handler closes the client) only after B's terminal arrives, and frames
// after the rejection, including B's terminal, are not delivered.
func TestRelay_PipelinedTurnRejectedAfterEarlierTurnSettles(t *testing.T) {
	t.Parallel()
	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn(nil, false)
	rejection := errors.New("TEST_ONLY_REJECTED")
	var mu sync.Mutex
	var completed []string
	type relayOut struct {
		result RelayResult
		exit   *RelayExit
		at     time.Time
	}
	done := make(chan relayOut, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		result, exit := Relay(ctx, clientConn, upstreamConn, []byte(`{"type":"response.create","model":"gpt-5.4","input":"A"}`), RelayOptions{
			UpstreamDrainTimeout:        100 * time.Millisecond,
			OutstandingTurnDrainTimeout: 3 * time.Second,
			OnClientTurn: func(turn int, _ coderws.MessageType, _ []byte) error {
				if turn == 3 {
					return rejection
				}
				return nil
			},
			OnTurnComplete: func(turn RelayTurnResult) {
				mu.Lock()
				completed = append(completed, turn.RequestID)
				mu.Unlock()
			},
		})
		done <- relayOut{result: result, exit: exit, at: time.Now()}
	}()

	clientConn.readCh <- passthroughTestFrame{msgType: coderws.MessageText, payload: []byte(`{"type":"response.create","model":"gpt-5.4","input":"B"}`)}
	require.Eventually(t, func() bool { return len(upstreamConn.Writes()) == 2 }, 2*time.Second, 5*time.Millisecond)

	upstreamConn.readCh <- passthroughTestFrame{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_A","usage":{"input_tokens":1,"output_tokens":1}}}`)}
	require.Eventually(t, func() bool { return clientWroteResponse(clientConn, "resp_A") }, 2*time.Second, 5*time.Millisecond)

	clientConn.readCh <- passthroughTestFrame{msgType: coderws.MessageText, payload: []byte(`{"type":"response.create","model":"gpt-5.4","input":"C"}`)}
	select {
	case <-done:
		t.Fatal("relay must keep draining while B is outstanding")
	case <-time.After(300 * time.Millisecond):
	}
	completedBAt := time.Now()
	upstreamConn.readCh <- passthroughTestFrame{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_B","usage":{"input_tokens":2,"output_tokens":2}}}`)}

	out := <-done
	require.False(t, out.at.Before(completedBAt))
	require.NotNil(t, out.exit)
	require.Equal(t, relayStageClientTurnRejected, out.exit.Stage)
	require.ErrorIs(t, out.exit.Err, rejection)
	require.Len(t, upstreamConn.Writes(), 2, "the rejected turn must not reach upstream")
	mu.Lock()
	require.Equal(t, []string{"resp_A", "resp_B"}, completed, "each admitted turn is billed exactly once")
	mu.Unlock()
	require.Empty(t, out.result.UnbilledResponseIDs)
	require.Zero(t, out.result.UnbilledPendingTurns)
	require.True(t, clientWroteResponse(clientConn, "resp_A"))
	require.False(t, clientWroteResponse(clientConn, "resp_B"), "frames after a rejection are dropped")
}

func clientWroteResponse(conn *passthroughTestFrameConn, responseID string) bool {
	for _, frame := range conn.Writes() {
		if strings.Contains(string(frame.payload), responseID) {
			return true
		}
	}
	return false
}
