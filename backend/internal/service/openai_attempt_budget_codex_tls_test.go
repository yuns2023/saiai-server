//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// policySequenceUpstream records policy-path calls and only accepts requests
// through DoWithTransportPolicy (a std-TLS Do call fails the test).
type policySequenceUpstream struct {
	httpUpstreamSequenceRecorder
	t        *testing.T
	policies []tlsfingerprint.TransportPolicy
}

func (u *policySequenceUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.t.Fatal("opted-in Codex request must not use the standard transport")
	return nil, nil
}

func (u *policySequenceUpstream) DoWithTransportPolicy(req *http.Request, proxyURL string, accountID int64, concurrency int, policy tlsfingerprint.TransportPolicy) (*http.Response, error) {
	u.policies = append(u.policies, policy)
	return u.httpUpstreamSequenceRecorder.Do(req, proxyURL, accountID, concurrency)
}

// The TLS policy path keeps the attempt-budget contract: one reservation per
// dispatch, no implicit redirect, no replayable body, and a denied retry is
// never sent.
func TestOpenAIProviderAttemptBudget_CodexTLSPolicyPathKeepsBudgetRedirectAndNoReplay(t *testing.T) {
	svc, cache := attemptBudgetTestService(1)
	svc.cfg.Gateway.TLSFingerprint.Enabled = true
	c, recorder := auditOAuthPolicyContext("/v1/responses")
	c.Set("api_key", &APIKey{ID: 7})
	upstream := &policySequenceUpstream{t: t, httpUpstreamSequenceRecorder: httpUpstreamSequenceRecorder{responses: []*http.Response{
		auditOAuthPolicyResponse(500, http.Header{"Content-Type": {"application/json"}}, `{"error":{"message":"TEST_ONLY_retry"}}`),
		auditOAuthPolicyResponse(200, http.Header{"Content-Type": {"application/json"}}, `{"id":"resp_should_not_be_sent"}`),
	}}}
	svc.httpUpstream = upstream
	account := auditOAuthPolicyAccount()
	account.Extra = map[string]any{"enable_tls_fingerprint": true}
	body := []byte(`{ "model":"gpt-6.1-sol", "stream":false, "input":[] }`)
	_, err := svc.Forward(context.Background(), c, account, body)
	require.ErrorIs(t, err, ErrOpenAIProviderAttemptBudgetExhausted)
	require.Equal(t, 1, upstream.callCount, "denied retry must not be dispatched")
	require.Equal(t, []tlsfingerprint.TransportPolicy{tlsfingerprint.CodexHTTPTransportPolicy()}, upstream.policies)
	require.Equal(t, body, upstream.bodies[0])
	require.Nil(t, upstream.reqs[0].GetBody, "body must not be replayable by the transport")
	require.True(t, NativeCodexRejectsRedirects(upstream.reqs[0].Context()))
	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
	require.Equal(t, int64(1), cache.used)
}
