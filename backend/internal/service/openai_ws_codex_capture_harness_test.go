package service

import (
	"context"
	"crypto/x509"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// TestCodexCaptureHarnessWS drives the production WS dialer through a local
// capture front so its ClientHellos can be compared with real codex-cli
// captures. Skipped unless CODEX_TLS_CAPTURE_PROXY and CODEX_TLS_CAPTURE_CA
// are set; see docs/CODEX_TLS_FINGERPRINT.md ("Reproducing the evidence").
func TestCodexCaptureHarnessWS(t *testing.T) {
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
	previous := openAIWSPolicyRootCAs
	openAIWSPolicyRootCAs = func() *x509.CertPool { return pool }
	defer func() { openAIWSPolicyRootCAs = previous }()

	dialer := newDefaultOpenAIWSClientDialer()
	for i := 0; i < 12; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		conn, status, _, err := dialOpenAIWS(ctx, dialer, "wss://chatgpt.com/backend-api/codex/responses",
			http.Header{"User-Agent": []string{"codex_exec/0.163.0"}, "OpenAI-Beta": []string{"responses_websockets=2026-02-06"}},
			proxyURL, tlsfingerprint.CodexWSTransportPolicy())
		if err != nil {
			cancel()
			t.Fatalf("dial %d: status %d: %v", i, status, err)
		}
		if err := conn.WriteJSON(ctx, map[string]any{"type": "response.create", "model": "gpt-5.5"}); err != nil {
			t.Fatal(err)
		}
		msg, err := conn.ReadMessage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("ws %d: status %d first event %.60s", i, status, msg)
		_ = conn.Close()
		cancel()
	}
}
