//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func auditOAuthPolicyAccount() *Account {
	return &Account{ID: 999, Name: "MOCK_ONLY", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Concurrency: 1, Credentials: map[string]any{"access_token": "MOCK_ONLY", "chatgpt_account_id": "MOCK_ONLY_ACCOUNT"}}
}

func auditOAuthPolicyContext(path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	c.Request.Header.Set("User-Agent", "codex_vscode/0.159.2")
	c.Request.Header.Set("originator", "codex_vscode")
	c.Request.Header.Set("Version", "0.159.2")
	c.Request.Header.Set("OpenAI-Beta", "responses=MOCK_NATIVE")
	c.Request.Header["X-Codex-Future-Control"] = []string{"MOCK_A", "MOCK_B"}
	c.Request.Header.Set("X-Oai-Attestation", "MOCK_OPAQUE")
	c.Request.Header.Set("Authorization", "Bearer MOCK_INBOUND")
	c.Request.Header.Set("chatgpt-account-id", "MOCK_INBOUND_ACCOUNT")
	c.Request.Header.Set("Cookie", "MOCK_ONLY")
	c.Request.Header.Set("X-Forwarded-For", "192.0.2.1")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	return c, rec
}

func auditOAuthPolicyResponse(status int, headers http.Header, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}
}

func auditOAuthPolicyService(upstream *httpUpstreamSequenceRecorder) *OpenAIGatewayService {
	return &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream,
		responseHeaderFilter: responseheaders.CompileHeaderFilter(config.ResponseHeaderConfig{})}
}

func TestOpenAINativeOAuth_NormalHTTPAndCompactPreserveNativePayload(t *testing.T) {
	fixture := []byte(`{ "model":"gpt-6.1-sol", "stream":false, "store":false, "reasoning":{"effort":"minimal","mode":"pro"}, "instructions":"MOCK_ONLY", "input":[{"type":"reasoning","encrypted_content":"MOCK_ONLY","summary":[]}], "tools":[{"type":"function","name":"fixture","parameters":{"type":"object","additionalProperties":true}}], "previous_response_id":"resp_mock_previous", "prompt_cache_key":"mock_cache", "client_metadata":{"thread_id":"mock_thread","future":"MOCK_ONLY"}, "prompt_cache_retention":"24h", "safety_identifier":"MOCK_ONLY", "max_output_tokens":123, "future_root":{"unknown":[1,null,true]} }`)
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		for _, encoded := range []bool{false, true} {
			label := strings.TrimPrefix(path, "/v1/") + "/plain"
			if encoded {
				label = strings.TrimPrefix(path, "/v1/") + "/zstd"
			}
			t.Run(label, func(t *testing.T) {
				c, _ := auditOAuthPolicyContext(path)
				wire := fixture
				if encoded {
					var parsed map[string]any
					require.NoError(t, json.Unmarshal(fixture, &parsed))
					c.Set(OpenAIParsedRequestBodyKey, parsed)
					encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
					require.NoError(t, err)
					wire = encoder.EncodeAll(fixture, nil)
					encoder.Close()
					c.Request.Header.Set("Content-Encoding", "zstd")
				}
				upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{auditOAuthPolicyResponse(200, http.Header{"Content-Type": {"application/json"}}, `{"id":"resp_mock_ok","usage":{"input_tokens":1,"output_tokens":1},"future_response":{"keep":true}}`)}}
				result, err := auditOAuthPolicyService(upstream).Forward(context.Background(), c, auditOAuthPolicyAccount(), wire)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, 1, upstream.callCount)
				require.Equal(t, sha256.Sum256(wire), sha256.Sum256(upstream.bodies[0]))
				headers := upstream.reqs[0].Header
				require.True(t, NativeCodexRejectsRedirects(upstream.reqs[0].Context()))
				for _, key := range []string{"User-Agent", "originator", "Version", "OpenAI-Beta", "X-Codex-Future-Control", "X-Oai-Attestation"} {
					require.Equal(t, c.Request.Header.Values(key), headers.Values(key), key)
				}
				require.Equal(t, "Bearer MOCK_ONLY", headers.Get("Authorization"))
				require.Equal(t, "MOCK_ONLY_ACCOUNT", headers.Get("chatgpt-account-id"))
				require.Empty(t, headers.Get("Cookie"))
				require.Empty(t, headers.Get("X-Forwarded-For"))
				if encoded {
					require.Equal(t, "zstd", headers.Get("Content-Encoding"))
				}
				if strings.HasSuffix(path, "/compact") {
					require.Equal(t, "/backend-api/codex/responses/compact", upstream.reqs[0].URL.Path)
				}
			})
		}
	}
}

