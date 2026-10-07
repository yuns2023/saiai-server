//go:build unit

package routes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The same fixture covers real public routing/auth/billing checks and actual
// HTTP/WS transports. Its account proxy terminates exclusively in loopback;
// no provider hostname is resolved or connected to on the Internet. Persistent
// repositories/Redis are in-memory doubles. Captures are never written to disk.
func TestNativeCodexGatewayRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	lab := &nativeRoutesLab{bindings: make(map[string]int64)}
	provider := httptest.NewUnstartedServer(http.HandlerFunc(lab.provider))
	provider.TLS = nativeRoutesTLS(t)
	provider.StartTLS()
	defer provider.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "chatgpt.com:443" {
			lab.blocked.Add(1)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		upstream, err := net.DialTimeout("tcp", provider.Listener.Addr().String(), 3*time.Second)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		downstream, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer downstream.Close()
		lab.mu.Lock()
		lab.connections = append(lab.connections, upstream, downstream)
		lab.mu.Unlock()
		_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffer.Flush()
		lab.tunnels.Add(1)
		done := make(chan struct{})
		go func() { _, _ = io.Copy(upstream, buffer); close(done) }()
		_, _ = io.Copy(downstream, upstream)
		_ = downstream.Close()
		<-done
	}))
	defer proxy.Close()
	host, port, err := net.SplitHostPort(proxy.Listener.Addr().String())
	require.NoError(t, err)
	proxyPort, err := strconv.Atoi(port)
	require.NoError(t, err)
	groupID, proxyID := int64(7), int64(1)
	account := service.Account{ID: 81, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID},
		Credentials: map[string]any{"access_token": "MOCK_ONLY", "chatgpt_account_id": "MOCK_ONLY_ACCOUNT"},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true}, ProxyID: &proxyID,
		Proxy: &service.Proxy{ID: proxyID, Protocol: "http", Host: host, Port: proxyPort}}
	user := &service.User{ID: 55, Status: service.StatusActive, Role: service.RoleUser, Balance: 100, Concurrency: 1}
	key := &service.APIKey{ID: 7, UserID: user.ID, Key: "TEST_ONLY_NATIVE_OAUTH_PROOF", Status: service.StatusActive, User: user,
		GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive,
			Hydrated: true, RateMultiplier: 1, CodexClientPolicy: "local_proxy_only"}}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.MaxBodySize = 16 << 20
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Pricing.DataDir, cfg.Pricing.UpdateIntervalHours = t.TempDir(), 24
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Pricing.DataDir, "model_pricing.json"), []byte(`{"gpt-image-2":{"input_cost_per_token":0.000005,"input_cost_per_image_token":0.000008,"output_cost_per_image_token":0.00003}}`), 0600))
	pricing := service.NewPricingService(cfg, nil)
	require.NoError(t, pricing.Initialize()) // fresh local file; no remote client
	defer pricing.Stop()
	cfg.Gateway.OpenAIWS.Enabled, cfg.Gateway.OpenAIWS.OAuthEnabled, cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true, true, true
	keyService := service.NewAPIKeyService(&nativeRoutesKeyRepo{key: key, lab: lab}, nil, nil, nil, nil, nil, cfg)
	billingCache := service.NewBillingCacheService(nil, &nativeRoutesUserRepo{user: user, lab: lab}, nil, nil, cfg)
	defer billingCache.Stop()
	concurrencyCache := &nativeRoutesConcurrency{}
	concurrency := service.NewConcurrencyService(concurrencyCache)
	cache := &nativeRoutesCache{lab: lab}
	svc := service.NewOpenAIGatewayService(&nativeRoutesAccountRepo{account: account}, &nativeRoutesUsageRepo{lab: lab}, lab,
		nil, nil, nil, cache, cfg, nil, concurrency, service.NewBillingService(cfg, pricing), nil, billingCache,
		repository.NewHTTPUpstream(cfg), &service.DeferredService{}, nil)
	defer svc.CloseOpenAIWSPool()
	gatewayHandler := handler.NewOpenAIGatewayHandler(svc, concurrency, billingCache, keyService, nil, nil, nil, cfg, nil)
	router := gin.New()
	// Observe before the versioned public middleware chain. Body reads are
	// restored exactly, and the WS observer only tees reads after Hijack.
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestID, fmt.Sprintf("mock_ingress_%d", lab.sequence.Add(1))))
		switch {
		case strings.HasSuffix(c.Request.URL.Path, "/models"):
			lab.record("gateway", "models", c.Request, nil, 0)
		case strings.EqualFold(c.GetHeader("Upgrade"), "websocket"):
			lab.record("gateway", "handshake", c.Request, nil, 0)
			c.Writer.Header()["X-Codex-Proof-Control"] = []string{"first", "second"}
			c.Writer = &nativeRoutesObserverWriter{ResponseWriter: c.Writer, lab: lab, request: c.Request}
		case c.Request.Method == http.MethodPost:
			body, err := io.ReadAll(io.LimitReader(c.Request.Body, 16<<20))
			if err != nil {
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
			lab.record("gateway", "http", c.Request, body, 0)
		}
		c.Next()
	})
	RegisterGatewayRoutes(router, &handler.Handlers{Gateway: &handler.GatewayHandler{}, OpenAIGateway: gatewayHandler},
		middleware.NewAPIKeyAuthMiddleware(keyService, nil, nil, cfg), keyService, nil, nil, nil, cfg)
	router.GET("/_proof/reset", func(c *gin.Context) {
		lab.reset()
		lab.mu.Lock()
		lab.imagegen = c.Query("imagegen") == "1"
		lab.mu.Unlock()
		c.Status(204)
	})
	router.GET("/_proof/report", func(c *gin.Context) { lab.mu.Lock(); defer lab.mu.Unlock(); c.JSON(200, lab.records) })
	router.GET("/_proof/control", func(c *gin.Context) { c.JSON(200, lab.control()) })
	router.GET("/_proof/events", func(c *gin.Context) {
		c.JSON(200, nativeRoutesToolEvents(c.Query("response"), c.Query("tool") == "1", c.Query("imagegen") == "1"))
	})
	router.GET("/_proof/image-response", func(c *gin.Context) { c.Data(200, "application/json", nativeRoutesImageResponse()) })
	gateway := httptest.NewServer(router)
	defer gateway.Close()
	defer lab.closeTunnels()
	request := func(method, path string, body []byte, compressed bool, authorized bool) (*http.Response, []byte) {
		req, err := http.NewRequest(method, gateway.URL+path, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header = http.Header{"User-Agent": {"codex_cli_rs/0.159.2"}, "Originator": {"codex_exec"},
			"Version": {"0.159.2"}, "Chatgpt-Account-Id": {"mock_client_account"}, "X-Codex-Future": {"first", "second"},
			"Session_id": {"MOCK_SESSION_A", "MOCK_SESSION_B"}, "Session-Id": {"MOCK_HYPHEN_SESSION"},
			"Conversation_id": {"MOCK_CONVERSATION"}, "Conversation-Id": {"MOCK_HYPHEN_CONVERSATION_A", "MOCK_HYPHEN_CONVERSATION_B"}}
		if strings.HasPrefix(path, "/v1/codex/images/") {
			req.Header.Set("Content-Type", "application/json")
		}
		if authorized {
			req.Header.Set("Authorization", "Bearer "+key.Key)
		}
		if compressed {
			req.Header.Set("Content-Encoding", "zstd")
		}
		client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp, data
	}
	t.Run("auth_rejects_before_egress", func(t *testing.T) {
		lab.reset()
		resp, _ := request("POST", "/v1/responses", []byte(`{"model":"gpt-5.5"}`), false, false)
		require.Equal(t, 401, resp.StatusCode)
		require.Empty(t, lab.providerRecords())
	})
	t.Run("models_without_client_version_preserves_native_catalog", func(t *testing.T) {
		lab.reset()
		resp, body := request("GET", "/v1/models?future=first%2Fsecond&future=last+value&flag", nil, false, true)
		require.Equal(t, 200, resp.StatusCode, string(body))
		require.JSONEq(t, `{"models":[],"future_catalog":true}`, string(body))
		seen := lab.providerRecords()
		require.Len(t, seen, 1)
		require.Equal(t, "future=first%2Fsecond&future=last+value&flag", seen[0]["query"])
	})
	for _, compact := range []bool{false, true} {
		for _, compressed := range []bool{false, true} {
			t.Run(fmt.Sprintf("wire_body_compact_%t_zstd_%t", compact, compressed), func(t *testing.T) {
				lab.reset()
				body := []byte("{ \"model\":\"gpt-5.5\",\"reasoning\":{\"effort\":\"high\"},\"stream\":true,\"previous_response_id\":null,\"future\":[null,true],\"input\":\"hello\" }")
				if compressed {
					encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
					require.NoError(t, err)
					body = encoder.EncodeAll(body, nil)
					encoder.Close()
				}
				path := "/v1/responses"
				if compact {
					path += "/compact"
				}
				resp, data := request("POST", path+"?future=a%2Fb&future=a+b&flag", body, compressed, true)
				require.Equal(t, 200, resp.StatusCode, string(data))
				seen := lab.providerRecords()
				require.Len(t, seen, 1)
				sum := sha256.Sum256(body)
				require.Equal(t, hex.EncodeToString(sum[:]), seen[0]["sha256"])
				require.Equal(t, "future=a%2Fb&future=a+b&flag", seen[0]["query"])
				require.True(t, seen[0]["selected_auth"].(bool))
				require.True(t, seen[0]["selected_account"].(bool))
				headers := seen[0]["headers"].(http.Header)
				require.Equal(t, []string{"MOCK_SESSION_A", "MOCK_SESSION_B"}, headers.Values("session_id"))
				require.Equal(t, []string{"MOCK_HYPHEN_SESSION"}, headers.Values("session-id"))
				require.Equal(t, []string{"MOCK_CONVERSATION"}, headers.Values("conversation_id"))
				require.Equal(t, []string{"MOCK_HYPHEN_CONVERSATION_A", "MOCK_HYPHEN_CONVERSATION_B"}, headers.Values("conversation-id"))
				if !compact {
					require.Equal(t, 1, lab.control()["usage_records"])
				}
			})
		}
	}
	t.Run("unverified_state_rejects_before_egress", func(t *testing.T) {
		lab.reset()
		resp, body := request("POST", "/v1/responses", []byte(`{"model":"gpt-5.5","client_metadata":{"x-codex-turn-state":"MOCK_UNKNOWN"}}`), false, true)
		require.Equal(t, 409, resp.StatusCode, string(body))
		require.Empty(t, lab.providerRecords())
	})
	for _, operation := range []string{"generations", "edits"} {
		t.Run("native_images_encoded_"+operation, func(t *testing.T) {
			lab.reset()
			body := []byte(`{ "model":"gpt-image-2", "prompt":"TEST_ONLY", "images":[{"image_url":"data:image/png;base64,VEVTVF9PTkxZ"}], "future":true }`)
			encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
			require.NoError(t, err)
			encoded := encoder.EncodeAll(body, nil)
			encoder.Close()
			resp, data := request("POST", "/v1/codex/images/"+operation+"?opaque=a%2Fb&opaque=%2B", encoded, true, true)
			require.Equal(t, 200, resp.StatusCode, string(data))
			require.Equal(t, nativeRoutesImageResponse(), data)
			require.Equal(t, "TEST_ONLY-image-request", resp.Header.Get("X-Codex-Imagegen-Request-Id"))
			seen := lab.providerRecords()
			require.Len(t, seen, 1)
			require.Equal(t, "/backend-api/codex/images/"+operation, seen[0]["path"])
			require.Equal(t, "opaque=a%2Fb&opaque=%2B", seen[0]["query"])
			digest := sha256.Sum256(encoded)
			require.Equal(t, hex.EncodeToString(digest[:]), seen[0]["sha256"])
			require.Eventually(t, func() bool { return lab.control()["usage_records"] == 1 }, time.Second, time.Millisecond)
			require.Equal(t, 1, lab.control()["billing_applications"])
			require.Zero(t, concurrencyCache.accounts.Load())
			require.Zero(t, concurrencyCache.users.Load())
		})
	}
	for _, imagegen := range []bool{false, true} {
		name := "official_binary"
		if imagegen {
			name = "official_binary_imagegen"
		}
		t.Run(name, func(t *testing.T) {
			codex, client := os.Getenv("SAIAI_PROOF_CODEX_BINARY"), os.Getenv("SAIAI_PROOF_CLIENT_BINARY")
			if codex == "" || client == "" {
				t.Skip("set official Codex and candidate SAIAI paths for the production-route wire proof")
			}
			driver, err := filepath.Abs("../../service/testdata/native_official_codex_probe.py")
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
			defer cancel()
			args := []string{driver, "--codex", codex, "--client", client, "--gateway", gateway.URL, "--production-routes"}
			if imagegen {
				args = append(args, "--imagegen")
			}
			command := exec.CommandContext(ctx, "python3", args...)
			output, err := command.CombinedOutput()
			require.NoError(t, err, "isolated public-route proof: %s", output)
			var report map[string]any
			require.NoError(t, json.Unmarshal(output, &report))
			require.Equal(t, "pass", report["result"])
			if path := os.Getenv("SAIAI_PROOF_REPORT"); path != "" {
				if imagegen {
					path += ".imagegen.json"
				}
				require.NoError(t, os.WriteFile(path, output, 0600))
			}
			t.Logf("public-route proof: %s", output)
		})
	}
	t.Run("attempt_budget_spans_http_ws_and_rejected_reconnects", func(t *testing.T) {
		lab.reset()
		cfg.Gateway.OpenAIProviderAttemptBudget = config.OpenAIProviderAttemptBudgetConfig{Enabled: true,
			ID: "TEST_ONLY-public-routes", APIKeyID: key.ID, MaxAttempts: 5,
			ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
		cache.budgetUsed = new(int64) // explicitly arm this in-memory test fixture
		defer func() { cfg.Gateway.OpenAIProviderAttemptBudget.Enabled = false }()
		body := []byte(`{ "model":"gpt-5.5", "stream":true, "future":true, "input":[] }`)
		resp, data := request("POST", "/v1/responses", body, false, true)
		require.Equal(t, 200, resp.StatusCode, string(data))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		headers := http.Header{"Authorization": {"Bearer " + key.Key}, "User-Agent": {"codex_cli_rs/0.160.0"},
			"Originator": {"codex_exec"}, "Version": {"0.160.0"}, "Chatgpt-Account-Id": {"TEST_ONLY_CLIENT_ACCOUNT"}}
		conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/responses?future=a%2Fb&future=a+b", &coderws.DialOptions{HTTPHeader: headers})
		require.NoError(t, err)
		defer conn.CloseNow()
		for _, payload := range [][]byte{
			[]byte(`{ "type":"response.create", "model":"gpt-5.5", "generate":false, "input":[], "future":true }`),
			[]byte(`{ "type":"response.create", "model":"gpt-5.5", "reasoning":{"effort":"high"}, "input":[], "future":true }`),
		} {
			require.NoError(t, conn.Write(ctx, coderws.MessageBinary, payload))
			for {
				_, event, readErr := conn.Read(ctx)
				require.NoError(t, readErr)
				if gjson.GetBytes(event, "type").String() == "response.completed" {
					break
				}
			}
			seen := lab.providerRecords()
			var last map[string]any
			for _, record := range seen {
				if record["kind"] == "frame" {
					last = record
				}
			}
			sum := sha256.Sum256(payload)
			require.Equal(t, hex.EncodeToString(sum[:]), last["sha256"])
			require.Equal(t, int(coderws.MessageBinary), last["message_type"])
		}
		require.NoError(t, conn.Close(coderws.StatusNormalClosure, "TEST_ONLY done"))
		for i := 0; i < 2; i++ {
			path, payload := "/v1/responses", body
			if i == 0 {
				path, payload = "/v1/codex/images/generations", []byte(`{"model":"gpt-image-2","prompt":"TEST_ONLY"}`)
			}
			response, data := request("POST", path, payload, false, true)
			require.Equal(t, 200, response.StatusCode, string(data))
		}
		before := len(lab.providerRecords())
		for i := 0; i < 100; i++ {
			response, data := request("POST", "/v1/responses", body, false, true)
			require.Equal(t, http.StatusTooManyRequests, response.StatusCode)
			require.Contains(t, string(data), "attempt_budget_exhausted")
			require.NotContains(t, string(data), "response.completed")
		}
		require.Equal(t, before, len(lab.providerRecords()), "refused attempts must not reach provider")
		reconnect, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: headers})
		require.NoError(t, err)
		defer reconnect.CloseNow()
		require.NoError(t, reconnect.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)))
		_, refusal, readErr := reconnect.Read(ctx)
		if readErr == nil {
			require.Contains(t, string(refusal), "budget exhausted")
			require.NotContains(t, string(refusal), "response.completed")
			_, _, readErr = reconnect.Read(ctx)
		}
		require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(readErr))
		modelAttempts := 0
		for _, record := range lab.providerRecords() {
			if record["kind"] == "http" || record["kind"] == "frame" {
				modelAttempts++
			}
		}
		require.Equal(t, 5, modelAttempts)
		require.Equal(t, int64(5), *cache.budgetUsed)
		require.Eventually(t, func() bool { return concurrencyCache.users.Load() == 0 && concurrencyCache.accounts.Load() == 0 }, time.Second, 10*time.Millisecond)
	})
	require.Zero(t, lab.blocked.Load(), "no unexpected provider destination")
}

