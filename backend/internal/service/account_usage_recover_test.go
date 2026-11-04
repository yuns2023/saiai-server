package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const recoverableErrorMessage = "Authentication failed (401): UNAUTHENTICATED"

// deadRefreshTokenMessages are written by the token refresh service; only re-authorization fixes them.
var deadRefreshTokenMessages = []string{
	"Token refresh failed (non-retryable): invalid_grant",
	"Token refresh failed (non-retryable): invalid_refresh_token",
	"Token refresh failed (non-retryable): invalid_client",
}

// usageRecoverRepo records ClearError calls; other writes are accepted and ignored.
type usageRecoverRepo struct {
	AccountRepository
	account *Account

	mu      sync.Mutex
	cleared []int64
}

func (r *usageRecoverRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	copied := *r.account
	return &copied, nil
}

func (r *usageRecoverRepo) ClearError(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleared = append(r.cleared, id)
	return nil
}

func (r *usageRecoverRepo) clearCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.cleared)
}

func (r *usageRecoverRepo) UpdateExtra(context.Context, int64, map[string]any) error { return nil }
func (r *usageRecoverRepo) SetRateLimited(context.Context, int64, time.Time) error   { return nil }

type countingClaudeUsageFetcher struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *countingClaudeUsageFetcher) FetchUsage(ctx context.Context, accessToken, proxyURL string) (*ClaudeUsageResponse, error) {
	return f.FetchUsageWithOptions(ctx, &ClaudeUsageFetchOptions{AccessToken: accessToken, ProxyURL: proxyURL})
}

func (f *countingClaudeUsageFetcher) FetchUsageWithOptions(context.Context, *ClaudeUsageFetchOptions) (*ClaudeUsageResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &ClaudeUsageResponse{}, nil
}

func (f *countingClaudeUsageFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func erroredAnthropicOAuthAccount() *Account {
	return &Account{
		ID: 77, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusError,
		ErrorMessage: recoverableErrorMessage,
		Credentials:  map[string]any{"access_token": "TEST_ONLY_TOKEN"},
	}
}

func newClaudeRecoverFixture(account *Account, fetcher *countingClaudeUsageFetcher) (*AccountUsageService, *usageRecoverRepo) {
	repo := &usageRecoverRepo{account: account}
	svc := &AccountUsageService{accountRepo: repo, usageFetcher: fetcher, cache: NewUsageCache()}
	// Window stats are cached so the test does not need a usage log repository.
	svc.cache.windowStatsCache.Store(account.ID, &windowStatsCache{stats: &WindowStats{}, timestamp: time.Now()})
	return svc, repo
}

func TestGetUsageAnthropicCachedResponseDoesNotClearError(t *testing.T) {
	account := erroredAnthropicOAuthAccount()
	fetcher := &countingClaudeUsageFetcher{}
	svc, repo := newClaudeRecoverFixture(account, fetcher)
	// A success cached shortly before the account errored proves nothing about now.
	svc.cache.apiCache.Store(account.ID, &apiUsageCache{response: &ClaudeUsageResponse{}, timestamp: time.Now()})

	_, err := svc.GetUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.Zero(t, fetcher.callCount())
	require.Zero(t, repo.clearCount(), "cached usage must not recover the account")
}

func TestGetUsageAnthropicFreshSuccessClearsRecoverableError(t *testing.T) {
	account := erroredAnthropicOAuthAccount()
	fetcher := &countingClaudeUsageFetcher{}
	svc, repo := newClaudeRecoverFixture(account, fetcher)

	_, err := svc.GetUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 1, fetcher.callCount())
	require.Equal(t, []int64{account.ID}, repo.cleared)

	// The second view is served from the success cache and does not clear again.
	_, err = svc.GetUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 1, fetcher.callCount())
	require.Equal(t, 1, repo.clearCount())
}

func TestGetUsageAnthropicFreshFailureKeepsError(t *testing.T) {
	account := erroredAnthropicOAuthAccount()
	fetcher := &countingClaudeUsageFetcher{err: fmt.Errorf("usage returned status 401")}
	svc, repo := newClaudeRecoverFixture(account, fetcher)

	_, err := svc.GetUsage(context.Background(), account.ID)
	require.Error(t, err)
	require.Zero(t, repo.clearCount())
}

func TestGetUsageOpenAISnapshotOnlyDoesNotClearError(t *testing.T) {
	svc, recorder, _ := whamUsageFixture(t, `{}`)
	account := whamOAuthAccount()
	account.Status = StatusError
	account.ErrorMessage = recoverableErrorMessage
	// Both windows are present and the snapshot is not stale, so no upstream probe runs.
	account.Extra = map[string]any{
		"codex_5h_used_percent": 10.0,
		"codex_5h_reset_at":     time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"codex_7d_used_percent": 20.0,
		"codex_7d_reset_at":     time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
	}
	repo := &usageRecoverRepo{account: account}
	svc.accountRepo = repo

	_, err := svc.GetUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.Zero(t, recorder.count())
	require.Zero(t, repo.clearCount(), "reading the stored snapshot must not recover the account")
}

