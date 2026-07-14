package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const anthropicTerminalRecoveryPrefix = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n"

const anthropicTerminalRecoveryClosedBlock = "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"

func newAnthropicTerminalRecoveryTestService(grace time.Duration) *GatewayService {
	return &GatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			StreamDataIntervalTimeout:           0,
			StreamKeepaliveInterval:             0,
			AnthropicStreamTerminalGraceSeconds: 10,
			MaxLineSize:                         defaultMaxLineSize,
		}},
		rateLimitService:                     &RateLimitService{},
		anthropicStreamTerminalGraceOverride: grace,
	}
}

func newAnthropicTerminalRecoveryContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil)
	return c, recorder
}

func anthropicTerminalRecoveryAccount(accountType string) *Account {
	return &Account{ID: 216, Platform: PlatformAnthropic, Type: accountType}
}

func anthropicFinalDelta(outputTokens int) string {
	return "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":" +
		strconv.Itoa(outputTokens) + "}}\n\n"
}

type blockOnStreamMarkerWriter struct {
	gin.ResponseWriter
	marker   string
	blocked  chan struct{}
	release  <-chan struct{}
	didBlock bool
}

func (w *blockOnStreamMarkerWriter) Write(data []byte) (int, error) {
	if !w.didBlock && strings.Contains(string(data), w.marker) {
		w.didBlock = true
		close(w.blocked)
		<-w.release
	}
	return w.ResponseWriter.Write(data)
}

func TestGatewayService_AnthropicTerminalRecovery_FinalDeltaThenOpenStream(t *testing.T) {
	svc := newAnthropicTerminalRecoveryTestService(25 * time.Millisecond)
	c, recorder := newAnthropicTerminalRecoveryContext(t)
	ctx := SetClaudeCodeClient(context.Background(), true)
	account := anthropicTerminalRecoveryAccount(AccountTypeSetupToken)

	reader, writer := io.Pipe()
	releaseWriter := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		_, _ = io.WriteString(writer, anthropicTerminalRecoveryPrefix)
		_, _ = io.WriteString(writer, anthropicTerminalRecoveryClosedBlock)
		_, _ = io.WriteString(writer, anthropicFinalDelta(7))
		_, _ = io.WriteString(writer, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
		<-releaseWriter
		_ = writer.Close()
	}()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Request-Id": []string{"req_terminal_recovery"}},
		Body:       reader,
	}
	type outcome struct {
		result *streamingResult
		err    error
	}
	resultCh := make(chan outcome, 1)
	go func() {
		result, err := svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "claude-opus-4-8", "claude-opus-4-8", false)
		resultCh <- outcome{result: result, err: err}
	}()

	var got outcome
	select {
	case got = <-resultCh:
	case <-time.After(500 * time.Millisecond):
		_ = reader.CloseWithError(context.DeadlineExceeded)
		close(releaseWriter)
		<-writerDone
		t.Fatal("terminal recovery did not finish before the Claude Code watchdog window")
	}
	close(releaseWriter)
	_ = reader.Close()
	<-writerDone

	require.NoError(t, got.err)
	require.NotNil(t, got.result)
	require.Equal(t, 10, got.result.usage.InputTokens)
	require.Equal(t, 7, got.result.usage.OutputTokens)
	require.False(t, got.result.clientDisconnect)
	body := recorder.Body.String()
	require.Contains(t, body, "message_delta")
	require.Equal(t, 1, strings.Count(body, "event: message_stop"))
	require.Equal(t, 1, strings.Count(body, `data: {"type":"message_stop"}`))
}

func TestGatewayService_AnthropicTerminalRecovery_RealMessageStopReturnsBeforeEOF(t *testing.T) {
	svc := newAnthropicTerminalRecoveryTestService(500 * time.Millisecond)
	c, recorder := newAnthropicTerminalRecoveryContext(t)
	ctx := SetClaudeCodeClient(context.Background(), true)
	account := anthropicTerminalRecoveryAccount(AccountTypeOAuth)

	reader, writer := io.Pipe()
	releaseWriter := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		_, _ = io.WriteString(writer, anthropicTerminalRecoveryPrefix)
		_, _ = io.WriteString(writer, anthropicTerminalRecoveryClosedBlock)
		_, _ = io.WriteString(writer, anthropicFinalDelta(7))
		_, _ = io.WriteString(writer, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		<-releaseWriter
		_ = writer.Close()
	}()

	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}
	started := time.Now()
	result, err := svc.handleStreamingResponse(ctx, resp, c, account, started, "claude-opus-4-8", "claude-opus-4-8", false)
	elapsed := time.Since(started)
	close(releaseWriter)
	_ = reader.Close()
	<-writerDone

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Less(t, elapsed, 250*time.Millisecond)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: message_stop"))
}