func nativeRoutesTLS(t *testing.T) *tls.Config {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "SAIAI MOCK ONLY"},
		DNSNames: []string{"chatgpt.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "public-ca.pem")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	t.Setenv("SAIAI_EXTRA_CA_FILE", path)
	// The TLS private key stays only in process memory.
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
}

type nativeRoutesLab struct {
	mu                                          sync.Mutex
	records                                     []map[string]any
	bindings                                    map[string]int64
	response, usageRecords, billingApplications int
	authChecks, balanceChecks                   int
	connections                                 []net.Conn
	imagegen                                    bool
	sequence, tunnels, blocked                  atomic.Int64
}

func (l *nativeRoutesLab) closeTunnels() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, conn := range l.connections {
		_ = conn.Close()
	}
}

func (l *nativeRoutesLab) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records, l.response, l.usageRecords, l.billingApplications = nil, 0, 0, 0
	l.imagegen = false
	l.authChecks, l.balanceChecks = 0, 0
}
func (l *nativeRoutesLab) control() map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	return map[string]any{"usage_records": l.usageRecords, "billing_applications": l.billingApplications,
		"auth_checks": l.authChecks, "balance_checks": l.balanceChecks, "blocked_provider_destinations": l.blocked.Load(),
		"transport": "production HTTPUpstream and WS dialer over loopback TLS", "run_mode": "standard"}
}
func (l *nativeRoutesLab) providerRecords() []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var result []map[string]any
	for _, record := range l.records {
		if record["stage"] == "provider" {
			result = append(result, record)
		}
	}
	return result
}
func (l *nativeRoutesLab) record(stage, kind string, r *http.Request, body []byte, messageType int) {
	encodedDigest := sha256.Sum256(body)
	decoded := body
	if len(body) > 0 && r.Header.Get("Content-Encoding") == "zstd" {
		decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		if err == nil {
			decoded, _ = decoder.DecodeAll(body, nil)
			decoder.Close()
		}
	}
	decodedDigest := sha256.Sum256(decoded)
	headers := r.Header.Clone()
	for _, name := range []string{"Authorization", "Cookie", "Chatgpt-Account-Id"} {
		headers.Del(name)
	}
	record := map[string]any{"stage": stage, "kind": kind, "method": r.Method, "path": r.URL.Path, "query": r.URL.RawQuery,
		"headers": headers, "sha256": hex.EncodeToString(encodedDigest[:]), "decoded_sha256": hex.EncodeToString(decodedDigest[:]), "message_type": messageType}
	if stage == "provider" {
		record["selected_auth"] = r.Header.Get("Authorization") == "Bearer MOCK_ONLY"
		record["selected_account"] = r.Header.Get("Chatgpt-Account-Id") == "MOCK_ONLY_ACCOUNT"
		record["no_cookie"] = r.Header.Get("Cookie") == ""
	}
	l.mu.Lock()
	l.records = append(l.records, record)
	l.mu.Unlock()
}
func (l *nativeRoutesLab) provider(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/models") {
		l.record("provider", "models", r, nil, 0)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[],"future_catalog":true}`))
		return
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		l.record("provider", "handshake", r, nil, 0)
		w.Header().Set("X-Codex-Turn-State", "MOCK_HANDSHAKE_STATE")
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(16 << 20)
		for {
			kind, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			l.record("provider", "frame", r, body, int(kind))
			for _, event := range l.next(body) {
				if conn.Write(r.Context(), coderws.MessageText, event) != nil {
					return
				}
			}
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		return
	}
	l.record("provider", "http", r, body, 0)
	if strings.HasPrefix(r.URL.Path, "/backend-api/codex/images/") {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Codex-Imagegen-Request-Id", "TEST_ONLY-image-request")
		_, _ = w.Write(nativeRoutesImageResponse())
		return
	}
	if strings.HasSuffix(r.URL.Path, "/compact") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_mock_compact","output":[],"future":true}`))
		return
	}
	if r.Header.Get("Content-Encoding") == "zstd" {
		decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return
		}
		body, err = decoder.DecodeAll(body, nil)
		decoder.Close()
		if err != nil {
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Codex-Turn-State", "MOCK_FRAME_STATE")
	for _, event := range l.next(body) {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
	}
}
func (l *nativeRoutesLab) next(body []byte) [][]byte {
	if generate := gjson.GetBytes(body, "generate"); generate.Exists() && !generate.Bool() {
		return nativeRoutesEvents("resp_mock_warmup", false)
	}
	l.mu.Lock()
	l.response++
	number := l.response
	imagegen := l.imagegen
	l.mu.Unlock()
	return nativeRoutesToolEvents(fmt.Sprintf("resp_mock_%d", number), number == 1, imagegen)
}
func nativeRoutesEvents(responseID string, tool bool) [][]byte {
	return nativeRoutesToolEvents(responseID, tool, false)
}

