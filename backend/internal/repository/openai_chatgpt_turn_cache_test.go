//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Run against a disposable, network-disabled Redis over a task-owned Unix
// socket. Normal unit jobs skip it; it never accepts a production Redis URL.
func TestNativeChatImageBillingRedisSealsConcurrentReplaysAndRetainsTTL(t *testing.T) {
	socket := os.Getenv("SAIAI_TEST_REDIS_SOCKET")
	if socket == "" {
		t.Skip("disposable Redis socket not supplied")
	}
	require.True(t, filepath.IsAbs(socket))
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Network: "unix", Addr: socket, DialTimeout: 3 * time.Second})
	t.Cleanup(func() { _ = rdb.Close() })
	require.NoError(t, rdb.Ping(ctx).Err())
	cache := &gatewayCache{rdb: rdb}
	key := "chatgpt:turn:{TEST_ONLY_" + uuid.NewString() + "}"
	t.Cleanup(func() { _ = rdb.Del(ctx, key).Err() })
	initial := &service.ChatGPTTurnSnapshot{Identity: service.ChatGPTTurnBillingIdentity{RequestID: "TEST_ONLY", PayloadHash: "TEST_ONLY"},
		AccountID: 1, StartedAt: time.Now(), BasePriceUSD: .01,
		ImagePricesUSD: map[string]float64{"1K": .02, "2K": .04, "4K": .08, "unknown": .01}}
	_, err := cache.PutChatGPTTurnIfAbsent(ctx, key, initial, time.Minute)
	require.NoError(t, err)
	_, err = cache.MergeChatGPTTurnImages(ctx, key, service.ChatGPTImageEvidence{AssetHashes: []string{strings.Repeat("a", 64)}})
	require.NoError(t, err)
	_, err = cache.MergeChatGPTTurnImages(ctx, key, service.ChatGPTImageEvidence{AssetHashes: []string{strings.Repeat("a", 64)}, AssetSizes: []string{"2048x2048"}})
	require.NoError(t, err)
	ttl := rdb.PTTL(ctx, key).Val()
	var wg sync.WaitGroup
	results := make(chan *service.ChatGPTTurnSnapshot, 8)
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := cache.SealChatGPTTurnBilling(ctx, key)
			results <- value
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	for value := range results {
		require.True(t, value.TerminalSeen)
		require.Equal(t, .04, value.ImageBilling.CostUSD)
		require.Equal(t, 1, value.ImageBilling.Count)
		require.Equal(t, "2048x2048", value.ImageBilling.Size)
	}
	_, err = cache.MergeChatGPTTurnImages(ctx, key, service.ChatGPTImageEvidence{AssetHashes: []string{strings.Repeat("b", 64)}, AssetSizes: []string{"3840x2160"}})
	require.NoError(t, err)
	again, err := cache.SealChatGPTTurnBilling(ctx, key)
	require.NoError(t, err)
	require.Equal(t, .04, again.ImageBilling.CostUSD, "later delivery metadata cannot increase the frozen charge")
	require.LessOrEqual(t, rdb.PTTL(ctx, key).Val(), ttl)

	// A stale read must not seal new evidence under an older image price total.
	initial.Images = service.ChatGPTImageEvidence{AssetHashes: []string{strings.Repeat("c", 64)}, AssetSizes: []string{"1024x1024"}}
	raw, err := json.Marshal(initial)
	require.NoError(t, err)
	require.NoError(t, rdb.Set(ctx, key, raw, time.Minute).Err())
	stale, err := json.Marshal(service.ChatGPTImageEvidence{})
	require.NoError(t, err)
	result, err := chatGPTSealBilling.Run(ctx, rdb, []string{key}, string(stale), `{"Count":0,"Size":"","CostUSD":0}`).Text()
	require.NoError(t, err)
	require.Equal(t, "retry", result)
	pending, err := cache.GetChatGPTTurn(ctx, key)
	require.NoError(t, err)
	require.False(t, pending.TerminalSeen)
	require.Nil(t, pending.ImageBilling)
}
