//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func awaitCodexQuotaUpdates(t *testing.T, repo *openAICodexSnapshotAsyncRepo, used float64) map[string]any {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case updates := <-repo.updateExtraCh:
			if updates["codex_5h_used_percent"] == used {
				return updates
			}
		case <-timer.C:
			t.Fatalf("Codex five-hour quota %v was not persisted", used)
			return nil
		}
	}
}

func TestCodexQuotaHeadersSupportAbsoluteAndLegacyResets(t *testing.T) {
	reset := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	for _, scenario := range []struct {
		name, absolute, relative string
		wantRelative             int
	}{
		{name: "absolute", absolute: fmt.Sprint(reset.Unix())},
		{name: "absolute_takes_precedence", absolute: fmt.Sprint(reset.Unix()), relative: "99"},
		{name: "legacy", relative: "99", wantRelative: 99},
		{name: "invalid_absolute_legacy", absolute: "invalid", relative: "99", wantRelative: 99},
		{name: "expired", absolute: "1"},
		{name: "overflow", absolute: "9223372036854775807"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			headers := http.Header{}
			for _, window := range []string{"primary", "secondary"} {
				headers.Set("x-codex-"+window+"-used-percent", "90")
				headers.Set("x-codex-"+window+"-reset-at", scenario.absolute)
				headers.Set("x-codex-"+window+"-reset-after-seconds", scenario.relative)
			}
			headers.Set("x-codex-primary-window-minutes", "300")
			headers.Set("x-codex-secondary-window-minutes", "10080")
			snapshot := ParseCodexRateLimitHeaders(headers)
			require.NotNil(t, snapshot)
			for _, seconds := range []*int{snapshot.PrimaryResetAfterSeconds, snapshot.SecondaryResetAfterSeconds} {
				if scenario.name == "overflow" {
					require.Nil(t, seconds)
					continue
				}
				require.NotNil(t, seconds)
				if strings.HasPrefix(scenario.name, "absolute") {
					require.Equal(t, reset, codexSnapshotBaseTime(snapshot, time.Now()).Add(time.Duration(*seconds)*time.Second))
				} else {
					require.Equal(t, scenario.wantRelative, *seconds)
				}
			}
		})
	}
	for _, invalid := range []string{"NaN", "+Inf", "-Inf"} {
		require.Nil(t, ParseCodexRateLimitHeaders(http.Header{"X-Codex-Primary-Used-Percent": {invalid}}))
	}
}

