package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

type openAIWSQuotaGateConn struct {
	*openAIWSCaptureConn
	nextTurnWritten chan struct{}
	writeCount      atomic.Int32
	readCount       atomic.Int32
	gateOnce        sync.Once
	holdOpen        bool
}

func (connection *openAIWSQuotaGateConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if connection.readCount.Add(1) == 2 && connection.nextTurnWritten != nil {
		select {
		case <-ctx.Done():
			return coderws.MessageText, nil, ctx.Err()
		case <-connection.nextTurnWritten:
		}
	}
	if connection.holdOpen {
		connection.mu.Lock()
		empty := len(connection.events) == 0
		connection.mu.Unlock()
		if empty {
			<-ctx.Done()
			return coderws.MessageText, nil, ctx.Err()
		}
	}
	return connection.openAIWSCaptureConn.ReadFrame(ctx)
}

func (connection *openAIWSQuotaGateConn) WriteFrame(ctx context.Context, messageType coderws.MessageType, payload []byte) error {
	if err := connection.openAIWSCaptureConn.WriteFrame(ctx, messageType, payload); err != nil {
		return err
	}
	if connection.writeCount.Add(1) == 2 && connection.nextTurnWritten != nil {
		connection.gateOnce.Do(func() { close(connection.nextTurnWritten) })
	}
	return nil
}

type openAIWSQuotaHandshakeDialer struct {
	queue       *openAIWSQueueDialer
	resetAt     time.Time
	rejectFirst bool
	calls       atomic.Int32
}

func (dialer *openAIWSQuotaHandshakeDialer) Dial(ctx context.Context, wsURL string, headers http.Header, proxyURL string) (openAIWSClientConn, int, http.Header, error) {
	if dialer.calls.Add(1) == 1 && dialer.rejectFirst {
		responseHeaders := http.Header{
			"X-Codex-Primary-Used-Percent":        {"100"},
			"X-Codex-Primary-Window-Minutes":      {"300"},
			"X-Codex-Primary-Reset-After-Seconds": {fmt.Sprint(int(time.Until(dialer.resetAt).Seconds()))},
		}
		return nil, http.StatusTooManyRequests, responseHeaders, errors.New("synthetic quota handshake rejection")
	}
	connection, status, responseHeaders, err := dialer.queue.Dial(ctx, wsURL, headers, proxyURL)
	if err == nil {
		responseHeaders = http.Header{"X-Codex-Primary-Used-Percent": {"44"}, "X-Test-Metadata": {"preserved"}}
	}
	return connection, status, responseHeaders, err
}

