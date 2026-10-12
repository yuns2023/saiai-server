package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type codexResetRepo struct {
	stubOpenAIAccountRepo
	updates    map[string]any
	clears     int
	limited    time.Time
	writeError error
}

func (r *codexResetRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	if r.writeError != nil {
		return r.writeError
	}
	r.updates = updates
	return nil
}
func (r *codexResetRepo) ClearRateLimit(context.Context, int64) error { r.clears++; return nil }
func (r *codexResetRepo) SetRateLimited(_ context.Context, _ int64, until time.Time) error {
	r.limited = until
	return nil
}

func codexResetFixture(t *testing.T, handler http.HandlerFunc) (*OpenAIQuotaResetService, *codexResetRepo) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	repo := &codexResetRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "mock-token", "chatgpt_account_id": "mock-workspace", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)},
	}}}}
	svc := NewOpenAIQuotaResetService(repo, nil, NewOpenAITokenProvider(repo, nil, nil), nil)
	svc.baseURL = server.URL + "/backend-api/wham"
	return svc, repo
}

func resetUsageBody(allowed bool, percent float64) string {
	return fmt.Sprintf(`{"rate_limit":{"allowed":%t,"limit_reached":%t,"primary_window":{"used_percent":%g,"limit_window_seconds":18000,"reset_at":%d},"secondary_window":{"used_percent":12,"limit_window_seconds":604800,"reset_at":%d}}}`, allowed, !allowed, percent, time.Now().Add(time.Hour).Unix(), time.Now().Add(48*time.Hour).Unix())
}

func TestCodexResetListDoesNotConsumeOrMutate(t *testing.T) {
	svc, repo := codexResetFixture(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		require.Equal(t, "/backend-api/wham/rate-limit-reset-credits", r.URL.Path)
		require.Equal(t, "Bearer mock-token", r.Header.Get("Authorization"))
		require.Equal(t, "mock-workspace", r.Header.Get("ChatGPT-Account-Id"))
		_, _ = w.Write([]byte(`{"available_count":1,"credits":[{"id":"reset-1","reset_type":"codex_rate_limits","status":"available","granted_at":"2026-06-17T00:00:00Z","expires_at":null}],"private_field":"not-exposed"}`))
	})
	details, err := svc.List(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, details.AvailableCount)
	require.Equal(t, "reset-1", details.Credits[0].ID)
	require.Nil(t, repo.updates)
	require.Zero(t, repo.clears)
}

func TestCodexResetConsumeReconcilesAndForwardsIdempotency(t *testing.T) {
	consumes := 0
	svc, repo := codexResetFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			require.Equal(t, "POST", r.Method)
			require.Equal(t, "application/json", r.Header.Get("Content-Type"))
			var payload map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Equal(t, map[string]string{"redeem_request_id": "same-key", "credit_id": "reset-1"}, payload)
			consumes++
			if consumes == 1 {
				_, _ = w.Write([]byte(`{"code":"reset","windows_reset":2}`))
			} else {
				_, _ = w.Write([]byte(`{"code":"already_redeemed","windows_reset":0}`))
			}
		case "/backend-api/wham/usage":
			require.Equal(t, "GET", r.Method)
			_, _ = w.Write([]byte(resetUsageBody(true, 0)))
		default:
			t.Errorf("unexpected request %s (no model requests allowed)", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	for _, outcome := range []string{"reset", "already_redeemed"} {
		result, err := svc.Consume(context.Background(), 1, OpenAIResetRequest{"same-key", "reset-1"})
		require.NoError(t, err)
		require.Equal(t, outcome, result.Outcome)
		require.True(t, result.QuotaSynced)
	}
	require.Equal(t, 0.0, repo.updates["codex_5h_used_percent"])
	require.Equal(t, 12.0, repo.updates["codex_7d_used_percent"])
	require.NotEmpty(t, repo.updates["codex_5h_reset_at"])
	require.Equal(t, 2, repo.clears)
}

func TestCodexResetOutcomesAndRefreshFailures(t *testing.T) {
	for _, tc := range []struct {
		name, code, usage          string
		status                     int
		writeFail, synced, cleared bool
	}{
		{"no-credit", "no_credit", "", 200, false, false, false},
		{"nothing-to-reset", "nothing_to_reset", resetUsageBody(true, 0), 200, false, true, true},
		{"unknown-permission", "reset", strings.Replace(resetUsageBody(true, 0), `"allowed":true,`, "", 1), 200, false, true, false},
		{"still-limited", "reset", resetUsageBody(false, 100), 200, false, true, false},
		{"usage-failed", "reset", "sensitive-provider-body", 503, false, false, false},
		{"missing-windows", "reset", `{"rate_limit":{"allowed":true,"limit_reached":false}}`, 200, false, false, false},
		{"db-failed", "reset", resetUsageBody(true, 0), 200, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usageCalls := 0
			svc, repo := codexResetFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/consume") {
					_, _ = fmt.Fprintf(w, `{"code":%q,"windows_reset":2}`, tc.code)
					return
				}
				require.True(t, strings.HasSuffix(r.URL.Path, "/usage"))
				usageCalls++
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.usage))
			})
			if tc.writeFail {
				repo.writeError = errors.New("database unavailable")
			}
			result, err := svc.Consume(context.Background(), 1, OpenAIResetRequest{"key", "credit"})
			require.NoError(t, err)
			require.Equal(t, tc.code, result.Outcome)
			require.Equal(t, tc.synced, result.QuotaSynced)
			require.Equal(t, tc.cleared, repo.clears > 0)
			if tc.code == "no_credit" {
				require.Zero(t, usageCalls)
				require.Nil(t, repo.updates)
			}
			if tc.name == "still-limited" {
				require.False(t, repo.limited.IsZero())
			}
		})
	}
}

