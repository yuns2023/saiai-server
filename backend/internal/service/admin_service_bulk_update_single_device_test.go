//go:build unit

package service

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
)

// singleDeviceBulkRepo records every extra write so the test can replay them
// in order and compute what each row ends up with. BulkUpdate replays the
// repo SQL: extra || patch, then ExtraDefaults for keys missing or null.
type singleDeviceBulkRepo struct {
	accountRepoStubForBulkUpdate
	writes           []singleDeviceExtraWrite
	updateExtraCalls int
	lastBulk         AccountBulkUpdate
}

type singleDeviceExtraWrite struct {
	ids      []int64
	patch    map[string]any
	defaults map[string]any
}

func (s *singleDeviceBulkRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	s.updateExtraCalls++
	s.writes = append(s.writes, singleDeviceExtraWrite{ids: []int64{id}, patch: maps.Clone(updates)})
	return nil
}

func (s *singleDeviceBulkRepo) BulkUpdate(ctx context.Context, ids []int64, updates AccountBulkUpdate) (int64, error) {
	s.lastBulk = updates
	if len(updates.Extra) > 0 || len(updates.ExtraDefaults) > 0 {
		s.writes = append(s.writes, singleDeviceExtraWrite{ids: append([]int64{}, ids...), patch: maps.Clone(updates.Extra), defaults: maps.Clone(updates.ExtraDefaults)})
	}
	return s.accountRepoStubForBulkUpdate.BulkUpdate(ctx, ids, updates)
}

// finalExtra replays the recorded writes on top of the original rows.
func (s *singleDeviceBulkRepo) finalExtra(accounts []*Account) map[int64]map[string]any {
	out := make(map[int64]map[string]any, len(accounts))
	for _, account := range accounts {
		out[account.ID] = maps.Clone(account.Extra)
		if out[account.ID] == nil {
			out[account.ID] = map[string]any{}
		}
	}
	for _, write := range s.writes {
		for _, id := range write.ids {
			maps.Copy(out[id], write.patch)
			for key, value := range write.defaults {
				if current, ok := out[id][key]; !ok || current == nil {
					out[id][key] = value
				}
			}
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

	// One shared write; it carries no account's own keys.
	require.Zero(t, repo.updateExtraCalls)
	require.Len(t, repo.writes, 1)
	require.Equal(t, map[string]any{"custom_flag": "on"}, repo.writes[0].patch)
	require.Equal(t, claudeOAuthSingleDeviceDefaultExtra(), repo.writes[0].defaults)
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
	require.Nil(t, repo.writes[0].defaults)
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
	require.Zero(t, repo.updateExtraCalls)
	require.Len(t, repo.writes, 1)
	require.NotContains(t, repo.writes[0].patch, "runtime_marker")
	require.Nil(t, final[2]["runtime_marker"])
}

func TestAdminService_BulkUpdateAccounts_SwitchSharedToSingleDeviceGetsDefaults(t *testing.T) {
	a := newSingleDeviceBulkAccount(1, "11111111-2222-4333-8444-555555555555", "device-a")
	b := newSingleDeviceBulkAccount(2, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "device-b")
	a.Extra["claude_oauth_mode"] = "shared"
	b.Extra["claude_oauth_mode"] = "shared"
	b.Extra["claude_oauth_disable_token_refresh"] = false
	original := cloneAccounts([]*Account{a, b})
	repo := &singleDeviceBulkRepo{accountRepoStubForBulkUpdate: accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{a, b}}}
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1, 2},
		Extra:      map[string]any{"claude_oauth_mode": ClaudeOAuthModeSingleDevice},
	})
	require.NoError(t, err)
	require.Zero(t, repo.updateExtraCalls)
	final := repo.finalExtra(original)
	require.Equal(t, ClaudeOAuthModeSingleDevice, final[1]["claude_oauth_mode"])
	require.Equal(t, ClaudeOAuthModeSingleDevice, final[2]["claude_oauth_mode"])
	require.Equal(t, "device-a", final[1]["device_id"])
	require.Equal(t, "device-b", final[2]["device_id"])
	require.Equal(t, true, final[1]["claude_oauth_disable_token_refresh"])
	require.Equal(t, false, final[2]["claude_oauth_disable_token_refresh"])
	require.Equal(t, DefaultClaudeOAuthTokenDisableBeforeExpiryMinutes, final[1]["claude_oauth_token_disable_before_expiry_minutes"])
	require.Equal(t, DefaultClaudeOAuthTokenDisableBeforeExpiryMinutes, final[2]["claude_oauth_token_disable_before_expiry_minutes"])
}

// Forged server-owned keys are stripped; rows without a stored schedule get
// no schedule write, and the bulk write carries only the defaults.
func TestAdminService_BulkUpdateAccounts_SingleDeviceOnlyServerOwnedKeys(t *testing.T) {
	a := newSingleDeviceBulkAccount(1, "11111111-2222-4333-8444-555555555555", "device-a")
	b := newSingleDeviceBulkAccount(2, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "device-b")
	original := cloneAccounts([]*Account{a, b})
	repo := &singleDeviceBulkRepo{accountRepoStubForBulkUpdate: accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{a, b}}}
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1, 2},
		Extra:      map[string]any{CarpoolNextMaintenanceKey: "forged", CarpoolMaintenanceGenerationKey: "forged"},
	})
	require.NoError(t, err)
	require.Zero(t, repo.updateExtraCalls)
	require.Empty(t, repo.lastBulk.Extra)
	require.Equal(t, claudeOAuthSingleDeviceDefaultExtra(), repo.lastBulk.ExtraDefaults)
	final := repo.finalExtra(original)
	for id, account := range map[int64]*Account{1: original[0], 2: original[1]} {
		want := maps.Clone(account.Extra)
		maps.Copy(want, claudeOAuthSingleDeviceDefaultExtra())
		require.Equal(t, want, final[id])
	}
}

// A single-device row with a leftover schedule has it cleared even when the
// admin patch is empty (same as before the fix).
func TestAdminService_BulkUpdateAccounts_SingleDeviceEmptyPatchClearsStaleSchedule(t *testing.T) {
	a := newSingleDeviceBulkAccount(1, "11111111-2222-4333-8444-555555555555", "device-a")
	a.Extra[CarpoolNextMaintenanceKey] = "2026-01-01T00:00:00Z"
	b := newSingleDeviceBulkAccount(2, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "device-b")
	repo := &singleDeviceBulkRepo{accountRepoStubForBulkUpdate: accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{a, b}}}
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1, 2},
		Extra:      map[string]any{},
	})
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateExtraCalls)
	require.Equal(t, []int64{1}, repo.writes[0].ids)
	require.Equal(t, "", repo.writes[0].patch[CarpoolNextMaintenanceKey])
	require.NotContains(t, repo.writes[0].patch, "account_uuid")
	require.Equal(t, claudeOAuthSingleDeviceDefaultExtra(), repo.lastBulk.ExtraDefaults)
}