func nativeRoutesImageResponse() []byte {
	return []byte(`{"created":1,"background":"opaque","data":[{"b64_json":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==","generation_id":"gen_TEST_ONLY"}],"usage":{"input_tokens":5,"input_tokens_details":{"text_tokens":5,"image_tokens":0},"output_tokens":7}}`)
}

func nativeRoutesToolEvents(responseID string, tool, imagegen bool) [][]byte {
	if responseID == "" {
		responseID = "resp_mock_1"
	}
	items := []map[string]any{{"type": "response.created", "response": map[string]any{"id": responseID}},
		{"type": "response.metadata", "headers": map[string]any{"x-codex-turn-state": "MOCK_FRAME_STATE"}}}
	if tool {
		call := map[string]any{"type": "function_call", "call_id": "mock_plan_call", "name": "update_plan", "arguments": `{"plan":[{"step":"Mock protocol check","status":"completed"}]}`}
		if imagegen {
			call = map[string]any{"type": "function_call", "call_id": "mock_image_call", "name": "imagegen", "namespace": "image_gen", "arguments": `{"prompt":"TEST_ONLY-cat","transparent_background":false}`}
		}
		items = append(items, map[string]any{"type": "response.output_item.done", "item": call})
	} else {
		items = append(items, map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "role": "assistant", "id": "msg_mock_1", "content": []map[string]any{{"type": "output_text", "text": "MOCK_ONLY_SUCCESS"}}}})
	}
	tokens := 1
	if responseID == "resp_mock_warmup" {
		tokens = 0
	}
	items = append(items, map[string]any{"type": "response.completed", "response": map[string]any{"id": responseID, "status": "completed", "usage": map[string]any{"input_tokens": tokens, "output_tokens": tokens, "total_tokens": 2 * tokens}}})
	var result [][]byte
	for _, item := range items {
		encoded, _ := json.Marshal(item)
		result = append(result, encoded)
	}
	return result
}

