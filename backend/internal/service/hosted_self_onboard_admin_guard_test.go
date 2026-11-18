//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Bulk edit cannot clear the proxy of a self-onboarded account; the check
// runs before any write, so a mixed batch changes nothing.
func TestBulkUpdateRefusesClearingSelfOnboardedProxy(t *testing.T) {
	proxyID := int64(3)
	repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{
		{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, ProxyID: &proxyID},
		{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, ProxyID: &proxyID, SelfOnboarded: true},
	}}
	svc := &adminServiceImpl{accountRepo: repo}
	clear := int64(0)
	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1, 2}, ProxyID: &clear})
	require.ErrorIs(t, err, ErrSelfOnboardedAccountNeedsProxy)
	require.Empty(t, repo.bulkUpdateIDs, "nothing written")

	// Moving to another proxy, or clearing only ordinary accounts, still works.
	other := int64(4)
	_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1, 2}, ProxyID: &other})
	require.NoError(t, err)
	repo.getByIDsAccounts = repo.getByIDsAccounts[:1]
	_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, ProxyID: &clear})
	require.NoError(t, err)
}

// The type of a self-onboarded account is fixed; resending the same type
// (full-object edit) is fine, and ordinary accounts are unaffected.
func TestUpdateAccountTypeLockedForSelfOnboarded(t *testing.T) {
	proxyID := int64(3)
	now := time.Now()
	self := func() *Account {
		return &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, ProxyID: &proxyID, SelfOnboarded: true,
			Credentials: map[string]any{}, Extra: map[string]any{}}
	}
	require.NoError(t, applyUpdateAccountInput(self(), &UpdateAccountInput{Type: AccountTypeSetupToken, Name: "renamed"}, nil, now))
	require.ErrorIs(t, applyUpdateAccountInput(self(), &UpdateAccountInput{Type: AccountTypeOAuth}, nil, now), ErrSelfOnboardedAccountTypeLocked)
	require.ErrorIs(t, applyUpdateAccountInput(self(), &UpdateAccountInput{Type: AccountTypeAPIKey}, nil, now), ErrSelfOnboardedAccountTypeLocked)
	clear := int64(0)
	require.ErrorIs(t, applyUpdateAccountInput(self(), &UpdateAccountInput{ProxyID: &clear}, nil, now), ErrSelfOnboardedAccountNeedsProxy)

	plain := self()
	plain.SelfOnboarded = false
	require.NoError(t, applyUpdateAccountInput(plain, &UpdateAccountInput{Type: AccountTypeOAuth, ProxyID: &clear}, nil, now))
	require.Equal(t, AccountTypeOAuth, plain.Type)
}

type proxyRepoStubForUpdate struct {
	proxyRepoStub
	proxy      *Proxy
	updated    int
	txUpdates  int
	accountIDs []int64
	txErr      error
}

func (s *proxyRepoStubForUpdate) GetByID(context.Context, int64) (*Proxy, error) {
	cp := *s.proxy
	return &cp, nil
}

func (s *proxyRepoStubForUpdate) Update(_ context.Context, p *Proxy) error {
	s.updated++
	cp := *p
	s.proxy = &cp
	return nil
}

func (s *proxyRepoStubForUpdate) UpdateAndQueueAccountRefresh(_ context.Context, p *Proxy) ([]int64, error) {
	if s.txErr != nil {
		return nil, s.txErr
	}
	s.txUpdates++
	cp := *p
	s.proxy = &cp
	return s.accountIDs, nil
}

type accountRepoStubForProxyRefresh struct {
	accountRepoStub
	synced [][]int64
}

func (s *accountRepoStubForProxyRefresh) SyncSchedulerAccountSnapshots(_ context.Context, ids []int64) {
	s.synced = append(s.synced, ids)
}

// A proxy status / address / credential change is saved together with the
// account refresh events (one transaction) and then syncs the cached keys; a
// rename is a plain update. A failed transaction is an error, not success.
func TestUpdateProxyRefreshesAccountsOnRelevantChange(t *testing.T) {
	proxies := &proxyRepoStubForUpdate{proxy: &Proxy{ID: 9, Name: "p", Protocol: "http", Host: "203.0.113.1", Port: 3128, Status: StatusActive,
		Username: "a|b", Password: "c"}, accountIDs: []int64{7, 8}}
	accounts := &accountRepoStubForProxyRefresh{}
	svc := &adminServiceImpl{proxyRepo: proxies, accountRepo: accounts}
	ctx := context.Background()

	_, err := svc.UpdateProxy(ctx, 9, &UpdateProxyInput{Name: "renamed"})
	require.NoError(t, err)
	require.Equal(t, 1, proxies.updated)
	require.Zero(t, proxies.txUpdates)
	require.Empty(t, accounts.synced)

	for _, in := range []*UpdateProxyInput{
		{Status: "disabled"},
		{Host: "203.0.113.2"},
		{Port: 8080},
		{Protocol: "socks5h"},
		{Username: "a", Password: "b|c"}, // "a|b"+"c" vs "a"+"b|c": fields, not a joined string
	} {
		_, err = svc.UpdateProxy(ctx, 9, in)
		require.NoError(t, err)
	}
	require.Equal(t, 5, proxies.txUpdates)
	require.Len(t, accounts.synced, 5)
	require.Equal(t, []int64{7, 8}, accounts.synced[0])

	proxies.txErr = errors.New("outbox insert failed")
	_, err = svc.UpdateProxy(ctx, 9, &UpdateProxyInput{Status: StatusActive})
	require.Error(t, err)
	require.Len(t, accounts.synced, 5, "no cache sync after a failed transaction")
}

