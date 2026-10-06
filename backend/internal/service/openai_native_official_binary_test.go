//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// This opt-in test runs the real official executable and the real SAIAI
// executable. The Python driver terminates TLS and records digests in memory
// before forwarding to SAIAI. All provider transports are replaced with local
// mocks; production credentials, captures, DB and Redis are never read.
func TestOpenAINativeOAuth_OfficialBinaryLoopback(t *testing.T) {
	codex, client := os.Getenv("SAIAI_PROOF_CODEX_BINARY"), os.Getenv("SAIAI_PROOF_CLIENT_BINARY")
	if codex == "" || client == "" {
		t.Skip("set SAIAI_PROOF_CODEX_BINARY and SAIAI_PROOF_CLIENT_BINARY for the isolated official-binary proof")
	}
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled, cfg.Gateway.OpenAIWS.OAuthEnabled, cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true, true, true
	account := auditOAuthPolicyAccount()
	account.Extra = map[string]any{"responses_websockets_v2_enabled": true}
	lab := &nativeOfficialProofLab{}
	svc := auditOAuthPolicyService(nil)
	svc.cfg, svc.httpUpstream, svc.openaiWSPassthroughDialer = cfg, lab, lab
	svc.openaiWSResolver = NewOpenAIWSProtocolResolver(cfg)
	modelsClient, err := httpclient.GetClient(httpclient.Options{Timeout: codexModelsManifestRequestTimeout, ResponseHeaderTimeout: 10 * time.Second})
	require.NoError(t, err)
	original := modelsClient.Transport
	modelsClient.Transport = auditOAuthPolicyRoundTripper(func(r *http.Request) (*http.Response, error) {
		lab.record("provider", "models", r, nil, 0)
		return auditOAuthPolicyResponse(200, http.Header{"Content-Type": {"application/json"}, "X-Models-Etag": {"MOCK_MODELS"}}, `{"models":[]}`), nil
	})
	defer func() { modelsClient.Transport = original }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_proof/reset" {
			lab.mu.Lock()
			lab.records, lab.response = nil, 0
			lab.mu.Unlock()
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/_proof/report" {
			lab.mu.Lock()
			defer lab.mu.Unlock()
			_ = json.NewEncoder(w).Encode(lab.records)
			return
		}
		if r.URL.Path == "/_proof/events" {
			_ = json.NewEncoder(w).Encode(nativeOfficialMockEvents(r.URL.Query().Get("response"), r.URL.Query().Get("tool") == "1"))
			return
		}
		c, _ := gin.CreateTestContext(w)
		c.Request = r
		c.Set("api_key", &APIKey{ID: 7, UserID: 55})
		if strings.HasSuffix(r.URL.Path, "/models") {
			lab.record("gateway", "models", r, nil, 0)
			result, fetchErr := svc.FetchNativeCodexModelsManifest(r.Context(), account, r.URL.RawQuery, r.Header.Get("If-None-Match"), r.Header)
			if fetchErr != nil {
				http.Error(w, "local model manifest fixture failed", 500)
				return
			}
			svc.WriteNativeCodexResponseHeaders(w.Header(), result.Headers)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(result.Body)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, "only the local proof endpoints are enabled", 404)
			return
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			lab.record("gateway", "handshake", r, nil, 0)
			w.Header()["X-Codex-Proof-Control"] = []string{"first", "second"}
			conn, acceptErr := coderws.Accept(c.Writer, r, nil)
			if acceptErr != nil {
				return
			}
			defer conn.CloseNow()
			conn.SetReadLimit(16 * 1024 * 1024)
			kind, first, readErr := conn.Read(r.Context())
			if readErr != nil {
				return
			}
			lab.record("gateway", "frame", r, first, int(kind))
			hooks := &OpenAIWSIngressHooks{}
			hooks.OnClientTurn = func(turn int, payload []byte) error {
				if turn > 1 {
					lab.record("gateway", "frame", r, payload, int(coderws.MessageText))
				}
				return nil
			}
			_ = svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, account, "MOCK_ONLY", kind, first, hooks)
			return
		}
		wire, readErr := io.ReadAll(io.LimitReader(r.Body, 16*1024*1024))
		if readErr != nil {
			return
		}
		lab.record("gateway", "http", r, wire, 0)
		if r.Header.Get("Content-Encoding") == "zstd" {
			decoder, decodeErr := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
			if decodeErr != nil {
				return
			}
			decoded, decodeErr := decoder.DecodeAll(wire, nil)
			decoder.Close()
			if decodeErr != nil {
				return
			}
			var parsed map[string]any
			if json.Unmarshal(decoded, &parsed) != nil {
				return
			}
			c.Set(OpenAIParsedRequestBodyKey, parsed)
		}
		SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
		_, _ = svc.Forward(r.Context(), c, account, wire)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	driver, err := filepath.Abs("testdata/native_official_codex_probe.py")
	require.NoError(t, err)
	command := exec.CommandContext(ctx, "python3", driver, "--codex", codex, "--client", client, "--gateway", server.URL)
	// The driver creates its own allowlisted environment and isolated profiles.
	output, err := command.CombinedOutput()
	require.NoError(t, err, "isolated official-binary probe: %s", output)
	var report map[string]any
	require.NoError(t, json.Unmarshal(output, &report), "probe must emit only sanitized JSON")
	require.Equal(t, "pass", report["result"])
	if path := os.Getenv("SAIAI_PROOF_REPORT"); path != "" {
		require.NoError(t, os.WriteFile(path, output, 0600))
	}
	t.Logf("official-binary proof: %s", output)
}

