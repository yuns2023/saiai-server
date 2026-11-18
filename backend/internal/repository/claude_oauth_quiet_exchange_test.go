package repository

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

// The hosted self-onboarding exchange logs stage/status/correlation id only:
// no request body (code/verifier, even redacted), no raw transport error, no
// response body; returned errors carry no body either.
func TestClaudeExchangeQuietModeLogsNoBodies(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	const secretBody = `{"error":"invalid_grant","leak":"RESPONSEBODYSECRET"}`
	rt := newInProcessTransport(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(secretBody))
	}), nil)
	client, ok := NewClaudeOAuthClient().(*claudeOAuthService)
	require.True(t, ok)
	client.tokenURL = "http://in-process/token"
	client.clientFactory = func(string) (*req.Client, error) { return newTestReqClient(rt), nil }

	ctx := service.WithQuietOAuthExchange(context.Background(), "cid-1")
	_, err := client.ExchangeCodeForToken(ctx, "CODEVALUE#STATEVALUE", "VERIFIERVALUE", "STATEVALUE", "", true)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "RESPONSEBODYSECRET")
	logs := buf.String()
	require.Contains(t, logs, "correlation_id=cid-1", "log capture works")
	require.Contains(t, logs, "status=400")
	for _, secret := range []string{"RESPONSEBODYSECRET", "VERIFIERVALUE", "CODEVALUE", "Request Body", "redacted"} {
		require.NotContains(t, logs, secret)
	}

	// Transport failure: only a timeout flag, never the raw error text.
	buf.Reset()
	client.clientFactory = func(string) (*req.Client, error) {
		return newTestReqClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("proxyconnect tcp: dial tcp RAWNETWORKDETAIL")
		})), nil
	}
	_, err = client.ExchangeCodeForToken(ctx, "CODEVALUE#STATEVALUE", "VERIFIERVALUE", "STATEVALUE", "", true)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "RAWNETWORKDETAIL")
	require.Contains(t, buf.String(), "stage=exchange_request_failed timeout=false correlation_id=cid-1")
	require.NotContains(t, buf.String(), "RAWNETWORKDETAIL")

	// The admin path keeps its previous messages.
	_, err = client.ExchangeCodeForToken(context.Background(), "CODEVALUE#STATEVALUE", "VERIFIERVALUE", "STATEVALUE", "", true)
	require.ErrorContains(t, err, "RAWNETWORKDETAIL")
}
