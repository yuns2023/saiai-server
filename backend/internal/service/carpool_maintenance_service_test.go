package service

import (
	"context"
	"errors"
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
	require.Zero(t, stats.Eligible)
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

func (s *carpoolMaintenanceAccountRepoStub) AdvanceCarpoolMaintenance(ctx context.Context, id int64, expected, next string) error {
	if s.accounts[id].GetCarpoolNextMaintenance() != expected {
		return nil
	}
	return s.UpdateExtra(ctx, id, map[string]any{CarpoolNextMaintenanceKey: next})
}
func (s *carpoolMaintenanceCacheStub) ResetCarpoolAdmissionCycle(_ context.Context, id int64, limit int, policy CarpoolAdmissionPolicy, due string) (int, error) {
	s.calls = append(s.calls, carpoolMaintenanceRotationCall{accountID: id, limit: limit, day: due})
	return policy.Admissions, nil
}
func TestCarpoolSlotsPerAccountScheduleAndMissedPeriods(t *testing.T) {
	now := time.Date(2026, 10, 19, 10, 30, 0, 0, time.UTC)
	a, b := newAutoCarpoolAccount(1, 10), newAutoCarpoolAccount(2, 20)
	a.Extra["claude_oauth_carpool_maintenance_interval_hours"] = 6
	a.Extra["claude_oauth_carpool_maintenance_admissions_per_cycle"] = 2
	b.Extra["claude_oauth_carpool_maintenance_interval_hours"] = 12
	repo := &carpoolMaintenanceAccountRepoStub{accounts: map[int64]*Account{1: a, 2: b}}
	cache := &carpoolMaintenanceCacheStub{}
	svc := NewCarpoolMaintenanceService(repo, cache, nil)
	_, err := svc.runOnce(context.Background(), now)
	require.NoError(t, err)
	require.Empty(t, cache.calls)
	require.Equal(t, "2026-10-19T16:30:00Z", a.GetCarpoolNextMaintenance())
	_, err = svc.runOnce(context.Background(), now.Add(6*time.Hour-time.Second))
	require.NoError(t, err)
	require.Empty(t, cache.calls)
	_, err = svc.runOnce(context.Background(), now.Add(6*time.Hour))
	require.NoError(t, err)
	require.Len(t, cache.calls, 1)
	require.Equal(t, int64(1), cache.calls[0].accountID)
	// Recreate the worker after several missed periods: one pass, no catch-up loop.
	svc = NewCarpoolMaintenanceService(repo, cache, nil)
	_, err = svc.runOnce(context.Background(), now.Add(49*time.Hour))
	require.NoError(t, err)
	require.Len(t, cache.calls, 3)
	_, err = svc.runOnce(context.Background(), now.Add(49*time.Hour))
	require.NoError(t, err)
	require.Len(t, cache.calls, 3)
	require.Equal(t, 10, a.GetClaudeOAuthCarpoolDeviceLimit())
	require.Equal(t, 20, b.GetClaudeOAuthCarpoolDeviceLimit())
}
func TestNormalizeCarpoolSchedule(t *testing.T) {
	now := time.Date(2026, 10, 19, 10, 30, 0, 0, time.UTC)
	a := newAutoCarpoolAccount(1, 10)
	a.Extra[CarpoolNextMaintenanceKey] = "2000-01-01T00:00:00Z"
	NormalizeCarpoolMaintenance(a, nil, now)
	require.Equal(t, "2026-10-19T10:30:00Z", a.GetCarpoolNextMaintenance())
	require.Equal(t, a.GetCarpoolNextMaintenance(), a.CarpoolAdmissionPolicy().InitialDue)
	previous := cloneCarpoolMaintenanceAccount(a)
	a.Extra[CarpoolNextMaintenanceKey] = "forged"
	a.Extra[CarpoolMaintenanceGenerationKey] = "forged"
	a.Extra["claude_oauth_carpool_maintenance_admissions_per_cycle"] = 2
	NormalizeCarpoolMaintenance(a, &previous, now.Add(time.Hour))
	require.Equal(t, previous.GetCarpoolNextMaintenance(), a.GetCarpoolNextMaintenance())
	require.Equal(t, previous.CarpoolAdmissionPolicy().Generation, a.CarpoolAdmissionPolicy().Generation)
	a.Extra["claude_oauth_carpool_maintenance_interval_hours"] = 6
	NormalizeCarpoolMaintenance(a, &previous, now.Add(time.Hour))
	require.Equal(t, "2026-10-19T17:30:00Z", a.GetCarpoolNextMaintenance())
	require.Greater(t, a.CarpoolAdmissionPolicy().Generation, previous.CarpoolAdmissionPolicy().Generation)
	previous = cloneCarpoolMaintenanceAccount(a)
	a.Extra["claude_oauth_carpool_unlimited_devices"] = true
	NormalizeCarpoolMaintenance(a, &previous, now)
	require.Empty(t, a.GetCarpoolNextMaintenance())
}

func TestCarpoolEnableReservesFirstCycleOnNextWorkerPass(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	a := newAutoCarpoolAccount(1, 12)
	a.Extra["claude_oauth_carpool_maintenance_interval_hours"] = 6
	a.Extra["claude_oauth_carpool_maintenance_admissions_per_cycle"] = 1
	NormalizeCarpoolMaintenance(a, nil, now)
	repo := &carpoolMaintenanceAccountRepoStub{accounts: map[int64]*Account{1: a}}
	cache := &carpoolMaintenanceCacheStub{}
	svc := NewCarpoolMaintenanceService(repo, cache, nil)
	_, err := svc.runOnce(context.Background(), now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, cache.calls, 1)
	require.Equal(t, "2026-10-21T16:00:00Z", a.GetCarpoolNextMaintenance())
	previous := cloneCarpoolMaintenanceAccount(a)
	a.Extra[CarpoolInitialMaintenanceKey] = "forged"
	NormalizeCarpoolMaintenance(a, &previous, now.Add(2*time.Minute))
	require.Equal(t, previous.CarpoolAdmissionPolicy().InitialDue, a.CarpoolAdmissionPolicy().InitialDue)
	_, err = svc.runOnce(context.Background(), now.Add(2*time.Minute))
	require.NoError(t, err)
	require.Len(t, cache.calls, 1)
}

func TestCarpoolMaintenanceResultLogLevel(t *testing.T) {
	sink, restore := captureStructuredLog(t)
	defer restore()

	logCarpoolMaintenanceResult(carpoolMaintenanceStats{Eligible: 2}, nil)
	logCarpoolMaintenanceResult(carpoolMaintenanceStats{Failed: 2}, errors.Join(errors.New("account 1: boom"), errors.New("account 2: boom")))

	require.True(t, sink.ContainsMessageAtLevel("[CarpoolMaintenance] complete: eligible=2 expanded=0 rotated=0 already_processed=0 free_capacity=0 failed=0", "info"))
	require.False(t, sink.ContainsMessageAtLevel("[CarpoolMaintenance] complete:", "error"))
	require.True(t, sink.ContainsMessageAtLevel("[CarpoolMaintenance] completed with errors: eligible=0 expanded=0 rotated=0 already_processed=0 free_capacity=0 failed=2 err=account 1: boom account 2: boom", "error"))
	require.True(t, sink.ContainsFieldValue("component", "service.carpool_maintenance"))
}
