package repository

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// codexOpenSSLExtensionsWithoutSNI is the pinned OpenSSL order minus SNI
// (the fixtures dial an IP literal, for which OpenSSL sends no SNI).
var codexOpenSSLExtensionsWithoutSNI = []uint16{65281, 11, 10, 35, 22, 23, 13, 43, 45, 51}

type observedHello struct {
	extensions []uint16
	alpn       []string
}

// newPolicyTLSServer starts a TLS fixture that records each ClientHello and
// trusts it for policy transports for the duration of the test.
func newPolicyTLSServer(t *testing.T, handler http.Handler) (*httptest.Server, func() []observedHello) {
	t.Helper()
	var mu sync.Mutex
	var hellos []observedHello
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		mu.Lock()
		hellos = append(hellos, observedHello{extensions: append([]uint16(nil), h.Extensions...), alpn: append([]string(nil), h.SupportedProtos...)})
		mu.Unlock()
		return nil, nil
	}}
	server.StartTLS()
	t.Cleanup(server.Close)
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	previous := policyTransportRootCAs
	policyTransportRootCAs = func() *x509.CertPool { return pool }
	t.Cleanup(func() { policyTransportRootCAs = previous })
	return server, func() []observedHello {
		mu.Lock()
		defer mu.Unlock()
		return append([]observedHello(nil), hellos...)
	}
}

func policyTestConfig(reserve int) *config.Config {
	cfg := longConnectionTestConfig(true, reserve)
	cfg.Gateway.TLSFingerprint.Enabled = true
	return cfg
}

func closeAllClients(svc *httpUpstreamService) {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	for _, e := range svc.clients {
		e.client.CloseIdleConnections()
	}
}

func TestDoWithTransportPolicySendsPinnedCodexHelloOverHTTP1(t *testing.T) {
	server, hellos := newPolicyTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto)
	}))
	svc, ok := NewHTTPUpstream(policyTestConfig(2)).(*httpUpstreamService)
	require.True(t, ok)
	defer closeAllClients(svc)

	req, err := http.NewRequest(http.MethodGet, server.URL+"/backend-api/codex/models", nil)
	require.NoError(t, err)
	resp, err := svc.DoWithTransportPolicy(req, "", 7, 1, tlsfingerprint.CodexHTTPTransportPolicy())
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, "HTTP/1.1", string(body))

	seen := hellos()
	require.Len(t, seen, 1)
	require.Equal(t, codexOpenSSLExtensionsWithoutSNI, seen[0].extensions)
	require.Empty(t, seen[0].alpn, "Codex HTTP baseline sends no ALPN")

	require.Len(t, svc.clients, 1)
	for key, entry := range svc.clients {
		require.True(t, strings.HasPrefix(key, "tlspolicy:codex/"+tlsfingerprint.BaselineCodex0163LinuxHTTPOpenSSL+"/http|"), key)
		transport := entry.client.Transport.(*http.Transport)
		require.Nil(t, transport.Proxy, "policy transports tunnel inside the dialer")
		require.NotNil(t, transport.TLSNextProto)
		require.Empty(t, transport.TLSNextProto, "HTTP/2 must be disabled")
		require.Equal(t, 3, transport.MaxConnsPerHost, "bounded aux reserve on the HTTP/1.1 pool")
		require.Zero(t, atomic.LoadInt64(&entry.inFlight))
	}
}

func TestDoWithTransportPolicyRespectsGlobalSwitchAndZeroPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	for _, tc := range []struct {
		name   string
		global bool
		policy tlsfingerprint.TransportPolicy
	}{
		{"global_off", false, tlsfingerprint.CodexHTTPTransportPolicy()},
		{"zero_policy", true, tlsfingerprint.TransportPolicy{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := policyTestConfig(2)
			cfg.Gateway.TLSFingerprint.Enabled = tc.global
			svc := NewHTTPUpstream(cfg).(*httpUpstreamService)
			defer closeAllClients(svc)
			req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
			resp, err := svc.DoWithTransportPolicy(req, "", 1, 1, tc.policy)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			for key := range svc.clients {
				require.False(t, strings.HasPrefix(key, "tls"), key)
			}
		})
	}
}

func TestDoWithTransportPolicyFailsLoudlyInsteadOfFallingBack(t *testing.T) {
	var hits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer plain.Close()
	svc := NewHTTPUpstream(policyTestConfig(2)).(*httpUpstreamService)
	defer closeAllClients(svc)

	// Unknown baseline / role mismatch.
	req, _ := http.NewRequest(http.MethodGet, plain.URL, nil)
	_, err := svc.DoWithTransportPolicy(req, "", 1, 1, tlsfingerprint.TransportPolicy{Family: tlsfingerprint.FamilyCodex, Baseline: tlsfingerprint.BaselineCodex0163LinuxWSRustls, Role: tlsfingerprint.RoleHTTP})
	require.ErrorIs(t, err, tlsfingerprint.ErrUnknownTransportPolicy)
	// Plain HTTP would bypass both the fingerprint and the proxy tunnel.
	req, _ = http.NewRequest(http.MethodGet, plain.URL, nil)
	_, err = svc.DoWithTransportPolicy(req, "", 1, 1, tlsfingerprint.CodexHTTPTransportPolicy())
	require.Error(t, err)
	require.Zero(t, hits.Load(), "no request may be sent without the requested TLS policy")
}