type nativeRoutesAccountRepo struct {
	service.AccountRepository
	account service.Account
}

func (r *nativeRoutesAccountRepo) GetByID(context.Context, int64) (*service.Account, error) {
	account := r.account
	return &account, nil
}
func (r *nativeRoutesAccountRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]service.Account, error) {
	return []service.Account{r.account}, nil
}
func (r *nativeRoutesAccountRepo) ListSchedulableByPlatform(context.Context, string) ([]service.Account, error) {
	return []service.Account{r.account}, nil
}

type nativeRoutesKeyRepo struct {
	service.APIKeyRepository
	key *service.APIKey
	lab *nativeRoutesLab
}

func (r *nativeRoutesKeyRepo) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	if key != r.key.Key {
		return nil, service.ErrAPIKeyNotFound
	}
	r.lab.mu.Lock()
	r.lab.authChecks++
	r.lab.mu.Unlock()
	result := *r.key
	return &result, nil
}
func (*nativeRoutesKeyRepo) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

type nativeRoutesUserRepo struct {
	service.UserRepository
	user *service.User
	lab  *nativeRoutesLab
}

func (r *nativeRoutesUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	r.lab.mu.Lock()
	r.lab.balanceChecks++
	r.lab.mu.Unlock()
	result := *r.user
	return &result, nil
}

