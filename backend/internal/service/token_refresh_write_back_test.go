//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type recordingSchedulerCache struct {
	SchedulerCache
	published []*Account
}

func (c *recordingSchedulerCache) SetAccount(_ context.Context, account *Account) error {
	clone := *account
	c.published = append(c.published, &clone)
	return nil
}

// clearingTokenRefreshRepo models the database: ClearTempUnschedulable
// changes the stored row, as the real repository does.
type clearingTokenRefreshRepo struct {
	tokenRefreshAccountRepo
}

func (r *clearingTokenRefreshRepo) ClearTempUnschedulable(ctx context.Context, id int64) error {
	r.clearTempCalls++
	stored := *r.accountsByID[id]
	stored.TempUnschedulableUntil = nil
	r.accountsByID[id] = &stored
	return nil
}

// Production: postRefreshActions put an in-memory object into the scheduler
// cache, so state changed after it was read (a 429, a disable, or the cooldown
// cleared just above) was undone for the scheduler. It must publish what the
// database holds at that moment.
func TestTokenRefreshService_PublishesCommittedAccountNotStaleObject(t *testing.T) {
	resetAt := time.Now().Add(30 * time.Minute)
	cooldown := time.Now().Add(10 * time.Minute)
	repo := &clearingTokenRefreshRepo{}
	repo.accountsByID = map[int64]*Account{5: {ID: 5, Platform: PlatformGemini, Type: AccountTypeOAuth, Status: StatusActive,
		RateLimitResetAt: &resetAt, TempUnschedulableUntil: &cooldown, Credentials: map[string]any{"access_token": "new-token"}}}
	cache := &recordingSchedulerCache{}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, cache,
		&config.Config{TokenRefresh: config.TokenRefreshConfig{MaxRetries: 1}}, nil)

	// The refresh starts on an account in its 401 cooldown.
	started := &Account{ID: 5, Platform: PlatformGemini, Type: AccountTypeOAuth, Status: StatusActive, TempUnschedulableUntil: &cooldown}
	refresher := &tokenRefresherStub{credentials: map[string]any{"access_token": "new-token"}}
	require.NoError(t, svc.refreshWithRetry(context.Background(), started, refresher, refresher, time.Hour))

	require.Zero(t, repo.wholeRowUpdates, "refresh writes credentials only")
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 1, repo.clearTempCalls)
	require.Len(t, cache.published, 1)
	require.NotNil(t, cache.published[0].RateLimitResetAt, "a rate limit written during the refresh survives in the cache")
	require.Nil(t, cache.published[0].TempUnschedulableUntil, "the cache gets the row after the cooldown was cleared")
	require.Equal(t, "new-token", cache.published[0].GetCredential("access_token"))
	require.NotNil(t, cache.published[0].Credentials["_token_version"], "the published row carries the new credentials")
}

// A cooldown set by another request while the refresh ran is not this
// refresh's to clear.
func TestTokenRefreshService_KeepsCooldownSetDuringRefresh(t *testing.T) {
	cooldown := time.Now().Add(10 * time.Minute)
	repo := &clearingTokenRefreshRepo{}
	repo.accountsByID = map[int64]*Account{5: {ID: 5, Platform: PlatformGemini, Type: AccountTypeOAuth, Status: StatusActive,
		TempUnschedulableUntil: &cooldown, Credentials: map[string]any{"access_token": "new-token"}}}
	cache := &recordingSchedulerCache{}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, cache,
		&config.Config{TokenRefresh: config.TokenRefreshConfig{MaxRetries: 1}}, nil)

	started := &Account{ID: 5, Platform: PlatformGemini, Type: AccountTypeOAuth, Status: StatusActive}
	refresher := &tokenRefresherStub{credentials: map[string]any{"access_token": "new-token"}}
	require.NoError(t, svc.refreshWithRetry(context.Background(), started, refresher, refresher, time.Hour))

	require.Zero(t, repo.clearTempCalls)
	require.Len(t, cache.published, 1)
	require.NotNil(t, cache.published[0].TempUnschedulableUntil)
}

func TestAdoptStoredCredentials_UsesWinningValue(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	repo.accountsByID = map[int64]*Account{8: {ID: 8, Credentials: map[string]any{"project_id": "admin-set"}}}
	account := &Account{ID: 8, Credentials: map[string]any{"project_id": "", "tier_id": "detected-tier"}}

	adoptStoredCredentials(context.Background(), repo, account, "project_id", "tier_id")
	require.Equal(t, "admin-set", account.Credentials["project_id"])
	_, hasTier := account.Credentials["tier_id"]
	require.False(t, hasTier, "a key absent in storage is not kept from the rejected detection")
}

