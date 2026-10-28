package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSPassthroughFailureMetadataAndRawFrames(t *testing.T) {
	for _, tc := range []struct {
		name       string
		frame      string
		status     int
		code       string
		reason     string
		responseID string
	}{
		{
			name: "model_refusal", status: 400, code: "[REDACTED]",
			frame: ` {"type":"error","status":400,"error":{"type":"invalid_request_error","code":"TEST_ONLY_TOKEN","message":"The 'test-luna' model is not supported when using Codex with a ChatGPT account. Synthetic echoed credential TEST_ONLY_TOKEN"}} `,
		},
		{
			name: "output_limit", reason: "max_output_tokens", responseID: "resp_incomplete",
			frame: ` {"type":"response.incomplete","response":{"id":"resp_incomplete","incomplete_details":{"reason":"max_output_tokens","extra":"TEST_ONLY_PRIVATE_CONTENT"}}} `,
		},
		{
			name: "content_filter", reason: "content_filter", responseID: "resp_incomplete",
			frame: ` {"type":"response.incomplete","response":{"id":"resp_incomplete","incomplete_details":{"reason":"content_filter"}}} `,
		},
		{
			name: "steered", reason: "steered", responseID: "resp_incomplete",
			frame: ` {"type":"response.incomplete","response":{"id":"resp_incomplete","incomplete_details":{"reason":"steered"}}} `,
		},
		{
			name: "missing_reason", responseID: "resp_incomplete",
			frame: ` {"type":"response.incomplete","response":{"id":"resp_incomplete"}} `,
		},
		{
			name: "echoed_credential", reason: "[REDACTED]", responseID: "resp_incomplete",
			frame: ` {"type":"response.incomplete","response":{"id":"resp_incomplete","incomplete_details":{"reason":"TEST_ONLY_TOKEN"}}} `,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Security.URLAllowlist.Enabled = false
			cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
			request := []byte(` {"type":"response.create","model":"test-luna","reasoning":{"effort":"low"},"input":[],"tools":[{"type":"image_gen"}]} `)
			frame := []byte(tc.frame)
			upstream := &openAIWSCaptureConn{events: [][]byte{frame}}
			dialer := &openAIWSCaptureDialer{conn: upstream, handshake: http.Header{"X-Request-Id": []string{"TEST_ONLY_UPSTREAM_TRACE"}, "Cf-Ray": []string{"8f1a2b3c4d5e6f70-SJC"}}}
			svc := &OpenAIGatewayService{cfg: cfg, openaiWSPassthroughDialer: dialer, cache: &stubGatewayCache{}}
			account := &Account{ID: 452, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			done := make(chan struct{})
			var successes int
			var failures []*OpenAIWSUpstreamFailure
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r
				ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
				defer cancel()
				messageType, first, err := conn.Read(ctx)
				if err != nil {
					return
				}
				_ = svc.proxyResponsesWebSocketV2Passthrough(ctx, c, conn, account, "TEST_ONLY_TOKEN", messageType, first, &OpenAIWSIngressHooks{
					AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
						if result != nil && err == nil {
							successes++
						}
					},
					OnUpstreamError: func(failure *OpenAIWSUpstreamFailure) { failures = append(failures, failure) },
				}, OpenAIWSProtocolDecision{}, nil)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer func() { _ = conn.CloseNow() }()
			require.NoError(t, conn.Write(ctx, coderws.MessageText, request))
			_, got, err := conn.Read(ctx)
			require.NoError(t, err)
			require.Equal(t, frame, got)
			_ = conn.CloseNow()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("mock relay did not finish")
			}
			require.Zero(t, successes, "error or graceful EOF must not trigger a successful usage callback")
			require.Len(t, failures, 1)
			require.Equal(t, tc.status, failures[0].Status)
			require.Equal(t, "test-luna", failures[0].Model)
			require.Equal(t, 1, failures[0].Turn)
			require.Equal(t, int64(452), failures[0].AccountID)
			require.Equal(t, HashUsageRequestPayload(request), failures[0].RequestPayloadHash)
			require.Equal(t, "TEST_ONLY_UPSTREAM_TRACE", failures[0].UpstreamRequestID)
			require.Equal(t, "8f1a2b3c4d5e6f70-SJC", failures[0].UpstreamCFRay)
			require.NotNil(t, failures[0].RelayDiagnostics)
			require.NotNil(t, failures[0].RelayDiagnostics.MsSinceSent)
			require.NotContains(t, failures[0].Message, "TEST_ONLY_TOKEN")
			require.Equal(t, tc.code, failures[0].Code)
			require.Equal(t, tc.reason, failures[0].IncompleteReason)
			require.Equal(t, tc.responseID, failures[0].ResponseID)
			require.NotContains(t, failures[0].Message, "TEST_ONLY_PRIVATE_CONTENT")
			if tc.responseID != "" {
				wantMessage := "Upstream WebSocket response.incomplete"
				if tc.reason != "" {
					wantMessage += " (" + tc.reason + ")"
				}
				require.Equal(t, wantMessage, failures[0].Message)
			}
			require.Equal(t, request, upstream.rawWrites[0])
			require.Equal(t, 1, dialer.DialCount(), "do not retry or switch accounts for a generic model refusal")
		})
	}
}