type nativeRoutesCache struct {
	service.GatewayCache
	lab        *nativeRoutesLab
	budgetUsed *int64
}

func (c *nativeRoutesCache) ReserveOpenAIProviderAttempt(_ context.Context, _ string, _ int64, limit int64, _ time.Time) (int64, error) {
	c.lab.mu.Lock()
	defer c.lab.mu.Unlock()
	if c.budgetUsed == nil {
		return 0, service.ErrOpenAIProviderAttemptBudgetUnarmed
	}
	if *c.budgetUsed >= limit {
		return *c.budgetUsed, service.ErrOpenAIProviderAttemptBudgetExhausted
	}
	*c.budgetUsed++
	return *c.budgetUsed, nil
}

func (c *nativeRoutesCache) GetSessionAccountID(_ context.Context, _ int64, key string) (int64, error) {
	c.lab.mu.Lock()
	defer c.lab.mu.Unlock()
	return c.lab.bindings[key], nil
}
func (c *nativeRoutesCache) SetSessionAccountID(_ context.Context, _ int64, key string, id int64, _ time.Duration) error {
	c.lab.mu.Lock()
	defer c.lab.mu.Unlock()
	c.lab.bindings[key] = id
	return nil
}
func (c *nativeRoutesCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}
func (c *nativeRoutesCache) DeleteSessionAccountID(_ context.Context, _ int64, key string) error {
	c.lab.mu.Lock()
	defer c.lab.mu.Unlock()
	delete(c.lab.bindings, key)
	return nil
}