func TestGatewayService_AnthropicTerminalRecovery_QualifiedEOF(t *testing.T) {
	svc := newAnthropicTerminalRecoveryTestService(500 * time.Millisecond)
	c, recorder := newAnthropicTerminalRecoveryContext(t)
	ctx := SetClaudeCodeClient(context.Background(), true)
	account := anthropicTerminalRecoveryAccount(AccountTypeSetupToken)
	stream := anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock + anthropicFinalDelta(0)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}

	result, err := svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "claude-opus-4-8", "claude-opus-4-8", false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 0, result.usage.OutputTokens)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: message_stop"))
}

func TestGatewayService_AnthropicTerminalRecovery_RealMessageStopWithoutTrailingBlankLine(t *testing.T) {
	svc := newAnthropicTerminalRecoveryTestService(500 * time.Millisecond)
	c, recorder := newAnthropicTerminalRecoveryContext(t)
	ctx := SetClaudeCodeClient(context.Background(), true)
	account := anthropicTerminalRecoveryAccount(AccountTypeSetupToken)
	stream := anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock + anthropicFinalDelta(7) +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}"
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}

	result, err := svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "claude-opus-4-8", "claude-opus-4-8", false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 7, result.usage.OutputTokens)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: message_stop"))
}

func TestGatewayService_AnthropicTerminalRecovery_DisabledRemainsStrict(t *testing.T) {
	svc := newAnthropicTerminalRecoveryTestService(0)
	svc.cfg.Gateway.AnthropicStreamTerminalGraceSeconds = 0
	c, recorder := newAnthropicTerminalRecoveryContext(t)
	ctx := SetClaudeCodeClient(context.Background(), true)
	account := anthropicTerminalRecoveryAccount(AccountTypeSetupToken)
	stream := anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock + anthropicFinalDelta(7)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}

	result, err := svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "claude-opus-4-8", "claude-opus-4-8", false)

	require.Error(t, err)
	require.Contains(t, err.Error(), "missing terminal event")
	require.NotNil(t, result)
	require.NotContains(t, recorder.Body.String(), "event: message_stop")
}

func TestGatewayService_AnthropicTerminalRecovery_StreamErrorWinsAfterFinalDelta(t *testing.T) {
	svc := newAnthropicTerminalRecoveryTestService(500 * time.Millisecond)
	c, recorder := newAnthropicTerminalRecoveryContext(t)
	ctx := SetClaudeCodeClient(context.Background(), true)
	account := anthropicTerminalRecoveryAccount(AccountTypeOAuth)
	stream := anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock + anthropicFinalDelta(7) +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"stream boom\"}}\n\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Request-Id": []string{"req_terminal_error"}},
		Body:       io.NopCloser(strings.NewReader(stream)),
	}

	result, err := svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "claude-opus-4-8", "claude-opus-4-8", false)

	require.Error(t, err)
	require.Nil(t, result)
	var streamErr *upstreamStreamError
	require.ErrorAs(t, err, &streamErr)
	require.NotContains(t, recorder.Body.String(), "event: message_stop")
}