func TestGetUsageOpenAIWhamSuccessClearsRecoverableError(t *testing.T) {
	body := fmt.Sprintf(`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":40,"limit_window_seconds":18000,"reset_at":%d},"secondary_window":{"used_percent":12,"limit_window_seconds":604800,"reset_at":%d}}}`,
		time.Now().Add(time.Hour).Unix(), time.Now().Add(48*time.Hour).Unix())
	svc, recorder, _ := whamUsageFixture(t, body)
	account := whamOAuthAccount()
	account.Status = StatusError
	account.ErrorMessage = recoverableErrorMessage
	repo := &usageRecoverRepo{account: account}
	svc.accountRepo = repo

	_, err := svc.GetUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 1, recorder.count())
	require.Equal(t, []int64{account.ID}, repo.cleared)
}

func TestGetUsageOpenAIWhamFailureKeepsError(t *testing.T) {
	svc, recorder, _ := whamUsageFixture(t, `{}`)
	recorder.status = 401
	account := whamOAuthAccount()
	account.Status = StatusError
	account.ErrorMessage = recoverableErrorMessage
	repo := &usageRecoverRepo{account: account}
	svc.accountRepo = repo

	_, err := svc.GetUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 1, recorder.count())
	require.Zero(t, repo.clearCount())
}

func TestGetUsageGeminiLocalStatsDoNotClearError(t *testing.T) {
	account := &Account{ID: 88, Platform: PlatformGemini, Type: AccountTypeOAuth, Status: StatusError, ErrorMessage: recoverableErrorMessage}
	repo := &usageRecoverRepo{account: account}
	svc := &AccountUsageService{accountRepo: repo, cache: NewUsageCache()}

	_, err := svc.GetUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.Zero(t, repo.clearCount())
}

func TestGetUsageAnthropicFreshSuccessKeepsDeadRefreshTokenError(t *testing.T) {
	for _, msg := range deadRefreshTokenMessages {
		account := erroredAnthropicOAuthAccount()
		account.ErrorMessage = msg
		fetcher := &countingClaudeUsageFetcher{}
		svc, repo := newClaudeRecoverFixture(account, fetcher)

		_, err := svc.GetUsage(context.Background(), account.ID)
		require.NoError(t, err)
		require.Equal(t, 1, fetcher.callCount())
		require.Zero(t, repo.clearCount(), msg)
	}
}

func TestGetUsageOpenAIWhamSuccessKeepsDeadRefreshTokenError(t *testing.T) {
	body := fmt.Sprintf(`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":40,"limit_window_seconds":18000,"reset_at":%d},"secondary_window":{"used_percent":12,"limit_window_seconds":604800,"reset_at":%d}}}`,
		time.Now().Add(time.Hour).Unix(), time.Now().Add(48*time.Hour).Unix())
	for _, msg := range deadRefreshTokenMessages {
		svc, recorder, _ := whamUsageFixture(t, body)
		account := whamOAuthAccount()
		account.Status = StatusError
		account.ErrorMessage = msg
		repo := &usageRecoverRepo{account: account}
		svc.accountRepo = repo

		_, err := svc.GetUsage(context.Background(), account.ID)
		require.NoError(t, err)
		require.Equal(t, 1, recorder.count())
		require.Zero(t, repo.clearCount(), msg)
	}
}

type usageExtraCountingRepo struct {
	*usageRecoverRepo
	extraWrites int
}

func (r *usageExtraCountingRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	r.extraWrites++
	return nil
}

func TestGetUsageAnthropicCachedResponseKeepsNewerPassiveSample(t *testing.T) {
	cachedAt := time.Now().Add(-2 * time.Minute)
	for name, tc := range map[string]struct {
		sampledAt time.Time
		writes    int
	}{
		"passive sample newer than the cache": {cachedAt.Add(time.Minute), 0},
		"passive sample older than the cache": {cachedAt.Add(-time.Minute), 1},
	} {
		account := &Account{ID: 78, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive,
			Credentials: map[string]any{"access_token": "TEST_ONLY_TOKEN"},
			Extra:       map[string]any{"passive_usage_sampled_at": tc.sampledAt.UTC().Format(time.RFC3339)}}
		fetcher := &countingClaudeUsageFetcher{}
		svc, base := newClaudeRecoverFixture(account, fetcher)
		repo := &usageExtraCountingRepo{usageRecoverRepo: base}
		svc.accountRepo = repo
		svc.cache.apiCache.Store(account.ID, &apiUsageCache{response: &ClaudeUsageResponse{}, timestamp: cachedAt})

		_, err := svc.GetUsage(context.Background(), account.ID)
		require.NoError(t, err, name)
		require.Zero(t, fetcher.callCount(), name)
		require.Equal(t, tc.writes, repo.extraWrites, name)
	}
}