type nativeRoutesConcurrency struct {
	service.ConcurrencyCache
	users, accounts atomic.Int64
}

func (c *nativeRoutesConcurrency) AcquireUserSlot(context.Context, int64, int, string) (bool, error) {
	c.users.Add(1)
	return true, nil
}
func (c *nativeRoutesConcurrency) ReleaseUserSlot(context.Context, int64, string) error {
	c.users.Add(-1)
	return nil
}
func (c *nativeRoutesConcurrency) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	c.accounts.Add(1)
	return true, nil
}
func (c *nativeRoutesConcurrency) ReleaseAccountSlot(context.Context, int64, string) error {
	c.accounts.Add(-1)
	return nil
}
func (*nativeRoutesConcurrency) GetAccountsLoadBatch(_ context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	result := make(map[int64]*service.AccountLoadInfo)
	for _, account := range accounts {
		result[account.ID] = &service.AccountLoadInfo{AccountID: account.ID}
	}
	return result, nil
}

type nativeRoutesUsageRepo struct {
	service.UsageLogRepository
	lab *nativeRoutesLab
}

func (r *nativeRoutesUsageRepo) CreateBestEffort(_ context.Context, _ *service.UsageLog) error {
	r.lab.mu.Lock()
	r.lab.usageRecords++
	r.lab.mu.Unlock()
	return nil
}
func (l *nativeRoutesLab) Apply(_ context.Context, _ *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	l.mu.Lock()
	l.billingApplications++
	l.mu.Unlock()
	return &service.UsageBillingApplyResult{Applied: true}, nil
}