func TestOpenAINativeOAuth_RejectsNonOfficialWithoutLegacyTransform(t *testing.T) {
	c, rec := auditOAuthPolicyContext("/v1/responses")
	c.Request.Header.Set("User-Agent", "curl/8.0")
	c.Request.Header.Del("originator")
	upstream := &httpUpstreamSequenceRecorder{}
	_, err := auditOAuthPolicyService(upstream).Forward(context.Background(), c, auditOAuthPolicyAccount(), []byte(`{"model":"gpt-6.1-sol","input":"MOCK_ONLY"}`))
	require.Error(t, err)
	require.Equal(t, 403, rec.Code)
	require.Equal(t, 0, upstream.callCount)
}

func TestOpenAINativeOAuth_HTTPTerminalPreservesQuery(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		t.Run(path, func(t *testing.T) {
			c, _ := auditOAuthPolicyContext(path + "?future_control=MOCK_A&future_control=MOCK_B")
			req, err := auditOAuthPolicyService(nil).buildUpstreamRequest(context.Background(), c, auditOAuthPolicyAccount(), []byte(`{"model":"gpt-6.1-sol"}`), "MOCK_ONLY", true, "", true)
			require.NoError(t, err)
			require.NotEmpty(t, c.Request.URL.RawQuery)
			require.Equal(t, c.Request.URL.RawQuery, req.URL.RawQuery)
		})
	}
}

func TestOpenAINativeOAuth_NonStreamingAcceptIsPreserved(t *testing.T) {
	c, _ := auditOAuthPolicyContext("/v1/responses")
	c.Request.Header.Set("Accept", "application/json; profile=MOCK_ONLY")
	req, err := auditOAuthPolicyService(nil).buildUpstreamRequest(context.Background(), c, auditOAuthPolicyAccount(), []byte(`{"model":"gpt-6.1-sol","stream":false}`), "MOCK_ONLY", false, "", true)
	require.NoError(t, err)
	require.Equal(t, c.GetHeader("Accept"), req.Header.Get("Accept"))
}

func TestOpenAINativeOAuth_EmptyPreviousResponseIsPreserved(t *testing.T) {
	for _, value := range []string{`""`, `null`} {
		t.Run(value, func(t *testing.T) {
			c, _ := auditOAuthPolicyContext("/v1/responses")
			body := []byte(`{"model":"gpt-6.1-sol","stream":false,"previous_response_id":` + value + `,"future_root":true}`)
			upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{auditOAuthPolicyResponse(200, http.Header{"Content-Type": {"application/json"}}, `{"id":"resp_mock_ok","usage":{"input_tokens":1,"output_tokens":1}}`)}}
			_, err := auditOAuthPolicyService(upstream).Forward(context.Background(), c, auditOAuthPolicyAccount(), body)
			require.NoError(t, err)
			require.True(t, gjson.GetBytes(body, "previous_response_id").Exists())
			require.Equal(t, sha256.Sum256(body), sha256.Sum256(upstream.bodies[0]))
			require.True(t, gjson.GetBytes(upstream.bodies[0], "future_root").Bool())
		})
	}
}

func TestOpenAINativeOAuth_HTTPRejectsLossyRecovery(t *testing.T) {
	for _, code := range []string{"invalid_encrypted_content", "previous_response_not_found"} {
		t.Run(code, func(t *testing.T) {
			c, rec := auditOAuthPolicyContext("/v1/responses")
			body := []byte(`{"model":"gpt-6.1-sol","stream":false,"store":false,"reasoning":{"effort":"high"},"input":[{"type":"reasoning","encrypted_content":"MOCK_ONLY","summary":[]},{"type":"message","role":"user","content":[{"type":"input_text","text":"MOCK_ONLY"}]}],"future_root":true}`)
			if code == "previous_response_not_found" {
				body = []byte(`{"model":"gpt-6.1-sol","stream":false,"store":false,"reasoning":{"effort":"high"},"previous_response_id":"resp_mock_history","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"MOCK_ONLY"}]}],"future_root":true}`)
			}
			upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{
				auditOAuthPolicyResponse(400, http.Header{"Content-Type": {"application/json"}}, `{"error":{"type":"invalid_request_error","code":"`+code+`","message":"fixture_validation_failed"}}`),
				auditOAuthPolicyResponse(200, http.Header{"Content-Type": {"application/json"}}, `{"id":"resp_mock_ok","usage":{"input_tokens":1,"output_tokens":1}}`),
			}}
			_, err := auditOAuthPolicyService(upstream).Forward(context.Background(), c, auditOAuthPolicyAccount(), body)
			require.Error(t, err)
			require.Equal(t, 1, upstream.callCount)
			require.Equal(t, sha256.Sum256(body), sha256.Sum256(upstream.bodies[0]))
			require.Equal(t, code, gjson.Get(rec.Body.String(), "error.code").String())
		})
	}
}

