package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCarpoolFreeSlotsRedis(t *testing.T) {
	socket := os.Getenv("SAIAI_TEST_CARPOOL_REDIS_SOCKET")
	if socket == "" {
		t.Skip("task-owned Redis Unix socket not supplied")
	}
	require.True(t, filepath.IsAbs(socket))
	rdb := redis.NewClient(&redis.Options{Network: "unix", Addr: socket})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	cache := &identityCache{rdb: rdb}
	due := "2026-10-19T16:30:00Z"
	for _, n := range []int{10, 9, 8, 6, 0, 12} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			id := time.Now().UnixNano()
			recorded, overflow := carpoolRecordedKey(id), carpoolOverflowKey(id)
			maintenance := carpoolAdmissionBudgetKey(id)
			t.Cleanup(func() { _ = rdb.Del(ctx, recorded, overflow, maintenance).Err() })
			for i := 0; i < n; i++ {
				raw, err := json.Marshal(service.CarpoolDeviceRecord{DeviceKey: fmt.Sprint(i), LastSeenAt: int64(i + 1)})
				require.NoError(t, err)
				require.NoError(t, rdb.HSet(ctx, recorded, fmt.Sprint(i), raw).Err())
			}
			// Different cache instances model concurrent Gateway replicas.
			var wg sync.WaitGroup
			counts := make(chan int, 4)
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					other := &identityCache{rdb: rdb}
					count, err := other.ResetCarpoolAdmissionCycle(ctx, id, 10, service.CarpoolAdmissionPolicy{Generation: "00000000000000000001", Admissions: 2}, due)
					if err != nil {
						t.Errorf("free slots: %v", err)
					}
					counts <- count
				}()
			}
			wg.Wait()
			close(counts)
			removed := 0
			for count := range counts {
				removed += count
			}
			require.Equal(t, max(0, n-8), removed)
			require.Equal(t, int64(min(n, 8)), rdb.HLen(ctx, recorded).Val())
			for i := 0; i < max(0, n-8); i++ {
				require.False(t, rdb.HExists(ctx, recorded, fmt.Sprint(i)).Val())
			}
			// A new admission after maintenance must survive retries of this period.
			raw, _ := json.Marshal(service.CarpoolDeviceRecord{DeviceKey: "new", LastSeenAt: 100})
			require.NoError(t, rdb.HSet(ctx, recorded, "new", raw).Err())
			count, err := cache.ResetCarpoolAdmissionCycle(ctx, id, 10, service.CarpoolAdmissionPolicy{Generation: "00000000000000000001", Admissions: 2}, due)
			require.NoError(t, err)
			require.Zero(t, count)
			count, err = cache.ResetCarpoolAdmissionCycle(ctx, id, 10, service.CarpoolAdmissionPolicy{Generation: "00000000000000000001", Admissions: 2}, "2026-10-19T10:30:00Z")
			require.NoError(t, err)
			require.Zero(t, count)
			require.True(t, rdb.HExists(ctx, recorded, "new").Val())
		})
	}
}

