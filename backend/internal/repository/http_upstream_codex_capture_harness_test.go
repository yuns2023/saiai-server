package repository

import (
	"crypto/x509"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// TestCodexCaptureHarnessHTTP drives the production HTTP policy path through a
// local capture front (loopback CONNECT proxy + TLS terminator, never
// upstream) so its ClientHellos can be compared with real codex-cli captures.
// Skipped unless CODEX_TLS_CAPTURE_PROXY and CODEX_TLS_CAPTURE_CA are set; see
// docs/CODEX_TLS_FINGERPRINT.md ("Reproducing the evidence").
func TestCodexCaptureHarnessHTTP(t *testing.T) {
	proxyURL, caFile := os.Getenv("CODEX_TLS_CAPTURE_PROXY"), os.Getenv("CODEX_TLS_CAPTURE_CA")
	if proxyURL == "" || caFile == "" {
		t.Skip("set CODEX_TLS_CAPTURE_PROXY and CODEX_TLS_CAPTURE_CA to run the capture harness")
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("no CA certificates")
	}
	previous := policyTransportRootCAs
	policyTransportRootCAs = func() *x509.CertPool { return pool }
	defer func() { policyTransportRootCAs = previous }()

	svc, ok := NewHTTPUpstream(policyTestConfig(2)).(*httpUpstreamService)
	if !ok {
		t.Fatal("unexpected upstream implementation")
	}
	defer closeAllClients(svc)
	for i := int64(0); i < 3; i++ {
		for _, target := range []struct{ method, url, body string }{
			{http.MethodGet, "https://chatgpt.com/backend-api/codex/models?client_version=0.163.0", ""},
			{http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", `{"model":"gpt-5.5","stream":true}`},
		} {
			req, _ := http.NewRequest(target.method, target.url, strings.NewReader(target.body))
			req.Header.Set("User-Agent", "codex_exec/0.163.0")
			// A distinct account per request forces a fresh connection/handshake.
			resp, err := svc.DoWithTransportPolicy(req, proxyURL, 1000+i*10+int64(len(target.body)%7), 1, tlsfingerprint.CodexHTTPTransportPolicy())
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			t.Logf("%s %s -> %s %d", target.method, target.url, resp.Proto, resp.StatusCode)
		}
	}
	time.Sleep(200 * time.Millisecond)
}