func TestOpenAINativeOAuth_ErrorEnvelopeIsPreserved(t *testing.T) {
	c, rec := auditOAuthPolicyContext("/v1/responses")
	original := `{"error":{"type":"invalid_request_error","code":"mock_unknown_error","param":"input","message":"fixture_validation_failed","future_error":true}}`
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{auditOAuthPolicyResponse(400, http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"MOCK_REQUEST"}}, original)}}
	_, err := auditOAuthPolicyService(upstream).Forward(context.Background(), c, auditOAuthPolicyAccount(), []byte(`{"model":"gpt-6.1-sol","stream":false}`))
	require.Error(t, err)
	require.Equal(t, 1, upstream.callCount)
	require.Equal(t, 400, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
	for _, key := range []string{"code", "param", "future_error"} {
		require.True(t, gjson.Get(rec.Body.String(), "error."+key).Exists(), key)
	}
}

func TestOpenAINativeOAuth_ResponseControlHeadersArePreserved(t *testing.T) {
	c, rec := auditOAuthPolicyContext("/v1/responses")
	headers := http.Header{"Content-Type": {"application/json"}}
	for _, key := range []string{"openai-model", "x-models-etag", "x-reasoning-included", "x-codex-turn-state", "x-codex-future-response"} {
		headers.Set(key, "MOCK_ONLY")
	}
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{auditOAuthPolicyResponse(200, headers, `{"id":"resp_mock_ok","future_response":true,"usage":{"input_tokens":1,"output_tokens":1}}`)}}
	_, err := auditOAuthPolicyService(upstream).Forward(context.Background(), c, auditOAuthPolicyAccount(), []byte(`{"model":"gpt-6.1-sol","stream":false}`))
	require.NoError(t, err)
	for _, key := range []string{"openai-model", "x-models-etag", "x-reasoning-included", "x-codex-turn-state", "x-codex-future-response"} {
		require.Equal(t, headers.Values(key), rec.Header().Values(key), key)
	}
	require.True(t, gjson.Get(rec.Body.String(), "future_response").Bool())
}

type auditOAuthPolicyRoundTripper func(*http.Request) (*http.Response, error)

func (f auditOAuthPolicyRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenAINativeOAuth_ModelsPreservesClientIdentity(t *testing.T) {
	client, err := httpclient.GetClient(httpclient.Options{Timeout: codexModelsManifestRequestTimeout, ResponseHeaderTimeout: 10 * time.Second})
	require.NoError(t, err)
	originalTransport := client.Transport
	defer func() { client.Transport = originalTransport }()
	var outgoing *http.Request
	manifest := `{"models":[{"slug":"gpt-6.1-sol","future_capability":true}],"future_root":true}`
	client.Transport = auditOAuthPolicyRoundTripper(func(r *http.Request) (*http.Response, error) {
		outgoing = r.Clone(r.Context())
		return auditOAuthPolicyResponse(200, http.Header{"Content-Type": {"application/json"}, "Etag": {"MOCK_ETAG"}}, manifest), nil
	})
	c, _ := auditOAuthPolicyContext("/v1/models?future=a%2Fb&client_version=0.159.2&future=a+b")
	c.Request.Header["If-None-Match"] = []string{`"MOCK_ETAG_A"`, `"MOCK_ETAG_B"`}
	result, err := auditOAuthPolicyService(nil).FetchNativeCodexModelsManifest(context.Background(), auditOAuthPolicyAccount(), c.Request.URL.RawQuery, `"MOCK_OLD_ETAG"`, c.Request.Header)
	require.NoError(t, err)
	require.NotNil(t, outgoing)
	require.Equal(t, sha256.Sum256([]byte(manifest)), sha256.Sum256(result.Body))
	require.Equal(t, "0.159.2", outgoing.URL.Query().Get("client_version"))
	require.Equal(t, c.Request.Header.Values("If-None-Match"), outgoing.Header.Values("If-None-Match"))
	require.Equal(t, c.GetHeader("User-Agent"), outgoing.Header.Get("User-Agent"))
	require.Equal(t, c.GetHeader("Originator"), outgoing.Header.Get("Originator"))
	require.Equal(t, c.GetHeader("OpenAI-Beta"), outgoing.Header.Get("OpenAI-Beta"))
	require.Equal(t, c.Request.Header.Values("X-Codex-Future-Control"), outgoing.Header.Values("X-Codex-Future-Control"))
	require.Equal(t, c.Request.URL.RawQuery, outgoing.URL.RawQuery)
	require.Equal(t, "Bearer MOCK_ONLY", outgoing.Header.Get("Authorization"))
	require.True(t, NativeCodexRejectsRedirects(outgoing.Context()))
}

func TestOpenAINativeOAuth_ModelsAndWSDoNotFollowRedirects(t *testing.T) {
	var redirected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			redirected.Add(1)
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Location", "/target")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	ctx := WithNativeCodexRedirectPolicy(context.Background())
	conn, status, _, err := newDefaultOpenAIWSClientDialer().Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), http.Header{"User-Agent": {"codex_cli_rs/0.159.2"}}, "")
	require.Error(t, err)
	require.Nil(t, conn)
	require.Equal(t, http.StatusTemporaryRedirect, status)
	require.Zero(t, redirected.Load())
	client, err := httpclient.GetClient(httpclient.Options{Timeout: codexModelsManifestRequestTimeout, ResponseHeaderTimeout: 10 * time.Second})
	require.NoError(t, err)
	original := client.Transport
	defer func() { client.Transport = original }()
	client.Transport = auditOAuthPolicyRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/target" {
			redirected.Add(1)
		}
		return auditOAuthPolicyResponse(http.StatusTemporaryRedirect, http.Header{"Location": {"/target"}}, ""), nil
	})
	c, _ := auditOAuthPolicyContext("/v1/models")
	_, err = auditOAuthPolicyService(nil).FetchNativeCodexModelsManifest(context.Background(), auditOAuthPolicyAccount(), "", "", c.Request.Header)
	require.Error(t, err)
	require.Zero(t, redirected.Load())
}