func TestCarpoolCycleAdmissionRedis(t *testing.T) {
	socket := os.Getenv("SAIAI_TEST_CARPOOL_REDIS_SOCKET")
	if socket == "" {
		t.Skip("task-owned Redis Unix socket not supplied")
	}
	require.True(t, filepath.IsAbs(socket))
	rdb := redis.NewClient(&redis.Options{Network: "unix", Addr: socket})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	cache := &identityCache{rdb: rdb}
	identity := service.NewIdentityService(cache, "TEST_ONLY_SECRET")
	policy := service.CarpoolAdmissionPolicy{Generation: "00000000000000000001", Admissions: 2}
	account := &service.Account{ID: time.Now().UnixNano(), Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Extra: map[string]any{
		"claude_oauth_mode": "carpool", "claude_oauth_carpool_device_limit": 10,
		"claude_oauth_carpool_auto_expand_enabled": true, "claude_oauth_carpool_maintenance_admissions_per_cycle": 2,
		service.CarpoolMaintenanceGenerationKey: policy.Generation, service.CarpoolNextMaintenanceKey: "2026-10-20T16:30:00Z",
	}}
	id := account.ID
	t.Cleanup(func() {
		_ = rdb.Del(ctx, carpoolRecordedKey(id), carpoolOverflowKey(id), carpoolAdmissionBudgetKey(id)).Err()
	})
	admit := func(device string) (*service.CarpoolDeviceRecord, error) {
		return identity.EnsureCarpoolDeviceAllowed(ctx, account, fmt.Sprintf(`{"device_id":%q}`, device), nil)
	}
	overview, err := identity.ListCarpoolDevices(ctx, account)
	require.NoError(t, err)
	require.Equal(t, 0, overview.RecordedCount)
	require.Equal(t, 2, *overview.AdmissionsRemaining)
	require.Equal(t, "2026-10-20T16:30:00Z", overview.NextMaintenanceAt)
	// With ten physically free slots only two concurrent new devices may enter.
	var wg sync.WaitGroup
	winners := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			device := fmt.Sprintf("new-%d", i)
			_, err := admit(device)
			if err == nil {
				winners <- device
			} else if !errors.Is(err, service.ErrClaudeOAuthCarpoolDevicesFull) {
				t.Errorf("admission: %v", err)
			}
		}(i)
	}
	wg.Wait()
	close(winners)
	admitted := []string{}
	for device := range winners {
		admitted = append(admitted, device)
	}
	require.Len(t, admitted, 2)
	require.Equal(t, int64(2), rdb.HLen(ctx, carpoolRecordedKey(id)).Val())
	remaining, err := cache.CarpoolAdmissionsRemaining(ctx, id, policy)
	require.NoError(t, err)
	require.Zero(t, remaining)
	// Existing devices do not spend another admission, including concurrent use.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := admit(admitted[0])
			if err != nil {
				t.Errorf("existing device: %v", err)
			}
		}()
	}
	wg.Wait()
	record, err := admit(admitted[0])
	require.NoError(t, err)
	require.NoError(t, cache.DeleteCarpoolDevice(ctx, id, record.DeviceKey))
	_, err = admit("after-manual-delete")
	require.ErrorIs(t, err, service.ErrClaudeOAuthCarpoolDevicesFull)
	// A cycle resets, rather than adds to, the allowance.
	_, err = cache.ResetCarpoolAdmissionCycle(ctx, id, 10, policy, "2026-10-20T16:30:00Z")
	require.NoError(t, err)
	_, err = admit("cycle-2-a")
	require.NoError(t, err)
	remaining, err = cache.CarpoolAdmissionsRemaining(ctx, id, policy)
	require.NoError(t, err)
	require.Equal(t, 1, remaining)
	_, err = cache.ResetCarpoolAdmissionCycle(ctx, id, 10, policy, "2026-10-20T16:30:00Z")
	require.NoError(t, err)
	remaining, err = cache.CarpoolAdmissionsRemaining(ctx, id, policy)
	require.NoError(t, err)
	require.Equal(t, 1, remaining, "retry must not restore spent allowance")
	_, err = cache.ResetCarpoolAdmissionCycle(ctx, id, 10, policy, "2026-10-21T16:30:00Z")
	require.NoError(t, err)
	remaining, err = cache.CarpoolAdmissionsRemaining(ctx, id, policy)
	require.NoError(t, err)
	require.Equal(t, 2, remaining, "unused admission must not carry over")
	_, err = admit("cycle-3-a")
	require.NoError(t, err)
	_, err = admit("cycle-3-b")
	require.NoError(t, err)
	_, err = admit("cycle-3-c")
	require.ErrorIs(t, err, service.ErrClaudeOAuthCarpoolDevicesFull)
	// Re-enabling starts a new generation; stale workers cannot reset it.
	newer := service.CarpoolAdmissionPolicy{Generation: "00000000000000000002", Admissions: 2}
	_, err = cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, id, "new-generation", service.ClientHints{}, 10, 100, newer)
	require.NoError(t, err)
	_, err = cache.ResetCarpoolAdmissionCycle(ctx, id, 10, policy, "2026-10-22T16:30:00Z")
	require.NoError(t, err)
	remaining, err = cache.CarpoolAdmissionsRemaining(ctx, id, newer)
	require.NoError(t, err)
	require.Equal(t, 1, remaining)
	_, err = admit("stale-policy")
	require.ErrorIs(t, err, service.ErrClaudeOAuthCarpoolDevicesFull)
	// Concurrent first requests from one device consume exactly one admission.
	newest := service.CarpoolAdmissionPolicy{Generation: "00000000000000000003", Admissions: 2}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, id, "same-new-device", service.ClientHints{}, 10, 100, newest)
			if err != nil {
				t.Errorf("same device: %v", err)
			}
		}()
	}
	wg.Wait()
	remaining, err = cache.CarpoolAdmissionsRemaining(ctx, id, newest)
	require.NoError(t, err)
	require.Equal(t, 1, remaining)
	// Disabling maintenance restores fixed-capacity admission; unlimited skips it.
	account.Extra["claude_oauth_carpool_auto_expand_enabled"] = false
	_, err = admit("maintenance-disabled")
	require.NoError(t, err)
	count := rdb.HLen(ctx, carpoolRecordedKey(id)).Val()
	account.Extra["claude_oauth_carpool_unlimited_devices"] = true
	record, err = admit("unlimited")
	require.NoError(t, err)
	require.Nil(t, record)
	require.Equal(t, count, rdb.HLen(ctx, carpoolRecordedKey(id)).Val())
}