func TestCodexResetRejectsUnsupportedAndInvalidInputs(t *testing.T) {
	svc, repo := codexResetFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("must not contact upstream") })
	for _, input := range []OpenAIResetRequest{{"", "credit"}, {"key", ""}, {strings.Repeat("x", 129), "credit"}} {
		_, err := svc.Consume(context.Background(), 1, input)
		require.Error(t, err)
	}
	for _, account := range []Account{{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, {Platform: PlatformAnthropic, Type: AccountTypeOAuth}, {Platform: PlatformOpenAI, Type: AccountTypeOAuth}} {
		account.ID = 1
		repo.accounts[0] = account
		_, err := svc.List(context.Background(), 1)
		require.Error(t, err)
		_, err = svc.Consume(context.Background(), 1, OpenAIResetRequest{"key", "credit"})
		require.Error(t, err)
	}
	require.Nil(t, repo.updates)
	require.Zero(t, repo.clears)
}

type codexResetProxyRepo struct {
	ProxyRepository
	proxy *Proxy
	err   error
}

func (r codexResetProxyRepo) GetByID(context.Context, int64) (*Proxy, error) { return r.proxy, r.err }

func TestCodexResetUsesConfiguredProxyAndFailsClosed(t *testing.T) {
	calls := 0
	svc, repo := codexResetFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("must not bypass the configured proxy") })
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "http://provider.invalid/backend-api/wham/rate-limit-reset-credits", r.URL.String())
		require.Equal(t, "Bearer mock-token", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"available_count":0,"credits":[]}`))
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(proxyURL.Port())
	require.NoError(t, err)
	svc.baseURL = "http://provider.invalid/backend-api/wham"
	proxyID := int64(9)
	repo.accounts[0].ProxyID = &proxyID
	svc.proxyRepo = codexResetProxyRepo{proxy: &Proxy{Protocol: "http", Host: proxyURL.Hostname(), Port: port}}
	_, err = svc.List(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	svc.proxyRepo = codexResetProxyRepo{err: errors.New("private-proxy-details")}
	_, err = svc.List(context.Background(), 1)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private-proxy-details")
	require.Equal(t, 1, calls)
}

func TestCodexResetDefaultsOmittedWindowCountLikeOfficialClient(t *testing.T) {
	svc, _ := codexResetFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/consume") {
			_, _ = w.Write([]byte(`{"code":"reset"}`))
			return
		}
		_, _ = w.Write([]byte(resetUsageBody(true, 0)))
	})
	result, err := svc.Consume(context.Background(), 1, OpenAIResetRequest{"key", "credit"})
	require.NoError(t, err)
	require.True(t, result.QuotaSynced)
	require.Zero(t, result.WindowsReset)
}

func TestCodexResetRedactsFailuresAndRejectsUnknownOutcome(t *testing.T) {
	for _, response := range []struct {
		status int
		body   string
	}{{401, "private-token"}, {200, `{"code":"unexpected"}`}, {200, `{"secret":"private-token"}`}, {302, "private-token"}} {
		svc, repo := codexResetFixture(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "/redirect")
			w.WriteHeader(response.status)
			_, _ = w.Write([]byte(response.body))
		})
		_, err := svc.Consume(context.Background(), 1, OpenAIResetRequest{"key", "credit"})
		require.Error(t, err)
		require.NotContains(t, err.Error(), "private-token")
		require.Nil(t, repo.updates)
		require.Zero(t, repo.clears)
	}
}
