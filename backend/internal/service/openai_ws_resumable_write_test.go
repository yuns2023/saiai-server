//go:build unit

package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

type openAIWSDownstreamWriteGate struct {
	enabled atomic.Bool
	started chan struct{}
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
	close   sync.Once
}

type openAIWSGatedDownstreamConn struct {
	net.Conn
	gate *openAIWSDownstreamWriteGate
}

func (connection *openAIWSGatedDownstreamConn) Write(payload []byte) (int, error) {
	if connection.gate.enabled.Load() {
		connection.gate.once.Do(func() {
			close(connection.gate.started)
			select {
			case <-connection.gate.release:
			case <-connection.gate.closed:
			}
		})
	}
	return connection.Conn.Write(payload)
}

func (connection *openAIWSGatedDownstreamConn) Close() error {
	connection.gate.close.Do(func() { close(connection.gate.closed) })
	return connection.Conn.Close()
}

type openAIWSGatedDownstreamListener struct {
	net.Listener
	gate *openAIWSDownstreamWriteGate
}

func (listener *openAIWSGatedDownstreamListener) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &openAIWSGatedDownstreamConn{Conn: connection, gate: listener.gate}, nil
}

func TestOpenAIWSResumableWriteSurvivesRelayCancellation(testContext *testing.T) {
	gate := &openAIWSDownstreamWriteGate{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	relayCtx, cancelRelay := context.WithCancel(ctx)
	defer cancelRelay()
	written := make(chan error, 1)
	serverErrors := make(chan error, 1)
	finished := make(chan struct{})
	defer close(finished)
	payload := []byte(`{"type":"codex.rate_limits","rate_limits":{}}`)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := coderws.Accept(writer, request, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer func() { _ = connection.CloseNow() }()
		resumable := newOpenAIWSResumableClientFrameConn(request.Context(), connection)
		defer resumable.Stop()
		gate.enabled.Store(true)
		writeCtx, cancelWrite := context.WithTimeout(relayCtx, 2*time.Second)
		written <- resumable.WriteFrame(writeCtx, coderws.MessageText, payload)
		cancelWrite()
		cancelledCtx, cancelCancelled := context.WithCancel(request.Context())
		cancelCancelled()
		if err := resumable.WriteFrame(cancelledCtx, coderws.MessageText, payload); err != context.Canceled {
			serverErrors <- err
			return
		}
		serverErrors <- resumable.WriteFrame(request.Context(), coderws.MessageText, []byte(`{"type":"response.completed"}`))
		<-finished
	}))
	server.Listener = &openAIWSGatedDownstreamListener{Listener: server.Listener, gate: gate}
	server.Start()
	defer server.Close()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(testContext, err)
	defer func() { _ = client.CloseNow() }()
	select {
	case <-gate.started:
	case <-ctx.Done():
		testContext.Fatal("downstream write never reached the gate")
	}
	cancelRelay()
	select {
	case <-gate.closed:
		testContext.Fatal("upstream cancellation closed the downstream socket")
	case err := <-written:
		testContext.Fatalf("blocked downstream write ended before release: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(gate.release)
	_, observed, err := client.Read(ctx)
	require.NoError(testContext, err)
	require.Equal(testContext, payload, observed)
	require.NoError(testContext, <-written)
	_, observed, err = client.Read(ctx)
	require.NoError(testContext, err)
	require.Equal(testContext, `{"type":"response.completed"}`, string(observed))
	require.NoError(testContext, <-serverErrors)
}