func TestGatewayService_AnthropicTerminalRecovery_QueuedCounterEventCancelsExpiredCandidate(t *testing.T) {
	const counterEvent = "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":null},\"usage\":{\"output_tokens\":8}}\n\n"

	// Block the downstream ping write after the candidate is armed. This gives
	// the scanner time to queue a complete counter-event while the grace timer
	// expires, exercising both possible select orderings at the boundary.
	const grace = 100 * time.Millisecond
	for iteration := 0; iteration < 20; iteration++ {
		svc := newAnthropicTerminalRecoveryTestService(grace)
		c, recorder := newAnthropicTerminalRecoveryContext(t)
		blocked := make(chan struct{})
		release := make(chan struct{})
		c.Writer = &blockOnStreamMarkerWriter{
			ResponseWriter: c.Writer,
			marker:         `data: {"type":"ping"}`,
			blocked:        blocked,
			release:        release,
		}
		ctx := SetClaudeCodeClient(context.Background(), true)
		account := anthropicTerminalRecoveryAccount(AccountTypeOAuth)

		reader, writer := io.Pipe()
		counterQueued := make(chan struct{})
		go func() {
			_, _ = io.WriteString(writer, anthropicTerminalRecoveryPrefix)
			_, _ = io.WriteString(writer, anthropicTerminalRecoveryClosedBlock)
			_, _ = io.WriteString(writer, anthropicFinalDelta(7))
			_, _ = io.WriteString(writer, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
			_, _ = io.WriteString(writer, counterEvent)
			close(counterQueued)
			_ = writer.Close()
		}()

		type outcome struct {
			result *streamingResult
			err    error
		}
		resultCh := make(chan outcome, 1)
		go func() {
			result, err := svc.handleStreamingResponse(ctx, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}, c, account, time.Now(), "claude-opus-4-8", "claude-opus-4-8", false)
			resultCh <- outcome{result: result, err: err}
		}()

		select {
		case <-blocked:
		case <-time.After(time.Second):
			t.Fatal("ping write did not block")
		}
		select {
		case <-counterQueued:
		case <-time.After(time.Second):
			t.Fatal("counter-event was not queued")
		}
		time.Sleep(grace + 10*time.Millisecond)
		close(release)
		got := <-resultCh

		require.Error(t, got.err, "iteration %d", iteration)
		require.Contains(t, got.err.Error(), "missing terminal event", "iteration %d", iteration)
		require.NotNil(t, got.result, "iteration %d", iteration)
		require.Equal(t, 8, got.result.usage.OutputTokens, "iteration %d", iteration)
		require.NotContains(t, recorder.Body.String(), "event: message_stop", "iteration %d", iteration)
	}
}

func TestGatewayService_AnthropicTerminalRecovery_QueuedNewCandidateGetsFreshGrace(t *testing.T) {
	const grace = 100 * time.Millisecond
	svc := newAnthropicTerminalRecoveryTestService(grace)
	c, recorder := newAnthropicTerminalRecoveryContext(t)
	blocked := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	c.Writer = &blockOnStreamMarkerWriter{
		ResponseWriter: c.Writer,
		marker:         `data: {"type":"ping"}`,
		blocked:        blocked,
		release:        release,
	}
	ctx := SetClaudeCodeClient(context.Background(), true)
	account := anthropicTerminalRecoveryAccount(AccountTypeSetupToken)

	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	secondCandidateQueued := make(chan struct{})
	closeUpstream := make(chan struct{})
	defer close(closeUpstream)
	go func() {
		_, _ = io.WriteString(writer, anthropicTerminalRecoveryPrefix)
		_, _ = io.WriteString(writer, anthropicTerminalRecoveryClosedBlock)
		_, _ = io.WriteString(writer, anthropicFinalDelta(7))
		_, _ = io.WriteString(writer, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
		_, _ = io.WriteString(writer, anthropicFinalDelta(8))
		close(secondCandidateQueued)
		<-closeUpstream
		_ = writer.Close()
	}()

	type outcome struct {
		result *streamingResult
		err    error
	}
	resultCh := make(chan outcome, 1)
	go func() {
		result, err := svc.handleStreamingResponse(ctx, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}, c, account, time.Now(), "claude-opus-4-8", "claude-opus-4-8", false)
		resultCh <- outcome{result: result, err: err}
	}()

	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("ping write did not block")
	}
	select {
	case <-secondCandidateQueued:
	case <-time.After(time.Second):
		t.Fatal("second terminal candidate was not queued")
	}
	time.Sleep(grace + 10*time.Millisecond)
	close(release)
	released = true

	select {
	case <-resultCh:
		t.Fatal("expired timer recovered immediately instead of respecting the newly armed grace timer")
	case <-time.After(grace / 2):
	}

	var got outcome
	select {
	case got = <-resultCh:
	case <-time.After(2 * grace):
		t.Fatal("fresh terminal grace did not recover the stream")
	}
	require.NoError(t, got.err)
	require.NotNil(t, got.result)
	require.Equal(t, 8, got.result.usage.OutputTokens)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: message_stop"))
}

func TestGatewayService_AnthropicTerminalRecovery_PendingErrorWinsOnUnexpectedEOF(t *testing.T) {
	svc := newAnthropicTerminalRecoveryTestService(500 * time.Millisecond)
	c, recorder := newAnthropicTerminalRecoveryContext(t)
	ctx := SetClaudeCodeClient(context.Background(), true)
	account := anthropicTerminalRecoveryAccount(AccountTypeSetupToken)
	payload := anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock + anthropicFinalDelta(7) +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"late stream boom\"}}"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Request-Id": []string{"req_terminal_pending_error"}},
		Body:       &streamReadCloser{payload: []byte(payload), err: io.ErrUnexpectedEOF},
	}

	result, err := svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "claude-opus-4-8", "claude-opus-4-8", false)

	require.Error(t, err)
	require.Nil(t, result)
	var streamErr *upstreamStreamError
	require.ErrorAs(t, err, &streamErr)
	require.Equal(t, "late stream boom", streamErr.message())
	require.NotContains(t, recorder.Body.String(), "event: message_stop")
}

