package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func codexTLSConfig(global bool) *config.Config {
	cfg := &config.Config{}
	cfg.Gateway.TLSFingerprint.Enabled = global
	return cfg
}

func codexOAuthAccount(id int64, extra map[string]any) *Account {
	return &Account{
		ID: id, Name: "codex", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "acct"},
		Extra:       extra, Status: StatusActive, Schedulable: true,
	}
}

func TestCodexTLSFingerprintAccountFlagIsOptInForOpenAIOAuthOnly(t *testing.T) {
	cases := []struct {
		name          string
		account       *Account
		codex, claude bool
	}{
		{"openai_oauth_default_off", codexOAuthAccount(1, nil), false, false},
		{"openai_oauth_explicit_true", codexOAuthAccount(1, map[string]any{"enable_tls_fingerprint": true}), true, false},
		{"openai_oauth_explicit_false", codexOAuthAccount(1, map[string]any{"enable_tls_fingerprint": false}), false, false},
		{"openai_oauth_non_bool", codexOAuthAccount(1, map[string]any{"enable_tls_fingerprint": "true"}), false, false},
		{"openai_apikey_never", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"enable_tls_fingerprint": true}}, false, false},
		{"openai_native_relay_never", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"enable_tls_fingerprint": true, "openai_upstream_protocol": OpenAIUpstreamProtocolCodexNativeRelayV1}}, false, false},
		{"anthropic_oauth_default_on", &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, false, true},
		{"anthropic_oauth_explicit_false", &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": false}}, false, false},
		{"nil", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.codex, tc.account.IsCodexTLSFingerprintEnabled())
			if tc.account != nil {
				require.Equal(t, tc.claude, tc.account.IsTLSFingerprintEnabled(), "Claude flag semantics unchanged")
			}
			for _, global := range []bool{false, true} {
				for _, role := range []tlsfingerprint.Role{tlsfingerprint.RoleHTTP, tlsfingerprint.RoleWS} {
					policy := codexTransportPolicyFor(codexTLSConfig(global), tc.account, role)
					require.Equal(t, global && tc.codex, policy.Enabled())
					if policy.Enabled() {
						require.Equal(t, role, policy.Role)
						require.NoError(t, policy.Validate())
					}
				}
			}
			require.False(t, codexTransportPolicyFor(nil, tc.account, tlsfingerprint.RoleHTTP).Enabled())
		})
	}
}

// policyRecordingUpstream implements both HTTPUpstream and the optional
// policy interface, recording which path each request took.
type policyRecordingUpstream struct {
	httpUpstreamRecorder
	mu       sync.Mutex
	doCalls  int
	policies []tlsfingerprint.TransportPolicy
	paths    []string
}

func (u *policyRecordingUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	u.mu.Lock()
	u.doCalls++
	u.paths = append(u.paths, req.URL.Path)
	u.mu.Unlock()
	return u.httpUpstreamRecorder.Do(req, proxyURL, accountID, concurrency)
}

func (u *policyRecordingUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ bool) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func (u *policyRecordingUpstream) DoWithTransportPolicy(req *http.Request, proxyURL string, accountID int64, concurrency int, policy tlsfingerprint.TransportPolicy) (*http.Response, error) {
	u.mu.Lock()
	u.policies = append(u.policies, policy)
	u.paths = append(u.paths, req.URL.Path)
	u.mu.Unlock()
	return u.httpUpstreamRecorder.Do(req, proxyURL, accountID, concurrency)
}

func TestDoWithTransportPolicyNeverDowngrades(t *testing.T) {
	plain := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}}
	req, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/models", nil)
	_, err := doWithTransportPolicy(plain, req, "", 1, 1, tlsfingerprint.CodexHTTPTransportPolicy())
	require.ErrorIs(t, err, ErrTransportPolicyUnsupported)
	require.Nil(t, plain.lastReq, "no request may be sent with a different TLS stack")
	resp, err := doWithTransportPolicy(plain, req, "", 1, 1, tlsfingerprint.TransportPolicy{})
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

func newForwardTestContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.163.0")
	return c
}

func TestOpenAIForwardHTTPUsesCodexPolicyOnlyForOptedInOAuth(t *testing.T) {
	for _, tc := range []struct {
		name    string
		global  bool
		account *Account
		want    tlsfingerprint.TransportPolicy
	}{
		{"opted_in", true, codexOAuthAccount(11, map[string]any{"enable_tls_fingerprint": true}), tlsfingerprint.CodexHTTPTransportPolicy()},
		{"default_off", true, codexOAuthAccount(12, nil), tlsfingerprint.TransportPolicy{}},
		{"global_off", false, codexOAuthAccount(13, map[string]any{"enable_tls_fingerprint": true}), tlsfingerprint.TransportPolicy{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &policyRecordingUpstream{httpUpstreamRecorder: httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad","type":"invalid_request_error"}}`)),
			}}}
			svc := &OpenAIGatewayService{cfg: codexTLSConfig(tc.global), httpUpstream: upstream}
			body := []byte(`{"model":"gpt-5.1-codex","stream":false,"input":[{"type":"text","text":"hello"}]}`)
			_, _ = svc.Forward(context.Background(), newForwardTestContext(t), tc.account, body)
			require.NotNil(t, upstream.lastReq, "request must reach the upstream")
			require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
			if tc.want.Enabled() {
				require.Equal(t, []tlsfingerprint.TransportPolicy{tc.want}, upstream.policies)
				require.Zero(t, upstream.doCalls)
			} else {
				require.Empty(t, upstream.policies)
				require.Equal(t, 1, upstream.doCalls)
			}
		})
	}
}

