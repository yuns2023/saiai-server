package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	codexWindowsUA = "codex-tui/0.163.0 (Windows 10.0.26200; x86_64) unknown (codex-tui; 0.163.0)"
	codexMacUA     = "Codex Desktop/0.163.0-alpha.10.2 (Mac OS 15.5.0; arm64) Apple_Terminal/455.1 (Codex Desktop; 26.1013.31256)"
	codexLinux147  = "codex_exec/0.147.0 (Ubuntu 24.4.0; x86_64) VTE/6800 (codex_exec; 0.147.0)"
)

// The HTTP policy follows the User-Agent actually sent upstream (read from
// the built upstream request, after any gateway rewriting).
func TestOpenAIForwardHTTPSelectsBaselineFromOutboundUserAgent(t *testing.T) {
	for _, tc := range []struct {
		name, inboundUA string
	}{
		{"windows_tui", codexWindowsUA},
		{"mac_desktop", codexMacUA},
		{"linux_0147", codexLinux147},
		{"bare_codex_ua", "codex_cli_rs/0.163.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &policyRecordingUpstream{httpUpstreamRecorder: httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad","type":"invalid_request_error"}}`)),
			}}}
			svc := &OpenAIGatewayService{cfg: codexTLSConfig(true), httpUpstream: upstream}
			account := codexOAuthAccount(61, map[string]any{"enable_tls_fingerprint": true})
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
			c.Request.Header.Set("User-Agent", tc.inboundUA)
			body := []byte(`{"model":"gpt-5.1-codex","stream":false,"input":[{"type":"text","text":"hello"}]}`)
			_, _ = svc.Forward(context.Background(), c, account, body)
			require.NotNil(t, upstream.lastReq)
			outbound := upstream.lastReq.Header.Get("User-Agent")
			require.Len(t, upstream.policies, 1)
			require.Equal(t, tlsfingerprint.CodexTransportPolicyFor(tlsfingerprint.RoleHTTP, outbound), upstream.policies[0],
				"policy must be selected from the outbound UA %q", outbound)
		})
	}
}

func TestOpenAIForwardHTTPWindowsClientGetsSChannelBaseline(t *testing.T) {
	upstream := &policyRecordingUpstream{httpUpstreamRecorder: httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad","type":"invalid_request_error"}}`)),
	}}}
	svc := &OpenAIGatewayService{cfg: codexTLSConfig(true), httpUpstream: upstream}
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", codexWindowsUA)
	c.Request.Header.Set("originator", "codex-tui")
	body := []byte(`{"model":"gpt-5.1-codex","stream":false,"input":[{"type":"text","text":"hello"}]}`)
	_, _ = svc.Forward(context.Background(), c, codexOAuthAccount(62, map[string]any{"enable_tls_fingerprint": true}), body)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, codexWindowsUA, upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, []tlsfingerprint.TransportPolicy{{
		Family: tlsfingerprint.FamilyCodex, Baseline: tlsfingerprint.BaselineCodex0163WindowsHTTPSChannel, Role: tlsfingerprint.RoleHTTP,
	}}, upstream.policies)
}

func TestCodexControlTransportSelectsFromRequestUserAgent(t *testing.T) {
	tr := codexControlTransport{cfg: codexTLSConfig(true)}
	account := codexOAuthAccount(63, map[string]any{"enable_tls_fingerprint": true})
	req, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", nil)
	req.Header.Set("User-Agent", codexMacUA)
	require.Equal(t, tlsfingerprint.BaselineCodex0163MacOSHTTPSecureTransport, tr.policyFor(account, req).Baseline)
	req.Header.Set("User-Agent", codexCLIUserAgent) // bare synthetic UA: newest Linux
	require.Equal(t, tlsfingerprint.CodexHTTPTransportPolicy(), tr.policyFor(account, req))
	require.False(t, tr.policyFor(codexOAuthAccount(64, nil), req).Enabled())
}

type uaPolicyDialer struct {
	policyFakeDialer
	mu  sync.Mutex
	uas []string
}

func (d *uaPolicyDialer) DialWithPolicy(ctx context.Context, wsURL string, headers http.Header, proxyURL string, policy tlsfingerprint.TransportPolicy) (openAIWSClientConn, int, http.Header, error) {
	d.mu.Lock()
	d.uas = append(d.uas, headers.Get("User-Agent"))
	d.mu.Unlock()
	return d.policyFakeDialer.DialWithPolicy(ctx, wsURL, headers, proxyURL, policy)
}

func (d *uaPolicyDialer) Dial(ctx context.Context, wsURL string, headers http.Header, proxyURL string) (openAIWSClientConn, int, http.Header, error) {
	return d.DialWithPolicy(ctx, wsURL, headers, proxyURL, tlsfingerprint.TransportPolicy{})
}

func TestOpenAIWSPoolSelectsBaselineFromUpgradeUserAgent(t *testing.T) {
	cfg := codexTLSConfig(true)
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 4
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 4
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 1
	account := codexOAuthAccount(65, map[string]any{"enable_tls_fingerprint": true})
	svc := &OpenAIGatewayService{cfg: cfg, accountRepo: &accountFlagRepo{account: codexOAuthAccount(65, map[string]any{"enable_tls_fingerprint": true})}}
	dialer := &uaPolicyDialer{}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	pool.transportPolicyFor = svc.currentCodexWSTransportPolicy
	t.Cleanup(pool.Close)

	want := map[string]string{
		codexLinux147:     tlsfingerprint.BaselineCodex0155WSRustlsAwslcPQ,
		codexWindowsUA:    tlsfingerprint.BaselineCodex0163LinuxWSRustls,
		codexCLIUserAgent: tlsfingerprint.BaselineCodex0163LinuxWSRustls,
		"codex-tui/0.137.0 (Windows 10.0.26200; x86_64) unknown (codex-tui; 0.137.0)": tlsfingerprint.BaselineCodex0140WSRustlsRing,
	}
	for ua := range want {
		lease, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{
			Account: account, WSURL: "wss://chatgpt.com/backend-api/codex/responses",
			Headers: http.Header{"User-Agent": []string{ua}}, ForceNewConn: true,
		})
		require.NoError(t, err)
		lease.Release()
	}
	dialed := dialer.dialed()
	require.Len(t, dialed, len(want))
	for i, ua := range dialer.uas {
		require.Equal(t, want[ua], dialed[i].Baseline, "ua %q", ua)
		require.Equal(t, tlsfingerprint.RoleWS, dialed[i].Role)
	}
}
