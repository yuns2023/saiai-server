package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type carpoolMaintenanceAccountRepoStub struct {
	AccountRepository
	accounts map[int64]*Account
	updates  []carpoolMaintenanceUpdate
}

type carpoolMaintenanceUpdate struct {
	accountID int64
	values    map[string]any
}

func (s *carpoolMaintenanceAccountRepoStub) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	items := make([]Account, 0, len(s.accounts))
	for _, account := range s.accounts {
		if account.Platform != platform {
			continue
		}
		items = append(items, cloneCarpoolMaintenanceAccount(account))
	}
	return items, nil
}

func (s *carpoolMaintenanceAccountRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	account := s.accounts[id]
	if account == nil {
		return nil, ErrAccountNotFound
	}
	copyAccount := cloneCarpoolMaintenanceAccount(account)
	return &copyAccount, nil
}

func (s *carpoolMaintenanceAccountRepoStub) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	account := s.accounts[id]
	if account == nil {
		return ErrAccountNotFound
	}
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	copyUpdates := make(map[string]any, len(updates))
	for key, value := range updates {
		account.Extra[key] = value
		copyUpdates[key] = value
	}
	s.updates = append(s.updates, carpoolMaintenanceUpdate{accountID: id, values: copyUpdates})
	return nil
}

func cloneCarpoolMaintenanceAccount(account *Account) Account {
	copyAccount := *account
	copyAccount.Extra = make(map[string]any, len(account.Extra))
	for key, value := range account.Extra {
		copyAccount.Extra[key] = value
	}
	return copyAccount
}

type carpoolMaintenanceCacheStub struct {
	IdentityCache
	results           map[int64]*CarpoolDailyRotationResult
	calls             []carpoolMaintenanceRotationCall
	singleDeviceCalls []carpoolMaintenanceRotationCall
}

func (s *carpoolMaintenanceCacheStub) RotateSingleDeviceAdmissionForDay(_ context.Context, accountID int64, limit int, day string) (*CarpoolDailyRotationResult, error) {
	s.singleDeviceCalls = append(s.singleDeviceCalls, carpoolMaintenanceRotationCall{accountID: accountID, limit: limit, day: day})
	if result := s.results[accountID]; result != nil {
		return result, nil
	}
	return &CarpoolDailyRotationResult{Applied: true}, nil
}

func TestSingleDeviceAdmissionMaintenanceExpandsThenRotates(t *testing.T) {
	account := &Account{ID: 99, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{
		"claude_oauth_mode":                                        ClaudeOAuthModeSingleDevice,
		"claude_oauth_single_device_admission_enabled":             true,
		"claude_oauth_single_device_admission_auto_expand_enabled": true,
		"claude_oauth_single_device_admission_limit":               1,
		"claude_oauth_single_device_admission_target":              2,
	}}
	repo := &carpoolMaintenanceAccountRepoStub{accounts: map[int64]*Account{99: account}}
	cache := &carpoolMaintenanceCacheStub{results: map[int64]*CarpoolDailyRotationResult{99: {Applied: true, RecordedCount: 2, Evicted: &CarpoolDeviceRecord{DeviceKey: "oldest"}}}}
	svc := NewCarpoolMaintenanceService(repo, cache, &config.Config{Timezone: "UTC"})
	dayOne := time.Date(2026, time.September, 24, 0, 0, 0, 0, time.UTC)
	stats, err := svc.runOnce(context.Background(), dayOne)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Expanded)
	require.Equal(t, 2, account.GetClaudeOAuthSingleDeviceAdmissionLimit())
	require.Empty(t, cache.singleDeviceCalls)
	stats, err = svc.runOnce(context.Background(), dayOne.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, stats.AlreadyProcessed)
	stats, err = svc.runOnce(context.Background(), dayOne.Add(24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, stats.Rotated)
	require.Equal(t, []carpoolMaintenanceRotationCall{{accountID: 99, limit: 2, day: "2026-09-25"}}, cache.singleDeviceCalls)
}

type carpoolMaintenanceRotationCall struct {
	accountID int64
	limit     int
	day       string
}

func (s *carpoolMaintenanceCacheStub) RotateCarpoolDeviceForDay(_ context.Context, accountID int64, limit int, day string) (*CarpoolDailyRotationResult, error) {
	s.calls = append(s.calls, carpoolMaintenanceRotationCall{accountID: accountID, limit: limit, day: day})
	if result := s.results[accountID]; result != nil {
		copyResult := *result
		return &copyResult, nil
	}
	return &CarpoolDailyRotationResult{Applied: true}, nil
}

func newAutoCarpoolAccount(id int64, limit int) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"claude_oauth_mode":                        ClaudeOAuthModeCarpool,
			"claude_oauth_carpool_device_limit":        limit,
			"claude_oauth_carpool_auto_expand_enabled": true,
		},
	}
}

