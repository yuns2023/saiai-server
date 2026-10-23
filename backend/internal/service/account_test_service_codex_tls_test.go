//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func completedSSE() *http.Response {
	resp := newJSONResponse(http.StatusOK, "")
	resp.Body = io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"))
	return resp
}

// OpenAI accounts never get the Claude TLS profile; an opted-in OAuth account
// is tested with the same Codex HTTP identity its traffic uses.
func TestAccountTestService_OpenAIUsesCodexPolicyNeverClaudeProfile(t *testing.T) {
	ctx, _ := newAccountTestContext()
	upstream := &policyRecordingUpstream{httpUpstreamRecorder: httpUpstreamRecorder{resp: completedSSE()}}
	svc := &AccountTestService{accountRepo: &openAIAccountTestRepo{}, httpUpstream: upstream, cfg: codexTLSConfig(true)}
	account := codexOAuthAccount(91, map[string]any{"enable_tls_fingerprint": true})
	require.NoError(t, svc.testOpenAIAccountConnection(ctx, account, "gpt-5.4"))
	require.Equal(t, []tlsfingerprint.TransportPolicy{tlsfingerprint.CodexHTTPTransportPolicy()}, upstream.policies)

	for _, tc := range []struct {
		name    string
		account *Account
	}{
		{"oauth_default_off", codexOAuthAccount(92, nil)},
		{"apikey_with_flag", &Account{ID: 93, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test"}, Extra: map[string]any{"enable_tls_fingerprint": true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := newAccountTestContext()
			queued := &queuedHTTPUpstream{responses: []*http.Response{completedSSE()}}
			svc := &AccountTestService{accountRepo: &openAIAccountTestRepo{}, httpUpstream: queued, cfg: codexTLSConfig(true)}
			_ = svc.testOpenAIAccountConnection(ctx, tc.account, "gpt-5.4")
			require.Equal(t, []bool{false}, queued.tlsFlags, "OpenAI must never request the Claude TLS profile")
		})
	}
}