func TestOpenAINativeOAuth_HTTPRedirectIsReturnedWithoutSuccessOrReplay(t *testing.T) {
	c, rec := auditOAuthPolicyContext("/v1/responses")
	body := `{"error":{"code":"mock_redirect","future":true}}`
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{auditOAuthPolicyResponse(307, http.Header{"Location": {"/mock_target"}, "Content-Type": {"application/json"}}, body)}}
	_, err := auditOAuthPolicyService(upstream).Forward(context.Background(), c, auditOAuthPolicyAccount(), []byte(`{"model":"gpt-5.5","stream":true}`))
	require.Error(t, err)
	require.Equal(t, 307, rec.Code)
	require.Equal(t, "/mock_target", rec.Header().Get("Location"))
	require.Equal(t, body, rec.Body.String())
	require.Equal(t, 1, upstream.callCount)
}

func TestOpenAINativeOAuth_WSFramesAndHandshakeArePreserved(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	responses := [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_mock_ws_1","future_response":true,"usage":{"input_tokens":1,"output_tokens":1}}}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_mock_ws_2","future_response":true,"usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	upstream := &openAIWSCaptureConn{events: responses, eventTypes: []coderws.MessageType{coderws.MessageBinary, coderws.MessageText}, readDelays: []time.Duration{0, 500 * time.Millisecond}}
	dialer := &openAIWSCaptureDialer{conn: upstream}
	svc := &OpenAIGatewayService{cfg: cfg, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), openaiWSPassthroughDialer: dialer}
	account := auditOAuthPolicyAccount()
	account.Extra = map[string]any{"responses_websockets_v2_enabled": true}
	errCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer conn.CloseNow()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r.Clone(r.Context())
		sessionHash := svc.openAISessionHashForTurnState(c, "")
		svc.getOpenAIWSStateStore().BindSessionTurnState(0, openAIWSUserTurnStateSessionHash(0, account.ID, sessionHash), "MOCK_OWNED_STATE", time.Minute)
		if svc.resolveOpenAIWSTurnStateForAccount(account, 0, sessionHash, "MOCK_OWNED_STATE") != "MOCK_OWNED_STATE" {
			errCh <- io.ErrUnexpectedEOF
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		kind, first, err := conn.Read(ctx)
		cancel()
		if err != nil {
			errCh <- err
			return
		}
		errCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, account, "MOCK_ONLY", kind, first, nil)
	}))
	defer server.Close()
	c, _ := auditOAuthPolicyContext("/v1/responses")
	c.Request.Header.Set("session_id", "00000000-0000-4000-8000-000000000001")
	c.Request.Header.Set("x-codex-turn-state", "MOCK_OWNED_STATE")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses?future_control=MOCK_ONLY", &coderws.DialOptions{HTTPHeader: c.Request.Header})
	cancel()
	require.NoError(t, err)
	defer conn.CloseNow()
	requests := [][]byte{
		[]byte(`{ "type":"response.create", "model":"gpt-6.1-sol", "stream":false, "future_root":true, "client_metadata":{"x-codex-turn-state":"MOCK_OWNED_STATE"} }`),
		[]byte(`{ "type":"response.create", "model":"gpt-6.1-sol", "stream":false, "previous_response_id":"resp_mock_ws_1", "future_root":true }`),
	}
	kinds := []coderws.MessageType{coderws.MessageBinary, coderws.MessageText}
	for i := range requests {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := conn.Write(ctx, kinds[i], requests[i])
		cancel()
		require.NoError(t, err)
		ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
		kind, response, err := conn.Read(ctx)
		cancel()
		require.NoError(t, err)
		require.Equal(t, kinds[i], kind)
		require.Equal(t, sha256.Sum256(responses[i]), sha256.Sum256(response))
	}
	conn.Close(coderws.StatusNormalClosure, "done")
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("local websocket shutdown timeout")
	}
	require.Equal(t, 1, dialer.DialCount())
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	require.Equal(t, kinds, upstream.writeTypes)
	require.Len(t, upstream.rawWrites, 2)
	for i := range requests {
		require.Equal(t, sha256.Sum256(requests[i]), sha256.Sum256(upstream.rawWrites[i]))
	}
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	require.Equal(t, "MOCK_OWNED_STATE", dialer.lastHeaders.Get("x-codex-turn-state"))
	require.Contains(t, dialer.lastURL, "future_control=MOCK_ONLY")
	require.Equal(t, "codex_vscode/0.159.2", dialer.lastHeaders.Get("User-Agent"))
	require.Equal(t, "responses=MOCK_NATIVE", dialer.lastHeaders.Get("OpenAI-Beta"))
}

