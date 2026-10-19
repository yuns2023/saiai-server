package repository

import (
	"context"
	"encoding/json"
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
			maintenance := fmt.Sprintf("claude_carpool_slots_v2:%d", id)
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
					count, err := other.FreeCarpoolSlots(ctx, id, 10, 2, due)
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
			count, err := cache.FreeCarpoolSlots(ctx, id, 10, 2, due)
			require.NoError(t, err)
			require.Zero(t, count)
			count, err = cache.FreeCarpoolSlots(ctx, id, 10, 2, "2026-10-19T10:30:00Z")
			require.NoError(t, err)
			require.Zero(t, count)
			require.True(t, rdb.HExists(ctx, recorded, "new").Val())
		})
	}
}