func TestCodexQuotaHTTPKeepsInternalUsageAndHidesProviderLimits(t *testing.T) {
	for _, mode := range []string{"json", "sse", "compact"} {
		t.Run(mode, func(t *testing.T) {
			path := "/v1/responses"
			if mode == "compact" {
				path += "/compact"
			}
			c, rec := auditOAuthPolicyContext(path)
			reset := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			headers := http.Header{"Content-Type": {"application/json"}}
			headers.Set("x-codex-primary-used-percent", "20")
			headers.Set("x-codex-primary-window-minutes", "300")
			headers.Set("x-codex-primary-reset-at", fmt.Sprint(reset.Unix()))
			for _, name := range []string{"x-codex-secondary-used-percent", "x-codex-bengalfox-primary-used-percent", "x-codex-credits-balance", "x-codex-promo-message"} {
				headers.Set(name, "MOCK_QUOTA")
			}
			headers.Set("x-codex-turn-state", "MOCK_STATE")
			headers.Set("x-codex-future-control", "MOCK_CONTROL")
			completion := `{"id":"resp_quota_http","status":"completed","usage":{"input_tokens":2,"output_tokens":3}}`
			wire := completion
			if mode == "sse" {
				headers.Set("Content-Type", "text/event-stream")
				// Codex 0.162.1 queues its model-switch prompt at >=90%.
				quota := fmt.Sprintf(`{"type":"codex.rate_limits","plan_type":"pro","rate_limits":{"primary":{"used_percent":95,"window_minutes":300,"reset_at":%d},"secondary":{"used_percent":80,"window_minutes":10080,"reset_at":%d}}}`, reset.Unix(), reset.Unix())
				wire = "event:codex.rate_limits\ndata: " + quota + "\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + completion + "}\n\n"
			}
			upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{auditOAuthPolicyResponse(200, headers, wire)}}
			repo := &openAICodexSnapshotAsyncRepo{updateExtraCh: make(chan map[string]any, 8)}
			svc := auditOAuthPolicyService(upstream)
			svc.accountRepo = repo
			svc.codexSnapshotThrottle = newAccountWriteThrottle(0)
			result, err := svc.Forward(context.Background(), c, auditOAuthPolicyAccount(), []byte(fmt.Sprintf(`{"model":"gpt-6.1-sol","stream":%v}`, mode == "sse")))
			require.NoError(t, err)
			require.Equal(t, 3, result.Usage.OutputTokens)
			require.NotContains(t, rec.Body.String(), "codex.rate_limits")
			for name := range headers {
				if isOpenAICodexQuotaHeader(name) {
					require.Empty(t, rec.Header().Get(name), name)
				}
			}
			require.Equal(t, "MOCK_STATE", rec.Header().Get("x-codex-turn-state"))
			require.Equal(t, "MOCK_CONTROL", rec.Header().Get("x-codex-future-control"))
			used := 20.0
			if mode == "sse" {
				used = 95
			}
			updates := awaitCodexQuotaUpdates(t, repo, used)
			require.Equal(t, reset.Format(time.RFC3339), updates["codex_5h_reset_at"])
			if mode == "sse" {
				require.Equal(t, 80.0, updates["codex_7d_used_percent"])
			}
		})
	}
}