func TestOpenAINativeOAuth_CrossAccountReplayRejectsBoundState(t *testing.T) {
	original := []byte(`{"type":"response.create","model":"gpt-6.1-sol","previous_response_id":"resp_mock_previous","reasoning":{"effort":"high"},"future_root":true,"input":[{"type":"reasoning","encrypted_content":"MOCK_ONLY","summary":[]},{"type":"message","role":"user","content":"MOCK_ONLY"}]}`)
	replay, ok, err := buildOpenAINativeWSFailoverPayload(original)
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, replay)
}

func TestOpenAINativeOAuth_SSEPreservesUnknownJSONEvents(t *testing.T) {
	c, rec := auditOAuthPolicyContext("/v1/responses")
	events := []string{`{"type":"response.future_extension","future_event":{"keep":true}}`, `{"type":"response.completed","response":{"id":"resp_mock_sse","future_response":true,"usage":{"input_tokens":1,"output_tokens":1}}}`}
	wire := "event: response.future_extension\ndata: " + events[0] + "\n\nevent: response.completed\ndata: " + events[1] + "\n\n"
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{auditOAuthPolicyResponse(200, http.Header{"Content-Type": {"text/event-stream"}}, wire)}}
	_, err := auditOAuthPolicyService(upstream).Forward(context.Background(), c, auditOAuthPolicyAccount(), []byte(`{"model":"gpt-6.1-sol","stream":true,"future_root":true}`))
	require.NoError(t, err)
	var observed []string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "data:") {
			observed = append(observed, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	require.Equal(t, events, observed)
}

func TestOpenAINativeOAuth_HeaderPresenceAndSessionIsolation(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		c, _ := auditOAuthPolicyContext(path + "?dup=a%2Fb&dup=a+b&empty=&flag")
		c.Request.Header["Accept"] = []string{"application/json; profile=future", "text/event-stream"}
		c.Request.Header.Set("session_id", "client_session")
		c.Request.Header.Set("Conversation-Id", "client_conversation")
		c.Request.Header.Set("Connection", "keep-alive, X-Mock-Hop")
		c.Request.Header.Set("X-Mock-Hop", "private_hop")
		c.Request.Header.Set("Forwarded", "for=192.0.2.1")
		c.Request.Header.Set("Via", "mock_gateway")
		svc := auditOAuthPolicyService(nil)
		account := auditOAuthPolicyAccount()
		req, err := svc.buildUpstreamRequest(context.Background(), c, account, []byte(`{"model":"gpt-6.1-sol"}`), "MOCK_ONLY", false, "body_cache_must_not_replace_headers", true)
		require.NoError(t, err)
		require.Equal(t, c.Request.URL.RawQuery, req.URL.RawQuery)
		require.Equal(t, c.Request.Header.Values("Accept"), req.Header.Values("Accept"))
		require.Equal(t, isolateOpenAIUserSessionIDForAccount(0, account.ID, "client_session"), req.Header.Get("session_id"))
		require.Equal(t, isolateOpenAIUserSessionIDForAccount(0, account.ID, "client_conversation"), req.Header.Get("Conversation-Id"))
		require.Empty(t, req.Header.Values("conversation_id"))
		for _, name := range []string{"Connection", "X-Mock-Hop", "Forwarded", "Via"} {
			require.Empty(t, req.Header.Values(name), name)
		}
		c.Request.Header.Del("session_id")
		c.Request.Header.Del("Conversation-Id")
		c.Request.Header.Del("Accept")
		req, err = svc.buildUpstreamRequest(context.Background(), c, account, nil, "MOCK_ONLY", false, "cache_key", true)
		require.NoError(t, err)
		require.Empty(t, req.Header.Values("session_id"))
		require.Empty(t, req.Header.Values("conversation_id"))
		require.Empty(t, req.Header.Values("Accept"))
		require.Empty(t, req.Header.Values("Content-Type"))
	}
}