type nativeOfficialProofLab struct {
	mu       sync.Mutex
	records  []map[string]any
	response int
}

func (l *nativeOfficialProofLab) record(stage, kind string, r *http.Request, payload []byte, messageType int) {
	sum := sha256.Sum256(payload)
	headers := r.Header.Clone()
	for _, name := range []string{"Authorization", "Cookie", "Chatgpt-Account-Id"} {
		headers.Del(name)
	}
	item := map[string]any{"stage": stage, "kind": kind, "method": r.Method, "path": r.URL.Path,
		"query": r.URL.RawQuery, "headers": headers, "sha256": hex.EncodeToString(sum[:]), "size": len(payload), "message_type": messageType}
	if stage == "provider" {
		item["selected_auth"] = r.Header.Get("Authorization") == "Bearer MOCK_ONLY"
		item["selected_account"] = r.Header.Get("Chatgpt-Account-Id") == "MOCK_ONLY_ACCOUNT"
		item["no_cookie"] = r.Header.Get("Cookie") == ""
	}
	decoded := payload
	if r.Header.Get("Content-Encoding") == "zstd" && len(payload) > 0 {
		decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		if err == nil {
			decoded, _ = decoder.DecodeAll(payload, nil)
			decoder.Close()
		}
	}
	if len(decoded) > 0 {
		digest := sha256.Sum256(decoded)
		item["decoded_sha256"] = hex.EncodeToString(digest[:])
		item["model"] = gjson.GetBytes(decoded, "model").String()
		item["reasoning"] = gjson.GetBytes(decoded, "reasoning").Value()
		item["has_turn_state"] = gjson.GetBytes(decoded, "client_metadata.x-codex-turn-state").Exists() || r.Header.Get(openAIWSTurnStateHeader) != ""
		item["has_tool_output"] = strings.Contains(string(decoded), `"function_call_output"`)
		item["warmup"] = gjson.GetBytes(decoded, "generate").Exists() && !gjson.GetBytes(decoded, "generate").Bool()
	}
	l.mu.Lock()
	l.records = append(l.records, item)
	l.mu.Unlock()
}

func (l *nativeOfficialProofLab) next(payload []byte) [][]byte {
	warmup := gjson.GetBytes(payload, "generate")
	if warmup.Exists() && !warmup.Bool() {
		return nativeOfficialMockEvents("resp_mock_warmup", false)
	}
	l.mu.Lock()
	l.response++
	number := l.response
	l.mu.Unlock()
	return nativeOfficialMockEvents(fmt.Sprintf("resp_mock_%d", number), number == 1)
}