func TestCodexQuotaWSPassthroughSamplesAndFilters(t *testing.T) {
	for _, mode := range []string{"handshake_only", "live_text", "live_binary", "native_relay", "api_key"} {
		t.Run(mode, func(t *testing.T) {
			reset := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			headers := http.Header{"X-Codex-Turn-State": {"MOCK_STATE"}, "X-Test-Metadata": {"kept"}}
			headers.Set("x-codex-primary-used-percent", "20")
			headers.Set("x-codex-primary-window-minutes", "300")
			headers.Set("x-codex-primary-reset-at", fmt.Sprint(reset.Unix()))
			// Exercise a value that triggers both the warning and model-switch UI.
			quota := []byte(fmt.Sprintf(`{"type":"codex.rate_limits","plan_type":"pro","rate_limits":{"primary":{"used_percent":95,"window_minutes":300,"reset_at":%d},"secondary":{"used_percent":80,"window_minutes":10080,"reset_at":%d}}}`, reset.Unix(), reset.Unix()))
			unknownQuota := []byte(`{"type":"codex.rate_limits","metered_limit_name":"model-specific","rate_limits":{"primary":{"used_percent":100,"window_minutes":300}}}`)
			created := []byte(`{"type":"response.created","response":{"id":"resp_quota_ws","output":[]}}`)
			completed := []byte(`{ "type":"response.completed", "response":{"id":"resp_quota_ws","future":true,"usage":{"input_tokens":2,"output_tokens":3}} }`)
			events := [][]byte{created, quota, unknownQuota, completed}
			if mode == "handshake_only" {
				events = [][]byte{created, completed}
			}
			conn := &openAIWSCaptureConn{events: events}
			if mode == "live_binary" {
				conn.eventTypes = []coderws.MessageType{coderws.MessageText, coderws.MessageBinary, coderws.MessageText, coderws.MessageText}
			}
			dialer := &openAIWSCaptureDialer{conn: conn, handshake: headers}
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.APIKeyEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModePassthrough
			account := auditOAuthPolicyAccount()
			account.Extra = map[string]any{"responses_websockets_v2_enabled": true}
			if mode == "native_relay" {
				account = newOpenAINativeRelayAccount("https://relay.example")
				account.Extra["responses_websockets_v2_enabled"] = true
			}
			if mode == "api_key" {
				account.Type = AccountTypeAPIKey
				account.Credentials = map[string]any{"api_key": "MOCK_ONLY", "base_url": "https://relay.example"}
				account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModePassthrough
			}
			repo := &openAICodexSnapshotAsyncRepo{updateExtraCh: make(chan map[string]any, 8)}
			svc := &OpenAIGatewayService{cfg: cfg, accountRepo: repo, codexSnapshotThrottle: newAccountWriteThrottle(0), openaiWSPassthroughDialer: dialer, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg)}
			results := make(chan *OpenAIForwardResult, 1)
			serverErr := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				client, err := coderws.Accept(w, r, nil)
				if err != nil {
					serverErr <- err
					return
				}
				defer func() { _ = client.CloseNow() }()
				c, _ := auditOAuthPolicyContext("/v1/responses")
				typ, request, err := client.Read(r.Context())
				if err != nil {
					serverErr <- err
					return
				}
				hooks := &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
					if err == nil && result != nil {
						// Mirror the handler: old handshake observations must not
						// replace the quota event seen during this turn.
						if account.IsOpenAIOAuth() {
							svc.UpdateCodexUsageSnapshotFromHeaders(r.Context(), account.ID, result.ResponseHeaders)
						}
						results <- result
					}
				}}
				serverErr <- svc.ProxyResponsesWebSocketFromClient(r.Context(), c, client, account, "MOCK_ONLY", typ, request, hooks)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer func() { _ = client.CloseNow() }()
			request := []byte(`{ "type":"response.create", "model":"gpt-6.1-sol", "input":[], "future":true }`)
			require.NoError(t, client.Write(ctx, coderws.MessageText, request))
			want := [][]byte{created, completed}
			if mode == "api_key" {
				want = events
			}
			for _, expected := range want {
				_, payload, err := client.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, expected, payload)
			}
			_ = client.Close(coderws.StatusNormalClosure, "done")
			select {
			case err := <-serverErr:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("relay did not finish")
			}
			result := <-results
			require.Equal(t, 3, result.Usage.OutputTokens)
			require.Equal(t, "MOCK_STATE", result.ResponseHeaders.Get("x-codex-turn-state"))
			require.Equal(t, "kept", result.ResponseHeaders.Get("x-test-metadata"))
			conn.mu.Lock()
			require.Equal(t, request, conn.rawWrites[0])
			conn.mu.Unlock()
			if mode == "api_key" {
				require.Equal(t, "20", result.ResponseHeaders.Get("x-codex-primary-used-percent"))
				return
			}
			require.Empty(t, result.ResponseHeaders.Get("x-codex-primary-used-percent"))
			if mode == "native_relay" {
				require.Empty(t, repo.updateExtraCh)
				return
			}
			used := 95.0
			if mode == "handshake_only" {
				used = 20
			}
			updates := awaitCodexQuotaUpdates(t, repo, used)
			require.Equal(t, reset.Format(time.RFC3339), updates["codex_5h_reset_at"])
			if mode != "handshake_only" {
				require.Equal(t, 80.0, updates["codex_7d_used_percent"])
			}
		})
	}
}