func TestGatewayService_AnthropicTerminalRecovery_FinalUsageSurvivesClientWriteFailure(t *testing.T) {
	svc := newAnthropicTerminalRecoveryTestService(25 * time.Millisecond)
	c, _ := newAnthropicTerminalRecoveryContext(t)
	c.Writer = &failWriteResponseWriter{ResponseWriter: c.Writer}
	ctx := SetClaudeCodeClient(context.Background(), true)
	account := anthropicTerminalRecoveryAccount(AccountTypeOAuth)
	stream := anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock + anthropicFinalDelta(7)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}

	result, err := svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "claude-opus-4-8", "claude-opus-4-8", false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.clientDisconnect)
	require.Equal(t, 10, result.usage.InputTokens)
	require.Equal(t, 7, result.usage.OutputTokens)
}

func TestGatewayService_AnthropicTerminalRecovery_IneligibleStreamsRemainStrict(t *testing.T) {
	tests := []struct {
		name    string
		ctx     context.Context
		account *Account
		stream  string
	}{
		{
			name:    "non Claude Code client",
			ctx:     context.Background(),
			account: anthropicTerminalRecoveryAccount(AccountTypeSetupToken),
			stream:  anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock + anthropicFinalDelta(7),
		},
		{
			name:    "Anthropic API key account",
			ctx:     SetClaudeCodeClient(context.Background(), true),
			account: anthropicTerminalRecoveryAccount(AccountTypeAPIKey),
			stream:  anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock + anthropicFinalDelta(7),
		},
		{
			name:    "open content block",
			ctx:     SetClaudeCodeClient(context.Background(), true),
			account: anthropicTerminalRecoveryAccount(AccountTypeOAuth),
			stream:  anthropicTerminalRecoveryPrefix + anthropicFinalDelta(7),
		},
		{
			name:    "missing final usage",
			ctx:     SetClaudeCodeClient(context.Background(), true),
			account: anthropicTerminalRecoveryAccount(AccountTypeSetupToken),
			stream: anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
		},
		{
			name:    "null stop reason",
			ctx:     SetClaudeCodeClient(context.Background(), true),
			account: anthropicTerminalRecoveryAccount(AccountTypeOAuth),
			stream: anthropicTerminalRecoveryPrefix + anthropicTerminalRecoveryClosedBlock +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":null},\"usage\":{\"output_tokens\":7}}\n\n",
		},
		{
			name:    "content block start without index",
			ctx:     SetClaudeCodeClient(context.Background(), true),
			account: anthropicTerminalRecoveryAccount(AccountTypeSetupToken),
			stream: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n" +
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
				anthropicFinalDelta(7),
		},
		{
			name:    "content block delta without start",
			ctx:     SetClaudeCodeClient(context.Background(), true),
			account: anthropicTerminalRecoveryAccount(AccountTypeOAuth),
			stream: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n" +
				anthropicFinalDelta(7),
		},
		{
			name:    "repeated message start",
			ctx:     SetClaudeCodeClient(context.Background(), true),
			account: anthropicTerminalRecoveryAccount(AccountTypeSetupToken),
			stream: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n" +
				"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n" +
				anthropicFinalDelta(7),
		},
		{
			name:    "content block before message start",
			ctx:     SetClaudeCodeClient(context.Background(), true),
			account: anthropicTerminalRecoveryAccount(AccountTypeOAuth),
			stream: "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
				"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n" +
				anthropicFinalDelta(7),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newAnthropicTerminalRecoveryTestService(20 * time.Millisecond)
			c, recorder := newAnthropicTerminalRecoveryContext(t)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tt.stream))}

			result, err := svc.handleStreamingResponse(tt.ctx, resp, c, tt.account, time.Now(), "claude-opus-4-8", "claude-opus-4-8", false)

			require.Error(t, err)
			require.Contains(t, err.Error(), "missing terminal event")
			require.NotNil(t, result)
			require.NotContains(t, recorder.Body.String(), "event: message_stop")
		})
	}
}