func TestCarpoolFullCycleRedis(t *testing.T) {
	socket := os.Getenv("SAIAI_TEST_CARPOOL_REDIS_SOCKET")
	if socket == "" {
		t.Skip("task-owned Redis Unix socket not supplied")
	}
	require.True(t, filepath.IsAbs(socket))
	rdb := redis.NewClient(&redis.Options{Network: "unix", Addr: socket})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &identityCache{rdb: rdb}
	ctx := context.Background()
	id := time.Now().UnixNano()
	t.Cleanup(func() {
		_ = rdb.Del(ctx, carpoolRecordedKey(id), carpoolOverflowKey(id), carpoolAdmissionBudgetKey(id)).Err()
	})
	for i := 0; i < 10; i++ {
		_, err := cache.GetOrCreateCarpoolDevice(ctx, id, fmt.Sprint(i), service.ClientHints{}, 10, int64(i+1))
		require.NoError(t, err)
	}
	policy := service.CarpoolAdmissionPolicy{Generation: "00000000000000000001", Admissions: 2}
	removed, err := cache.ResetCarpoolAdmissionCycle(ctx, id, 10, policy, "2026-10-20T16:30:00Z")
	require.NoError(t, err)
	require.Equal(t, 2, removed)
	require.False(t, rdb.HExists(ctx, carpoolRecordedKey(id), carpoolDeviceField("0")).Val())
	require.False(t, rdb.HExists(ctx, carpoolRecordedKey(id), carpoolDeviceField("1")).Val())
	for _, device := range []string{"existing-new-a", "existing-new-b"} {
		_, err := cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, id, device, service.ClientHints{}, 10, 100, policy)
		require.NoError(t, err)
	}
	_, err = cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, id, "third", service.ClientHints{}, 10, 100, policy)
	require.ErrorIs(t, err, service.ErrClaudeOAuthCarpoolDevicesFull)
	require.Equal(t, int64(10), rdb.HLen(ctx, carpoolRecordedKey(id)).Val())
}