func TestOpenAINativeOAuth_UnknownTurnStateRejectedBeforeHTTPProviderIO(t *testing.T) {
	c, rec := auditOAuthPolicyContext("/v1/responses")
	c.Request.Header.Set("session_id", "mock_session")
	c.Request.Header.Set(openAIWSTurnStateHeader, "state_from_other_account")
	upstream := &httpUpstreamSequenceRecorder{}
	_, err := auditOAuthPolicyService(upstream).Forward(context.Background(), c, auditOAuthPolicyAccount(), []byte(`{"model":"gpt-6.1-sol","stream":false}`))
	require.ErrorIs(t, err, errOpenAITurnStateAccountMismatch)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "turn_state_account_mismatch", gjson.Get(rec.Body.String(), "error.code").String())
	require.Zero(t, upstream.callCount)
}

func TestOpenAINativeOAuth_ResponseHeaderBoundary(t *testing.T) {
	svc := auditOAuthPolicyService(nil)
	input := http.Header{}
	input["X-Codex-Future"] = []string{"first", "second"}
	input.Set("X-Codex-Turn-State", "mock_owned")
	input.Set("Connection", "X-Mock-Hop")
	for _, name := range []string{"X-Mock-Hop", "Set-Cookie", "Authorization", "ChatGPT-Account-ID", "Content-Length", "Transfer-Encoding", "Via"} {
		input.Set(name, "private")
	}
	output := http.Header{}
	svc.writeOpenAINativeResponseHeaders(output, input)
	require.Equal(t, []string{"first", "second"}, output.Values("X-Codex-Future"))
	require.Equal(t, "mock_owned", output.Get("X-Codex-Turn-State"))
	for _, name := range []string{"Connection", "X-Mock-Hop", "Set-Cookie", "Authorization", "ChatGPT-Account-ID", "Content-Length", "Transfer-Encoding", "Via"} {
		require.Empty(t, output.Values(name), name)
	}
	svc.cfg.Security.ResponseHeaders.Enabled = true
	svc.cfg.Security.ResponseHeaders.ForceRemove = []string{"X-Codex-Future"}
	output = http.Header{}
	svc.writeOpenAINativeResponseHeaders(output, input)
	require.Empty(t, output.Values("X-Codex-Future"))
	require.Equal(t, "mock_owned", output.Get("X-Codex-Turn-State"))
}

func TestOpenAINativeOAuth_ErrorCredentialRedactionPreservesEnvelope(t *testing.T) {
	c, rec := auditOAuthPolicyContext("/v1/responses")
	wire := `{"error":{"code":"fixture_validation","param":"input","message":"Bearer MOCK_ONLY","future":true}}`
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{auditOAuthPolicyResponse(400, http.Header{"Content-Type": {"application/json"}}, wire)}}
	_, err := auditOAuthPolicyService(upstream).Forward(context.Background(), c, auditOAuthPolicyAccount(), []byte(`{"model":"gpt-6.1-sol","stream":false}`))
	require.Error(t, err)
	require.Equal(t, strings.ReplaceAll(wire, "MOCK_ONLY", "[REDACTED]"), rec.Body.String())
	require.Equal(t, 1, upstream.callCount)
}

func TestOpenAINativeOAuth_FreshWSFailoverIsByteIdenticalAndBoundStateIsRejected(t *testing.T) {
	fresh := []byte("{\n \"type\":\"response.create\", \"model\":\"gpt-6.1-sol\", \"future\":{\"keep\":true}, \"input\":[] }")
	tracker := newOpenAIWSPassthroughReplayTracker(NewOpenAIWSStateStore(nil), 0)
	tracker.RegisterTurn(1, coderws.MessageBinary, fresh)
	failure, ok := tracker.BuildFailoverError(11, nil)
	require.True(t, ok)
	require.Equal(t, coderws.MessageBinary, failure.MessageType())
	require.Equal(t, fresh, failure.ReplayPayload())
	for _, field := range []string{
		`"previous_response_id":null`,
		`"input":[{"type":"reasoning","encrypted_content":"mock_opaque"}]`,
		`"input":[{"type":"item_reference","id":"mock_item"}]`,
		`"input":[{"type":"function_call_output","call_id":"mock_call","output":"ok"}]`,
		`"client_metadata":{"x-codex-turn-state":"mock_bound"}`,
	} {
		t.Run(field, func(t *testing.T) {
			payload, portable, err := buildOpenAINativeWSFailoverPayload([]byte(`{"type":"response.create",` + field + `}`))
			require.NoError(t, err)
			require.False(t, portable)
			require.Nil(t, payload)
		})
	}
}