func TestCarpoolMaintenanceRunOnceExpandsToSixteenOnlyOncePerDay(t *testing.T) {
	account := newAutoCarpoolAccount(1, 15)
	repo := &carpoolMaintenanceAccountRepoStub{accounts: map[int64]*Account{1: account}}
	cache := &carpoolMaintenanceCacheStub{}
	svc := NewCarpoolMaintenanceService(repo, cache, &config.Config{Timezone: "Asia/Shanghai"})
	now := time.Date(2026, time.September, 19, 16, 30, 0, 0, time.UTC)

	stats, err := svc.runOnce(context.Background(), now)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Expanded)
	require.Equal(t, 16, account.GetClaudeOAuthCarpoolDeviceLimit())
	require.Equal(t, "2026-09-20", account.GetClaudeOAuthCarpoolLastMaintenanceDay())
	require.Empty(t, cache.calls)

	stats, err = svc.runOnce(context.Background(), now.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, stats.AlreadyProcessed)
	require.Equal(t, 1, len(repo.updates))
}

func TestCarpoolMaintenanceRunOnceUsesPerAccountTarget(t *testing.T) {
	expanding := newAutoCarpoolAccount(6, 16)
	expanding.Extra["claude_oauth_carpool_auto_maintenance_target"] = 20
	rotating := newAutoCarpoolAccount(7, 20)
	rotating.Extra["claude_oauth_carpool_auto_maintenance_target"] = 20
	repo := &carpoolMaintenanceAccountRepoStub{accounts: map[int64]*Account{
		6: expanding,
		7: rotating,
	}}
	cache := &carpoolMaintenanceCacheStub{results: map[int64]*CarpoolDailyRotationResult{
		7: {
			Applied:       true,
			RecordedCount: 20,
			Evicted: &CarpoolDeviceRecord{
				DeviceKey:  "oldest-device",
				LastSeenAt: 10,
			},
		},
	}}
	svc := NewCarpoolMaintenanceService(repo, cache, &config.Config{Timezone: "UTC"})

	stats, err := svc.runOnce(context.Background(), time.Date(2026, time.September, 19, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 1, stats.Expanded)
	require.Equal(t, 1, stats.Rotated)
	require.Equal(t, 17, expanding.GetClaudeOAuthCarpoolDeviceLimit())
	require.Equal(t, []carpoolMaintenanceRotationCall{{accountID: 7, limit: 20, day: "2026-09-19"}}, cache.calls)
}

func TestCarpoolMaintenanceRunOnceLeavesAdminLimitAboveTargetAlone(t *testing.T) {
	account := newAutoCarpoolAccount(2, 20)
	repo := &carpoolMaintenanceAccountRepoStub{accounts: map[int64]*Account{2: account}}
	cache := &carpoolMaintenanceCacheStub{results: map[int64]*CarpoolDailyRotationResult{
		2: {
			Applied:       true,
			RecordedCount: 20,
			Evicted: &CarpoolDeviceRecord{
				DeviceKey:  "device-hash",
				LastSeenAt: 10,
			},
		},
	}}
	svc := NewCarpoolMaintenanceService(repo, cache, &config.Config{Timezone: "UTC"})

	stats, err := svc.runOnce(context.Background(), time.Date(2026, time.September, 19, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 1, stats.FreeCapacity)
	require.Zero(t, stats.Rotated)
	require.Equal(t, 20, account.GetClaudeOAuthCarpoolDeviceLimit())
	require.Equal(t, "2026-09-19", account.GetClaudeOAuthCarpoolLastMaintenanceDay())
	require.Empty(t, cache.calls)
	require.NotContains(t, repo.updates[0].values, carpoolDeviceLimitKey)
}

func TestCarpoolMaintenanceRunOnceLeavesExistingFreeCapacityAlone(t *testing.T) {
	account := newAutoCarpoolAccount(3, 16)
	repo := &carpoolMaintenanceAccountRepoStub{accounts: map[int64]*Account{3: account}}
	cache := &carpoolMaintenanceCacheStub{results: map[int64]*CarpoolDailyRotationResult{
		3: {Applied: true, RecordedCount: 12},
	}}
	svc := NewCarpoolMaintenanceService(repo, cache, &config.Config{Timezone: "UTC"})

	stats, err := svc.runOnce(context.Background(), time.Date(2026, time.September, 19, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 1, stats.FreeCapacity)
	require.Zero(t, stats.Rotated)
	require.Equal(t, 16, account.GetClaudeOAuthCarpoolDeviceLimit())
}

func TestCarpoolMaintenanceRunOnceSkipsDisabledAndUnlimitedAccounts(t *testing.T) {
	disabled := newAutoCarpoolAccount(4, 5)
	disabled.Extra["claude_oauth_carpool_auto_expand_enabled"] = false
	unlimited := newAutoCarpoolAccount(5, 5)
	unlimited.Extra["claude_oauth_carpool_unlimited_devices"] = true
	repo := &carpoolMaintenanceAccountRepoStub{accounts: map[int64]*Account{4: disabled, 5: unlimited}}
	cache := &carpoolMaintenanceCacheStub{}
	svc := NewCarpoolMaintenanceService(repo, cache, &config.Config{Timezone: "UTC"})

	stats, err := svc.runOnce(context.Background(), time.Date(2026, time.September, 19, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Zero(t, stats.Eligible)
	require.Empty(t, repo.updates)
	require.Empty(t, cache.calls)
}
