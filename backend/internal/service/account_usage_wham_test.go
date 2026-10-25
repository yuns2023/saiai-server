package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type whamUsageRecorder struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
	status   int
	body     string
}

func (r *whamUsageRecorder) handler(w http.ResponseWriter, req *http.Request) {
	data, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.requests = append(r.requests, req.Clone(context.Background()))
	r.bodies = append(r.bodies, string(data))
	status, body := r.status, r.body
	r.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (r *whamUsageRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func whamUsageFixture(t *testing.T, body string) (*AccountUsageService, *whamUsageRecorder, *accountUsageCodexProbeRepo) {
	t.Helper()
	recorder := &whamUsageRecorder{body: body}
	server := httptest.NewServer(http.HandlerFunc(recorder.handler))
	t.Cleanup(server.Close)
	repo := &accountUsageCodexProbeRepo{updateExtraCh: make(chan map[string]any, 4), rateLimitCh: make(chan time.Time, 4)}
	svc := &AccountUsageService{accountRepo: repo, cache: NewUsageCache(), whamUsageURL: server.URL + "/backend-api/wham/usage"}
	return svc, recorder, repo
}

func whamOAuthAccount() *Account {
	return &Account{
		ID: 41, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "TEST_ONLY_TOKEN", "chatgpt_account_id": "TEST_ONLY_WORKSPACE"},
		Extra:       map[string]any{},
	}
}

func whamSortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestFetchCodexUsageViaWhamMatchesHeaderSnapshotShape(t *testing.T) {
	primaryReset := time.Now().Add(90 * time.Minute).Unix()
	body := fmt.Sprintf(`{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100,"limit_window_seconds":18000,"reset_at":%d},"secondary_window":{"used_percent":12,"limit_window_seconds":604800,"reset_at":%d}}}`,
		primaryReset, time.Now().Add(72*time.Hour).Unix())
	svc, recorder, _ := whamUsageFixture(t, body)

	updates, resetAt, observedAt, err := svc.fetchCodexUsageViaWham(context.Background(), whamOAuthAccount())
	require.NoError(t, err)
	require.False(t, observedAt.IsZero())
	require.Equal(t, 1, recorder.count())
	req := recorder.requests[0]
	require.Equal(t, http.MethodGet, req.Method)
	require.Equal(t, "/backend-api/wham/usage", req.URL.Path)
	require.Equal(t, "Bearer TEST_ONLY_TOKEN", req.Header.Get("Authorization"))
	require.Equal(t, "TEST_ONLY_WORKSPACE", req.Header.Get("ChatGPT-Account-Id"))
	require.Equal(t, "application/json", req.Header.Get("Accept"))
	require.Equal(t, codexCLIUserAgent, req.Header.Get("User-Agent"))
	require.Empty(t, recorder.bodies[0], "a control-plane GET carries no model payload")

	// The same windows reported through Codex response headers.
	headers := make(http.Header)
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-window-minutes", "300")
	headers.Set("x-codex-primary-reset-after-seconds", fmt.Sprint(primaryReset-time.Now().Unix()))
	headers.Set("x-codex-secondary-used-percent", "12")
	headers.Set("x-codex-secondary-window-minutes", "10080")
	headers.Set("x-codex-secondary-reset-after-seconds", fmt.Sprint(72*3600))
	headerUpdates, headerResetAt, err := extractOpenAICodexProbeSnapshot(&http.Response{StatusCode: http.StatusOK, Header: headers})
	require.NoError(t, err)

	require.Equal(t, whamSortedKeys(headerUpdates), whamSortedKeys(updates))
	for _, key := range []string{"codex_5h_used_percent", "codex_7d_used_percent", "codex_primary_used_percent", "codex_secondary_used_percent"} {
		require.Equal(t, headerUpdates[key], updates[key], key)
	}
	require.NotNil(t, resetAt)
	require.NotNil(t, headerResetAt)
	require.WithinDuration(t, *headerResetAt, *resetAt, 2*time.Second)
	require.WithinDuration(t, time.Unix(primaryReset, 0), *resetAt, 2*time.Second)
}

func TestFetchCodexUsageViaWhamSkipsAndRejects(t *testing.T) {
	svc, recorder, _ := whamUsageFixture(t, `{}`)
	apiKeyAccount := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "TEST_ONLY"}}
	noWorkspace := whamOAuthAccount()
	delete(noWorkspace.Credentials, "chatgpt_account_id")
	for _, account := range []*Account{nil, apiKeyAccount, noWorkspace} {
		updates, resetAt, _, err := svc.fetchCodexUsageViaWham(context.Background(), account)
		require.NoError(t, err)
		require.Nil(t, updates)
		require.Nil(t, resetAt)
	}
	require.Zero(t, recorder.count())

	// Incomplete windows and non-200 responses are errors, never partial data.
	_, _, _, err := svc.fetchCodexUsageViaWham(context.Background(), whamOAuthAccount())
	require.Error(t, err)
	recorder.mu.Lock()
	recorder.status, recorder.body = http.StatusUnauthorized, `{"detail":"TEST_ONLY"}`
	recorder.mu.Unlock()
	_, _, _, err = svc.fetchCodexUsageViaWham(context.Background(), whamOAuthAccount())
	require.Error(t, err)
}

func TestFetchCodexUsageViaWhamAcceptsSingleWindow(t *testing.T) {
	body := fmt.Sprintf(`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":null,"secondary_window":{"used_percent":30,"limit_window_seconds":604800,"reset_at":%d}}}`, time.Now().Add(48*time.Hour).Unix())
	svc, _, _ := whamUsageFixture(t, body)
	updates, resetAt, _, err := svc.fetchCodexUsageViaWham(context.Background(), whamOAuthAccount())
	require.NoError(t, err)
	require.Nil(t, resetAt)
	require.Equal(t, 30.0, updates["codex_7d_used_percent"])
	_, has5h := updates["codex_5h_used_percent"]
	require.False(t, has5h)

	// Reset reconciliation still requires both windows.
	var usage codexWhamUsage
	require.NoError(t, json.Unmarshal([]byte(body), &usage))
	_, _, err = codexWhamUsageSnapshot(&usage, time.Now().UTC(), true)
	require.Error(t, err)
}

func TestGetOpenAIUsageRefreshesViaWhamWithoutModelRequest(t *testing.T) {
	body := fmt.Sprintf(`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":40,"limit_window_seconds":18000,"reset_at":%d},"secondary_window":{"used_percent":12,"limit_window_seconds":604800,"reset_at":%d}}}`,
		time.Now().Add(time.Hour).Unix(), time.Now().Add(48*time.Hour).Unix())
	svc, recorder, repo := whamUsageFixture(t, body)
	account := whamOAuthAccount()

	usage, err := svc.getOpenAIUsage(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, 1, recorder.count())
	require.Equal(t, "/backend-api/wham/usage", recorder.requests[0].URL.Path, "no synthetic Responses request")
	require.NotNil(t, usage.FiveHour)
	require.InDelta(t, 40, usage.FiveHour.Utilization, 0.01)
	require.NotNil(t, usage.SevenDay)
	require.InDelta(t, 12, usage.SevenDay.Utilization, 0.01)
	select {
	case updates := <-repo.updateExtraCh:
		require.Equal(t, 40.0, updates["codex_5h_used_percent"])
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot was not persisted")
	}

	// The 10-minute per-account throttle still applies.
	account.Extra = map[string]any{}
	_, err = svc.getOpenAIUsage(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, 1, recorder.count())
}
