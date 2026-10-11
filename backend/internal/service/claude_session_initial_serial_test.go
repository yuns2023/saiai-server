package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionInitialSerialReleaseRequiresMessageStart(t *testing.T) {
	svc := newAnthropicTerminalRecoveryTestService(0)
	c, _ := newAnthropicTerminalRecoveryContext(t)
	reader, writer := io.Pipe()
	messageStartAllowed := make(chan struct{})
	responseStarted := make(chan struct{})
	finish := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := svc.handleStreamingResponse(context.Background(), &http.Response{
			StatusCode: 200, Header: http.Header{}, Body: reader,
		}, c, anthropicTerminalRecoveryAccount(AccountTypeSetupToken), time.Now(), "claude-opus-4-6", "claude-opus-4-6", false,
			func() { close(responseStarted) })
		done <- err
	}()
	go func() {
		defer writer.Close()
		_, _ = io.WriteString(writer, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
		_, _ = io.WriteString(writer, "event: message_start\ndata: not-json\n\n")
		_, _ = io.WriteString(writer, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
		<-messageStartAllowed
		_, _ = io.WriteString(writer, anthropicTerminalRecoveryPrefix)
		<-finish
		_, _ = io.WriteString(writer, anthropicTerminalRecoveryClosedBlock+anthropicFinalDelta(7)+"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}()
	t.Cleanup(func() {
		reader.Close()
		writer.Close()
	})
	select {
	case <-responseStarted:
		t.Fatal("headers, ping and malformed events must not release the startup lease")
	case <-time.After(25 * time.Millisecond):
	}
	close(messageStartAllowed)
	select {
	case <-responseStarted:
	case <-time.After(time.Second):
		t.Fatal("message_start must release before response completion")
	}
	select {
	case <-done:
		t.Fatal("response should still be streaming after startup release")
	default:
	}
	close(finish)
	require.NoError(t, <-done)
}

func TestSessionInitialSerialMessageStartNotifiesOnce(t *testing.T) {
	svc := newAnthropicTerminalRecoveryTestService(0)
	c, _ := newAnthropicTerminalRecoveryContext(t)
	count := 0
	_, err := svc.handleStreamingResponse(context.Background(), &http.Response{
		StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(
			anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock + anthropicFinalDelta(7) +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")),
	}, c, anthropicTerminalRecoveryAccount(AccountTypeSetupToken), time.Now(), "claude-opus-4-6", "claude-opus-4-6", false,
		func() { count++ })
	require.NoError(t, err)
	require.Equal(t, 1, count)
}
