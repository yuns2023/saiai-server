package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestDefaultOpenAIWSDialerOptInTraceCapturesHandshakeAndFrames(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "openai-ws-trace.jsonl")
	t.Setenv("SAIAI_OPENAI_WS_TRACE", "1")
	t.Setenv("SAIAI_OPENAI_WS_TRACE_PATH", tracePath)

	serverErr := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionDisabled})
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		messageType, payload, readErr := conn.Read(readCtx)
		cancelRead()
		if readErr != nil {
			serverErr <- readErr
			return
		}
		writeCtx, cancelWrite := context.WithTimeout(r.Context(), 3*time.Second)
		writeErr := conn.Write(writeCtx, messageType, payload)
		cancelWrite()
		serverErr <- writeErr
	}))
	defer wsServer.Close()

	dialer, ok := newDefaultOpenAIWSClientDialer().(*coderOpenAIWSClientDialer)
	require.True(t, ok)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, _, err := dialer.Dial(
		ctx,
		"ws"+strings.TrimPrefix(wsServer.URL, "http"),
		http.Header{"Authorization": []string{"Bearer trace-test-token"}},
		"",
	)
	require.NoError(t, err)
	require.NotNil(t, conn)
	frameConn, ok := conn.(openaiwsv2.FrameConn)
	require.True(t, ok, "trace wrapper must retain frame relay support")

	requestPayload := []byte(`{"type":"response.create","model":"gpt-test"}`)
	require.NoError(t, frameConn.WriteFrame(ctx, coderws.MessageText, requestPayload))
	messageType, payload, err := frameConn.ReadFrame(ctx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, messageType)
	require.Equal(t, requestPayload, payload)
	require.NoError(t, conn.Close())
	require.NoError(t, <-serverErr)

	traceBytes, err := os.ReadFile(tracePath)
	require.NoError(t, err)
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(traceBytes)), "\n") {
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		events = append(events, event)
	}
	require.GreaterOrEqual(t, len(events), 4)
	require.Equal(t, "handshake", events[0]["event"])
	require.Equal(t, "frame", events[2]["event"])
	require.Equal(t, "to_openai", events[2]["direction"])
	require.Equal(t, "frame", events[3]["event"])
	require.Equal(t, "from_openai", events[3]["direction"])
}
