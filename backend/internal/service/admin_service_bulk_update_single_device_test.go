//go:build unit

package service

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
)

// singleDeviceBulkRepo records every extra write so the test can replay them
// in order and compute what each row ends up with.
type singleDeviceBulkRepo struct {
	accountRepoStubForBulkUpdate
	writes []singleDeviceExtraWrite
}

type singleDeviceExtraWrite struct {
	ids   []int64
	patch map[string]any
}

func (s *singleDeviceBulkRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	s.writes = append(s.writes, singleDeviceExtraWrite{ids: []int64{id}, patch: maps.Clone(updates)})
	return nil
}

func (s *singleDeviceBulkRepo) BulkUpdate(ctx context.Context, ids []int64, updates AccountBulkUpdate) (int64, error) {
	if len(updates.Extra) > 0 {
		s.writes = append(s.writes, singleDeviceExtraWrite{ids: append([]int64{}, ids...), patch: maps.Clone(updates.Extra)})
	}
	return s.accountRepoStubForBulkUpdate.BulkUpdate(ctx, ids, updates)
}

// finalExtra replays the recorded `extra || patch` writes on top of the
// original rows.
func (s *singleDeviceBulkRepo) finalExtra(accounts []*Account) map[int64]map[string]any {
	out := make(map[int64]map[string]any, len(accounts))
	for _, account := range accounts {
		out[account.ID] = maps.Clone(account.Extra)
	}
	for _, write := range s.writes {
		for _, id := range write.ids {
			maps.Copy(out[id], write.patch)
		}
	}
	return out
}

func newSingleDeviceBulkAccount(id int64, uuid, deviceID string) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"claude_oauth_mode": ClaudeOAuthModeSingleDevice,
			"account_uuid":      uuid,
			"device_id":         deviceID,
		},
	}
}

func cloneAccounts(accounts []*Account) []*Account {
	out := make([]*Account, 0, len(accounts))
	for _, account := range accounts {
		cp := *account
		cp.Extra = maps.Clone(account.Extra)
		out = append(out, &cp)
	}
	return out
}

func TestAdminService_BulkUpdateAccounts_SingleDeviceKeepsPerAccountExtra(t *testing.T) {
	a := newSingleDeviceBulkAccount(1, "11111111-2222-4333-8444-555555555555", "device-a")
	b := newSingleDeviceBulkAccount(2, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "device-b")
	// b already chose its own refresh policy; the default must not override it.
	b.Extra["claude_oauth_disable_token_refresh"] = false
	b.Extra["claude_oauth_token_disable_before_expiry_minutes"] = 10
	original := cloneAccounts([]*Account{a, b})

	repo := &singleDeviceBulkRepo{accountRepoStubForBulkUpdate: accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{a, b}}}
	svc := &adminServiceImpl{accountRepo: repo}

	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1, 2},
		Extra:      map[string]any{"custom_flag": "on"},
	})
	require.NoError(t, err)
	require.Equal(t, 2, result.Success)

	final := repo.finalExtra(original)
	require.Equal(t, map[string]any{
		"claude_oauth_mode":                  ClaudeOAuthModeSingleDevice,
		"account_uuid":                       "11111111-2222-4333-8444-555555555555",
		"device_id":                          "device-a",
		"custom_flag":                        "on",
		"claude_oauth_disable_token_refresh": true,
		"claude_oauth_token_disable_before_expiry_minutes": DefaultClaudeOAuthTokenDisableBeforeExpiryMinutes,
	}, final[1])
	require.Equal(t, map[string]any{
		"claude_oauth_mode":                  ClaudeOAuthModeSingleDevice,
		"account_uuid":                       "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		"device_id":                          "device-b",
		"custom_flag":                        "on",
		"claude_oauth_disable_token_refresh": false,
		"claude_oauth_token_disable_before_expiry_minutes": 10,
	}, final[2])

	// No write may carry another account's own keys.
	for _, write := range repo.writes {
		require.NotContains(t, write.patch, "account_uuid")
		require.NotContains(t, write.patch, "device_id")
	}
}

func TestAdminService_BulkUpdateAccounts_MixedBatchAddsNoSingleDeviceDefaults(t *testing.T) {
	a := newSingleDeviceBulkAccount(1, "11111111-2222-4333-8444-555555555555", "device-a")
	b := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{
		"claude_oauth_mode": "shared",
		"account_uuid":      "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
	}}
	repo := &singleDeviceBulkRepo{accountRepoStubForBulkUpdate: accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{a, b}}}
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1, 2},
		Extra:      map[string]any{"custom_flag": "on"},
	})
	require.NoError(t, err)
	require.Len(t, repo.writes, 1)
	require.Equal(t, []int64{1, 2}, repo.writes[0].ids)
	require.Equal(t, map[string]any{"custom_flag": "on"}, repo.writes[0].patch)
}

func TestAdminService_BulkUpdateAccounts_SingleDeviceOneAccountGetsDefaults(t *testing.T) {
	a := newSingleDeviceBulkAccount(1, "11111111-2222-4333-8444-555555555555", "device-a")
	original := cloneAccounts([]*Account{a})
	repo := &singleDeviceBulkRepo{accountRepoStubForBulkUpdate: accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{a}}}
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1},
		Extra:      map[string]any{"custom_flag": "on"},
	})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"claude_oauth_mode":                  ClaudeOAuthModeSingleDevice,
		"account_uuid":                       "11111111-2222-4333-8444-555555555555",
		"device_id":                          "device-a",
		"custom_flag":                        "on",
		"claude_oauth_disable_token_refresh": true,
		"claude_oauth_token_disable_before_expiry_minutes": DefaultClaudeOAuthTokenDisableBeforeExpiryMinutes,
	}, repo.finalExtra(original)[1])
}

func TestAdminService_BulkUpdateAccounts_SingleDeviceExplicitNullGetsDefault(t *testing.T) {
	a := newSingleDeviceBulkAccount(1, "11111111-2222-4333-8444-555555555555", "device-a")
	b := newSingleDeviceBulkAccount(2, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "device-b")
	// An unrelated stored null must not be rewritten.
	b.Extra["runtime_marker"] = nil
	original := cloneAccounts([]*Account{a, b})
	repo := &singleDeviceBulkRepo{accountRepoStubForBulkUpdate: accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{a, b}}}
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1, 2},
		Extra:      map[string]any{"claude_oauth_disable_token_refresh": nil},
	})
	require.NoError(t, err)
	final := repo.finalExtra(original)
	for _, id := range []int64{1, 2} {
		require.Equal(t, true, final[id]["claude_oauth_disable_token_refresh"])
		require.Equal(t, DefaultClaudeOAuthTokenDisableBeforeExpiryMinutes, final[id]["claude_oauth_token_disable_before_expiry_minutes"])
	}
	for _, write := range repo.writes {
		require.NotContains(t, write.patch, "runtime_marker")
	}
}
