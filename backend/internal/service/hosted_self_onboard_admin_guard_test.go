//go:build unit

package service

import (
	"context"
	"testing"
	"time"

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
	proxy   *Proxy
	updated int
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

type accountRepoStubForProxyRefresh struct {
	accountRepoStub
	refreshed []int64
}

func (s *accountRepoStubForProxyRefresh) RefreshAccountsUsingProxy(_ context.Context, proxyID int64) error {
	s.refreshed = append(s.refreshed, proxyID)
	return nil
}

// A proxy status / address / credential change refreshes the accounts using
// it; a rename does not.
func TestUpdateProxyRefreshesAccountsOnRelevantChange(t *testing.T) {
	proxies := &proxyRepoStubForUpdate{proxy: &Proxy{ID: 9, Name: "p", Protocol: "http", Host: "203.0.113.1", Port: 3128, Status: StatusActive}}
	accounts := &accountRepoStubForProxyRefresh{}
	svc := &adminServiceImpl{proxyRepo: proxies, accountRepo: accounts}
	ctx := context.Background()

	_, err := svc.UpdateProxy(ctx, 9, &UpdateProxyInput{Name: "renamed"})
	require.NoError(t, err)
	require.Empty(t, accounts.refreshed)

	for _, in := range []*UpdateProxyInput{
		{Status: "disabled"},
		{Host: "203.0.113.2"},
		{Port: 8080},
		{Protocol: "socks5h"},
		{Username: "u", Password: "p"},
	} {
		_, err = svc.UpdateProxy(ctx, 9, in)
		require.NoError(t, err)
	}
	require.Len(t, accounts.refreshed, 5)
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