func TestDoWithTransportPolicyKeepsNativeCodexNoRedirectPolicy(t *testing.T) {
	var redirected atomic.Int32
	var posts atomic.Int32
	server, _ := newPolicyTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			redirected.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		posts.Add(1)
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Location", "/target")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	svc := NewHTTPUpstream(policyTestConfig(2)).(*httpUpstreamService)
	defer closeAllClients(svc)
	policy := tlsfingerprint.CodexHTTPTransportPolicy()

	wire := []byte(`{"model":"m"}`)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/responses", bytes.NewReader(wire))
	req = req.WithContext(service.WithNativeCodexRedirectPolicy(req.Context()))
	resp, err := svc.DoWithTransportPolicy(req, "", 9, 1, policy)
	require.NoError(t, err)
	require.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, int32(1), posts.Load())
	require.Zero(t, redirected.Load(), "TLS policy path must not issue an implicit second request")

	// The shared pooled client is unchanged for requests without the policy.
	req, _ = http.NewRequest(http.MethodPost, server.URL+"/responses", bytes.NewReader(wire))
	resp, err = svc.DoWithTransportPolicy(req, "", 9, 1, policy)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, int32(1), redirected.Load())
	for _, entry := range svc.clients {
		require.Zero(t, atomic.LoadInt64(&entry.inFlight), "body close must release in-flight accounting")
	}
}

func TestDoWithTransportPolicyAuxReserveLetsControlRequestPassOpenSSE(t *testing.T) {
	slowStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	server, _ := newPolicyTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			slowStarted <- struct{}{}
			<-release
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	svc := NewHTTPUpstream(policyTestConfig(1)).(*httpUpstreamService)
	defer closeAllClients(svc)
	policy := tlsfingerprint.CodexHTTPTransportPolicy()
	entry, err := svc.getClientEntryWithPolicy("", 3, 1, policy, false, false)
	require.NoError(t, err)
	require.Equal(t, 2, entry.client.Transport.(*http.Transport).MaxConnsPerHost)

	// Account concurrency 1: the SSE stream saturates the model slot. (With
	// reserve 0 this test times out: the reserve is what lets control pass.)
	var slow *http.Response
	slowReq, _ := http.NewRequest(http.MethodPost, server.URL+"/slow", strings.NewReader("{}"))
	slow, err = svc.DoWithTransportPolicy(slowReq, "", 3, 1, policy)
	require.NoError(t, err)
	<-slowStarted
	defer func() { close(release); _ = slow.Body.Close() }()

	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/backend-api/codex/models", nil)
		resp, err := svc.DoWithTransportPolicy(req, "", 3, 1, policy)
		if err == nil {
			err = resp.Body.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("control request queued behind the open SSE stream")
	}
}

func TestTransportPolicyAndClaudeFingerprintUseSeparatePools(t *testing.T) {
	server, _ := newPolicyTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	svc := NewHTTPUpstream(policyTestConfig(2)).(*httpUpstreamService)
	defer closeAllClients(svc)
	for _, policy := range []tlsfingerprint.TransportPolicy{{}, tlsfingerprint.CodexHTTPTransportPolicy(), {}} {
		req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
		resp, err := svc.DoWithTransportPolicy(req, "", 5, 1, policy)
		if policy.Enabled() {
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
		} else if err == nil {
			// The standard transport does not trust the fixture; only the
			// pool bookkeeping matters here.
			_ = resp.Body.Close()
		}
	}
	var policyKeys, standardKeys int
	for key := range svc.clients {
		if strings.HasPrefix(key, "tlspolicy:") {
			policyKeys++
		} else {
			standardKeys++
		}
	}
	require.Equal(t, 1, policyKeys)
	require.Equal(t, 1, standardKeys)
	// A Claude fingerprint client for the same account keys separately too.
	claude, err := svc.getClientEntryWithTLS("", 5, 1, tlsfingerprint.DefaultProfile(), false, false)
	require.NoError(t, err)
	policyEntry, err := svc.getClientEntryWithPolicy("", 5, 1, tlsfingerprint.CodexHTTPTransportPolicy(), false, false)
	require.NoError(t, err)
	require.NotSame(t, claude, policyEntry)
	require.Equal(t, 1, claude.client.Transport.(*http.Transport).MaxConnsPerHost, "Claude pool size unchanged")
}