func nativeOfficialMockEvents(responseID string, tool bool) [][]byte {
	if responseID == "" {
		responseID = "resp_mock_1"
	}
	items := []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": responseID}},
		{"type": "response.metadata", "headers": map[string]any{"x-codex-turn-state": "MOCK_FRAME_STATE"}},
	}
	if tool {
		items = append(items, map[string]any{"type": "response.output_item.done", "item": map[string]any{
			"type": "function_call", "call_id": "mock_plan_call", "name": "update_plan",
			"arguments": `{"plan":[{"step":"Mock protocol check","status":"completed"}]}`,
		}})
	} else {
		items = append(items, map[string]any{"type": "response.output_item.done", "item": map[string]any{
			"type": "message", "role": "assistant", "id": "msg_mock_1",
			"content": []map[string]any{{"type": "output_text", "text": "MOCK_ONLY_SUCCESS"}},
		}})
	}
	items = append(items, map[string]any{"type": "response.completed", "response": map[string]any{
		"id": responseID, "status": "completed", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
	}})
	var result [][]byte
	for _, item := range items {
		encoded, _ := json.Marshal(item)
		result = append(result, encoded)
	}
	return result
}

func (l *nativeOfficialProofLab) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	l.record("provider", "http", r, body, 0)
	events := l.next(body)
	var wire strings.Builder
	for _, event := range events {
		wire.WriteString("data: " + string(event) + "\n\n")
	}
	return auditOAuthPolicyResponse(200, http.Header{"Content-Type": {"text/event-stream"}, "X-Codex-Turn-State": {"MOCK_FRAME_STATE"}}, wire.String()), nil
}

func (l *nativeOfficialProofLab) DoWithTLS(r *http.Request, proxy string, id int64, concurrency int, _ bool) (*http.Response, error) {
	return l.Do(r, proxy, id, concurrency)
}

func (l *nativeOfficialProofLab) Dial(_ context.Context, rawURL string, headers http.Header, _ string) (openAIWSClientConn, int, http.Header, error) {
	u, _ := url.Parse(rawURL)
	l.record("provider", "handshake", &http.Request{Method: "GET", URL: u, Header: headers}, nil, 0)
	return &nativeOfficialProofWS{lab: l, request: &http.Request{Method: "GET", URL: u, Header: headers}, frames: make(chan []byte, 32), done: make(chan struct{})}, 101, http.Header{"X-Codex-Turn-State": {"MOCK_HANDSHAKE_STATE"}}, nil
}

type nativeOfficialProofWS struct {
	lab     *nativeOfficialProofLab
	request *http.Request
	frames  chan []byte
	done    chan struct{}
	once    sync.Once
}

func (c *nativeOfficialProofWS) Read(ctx context.Context) (coderws.MessageType, []byte, error) {
	select {
	case payload := <-c.frames:
		return coderws.MessageText, payload, nil
	case <-c.done:
		return 0, nil, io.EOF
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

func (c *nativeOfficialProofWS) Write(ctx context.Context, kind coderws.MessageType, payload []byte) error {
	c.lab.record("provider", "frame", c.request, payload, int(kind))
	for _, event := range c.lab.next(payload) {
		select {
		case c.frames <- event:
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return io.EOF
		}
	}
	return nil
}

func (c *nativeOfficialProofWS) Close() error { c.once.Do(func() { close(c.done) }); return nil }

func (c *nativeOfficialProofWS) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	return c.Read(ctx)
}
func (c *nativeOfficialProofWS) WriteFrame(ctx context.Context, kind coderws.MessageType, payload []byte) error {
	return c.Write(ctx, kind, payload)
}
func (c *nativeOfficialProofWS) ReadMessage(ctx context.Context) ([]byte, error) {
	_, payload, err := c.Read(ctx)
	return payload, err
}
func (c *nativeOfficialProofWS) WriteJSON(ctx context.Context, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.Write(ctx, coderws.MessageText, payload)
}
func (c *nativeOfficialProofWS) Ping(context.Context) error { return nil }