func TestTokenRefreshService_SkipsPublishWhenRereadFails(t *testing.T) {
	repo := &tokenRefreshAccountRepo{} // GetByID: not found
	cache := &recordingSchedulerCache{}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, cache,
		&config.Config{TokenRefresh: config.TokenRefreshConfig{MaxRetries: 1}}, nil)

	account := &Account{ID: 9, Platform: PlatformGemini, Type: AccountTypeOAuth}
	refresher := &tokenRefresherStub{credentials: map[string]any{"access_token": "new-token"}}
	require.NoError(t, svc.refreshWithRetry(context.Background(), account, refresher, refresher, time.Hour))
	require.Empty(t, cache.published, "never publish an object that was not read back from the database")
}

func TestRefreshIfNeeded_DeletedAccountStopsBeforeUpstream(t *testing.T) {
	account := &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	repo := &refreshAPIAccountRepo{account: account, getByIDErr: ErrAccountNotFound}
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"access_token": "x"}}

	_, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(context.Background(), account, executor, time.Minute)
	require.ErrorIs(t, err, ErrAccountNotFound)
	require.Zero(t, executor.refreshCalls, "no upstream token rotation for a deleted account")
	require.Zero(t, repo.updateCalls)
}

func TestRefreshIfNeeded_ReturnsAccountReadAfterWrite(t *testing.T) {
	resetAt := time.Now().Add(time.Hour)
	account := &Account{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	repo := &refreshAPIAccountRepo{account: account}
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"access_token": "new"}}
	// The row changes (a 429 lands) while the upstream refresh runs.
	executor.onRefresh = func() {
		repo.account = &Account{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeOAuth, RateLimitResetAt: &resetAt,
			Credentials: map[string]any{"access_token": "new"}}
	}

	result, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(context.Background(), account, executor, time.Minute)
	require.NoError(t, err)
	require.Zero(t, repo.wholeRowUpdates)
	require.Equal(t, "new", repo.savedCreds["access_token"])
	require.NotNil(t, result.Account.RateLimitResetAt, "result reflects the committed row")
}

func buildPathAWriteBackService(repo *clearingTokenRefreshRepo, executor *refreshAPIExecutorStub) (*TokenRefreshService, *recordingSchedulerCache) {
	cache := &recordingSchedulerCache{}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, cache,
		&config.Config{TokenRefresh: config.TokenRefreshConfig{MaxRetries: 1}}, nil)
	svc.SetRefreshAPI(NewOAuthRefreshAPI(repo, &mockTokenCacheForRefreshAPI{lockResult: true}))
	return svc, cache
}

// Unified API: the caller's object still shows an old cooldown C1, the
// lock-time read shows none, and another request sets C2 during the upstream
// call. C2 must survive (deciding from the caller's object would clear it).
func TestTokenRefreshService_PathA_DecidesFromLockTimeRead(t *testing.T) {
	c1 := time.Now().Add(5 * time.Minute)
	c2 := time.Now().Add(20 * time.Minute)
	repo := &clearingTokenRefreshRepo{}
	repo.accountsByID = map[int64]*Account{6: {ID: 6, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive}}
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"access_token": "new"}}
	executor.onRefresh = func() {
		row := *repo.accountsByID[6]
		row.TempUnschedulableUntil = &c2
		repo.accountsByID[6] = &row
	}
	svc, cache := buildPathAWriteBackService(repo, executor)

	caller := &Account{ID: 6, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, TempUnschedulableUntil: &c1}
	require.NoError(t, svc.refreshWithRetry(context.Background(), caller, executor, executor, time.Hour))

	require.Zero(t, repo.clearTempCalls, "a cooldown set during the upstream call is kept")
	require.Zero(t, repo.wholeRowUpdates)
	require.Len(t, cache.published, 1)
	require.NotNil(t, cache.published[0].TempUnschedulableUntil)
	require.Equal(t, "new", cache.published[0].GetCredential("access_token"), "new token and current state published together")
	require.NotNil(t, cache.published[0].Credentials["_token_version"])
}

// Unified API, other direction: the lock-time read shows the 401 cooldown the
// refresh exists to lift, even though the caller's object predates it.
func TestTokenRefreshService_PathA_ClearsCooldownSeenUnderLock(t *testing.T) {
	cooldown := time.Now().Add(10 * time.Minute)
	repo := &clearingTokenRefreshRepo{}
	repo.accountsByID = map[int64]*Account{6: {ID: 6, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, TempUnschedulableUntil: &cooldown}}
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"access_token": "new"}}
	svc, cache := buildPathAWriteBackService(repo, executor)

	caller := &Account{ID: 6, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive}
	require.NoError(t, svc.refreshWithRetry(context.Background(), caller, executor, executor, time.Hour))

	require.Equal(t, 1, repo.clearTempCalls)
	require.Len(t, cache.published, 1)
	require.Nil(t, cache.published[0].TempUnschedulableUntil)
}
