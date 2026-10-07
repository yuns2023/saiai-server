package repository

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Run without the PostgreSQL integration harness against a task-owned Redis
// UNIX socket. There is no TCP destination or credential parameter here.
func TestOpenAIProviderAttemptBudgetStoreLoopback(t *testing.T) {
	socket := os.Getenv("SAIAI_TEST_BUDGET_REDIS_SOCKET")
	if socket == "" {
		t.Skip("set SAIAI_TEST_BUDGET_REDIS_SOCKET to a task-owned Redis UNIX socket")
	}
	require.True(t, strings.HasSuffix(socket, "/budget-test-redis.sock"))
	rdb := redis.NewClient(&redis.Options{Network: "unix", Addr: socket, MaxRetries: -1})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	require.NoError(t, rdb.Ping(context.Background()).Err())
	testOpenAIProviderAttemptBudgetStore(t, rdb)
}

func testOpenAIProviderAttemptBudgetStore(t *testing.T, rdb *redis.Client) {
	ctx := context.Background()
	cache := &gatewayCache{rdb: rdb}
	id := "TEST_ONLY-" + uuid.NewString()
	key := buildOpenAIProviderAttemptBudgetKey(id)
	t.Cleanup(func() { require.NoError(t, rdb.Del(ctx, key).Err()) })
	deadline := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	_, err := cache.ReserveOpenAIProviderAttempt(ctx, id, 7, 5, deadline)
	require.ErrorIs(t, err, service.ErrOpenAIProviderAttemptBudgetUnarmed)
	require.Zero(t, rdb.Exists(ctx, key).Val(), "requests cannot arm themselves")
	require.NoError(t, cache.ArmOpenAIProviderAttemptBudget(ctx, id, 7, 5, deadline))

	var sent atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Go(func() {
			// A new repository object mirrors a process/replica restart.
			other, ok := NewGatewayCache(rdb).(service.OpenAIProviderAttemptBudgetStore)
			if !ok {
				t.Error("Gateway cache must implement the attempt budget")
				return
			}
			_, reserveErr := other.ReserveOpenAIProviderAttempt(ctx, id, 7, 5, deadline)
			if reserveErr == nil {
				sent.Add(1)
			} else if !errors.Is(reserveErr, service.ErrOpenAIProviderAttemptBudgetExhausted) {
				t.Errorf("unexpected reserve result: %v", reserveErr)
			}
		})
	}
	wg.Wait()
	require.Equal(t, int64(5), sent.Load())
	require.Equal(t, "5", rdb.HGet(ctx, key, "used").Val())
	require.Greater(t, rdb.TTL(ctx, key).Val(), time.Hour)
	require.Error(t, cache.ArmOpenAIProviderAttemptBudget(ctx, id, 7, 5, deadline))
	require.Error(t, cache.ArmOpenAIProviderAttemptBudget(ctx, id, 8, 10, deadline.Add(time.Hour)))
	for _, changed := range []struct {
		keyID, limit int64
		expires      time.Time
	}{
		{8, 5, deadline}, {7, 10, deadline}, {7, 5, deadline.Add(time.Hour)},
	} {
		_, err := cache.ReserveOpenAIProviderAttempt(ctx, id, changed.keyID, changed.limit, changed.expires)
		require.ErrorIs(t, err, service.ErrOpenAIProviderAttemptBudgetUnavailable)
	}
	_, err = cache.ReserveOpenAIProviderAttempt(ctx, id, 7, 5, time.Now().Add(-time.Second))
	require.ErrorIs(t, err, service.ErrOpenAIProviderAttemptBudgetExpired)
	require.NoError(t, rdb.HSet(ctx, key, "used", "TEST_ONLY_MALFORMED").Err())
	_, err = cache.ReserveOpenAIProviderAttempt(ctx, id, 7, 5, deadline)
	require.ErrorIs(t, err, service.ErrOpenAIProviderAttemptBudgetUnavailable)
	require.NoError(t, rdb.Del(ctx, key).Err())
	_, err = cache.ReserveOpenAIProviderAttempt(ctx, id, 7, 5, deadline)
	require.ErrorIs(t, err, service.ErrOpenAIProviderAttemptBudgetUnarmed, "cache loss must not renew the allowance")
}