type nativeRoutesObserverWriter struct {
	gin.ResponseWriter
	lab     *nativeRoutesLab
	request *http.Request
}

func (w *nativeRoutesObserverWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffer, err := w.ResponseWriter.Hijack()
	if err != nil {
		return nil, nil, err
	}
	observer := &nativeRoutesObserverConn{Conn: conn, lab: w.lab, request: w.request}
	// Bytes prefetched before the upgrade are part of the same stream too.
	var prefetched []byte
	if buffered := buffer.Reader.Buffered(); buffered > 0 {
		prefetched = make([]byte, buffered)
		_, _ = io.ReadFull(buffer.Reader, prefetched)
		observer.observe(prefetched)
	}
	reader := bufio.NewReader(io.MultiReader(bytes.NewReader(prefetched), observer))
	return observer, bufio.NewReadWriter(reader, buffer.Writer), nil
}

type nativeRoutesObserverConn struct {
	net.Conn
	lab     *nativeRoutesLab
	request *http.Request
	pending []byte
}

func (c *nativeRoutesObserverConn) Read(body []byte) (int, error) {
	count, err := c.Conn.Read(body)
	c.observe(body[:count])
	return count, err
}
func (c *nativeRoutesObserverConn) observe(data []byte) {
	c.pending = append(c.pending, data...)
	for len(c.pending) >= 2 {
		length, offset := uint64(c.pending[1]&127), 2
		if length == 126 {
			if len(c.pending) < 4 {
				return
			}
			length = uint64(binary.BigEndian.Uint16(c.pending[2:4]))
			offset = 4
		} else if length == 127 {
			if len(c.pending) < 10 {
				return
			}
			length = binary.BigEndian.Uint64(c.pending[2:10])
			offset = 10
		}
		masked := c.pending[1]&128 != 0
		if masked {
			offset += 4
		}
		if length > 16<<20 {
			c.pending = nil
			return
		}
		if uint64(len(c.pending)) < uint64(offset)+length {
			return
		}
		opcode := c.pending[0] & 15
		if opcode == 1 || opcode == 2 {
			payload := append([]byte(nil), c.pending[offset:offset+int(length)]...)
			if masked {
				mask := c.pending[offset-4 : offset]
				for index := range payload {
					payload[index] ^= mask[index%4]
				}
			}
			c.lab.record("gateway", "frame", c.request, payload, int(opcode))
		}
		c.pending = c.pending[offset+int(length):]
	}
}