func TestCarpoolUnlimitedCycleRedis(t *testing.T) {
	socket := os.Getenv("SAIAI_TEST_CARPOOL_REDIS_SOCKET")
	if socket == "" {
		t.Skip("task-owned Redis Unix socket not supplied")
	}
	rdb := redis.NewClient(&redis.Options{Network: "unix", Addr: socket})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	cache := &identityCache{rdb: rdb}
	id := time.Now().UnixNano()
	t.Cleanup(func() {
		_ = rdb.Del(ctx, carpoolRecordedKey(id), carpoolOverflowKey(id), carpoolAdmissionBudgetKey(id)).Err()
	})
	finite := service.CarpoolAdmissionPolicy{Generation: "00000000000000000001", Admissions: 1}
	_, err := cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, id, "first", service.ClientHints{}, 2, 1, finite)
	require.NoError(t, err)
	_, err = cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, id, "second", service.ClientHints{}, 2, 2, finite)
	require.ErrorIs(t, err, service.ErrClaudeOAuthCarpoolDevicesFull)
	unlimited := finite
	unlimited.Admissions = 0
	// A spent finite budget must not block an explicit switch to unlimited.
	_, err = cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, id, "second", service.ClientHints{}, 2, 3, unlimited)
	require.NoError(t, err)
	_, err = cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, id, "third", service.ClientHints{}, 2, 4, unlimited)
	require.ErrorIs(t, err, service.ErrClaudeOAuthCarpoolDevicesFull)
	remaining, err := cache.CarpoolAdmissionsRemaining(ctx, id, unlimited)
	require.NoError(t, err)
	require.Equal(t, -1, remaining)
	identity := service.NewIdentityService(cache, "TEST_ONLY_SECRET")
	account := &service.Account{ID: id, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Extra: map[string]any{
		"claude_oauth_mode": "carpool", "claude_oauth_carpool_device_limit": 2,
		"claude_oauth_carpool_auto_expand_enabled":              true,
		"claude_oauth_carpool_maintenance_admissions_per_cycle": 0,
	}}
	overview, err := identity.ListCarpoolDevices(ctx, account)
	require.NoError(t, err)
	require.Equal(t, 2, overview.RecordedCount)
	require.Equal(t, -1, *overview.AdmissionsRemaining, "overview must distinguish unlimited cycle budget from full capacity")
	removed, err := cache.ResetCarpoolAdmissionCycle(ctx, id, 2, unlimited, "2026-10-21T00:00:00Z")
	require.NoError(t, err)
	require.Zero(t, removed)
	require.EqualValues(t, 2, rdb.HLen(ctx, carpoolRecordedKey(id)).Val())
	require.NoError(t, cache.DeleteCarpoolDevice(ctx, id, carpoolDeviceField("second")))
	_, err = cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, id, "third", service.ClientHints{}, 2, 5, unlimited)
	require.NoError(t, err)
}

func TestCarpoolCycleAllowanceEditRedis(t *testing.T) {
	socket := os.Getenv("SAIAI_TEST_CARPOOL_REDIS_SOCKET")
	if socket == "" {
		t.Skip("task-owned Redis Unix socket not supplied")
	}
	rdb := redis.NewClient(&redis.Options{Network: "unix", Addr: socket})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	cache := &identityCache{rdb: rdb}
	id := time.Now().UnixNano()
	t.Cleanup(func() {
		_ = rdb.Del(ctx, carpoolRecordedKey(id), carpoolOverflowKey(id), carpoolAdmissionBudgetKey(id)).Err()
	})
	policy := service.CarpoolAdmissionPolicy{Generation: "00000000000000000001", Admissions: 1}
	due := "2026-10-21T00:00:00Z"
	_, err := cache.ResetCarpoolAdmissionCycle(ctx, id, 9, policy, due)
	require.NoError(t, err)
	_, err = cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, id, "first", service.ClientHints{}, 9, 1, policy)
	require.NoError(t, err)
	policy.Admissions = 3
	remaining, err := cache.CarpoolAdmissionsRemaining(ctx, id, policy)
	require.NoError(t, err)
	require.Equal(t, 2, remaining, "increase applies immediately without refunding spent admissions")
	_, err = cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, id, "second", service.ClientHints{}, 9, 2, policy)
	require.NoError(t, err)
	policy.Admissions = 1
	remaining, err = cache.CarpoolAdmissionsRemaining(ctx, id, policy)
	require.NoError(t, err)
	require.Zero(t, remaining)
	_, err = cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, id, "third", service.ClientHints{}, 9, 3, policy)
	require.ErrorIs(t, err, service.ErrClaudeOAuthCarpoolDevicesFull)
	_, err = cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, id, "first", service.ClientHints{}, 9, 4, policy)
	require.NoError(t, err, "decreasing allowance must not reject existing devices")
	policy.Admissions = 3
	_, err = cache.ResetCarpoolAdmissionCycle(ctx, id, 9, policy, due)
	require.NoError(t, err)
	remaining, err = cache.CarpoolAdmissionsRemaining(ctx, id, policy)
	require.NoError(t, err)
	require.Equal(t, 1, remaining, "an edit must not reset the existing cycle token")
}