// Self-onboarded accounts never join group-less pools (ungrouped keys,
// simple mode).
func TestSelfOnboardedNotInUngroupedPool(t *testing.T) {
	svc := &GatewayService{}
	plain := &Account{ID: 1}
	self := &Account{ID: 2, SelfOnboarded: true}
	require.True(t, svc.isAccountInGroup(plain, nil))
	require.False(t, svc.isAccountInGroup(self, nil))
	gid := int64(5)
	self.AccountGroups = []AccountGroup{{GroupID: gid}}
	require.True(t, svc.isAccountInGroup(self, &gid))

	out := withoutSelfOnboarded([]Account{{ID: 1}, {ID: 2, SelfOnboarded: true}, {ID: 3}})
	require.Equal(t, []int64{1, 3}, []int64{out[0].ID, out[1].ID})
	require.Len(t, out, 2)
	require.Empty(t, withoutSelfOnboarded(nil))
}

type simpleModeSnapshotCache struct {
	SchedulerCache
	accounts []*Account
}

func (c *simpleModeSnapshotCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return c.accounts, true, nil
}

type simpleModeAccountRepo struct {
	accountRepoStub
	accounts []Account
}

func (r *simpleModeAccountRepo) ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]Account, error) {
	return r.accounts, nil
}

func (r *simpleModeAccountRepo) ListSchedulableByPlatforms(context.Context, []string) ([]Account, error) {
	return r.accounts, nil
}

func (r *simpleModeAccountRepo) ListSchedulableByPlatform(context.Context, string) ([]Account, error) {
	return r.accounts, nil
}

// In simple mode a self-onboarded account is never a candidate, through
// every entry point: a (stale) snapshot bucket, the DB fallback with and
// without a group, and the sticky / by-ID checks.
func TestSimpleModeNeverSelectsSelfOnboarded(t *testing.T) {
	ctx := context.Background()
	simple := &config.Config{RunMode: config.RunModeSimple}
	self := Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive, Schedulable: true, SelfOnboarded: true}
	plain := Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Status: StatusActive, Schedulable: true}
	ids := func(list []Account) []int64 {
		out := []int64{}
		for _, a := range list {
			out = append(out, a.ID)
		}
		return out
	}
	gid := int64(5)

	// 1. Snapshot path with a stale global bucket (service + gateway).
	snapshot := NewSchedulerSnapshotService(&simpleModeSnapshotCache{accounts: []*Account{&plain, &self}}, nil, nil, nil, simple)
	got, _, err := snapshot.ListSchedulableAccounts(ctx, nil, PlatformAnthropic, false)
	require.NoError(t, err)
	require.Equal(t, []int64{1}, ids(got))
	gw := &GatewayService{cfg: simple, schedulerSnapshot: snapshot}
	got, _, err = gw.listSchedulableAccounts(ctx, &gid, PlatformAnthropic, false)
	require.NoError(t, err)
	require.Equal(t, []int64{1}, ids(got))

	// 2. No snapshot: mixed branch with a group id, mixed without, single platform.
	repo := &simpleModeAccountRepo{accounts: []Account{plain, self}}
	gw = &GatewayService{cfg: simple, accountRepo: repo}
	for _, tc := range []struct {
		group *int64
		force bool
	}{{&gid, false}, {nil, false}, {nil, true}, {&gid, true}} {
		got, _, err = gw.listSchedulableAccounts(ctx, tc.group, PlatformAnthropic, tc.force)
		require.NoError(t, err)
		require.Equal(t, []int64{1}, ids(got), "group=%v force=%v", tc.group != nil, tc.force)
	}

	// 3. Sticky / by-ID checks.
	self.AccountGroups = []AccountGroup{{GroupID: gid}}
	require.False(t, gw.isAccountInGroup(&self, &gid))
	require.False(t, gw.isAccountInGroup(&self, nil))
	require.False(t, gw.isAccountSchedulableForSelection(ctx, &self))
	require.False(t, gw.isAccountSchedulableForModelSelection(ctx, &self, ""))
	require.True(t, gw.isAccountSchedulableForSelection(ctx, &plain))

	// Standard mode: the exclusive group still works.
	std := &GatewayService{cfg: &config.Config{}}
	require.True(t, std.isAccountInGroup(&self, &gid))
	require.True(t, std.isAccountSchedulableForSelection(ctx, &self) == self.IsSchedulable())
}