func TestOpenAINativeOAuth_MetadataTurnStateOwnershipAndConcurrentTokens(t *testing.T) {
	c, _ := auditOAuthPolicyContext("/v1/responses")
	c.Request.Header.Set("session_id", "mock_session")
	account := auditOAuthPolicyAccount()
	svc := auditOAuthPolicyService(nil)
	hash := svc.openAISessionHashForTurnState(c, "")
	for _, state := range []string{"mock_state_one", "mock_state_two"} {
		svc.observeOpenAINativeMetadataTurnState(c, account, hash, []byte(`{"type":"response.metadata","headers":{"X-Codex-Turn-State":"`+state+`"}}`))
	}
	for _, state := range []string{"mock_state_one", "mock_state_two"} {
		frame := []byte(`{"type":"response.create","client_metadata":{"x-codex-turn-state":"` + state + `"}}`)
		require.NoError(t, svc.validateOpenAINativeFrameTurnState(c, account, frame))
		other := *account
		other.ID++
		require.ErrorIs(t, svc.validateOpenAINativeFrameTurnState(c, &other, frame), errOpenAITurnStateAccountMismatch)
	}
	frame := []byte(`{"type":"response.create","client_metadata":{"x-codex-turn-state":"unknown_state"}}`)
	require.ErrorIs(t, svc.validateOpenAINativeFrameTurnState(c, account, frame), errOpenAITurnStateAccountMismatch)
	require.NoError(t, svc.validateOpenAINativeFrameTurnState(c, account, []byte(`{"type":"response.create","client_metadata":{"future":true}}`)))
}

func TestOpenAINativeOAuth_SSEToolNamesAndMetadataRemainNative(t *testing.T) {
	c, rec := auditOAuthPolicyContext("/v1/responses")
	c.Request.Header.Set("session_id", "mock_thread")
	events := []string{
		`{"type":"response.metadata","headers":{"x-codex-turn-state":"mock_frame_state"},"future":true}`,
		`{"type":"response.output_item.added","item":{"type":"function_call","name":"apply_patch","arguments":"MOCK_ONLY"},"tool_calls":[{"function":{"name":"update_plan"}}]}`,
		`{"type":"response.completed","response":{"id":"resp_mock_tool","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`,
	}
	wire := "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{auditOAuthPolicyResponse(200, http.Header{"Content-Type": {"text/event-stream"}}, wire)}}
	svc := auditOAuthPolicyService(upstream)
	svc.toolCorrector = NewCodexToolCorrector()
	account := auditOAuthPolicyAccount()
	_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-6.1-sol","stream":true}`))
	require.NoError(t, err)
	var observed []string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if data, ok := extractOpenAISSEDataLine(line); ok {
			observed = append(observed, data)
		}
	}
	require.Equal(t, events, observed)
	c.Request.Header.Set(openAIWSTurnStateHeader, "mock_frame_state")
	require.NoError(t, svc.validateOpenAINativeTurnState(account, 0, svc.openAISessionHashForTurnState(c, ""), c.Request.Header))
}

func TestOpenAINativeOAuth_WSRejectsUnverifiedFrameStateBeforeDial(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	dialer := &openAIWSCaptureDialer{}
	svc := &OpenAIGatewayService{cfg: cfg, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), openaiWSPassthroughDialer: dialer}
	account := auditOAuthPolicyAccount()
	account.Extra = map[string]any{"responses_websockets_v2_enabled": true}
	errors := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			errors <- err
			return
		}
		defer conn.CloseNow()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r
		payload := []byte(`{"type":"response.create","model":"gpt-6.1-sol","client_metadata":{"x-codex-turn-state":"unknown_account_state"}}`)
		errors <- svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, account, "MOCK_ONLY", coderws.MessageText, payload, nil)
	}))
	defer server.Close()
	c, _ := auditOAuthPolicyContext("/v1/responses")
	c.Request.Header.Set("session_id", "mock_session")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &coderws.DialOptions{HTTPHeader: c.Request.Header})
	require.NoError(t, err)
	defer conn.CloseNow()
	select {
	case err := <-errors:
		require.ErrorIs(t, err, errOpenAITurnStateAccountMismatch)
		var closeError *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeError)
		require.Equal(t, coderws.StatusPolicyViolation, closeError.StatusCode())
	case <-ctx.Done():
		t.Fatal("state rejection did not finish")
	}
	require.Zero(t, dialer.DialCount())
}