func TestCodexQuotaWSPoolKeepsFreshLimitsAcrossLeaseReuse(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%v", stream), func(t *testing.T) {
			reset := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			headers := http.Header{"X-Codex-Turn-State": {"MOCK_STATE"}}
			headers.Set("x-codex-primary-used-percent", "20")
			headers.Set("x-codex-primary-window-minutes", "300")
			headers.Set("x-codex-primary-reset-at", fmt.Sprint(reset.Unix()))
			quota := func(used int) []byte {
				return []byte(fmt.Sprintf(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":%d,"window_minutes":300,"reset_at":%d},"secondary":{"used_percent":80,"window_minutes":10080,"reset_at":%d}}}`, used, reset.Unix(), reset.Unix()))
			}
			conn := &openAIWSCaptureConn{events: [][]byte{quota(81), []byte(`{"type":"response.completed","response":{"id":"resp_pool_1","usage":{"input_tokens":2,"output_tokens":3}}}`), quota(25), []byte(`{"type":"response.completed","response":{"id":"resp_pool_2","usage":{"input_tokens":4,"output_tokens":5}}}`)}}
			dialer := &openAIWSCaptureDialer{conn: conn, handshake: headers}
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			pool := newOpenAIWSConnPool(cfg)
			pool.setClientDialerForTest(dialer)
			defer pool.Close()
			repo := &openAICodexSnapshotAsyncRepo{updateExtraCh: make(chan map[string]any, 8)}
			svc := &OpenAIGatewayService{cfg: cfg, accountRepo: repo, codexSnapshotThrottle: newAccountWriteThrottle(0), openaiWSPool: pool}
			account := auditOAuthPolicyAccount()
			for index, used := range []float64{81, 25} {
				c, rec := auditOAuthPolicyContext("/v1/responses")
				body := map[string]any{"model": "gpt-6.1-sol", "stream": stream, "input": []any{}}
				result, err := svc.forwardOpenAIWSV2(context.Background(), c, account, body, "MOCK_ONLY", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, true, stream, "gpt-6.1-sol", "gpt-6.1-sol", time.Now(), 0, "")
				require.NoError(t, err)
				require.Equal(t, 3+2*index, result.Usage.OutputTokens)
				require.NotContains(t, rec.Body.String(), "codex.rate_limits")
				require.Empty(t, result.ResponseHeaders.Get("x-codex-primary-used-percent"))
				require.Equal(t, "MOCK_STATE", result.ResponseHeaders.Get("x-codex-turn-state"))
				svc.UpdateCodexUsageSnapshotFromHeaders(context.Background(), account.ID, result.ResponseHeaders)
				updates := awaitCodexQuotaUpdates(t, repo, used)
				require.Equal(t, 80.0, updates["codex_7d_used_percent"])
			}
			require.Equal(t, 1, dialer.DialCount(), "second turn must reuse the connection without resampling its old handshake")
			require.Empty(t, repo.updateExtraCh, "turn completion must not replace live limits with old handshake data")
		})
	}
}

func TestCodexQuotaWeeklyObservationsBypassThrottleAtBoundaries(t *testing.T) {
	repo := &openAICodexSnapshotAsyncRepo{updateExtraCh: make(chan map[string]any, 8)}
	svc := &OpenAIGatewayService{accountRepo: repo, codexSnapshotThrottle: newAccountWriteThrottle(time.Hour)}
	used5h, minutes5h, seconds := 20.0, 300, 3600
	used7d, minutes7d := 80.0, 10080
	snapshot := &OpenAICodexUsageSnapshot{PrimaryUsedPercent: &used5h, PrimaryWindowMinutes: &minutes5h, PrimaryResetAfterSeconds: &seconds, UpdatedAt: time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)}
	svc.updateCodexUsageSnapshot(context.Background(), 9901, snapshot)
	awaitCodexQuotaUpdates(t, repo, used5h)
	snapshot.SecondaryUsedPercent, snapshot.SecondaryWindowMinutes, snapshot.SecondaryResetAfterSeconds = &used7d, &minutes7d, &seconds
	svc.updateCodexUsageSnapshot(context.Background(), 9901, snapshot)
	require.Equal(t, 80.0, awaitCodexQuotaUpdates(t, repo, used5h)["codex_7d_used_percent"], "first weekly observation must survive the handshake throttle")
	used7d = 81
	svc.updateCodexUsageSnapshot(context.Background(), 9901, snapshot)
	select {
	case <-repo.updateExtraCh:
		t.Fatal("ordinary same-window observations must retain write throttling")
	case <-time.After(50 * time.Millisecond):
	}
	used7d = 100
	svc.updateCodexUsageSnapshot(context.Background(), 9901, snapshot)
	require.Equal(t, 100.0, awaitCodexQuotaUpdates(t, repo, used5h)["codex_7d_used_percent"])
	used7d = 10
	svc.updateCodexUsageSnapshot(context.Background(), 9901, snapshot)
	require.Equal(t, 10.0, awaitCodexQuotaUpdates(t, repo, used5h)["codex_7d_used_percent"])
}