func TestOpenAIWSQuotaFailoverMatrix(testContext *testing.T) {
	scenarios := []struct {
		name        string
		prefix      []string
		failedEvent bool
		quotaOnly   bool
		handshake   bool
		committed   bool
		noHistory   bool
		noSpare     bool
		window      int
	}{
		{name: "five_hour_limit", window: 300},
		{name: "weekly_limit", window: 10080},
		{name: "quota_control_before_error", prefix: []string{`{"type":"codex.rate_limits","plan_type":"pro","rate_limits":{"primary":{"used_percent":12,"window_minutes":300}}}`}},
		{name: "created_before_error", prefix: []string{`{"type":"response.created","response":{"id":"resp_quota_a_2","status":"in_progress","error":null,"output":[]}}`}},
		{name: "in_progress_before_error", prefix: []string{`{"type":"response.created","response":{"id":"resp_quota_a_2","output":[]}}`, `{"type":"response.in_progress","response":{"id":"resp_quota_a_2","output":[]}}`}},
		{name: "failed_response_envelope", failedEvent: true},
		{name: "five_hour_quota_event_next_turn", quotaOnly: true, window: 300},
		{name: "weekly_quota_event_next_turn", quotaOnly: true, window: 10080},
		{name: "handshake_429", handshake: true},
		{name: "partial_text_is_not_replayed", committed: true, prefix: []string{`{"type":"response.output_text.delta","response_id":"resp_quota_a_2","delta":"already visible"}`}},
		{name: "tool_call_is_not_replayed", committed: true, prefix: []string{`{"type":"response.output_item.added","response_id":"resp_quota_a_2","item":{"type":"function_call","call_id":"call_quota","name":"test_tool","arguments":"{}"}}`}},
		{name: "failed_output_is_not_replayed", committed: true, failedEvent: true},
		{name: "unknown_history_is_not_replayed", noHistory: true},
		{name: "no_replacement_fails_closed", noSpare: true},
	}
	for _, scenario := range scenarios {
		testContext.Run(scenario.name, func(testContext *testing.T) {
			gin.SetMode(gin.TestMode)
			cfg := &config.Config{}
			cfg.Security.URLAllowlist.Enabled = false
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 2
			resetAt := time.Now().Add(90 * time.Minute).Truncate(time.Second)
			quotaError := fmt.Sprintf(`{"code":"usage_limit_reached","type":"usage_limit_reached","message":"usage limit reached","resets_at":%d}`, resetAt.Unix())
			errorEvent := `{"type":"error","error":` + quotaError + `}`
			if scenario.failedEvent {
				errorEvent = `{"type":"response.failed","response":{"id":"resp_quota_a_2","status":"failed","error":` + quotaError + `}}`
				if scenario.committed {
					errorEvent = `{"type":"response.failed","response":{"id":"resp_quota_a_2","status":"failed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"already generated"}]}],"usage":{"output_tokens":1},"error":` + quotaError + `}}`
				}
			} else if scenario.window > 0 && !scenario.quotaOnly {
				errorEvent = fmt.Sprintf(`{"type":"error","error":%s,"headers":{"x-codex-primary-used-percent":"100","x-codex-primary-window-minutes":"%d","x-codex-primary-reset-after-seconds":"5400"}}`, quotaError, scenario.window)
			}
			firstCompletion := []byte(`{"type":"response.completed","response":{"id":"resp_quota_a_1","status":"completed","output":[{"type":"reasoning","encrypted_content":"account-bound","summary":[{"type":"summary_text","text":"safe"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}],"usage":{"input_tokens":2,"output_tokens":3}}}`)
			accountAConnection := &openAIWSCaptureConn{}
			if !scenario.handshake && !scenario.noHistory {
				accountAConnection.events = append(accountAConnection.events, firstCompletion)
			}
			for _, prefix := range scenario.prefix {
				accountAConnection.events = append(accountAConnection.events, []byte(prefix))
			}
			if scenario.quotaOnly {
				accountAConnection.events = append(accountAConnection.events, []byte(fmt.Sprintf(`{"type":"codex.rate_limits","plan_type":"pro","rate_limits":{"primary":{"used_percent":100,"window_minutes":%d,"reset_at":%d}}}`, scenario.window, resetAt.Unix())))
			} else {
				accountAConnection.events = append(accountAConnection.events, []byte(errorEvent))
			}
			secondCompletion := []byte(`{"type":"response.completed","response":{"id":"resp_quota_b_2","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second answer"}]}],"usage":{"input_tokens":8,"output_tokens":2}}}`)
			accountBQuota := []byte(fmt.Sprintf(`{"type":"codex.rate_limits","plan_type":"pro","rate_limits":{"primary":{"used_percent":2,"window_minutes":300,"reset_at":%d}}}`, resetAt.Unix()))
			accountBConnection := &openAIWSCaptureConn{events: [][]byte{accountBQuota, secondCompletion}}
			var accountAUpstream openAIWSClientConn = accountAConnection
			if !scenario.handshake && !scenario.noHistory && !scenario.quotaOnly {
				accountAUpstream = &openAIWSQuotaGateConn{openAIWSCaptureConn: accountAConnection, nextTurnWritten: make(chan struct{})}
			}
			if scenario.quotaOnly {
				accountAUpstream = &openAIWSQuotaGateConn{openAIWSCaptureConn: accountAConnection, holdOpen: true}
			}
			queue := &openAIWSQueueDialer{conns: []openAIWSClientConn{accountAUpstream, accountBConnection}}
			if scenario.handshake {
				queue.conns = []openAIWSClientConn{accountBConnection}
			}
			dialer := &openAIWSQuotaHandshakeDialer{queue: queue, resetAt: resetAt, rejectFirst: scenario.handshake}
			accountA := &Account{ID: 861, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
			accountB := &Account{ID: 862, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
			repository := &openAICodexSnapshotAsyncRepo{updateExtraCh: make(chan map[string]any, 16), rateLimitCh: make(chan time.Time, 16)}
			gateway := &OpenAIGatewayService{cfg: cfg, accountRepo: repository, rateLimitService: &RateLimitService{accountRepo: repository}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), openaiWSStateStore: NewOpenAIWSStateStore(nil), openaiWSPassthroughDialer: dialer}
			validatedTurns := make(chan int, 8)
			completedTurns := make(chan string, 8)
			completedHeaders := make(chan http.Header, 8)
			var switches atomic.Int32
			hooks := &OpenAIWSIngressHooks{
				OnClientTurn: func(turn int, _ []byte) error { validatedTurns <- turn; return nil },
				AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
					if turnErr == nil && result != nil && result.RequestID != "" && result.Usage.OutputTokens > 0 {
						completedTurns <- result.RequestID
						completedHeaders <- cloneHeader(result.ResponseHeaders)
					}
				},
				OnAccountExhausted: func(failure *OpenAIWSAccountFailoverError) (*OpenAIWSFailoverTarget, error) {
					switches.Add(1)
					if scenario.noSpare {
						return nil, NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "synthetic pool exhausted", nil)
					}
					return &OpenAIWSFailoverTarget{Account: accountB, Token: "TEST_ONLY_OAUTH_B"}, nil
				},
			}
			serverErrors := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				connection, acceptErr := coderws.Accept(writer, request, nil)
				if acceptErr != nil {
					serverErrors <- acceptErr
					return
				}
				defer connection.CloseNow()
				ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
				ginContext.Request = request.Clone(request.Context())
				ginContext.Request.Header.Set("User-Agent", "codex_cli_rs/0.153.4")
				ginContext.Request.Header.Set("originator", "codex_cli_rs")
				ginContext.Set("api_key", &APIKey{UserID: 77})
				messageType, payload, readErr := connection.Read(request.Context())
				if readErr != nil {
					serverErrors <- readErr
					return
				}
				serverErrors <- gateway.ProxyResponsesWebSocketFromClient(request.Context(), ginContext, connection, accountA, "TEST_ONLY_OAUTH_A", messageType, payload, hooks)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			client, _, dialErr := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(testContext, dialErr)
			defer client.CloseNow()
			firstRequest := []byte(`{"type":"response.create","model":"gpt-6-astra","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]}]}`)
			if scenario.noHistory {
				firstRequest = []byte(`{"type":"response.create","model":"gpt-6-astra","previous_response_id":"resp_unknown","input":[]}`)
			}
			require.NoError(testContext, client.Write(ctx, coderws.MessageText, firstRequest))
			if !scenario.handshake && !scenario.noHistory {
				_, payload, readErr := client.Read(ctx)
				require.NoError(testContext, readErr)
				require.Equal(testContext, "resp_quota_a_1", gjson.GetBytes(payload, "response.id").String())
				if scenario.quotaOnly {
					_, payload, readErr = client.Read(ctx)
					require.NoError(testContext, readErr)
					require.Equal(testContext, "codex.rate_limits", gjson.GetBytes(payload, "type").String())
				}
				require.NoError(testContext, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-6-astra","previous_response_id":"resp_quota_a_1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)))
			}
			var lastPayload []byte
			var replacementQuotaObserved bool
			for {
				_, payload, readErr := client.Read(ctx)
				if scenario.noSpare {
					require.Error(testContext, readErr)
					break
				}
				require.NoError(testContext, readErr)
				lastPayload = payload
				eventType := gjson.GetBytes(payload, "type").String()
				if eventType == "codex.rate_limits" && gjson.GetBytes(payload, "rate_limits.primary.used_percent").Float() == 2 {
					require.Equal(testContext, accountBQuota, payload)
					replacementQuotaObserved = true
				}
				if !scenario.committed && (eventType == "response.created" || eventType == "response.in_progress") {
					require.NotEqual(testContext, "resp_quota_a_2", gjson.GetBytes(payload, "response.id").String())
				}
				if eventType == "error" || eventType == "response.failed" || eventType == "response.completed" {
					break
				}
			}
			if scenario.committed || scenario.noHistory {
				require.Equal(testContext, int32(0), switches.Load())
				if scenario.failedEvent {
					require.Equal(testContext, "response.failed", gjson.GetBytes(lastPayload, "type").String())
				} else {
					require.Equal(testContext, "error", gjson.GetBytes(lastPayload, "type").String())
				}
			} else if !scenario.noSpare {
				require.Equal(testContext, "resp_quota_b_2", gjson.GetBytes(lastPayload, "response.id").String())
				require.Equal(testContext, int32(1), switches.Load())
				require.True(testContext, replacementQuotaObserved)
			}
			_ = client.Close(coderws.StatusNormalClosure, "done")
			select {
			case serverErr := <-serverErrors:
				if scenario.noSpare {
					require.Error(testContext, serverErr)
				} else {
					require.NoError(testContext, serverErr)
				}
			case <-ctx.Done():
				testContext.Fatal("quota failover did not finish")
			}
			if scenario.committed || scenario.noHistory || scenario.noSpare {
				return
			}
			var lastHeaders http.Header
			for len(completedHeaders) > 0 {
				lastHeaders = <-completedHeaders
			}
			require.Empty(testContext, lastHeaders.Get("x-codex-primary-used-percent"))
			require.Equal(testContext, "preserved", lastHeaders.Get("x-test-metadata"))
			require.Equal(testContext, int32(2), dialer.calls.Load())
			require.Len(testContext, accountBConnection.rawWrites, 1)
			require.False(testContext, gjson.GetBytes(accountBConnection.rawWrites[0], "previous_response_id").Exists())
			if scenario.handshake {
				require.Len(testContext, validatedTurns, 1)
				require.Len(testContext, completedTurns, 1)
			} else {
				require.Len(testContext, validatedTurns, 2)
				require.Len(testContext, completedTurns, 2)
				require.Len(testContext, gjson.GetBytes(accountBConnection.rawWrites[0], "input").Array(), 4)
				require.False(testContext, gjson.GetBytes(accountBConnection.rawWrites[0], "input.1.encrypted_content").Exists())
			}
			select {
			case actualReset := <-repository.rateLimitCh:
				require.WithinDuration(testContext, resetAt, actualReset, 2*time.Second)
			case <-ctx.Done():
				testContext.Fatal("quota reset was not recorded")
			}
		})
	}
}