func TestOpenAINativeOAuth_TurnStateOwnershipSurvivesRestartWithoutPersistingToken(t *testing.T) {
	cache := &stubGatewayCache{}
	first := &OpenAIGatewayService{cache: cache, cfg: &config.Config{}}
	account := auditOAuthPolicyAccount()
	first.bindOpenAINativeTurnState(account, 55, "old_session", "MOCK_OPAQUE_STATE")
	for key := range cache.sessionBindings {
		require.NotContains(t, key, "MOCK_OPAQUE_STATE")
		require.True(t, strings.HasPrefix(key, "openai_turn_state:"))
	}
	second := &OpenAIGatewayService{cache: cache, cfg: &config.Config{}}
	headers := make(http.Header)
	headers.Set(openAIWSTurnStateHeader, "MOCK_OPAQUE_STATE")
	// A restart and a different transport/session header do not change owner.
	require.NoError(t, second.validateOpenAINativeTurnState(account, 55, "", headers))
	require.NoError(t, second.validateOpenAINativeTurnState(account, 55, "new_session", headers))
	require.ErrorIs(t, second.validateOpenAINativeTurnState(account, 56, "old_session", headers), errOpenAITurnStateAccountMismatch)
	other := *account
	other.ID++
	require.ErrorIs(t, second.validateOpenAINativeTurnState(&other, 55, "old_session", headers), errOpenAITurnStateAccountMismatch)
}

func TestOpenAINativeOAuth_TurnStateSelectsOwnerBeforeStaleSessionSticky(t *testing.T) {
	group := int64(7)
	owner := *auditOAuthPolicyAccount()
	owner.Status, owner.Schedulable = StatusActive, true
	peer := owner
	peer.ID++
	cache := &stubGatewayCache{}
	svc := &OpenAIGatewayService{cache: cache, cfg: &config.Config{},
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{owner, peer}},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{})}
	svc.bindOpenAINativeTurnState(&owner, 55, "mock_session", "MOCK_ROUTING_STATE")
	for _, fromFrame := range []bool{false, true} {
		t.Run(map[bool]string{false: "header", true: "frame"}[fromFrame], func(t *testing.T) {
			c, _ := auditOAuthPolicyContext("/v1/responses")
			payload := []byte(`{"model":"gpt-5.5"}`)
			if fromFrame {
				payload = []byte(`{"model":"gpt-5.5","client_metadata":{"x-codex-turn-state":"MOCK_ROUTING_STATE"}}`)
			} else {
				c.Request.Header.Set(openAIWSTurnStateHeader, "MOCK_ROUTING_STATE")
			}
			require.NoError(t, svc.BindStickySession(c.Request.Context(), &group, "mock_session", peer.ID))
			selection, decision, err := svc.SelectAccountForNativeCodexRequest(c, &group, 55, "", "mock_session", "gpt-5.5", nil, OpenAIUpstreamTransportHTTPSSE, payload)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, owner.ID, selection.Account.ID)
			require.Equal(t, "turn_state", decision.Layer)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			selection, _, err = svc.SelectAccountForNativeCodexRequest(c, &group, 55, "", "mock_session", "gpt-5.5", map[int64]struct{}{owner.ID: {}}, OpenAIUpstreamTransportHTTPSSE, payload)
			require.ErrorIs(t, err, errOpenAITurnStateAccountMismatch)
			require.Nil(t, selection, "a bound continuation must never fall through to a peer")
		})
	}
}

func TestOpenAINativeOAuth_HTTPRejectsUnknownBodyTurnStateBeforeProviderIO(t *testing.T) {
	c, rec := auditOAuthPolicyContext("/v1/responses")
	upstream := &httpUpstreamSequenceRecorder{}
	_, err := auditOAuthPolicyService(upstream).Forward(context.Background(), c, auditOAuthPolicyAccount(), []byte(`{"model":"gpt-5.5","client_metadata":{"x-codex-turn-state":"MOCK_UNKNOWN"}}`))
	require.ErrorIs(t, err, errOpenAITurnStateAccountMismatch)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Zero(t, upstream.callCount)
}

func TestOpenAINativeOAuth_PreviousResponseOwnerSurvivesHTTPFallback(t *testing.T) {
	group := int64(7)
	owner := *auditOAuthPolicyAccount()
	owner.Status, owner.Schedulable = StatusActive, true
	owner.Extra = map[string]any{"openai_ws_force_http": true}
	peer := owner
	peer.ID++
	svc := &OpenAIGatewayService{cache: &stubGatewayCache{}, cfg: &config.Config{},
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{owner, peer}},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{})}
	ctx := context.Background()
	require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccountForUser(ctx, 55, "resp_mock_http", owner.ID, time.Hour))
	require.NoError(t, svc.BindStickySession(ctx, &group, "mock_session", peer.ID))
	selection, decision, err := svc.SelectAccountWithSchedulerForUser(ctx, &group, 55, "resp_mock_http", "mock_session", "gpt-5.5", nil, OpenAIUpstreamTransportHTTPSSE)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, owner.ID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerPreviousResponse, decision.Layer)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}