func TestOpenAIWSCFRayAcceptsOnlyRayIdentifiers(t *testing.T) {
	for value, want := range map[string]string{
		"8f1a2b3c4d5e6f70-SJC":        "8f1a2b3c4d5e6f70-SJC",
		" 8f1a2b3c4d5e6f70 ":          "8f1a2b3c4d5e6f70",
		"":                            "",
		"Bearer TEST_ONLY_SECRET":     "",
		"8f1a2b3c4d5e6f70-SJC\nX-Bad": "",
	} {
		require.Equal(t, want, openAIWSCFRay(http.Header{"Cf-Ray": []string{value}}), value)
	}
}

func TestOpenAIWSReplayTrackerCompleteResponseSettlesOnlyThatTurn(t *testing.T) {
	tracker := newOpenAIWSPassthroughReplayTracker(nil, 0)
	tracker.RegisterTurn(1, coderws.MessageText, []byte(`{"type":"response.create","model":"m","input":[]}`))
	tracker.ObserveUpstreamFrame([]byte(`{"type":"response.created","response":{"id":"resp_1"}}`))
	tracker.RegisterTurn(2, coderws.MessageText, []byte(`{"type":"response.create","model":"m","input":[]}`))
	_, ok := tracker.BuildFailoverError(7, nil)
	require.False(t, ok, "two pending turns cannot be replayed")
	tracker.CompleteResponse("resp_unknown")
	tracker.CompleteResponse("")
	_, ok = tracker.BuildFailoverError(7, nil)
	require.False(t, ok)
	tracker.CompleteResponse("resp_1")
	failure, ok := tracker.BuildFailoverError(7, nil)
	require.True(t, ok, "the remaining fresh turn is replayable again")
	require.Equal(t, 2, failure.Turn())
}

func TestOpenAIWSPassthroughClientInterruptedTurnIsBilledAndBound(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(map[bool]string{true: "accepted", false: "not_accepted"}[accepted], func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Security.URLAllowlist.Enabled = false
			cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
			request := []byte(`{"type":"response.create","model":"test-sol","input":[]}`)
			events := [][]byte{[]byte(`{"type":"response.created","response":{"id":"resp_interrupted","status":"in_progress"}}`)}
			if accepted {
				events = append(events, []byte(`{"type":"response.interrupt.accepted","response_id":"resp_interrupted","sequence_number":3}`))
			}
			events = append(events,
				[]byte(`{"type":"response.output_item.interrupted","response_id":"resp_interrupted"}`),
				[]byte(`{"type":"response.incomplete","response":{"id":"resp_interrupted","status":"incomplete","incomplete_details":{"reason":"interrupted"},"usage":{"input_tokens":34,"output_tokens":156}}}`),
			)
			upstream := &openAIWSCaptureConn{events: events}
			dialer := &openAIWSCaptureDialer{conn: upstream}
			svc := &OpenAIGatewayService{cfg: cfg, openaiWSPassthroughDialer: dialer, cache: &stubGatewayCache{}}
			account := &Account{ID: 452, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			done := make(chan struct{})
			var results []*OpenAIForwardResult
			var failures []*OpenAIWSUpstreamFailure
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r
				ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
				defer cancel()
				messageType, first, err := conn.Read(ctx)
				if err != nil {
					return
				}
				_ = svc.proxyResponsesWebSocketV2Passthrough(ctx, c, conn, account, "TEST_ONLY_TOKEN", messageType, first, &OpenAIWSIngressHooks{
					AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
						if result != nil && err == nil {
							results = append(results, result)
						}
					},
					OnUpstreamError: func(failure *OpenAIWSUpstreamFailure) { failures = append(failures, failure) },
				}, OpenAIWSProtocolDecision{}, nil)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer func() { _ = conn.CloseNow() }()
			require.NoError(t, conn.Write(ctx, coderws.MessageText, request))
			for range events {
				_, _, err := conn.Read(ctx)
				require.NoError(t, err)
			}
			_ = conn.CloseNow()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("mock relay did not finish")
			}
			boundAccount, err := svc.getOpenAIWSStateStore().GetResponseAccountForUser(context.Background(), 0, "resp_interrupted")
			require.NoError(t, err)
			if !accepted {
				require.Empty(t, results, "an unconfirmed interruption is not billed")
				require.Len(t, failures, 1)
				require.Equal(t, "interrupted", failures[0].IncompleteReason)
				require.Zero(t, boundAccount)
				return
			}
			require.Empty(t, failures, "a client interrupt is not an upstream error")
			require.Len(t, results, 1)
			require.Equal(t, "resp_interrupted", results[0].RequestID)
			require.Equal(t, 34, results[0].Usage.InputTokens)
			require.Equal(t, 156, results[0].Usage.OutputTokens)
			require.Equal(t, int64(452), boundAccount, "a continuation of the interrupted response routes to its account")
		})
	}
}