func TestCodexModelsManifestOAuthBranchUsesPolicyTransport(t *testing.T) {
	upstream := &policyRecordingUpstream{httpUpstreamRecorder: httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-5.5"}]}`)),
	}}}
	svc := &OpenAIGatewayService{cfg: codexTLSConfig(true), httpUpstream: upstream}
	_, _ = svc.FetchCodexModelsManifest(context.Background(), codexOAuthAccount(21, map[string]any{"enable_tls_fingerprint": true}), "0.163.0", "")
	require.Equal(t, []tlsfingerprint.TransportPolicy{tlsfingerprint.CodexHTTPTransportPolicy()}, upstream.policies)
	require.Equal(t, []string{"/backend-api/codex/models"}, upstream.paths)

	// Opted-out OAuth keeps the legacy shared client (never the upstream pool).
	var hits atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"models":[]}`)
	}))
	defer local.Close()
	previous := chatgptCodexModelsURL
	chatgptCodexModelsURL = local.URL + "/backend-api/codex/models"
	defer func() { chatgptCodexModelsURL = previous }()
	optedOut := &policyRecordingUpstream{}
	svc = &OpenAIGatewayService{cfg: codexTLSConfig(true), httpUpstream: optedOut}
	_, _ = svc.FetchCodexModelsManifest(context.Background(), codexOAuthAccount(22, nil), "0.163.0", "")
	require.Equal(t, int32(1), hits.Load())
	require.Empty(t, optedOut.paths)
}

func TestNativeCodexImageAndChatGPTPathsChoosePolicyByPurpose(t *testing.T) {
	// ChatGPT conversation traffic is not Codex CLI traffic: no Codex policy.
	upstream := &policyRecordingUpstream{httpUpstreamRecorder: httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}}}
	svc := &OpenAIGatewayService{cfg: codexTLSConfig(true), httpUpstream: upstream}
	req, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/f/conversation", strings.NewReader("{}"))
	resp, err := svc.doOpenAIUpstream(req, "", 1, 1)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, 1, upstream.doCalls)
	require.Empty(t, upstream.policies)

	// The explicit variant keeps the attempt-budget hooks and forwards the policy.
	req, _ = http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/images/generations", strings.NewReader("{}"))
	resp, err = svc.doOpenAIUpstreamWithPolicy(req, "", 1, 1, tlsfingerprint.CodexHTTPTransportPolicy())
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, []tlsfingerprint.TransportPolicy{tlsfingerprint.CodexHTTPTransportPolicy()}, upstream.policies)
}

// --- WebSocket ------------------------------------------------------------

type wsHelloRecorder struct {
	mu     sync.Mutex
	hellos []*tls.ClientHelloInfo
}

func (r *wsHelloRecorder) last() *tls.ClientHelloInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.hellos) == 0 {
		return nil
	}
	return r.hellos[len(r.hellos)-1]
}

func newCodexWSServer(t *testing.T) (*httptest.Server, *wsHelloRecorder, *atomic.Int32) {
	t.Helper()
	rec := &wsHelloRecorder{}
	var upgrades atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/ws", http.StatusFound)
			return
		case "/limited":
			w.Header().Set("Retry-After", "7")
			http.Error(w, `{"error":"rate"}`, http.StatusTooManyRequests)
			return
		}
		upgrades.Add(1)
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			typ, payload, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), typ, append([]byte("echo:"), payload...)); err != nil {
				return
			}
		}
	}))
	server.TLS = &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		rec.mu.Lock()
		rec.hellos = append(rec.hellos, h)
		rec.mu.Unlock()
		return nil, nil
	}}
	server.StartTLS()
	t.Cleanup(server.Close)
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	previous := openAIWSPolicyRootCAs
	openAIWSPolicyRootCAs = func() *x509.CertPool { return pool }
	t.Cleanup(func() { openAIWSPolicyRootCAs = previous })
	return server, rec, &upgrades
}

func newConnectProxyForTest(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var connects atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		connects.Add(1)
		upstream, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, "dial", http.StatusBadGateway)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go func() { _, _ = io.Copy(upstream, conn); _ = upstream.Close() }()
		_, _ = io.Copy(conn, upstream)
		_ = conn.Close()
	}))
	t.Cleanup(proxy.Close)
	return proxy, &connects
}

var codexRustlsExtensionSetWithoutSNI = map[uint16]bool{5: true, 10: true, 11: true, 13: true, 23: true, 35: true, 43: true, 45: true, 51: true}

func TestOpenAIWSPolicyDialerUpgradesWithRustlsHelloAndRelaysFrames(t *testing.T) {
	server, hellos, _ := newCodexWSServer(t)
	proxy, connects := newConnectProxyForTest(t)
	wsURL := "wss://" + server.Listener.Addr().String() + "/backend-api/codex/responses"
	for _, proxyURL := range []string{"", proxy.URL} {
		t.Run("proxy="+proxyURL, func(t *testing.T) {
			dialer := newDefaultOpenAIWSClientDialer().(*coderOpenAIWSClientDialer)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, status, _, err := dialOpenAIWS(ctx, dialer, wsURL, http.Header{"User-Agent": []string{"codex_cli_rs/0.163.0"}}, proxyURL, tlsfingerprint.CodexWSTransportPolicy())
			require.NoError(t, err)
			defer func() { _ = conn.Close() }()
			require.Equal(t, http.StatusSwitchingProtocols, status)

			require.NoError(t, conn.WriteJSON(ctx, map[string]string{"type": "response.create"}))
			msg, err := conn.ReadMessage(ctx)
			require.NoError(t, err)
			require.Equal(t, `echo:{"type":"response.create"}`, strings.TrimSpace(string(msg)))
			frames, ok := conn.(*coderOpenAIWSClientConn)
			require.True(t, ok)
			require.NoError(t, frames.WriteFrame(ctx, coderws.MessageBinary, []byte{1, 2, 3}))
			typ, payload, err := frames.ReadFrame(ctx)
			require.NoError(t, err)
			require.Equal(t, coderws.MessageBinary, typ)
			require.Equal(t, append([]byte("echo:"), 1, 2, 3), payload)

			hello := hellos.last()
			require.NotNil(t, hello)
			require.Empty(t, hello.SupportedProtos, "rustls WS baseline sends no ALPN")
			require.Len(t, hello.Extensions, len(codexRustlsExtensionSetWithoutSNI))
			for _, id := range hello.Extensions {
				require.True(t, codexRustlsExtensionSetWithoutSNI[id], "unexpected extension %d", id)
			}
			require.Equal(t, []tls.CurveID{tls.X25519MLKEM768, tls.X25519, tls.CurveP256, tls.CurveP384}, hello.SupportedCurves)
		})
	}
	require.Equal(t, int32(1), connects.Load(), "proxied dial must tunnel through CONNECT")
	dialer := newDefaultOpenAIWSClientDialer().(*coderOpenAIWSClientDialer)
	require.Zero(t, dialer.SnapshotTransportMetrics().ProxyClientCacheMisses, "policy clients do not skew proxy-client metrics")
}

func TestOpenAIWSPolicyDialerKeepsRedirectAnd429Semantics(t *testing.T) {
	server, _, upgrades := newCodexWSServer(t)
	base := "wss://" + server.Listener.Addr().String()
	dialer := newDefaultOpenAIWSClientDialer().(*coderOpenAIWSClientDialer)
	policy := tlsfingerprint.CodexWSTransportPolicy()

	ctx, cancel := context.WithTimeout(WithNativeCodexRedirectPolicy(context.Background()), 5*time.Second)
	defer cancel()
	_, status, _, err := dialer.DialWithPolicy(ctx, base+"/redirect", nil, "", policy)
	require.Error(t, err)
	require.Equal(t, http.StatusFound, status)
	require.Zero(t, upgrades.Load(), "redirect must not be followed for native Codex")

	_, status, headers, err := dialer.DialWithPolicy(context.Background(), base+"/limited", nil, "", policy)
	require.Error(t, err)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "7", headers.Get("Retry-After"))
	var handshakeErr *openAIWSHandshakeResponseError
	require.True(t, errors.As(err, &handshakeErr), "429 header receipt time must be preserved")
}

// policyFakeDialer records the policy of every dial.
type policyFakeDialer struct {
	mu       sync.Mutex
	policies []tlsfingerprint.TransportPolicy
}

func (d *policyFakeDialer) Dial(ctx context.Context, wsURL string, headers http.Header, proxyURL string) (openAIWSClientConn, int, http.Header, error) {
	return d.DialWithPolicy(ctx, wsURL, headers, proxyURL, tlsfingerprint.TransportPolicy{})
}

func (d *policyFakeDialer) DialWithPolicy(_ context.Context, _ string, _ http.Header, _ string, policy tlsfingerprint.TransportPolicy) (openAIWSClientConn, int, http.Header, error) {
	d.mu.Lock()
	d.policies = append(d.policies, policy)
	d.mu.Unlock()
	return &openAIWSFakeConn{}, http.StatusSwitchingProtocols, nil, nil
}

func (d *policyFakeDialer) dialed() []tlsfingerprint.TransportPolicy {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]tlsfingerprint.TransportPolicy(nil), d.policies...)
}

// accountFlagRepo serves the CURRENT account state to the policy resolver.
type accountFlagRepo struct {
	AccountRepository
	mu      sync.Mutex
	account *Account
}

func (r *accountFlagRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != id {
		return nil, errors.New("not found")
	}
	copied := *r.account
	return &copied, nil
}

func (r *accountFlagRepo) setFlag(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.account.Extra = map[string]any{"enable_tls_fingerprint": enabled}
}

// newResolvingPoolForTest wires the production resolver
// (currentCodexWSTransportPolicy) to a repo whose account flag can be toggled.
func newResolvingPoolForTest(t *testing.T, minIdle int) (*openAIWSConnPool, *policyFakeDialer, *accountFlagRepo, *Account) {
	t.Helper()
	cfg := codexTLSConfig(true)
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 4
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = minIdle
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 4
	cfg.Gateway.OpenAIWS.PoolTargetUtilization = 0.8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 1
	snapshot := codexOAuthAccount(41, nil) // the session's stale snapshot: flag absent
	repo := &accountFlagRepo{account: codexOAuthAccount(41, nil)}
	svc := &OpenAIGatewayService{cfg: cfg, accountRepo: repo}
	dialer := &policyFakeDialer{}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	pool.transportPolicyFor = svc.currentCodexWSTransportPolicy
	t.Cleanup(pool.Close)
	return pool, dialer, repo, snapshot
}

func TestOpenAIWSToggleAppliesToNewDialsWhileExistingConnsStayReusable(t *testing.T) {
	pool, dialer, repo, snapshot := newResolvingPoolForTest(t, 0)
	req := openAIWSAcquireRequest{Account: snapshot, WSURL: "wss://chatgpt.com/backend-api/codex/responses"}

	first, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	stdConnID := first.conn.id
	first.Release()
	require.Equal(t, []tlsfingerprint.TransportPolicy{{}}, dialer.dialed())

	// Toggle on in the CURRENT account state; the session keeps its old
	// *Account snapshot (flag absent).
	repo.setFlag(true)

	// The existing idle standard-TLS connection is still reused normally.
	reused, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, stdConnID, reused.conn.id)
	require.Len(t, dialer.dialed(), 1, "no eviction or re-dial on toggle")

	// While it is leased, the next acquire needs a NEW upstream dial, which
	// uses the newly enabled Codex policy.
	second, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	require.NotEqual(t, stdConnID, second.conn.id)
	require.Equal(t, []tlsfingerprint.TransportPolicy{{}, tlsfingerprint.CodexWSTransportPolicy()}, dialer.dialed())
	second.Release()
	reused.Release()

	// Toggle off again: both connections stay pooled and reusable, new dials
	// go back to standard TLS.
	repo.setFlag(false)
	ap, _ := pool.getAccountPool(snapshot.ID)
	ap.mu.Lock()
	evicted := pool.cleanupAccountLocked(ap, time.Now(), 4)
	conns := len(ap.conns)
	ap.mu.Unlock()
	require.Empty(t, evicted)
	require.Equal(t, 2, conns)
	forced := req
	forced.ForceNewConn = true
	third, err := pool.Acquire(context.Background(), forced)
	require.NoError(t, err)
	third.Release()
	require.Equal(t, tlsfingerprint.TransportPolicy{}, dialer.dialed()[2])
}

func TestOpenAIWSContinuationUnaffectedByToggle(t *testing.T) {
	pool, dialer, repo, snapshot := newResolvingPoolForTest(t, 0)
	req := openAIWSAcquireRequest{Account: snapshot, WSURL: "wss://chatgpt.com/backend-api/codex/responses"}
	lease, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	boundID := lease.conn.id
	lease.Release()

	repo.setFlag(true)
	req.PreferredConnID = boundID
	req.ForcePreferredConn = true
	lease, err = pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, boundID, lease.conn.id, "continuation keeps its connection")
	lease.Release()
	require.Len(t, dialer.dialed(), 1)
}

func TestOpenAIWSPrewarmResolvesCurrentPolicy(t *testing.T) {
	pool, dialer, repo, snapshot := newResolvingPoolForTest(t, 3)
	repo.setFlag(true)
	lease, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{Account: snapshot, WSURL: "wss://chatgpt.com/backend-api/codex/responses"})
	require.NoError(t, err)
	lease.Release()
	require.Eventually(t, func() bool { return len(dialer.dialed()) >= 3 }, 3*time.Second, 10*time.Millisecond)
	for _, p := range dialer.dialed() {
		require.Equal(t, tlsfingerprint.CodexWSTransportPolicy(), p, "prewarm dials resolve the current account state")
	}
}

func TestOpenAIWSPolicyRequiresCapableDialer(t *testing.T) {
	cfg := codexTLSConfig(true)
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSFakeDialer{})
	pool.transportPolicyFor = func(context.Context, *Account) tlsfingerprint.TransportPolicy {
		return tlsfingerprint.CodexWSTransportPolicy()
	}
	defer pool.Close()
	_, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{Account: codexOAuthAccount(42, nil), WSURL: "wss://chatgpt.com/backend-api/codex/responses"})
	require.ErrorIs(t, err, ErrTransportPolicyUnsupported)
}

// countingFlagRepo counts lookups and can fail them.
type countingFlagRepo struct {
	accountFlagRepo
	lookups atomic.Int32
	fail    bool
}

func (r *countingFlagRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.lookups.Add(1)
	if r.fail {
		return nil, errors.New("transient cache/DB failure")
	}
	return r.accountFlagRepo.GetByID(ctx, id)
}

func TestCurrentCodexWSTransportPolicyLookupAndFallback(t *testing.T) {
	on := map[string]any{"enable_tls_fingerprint": true}
	codexWS := tlsfingerprint.CodexWSTransportPolicy()
	t.Run("current_state_wins_over_snapshot", func(t *testing.T) {
		repo := &countingFlagRepo{accountFlagRepo: accountFlagRepo{account: codexOAuthAccount(44, on)}}
		svc := &OpenAIGatewayService{cfg: codexTLSConfig(true), accountRepo: repo}
		require.Equal(t, codexWS, svc.currentCodexWSTransportPolicy(context.Background(), codexOAuthAccount(44, nil)))
		repo.setFlag(false)
		require.False(t, svc.currentCodexWSTransportPolicy(context.Background(), codexOAuthAccount(44, on)).Enabled())
	})
	// Documented: on lookup failure the session's snapshot decides, so the
	// toggle may lag for that dial — in both directions.
	t.Run("lookup_failure_uses_stale_snapshot_toggled_off", func(t *testing.T) {
		repo := &countingFlagRepo{accountFlagRepo: accountFlagRepo{account: codexOAuthAccount(45, nil)}, fail: true}
		svc := &OpenAIGatewayService{cfg: codexTLSConfig(true), accountRepo: repo}
		require.Equal(t, codexWS, svc.currentCodexWSTransportPolicy(context.Background(), codexOAuthAccount(45, on)))
		require.Equal(t, int32(1), repo.lookups.Load())
	})
	t.Run("lookup_failure_uses_stale_snapshot_toggled_on", func(t *testing.T) {
		repo := &countingFlagRepo{accountFlagRepo: accountFlagRepo{account: codexOAuthAccount(46, on)}, fail: true}
		svc := &OpenAIGatewayService{cfg: codexTLSConfig(true), accountRepo: repo}
		require.False(t, svc.currentCodexWSTransportPolicy(context.Background(), codexOAuthAccount(46, nil)).Enabled())
		require.Equal(t, int32(1), repo.lookups.Load())
	})
	t.Run("global_off_or_ineligible_makes_no_lookup", func(t *testing.T) {
		repo := &countingFlagRepo{accountFlagRepo: accountFlagRepo{account: codexOAuthAccount(47, on)}}
		svc := &OpenAIGatewayService{cfg: codexTLSConfig(false), accountRepo: repo}
		require.False(t, svc.currentCodexWSTransportPolicy(context.Background(), codexOAuthAccount(47, on)).Enabled())
		svc.cfg = codexTLSConfig(true)
		apiKey := &Account{ID: 47, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: on}
		require.False(t, svc.currentCodexWSTransportPolicy(context.Background(), apiKey).Enabled())
		require.Zero(t, repo.lookups.Load())
	})
}
