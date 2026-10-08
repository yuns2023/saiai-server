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
func TestNativeChatRedisUploadOwnershipIsAtomicScopedAndNeverExtended(t *testing.T) {
	socket := os.Getenv("SAIAI_TEST_REDIS_SOCKET")
	if socket == "" {
		t.Skip("disposable Redis socket not supplied")
	}
	require.True(t, filepath.IsAbs(socket))
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Network: "unix", Addr: socket, DialTimeout: 3 * time.Second})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &gatewayCache{rdb: rdb}
	scope := service.ChatGPTTurnScope{UserID: time.Now().UnixNano(), APIKeyID: 2, GroupID: 3}
	session := scope.UploadSessionKey("TEST_ONLY_DEVICE")
	file, capability := scope.UploadedFileKey("file-TEST_ONLY"), scope.UploadURLKey("TEST_ONLY_CAPABILITY")
	t.Cleanup(func() { _ = rdb.Del(ctx, session, file, capability).Err() })
	var wg sync.WaitGroup
	results := make(chan int64, 8)
	for i := int64(1); i <= 8; i++ {
		wg.Add(1)
		go func(owner int64) {
			defer wg.Done()
			winner, err := cache.ClaimChatGPTUploadOwner(ctx, session, owner, time.Minute)
			if err != nil {
				t.Errorf("claim failed: %v", err)
			}
			results <- winner
		}(i)
	}
	wg.Wait()
	close(results)
	winner, err := cache.GetChatGPTUploadOwner(ctx, session)
	require.NoError(t, err)
	require.Positive(t, winner)
	for value := range results {
		require.Equal(t, winner, value)
	}
	require.NoError(t, cache.BindChatGPTUploadOwner(ctx, []string{file}, winner, time.Minute))
	ttl := rdb.PTTL(ctx, file).Val()
	require.Error(t, cache.BindChatGPTUploadOwner(ctx, []string{capability, file}, winner+10, time.Hour))
	require.False(t, rdb.Exists(ctx, capability).Val() > 0, "a conflict must leave no partial binding")
	require.NoError(t, cache.BindChatGPTUploadOwner(ctx, []string{file, capability}, winner, time.Hour))
	require.LessOrEqual(t, rdb.PTTL(ctx, file).Val(), ttl, "repeated binding must not extend existing ownership")
	other := service.ChatGPTTurnScope{UserID: scope.UserID, APIKeyID: 4, GroupID: 3}
	missing, err := cache.GetChatGPTUploadOwner(ctx, other.UploadedFileKey("file-TEST_ONLY"))
	require.NoError(t, err)
	require.Zero(t, missing)
}

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

func TestNativeChatRedisPendingSealAndScopedUpdateOwnership(t *testing.T) {
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
	scope := service.ChatGPTTurnScope{UserID: time.Now().UnixNano(), APIKeyID: 2, GroupID: 3}
	turn := &service.ChatGPTTurnSnapshot{Identity: service.ChatGPTTurnBillingIdentity{RequestID: uuid.NewString(), PayloadHash: "TEST_ONLY"}, AccountID: 11, BasePriceUSD: .03, StartedAt: time.Now()}
	key, alias, index := scope.TurnKey(turn.Identity), scope.ResumeKey("PRIVATE_CONVERSATION"), scope.UpdatesKey()
	t.Cleanup(func() { _ = rdb.Del(ctx, key, alias, index).Err() })
	_, err := cache.PutChatGPTTurnIfAbsent(ctx, key, turn, time.Minute)
	require.NoError(t, err)
	require.NoError(t, cache.BindChatGPTUpdates(ctx, alias, key, index))
	ttl := rdb.PTTL(ctx, key).Val()
	ids, err := cache.ListChatGPTUpdateAccounts(ctx, index)
	require.NoError(t, err)
	require.Equal(t, []int64{11}, ids)
	for _, other := range []service.ChatGPTTurnScope{{scope.UserID + 1, 2, 3}, {scope.UserID, 3, 3}, {scope.UserID, 2, 4}} {
		missing, err := cache.GetChatGPTResume(ctx, other.ResumeKey("PRIVATE_CONVERSATION"))
		require.NoError(t, err)
		require.Nil(t, missing)
		ids, err := cache.ListChatGPTUpdateAccounts(ctx, other.UpdatesKey())
		require.NoError(t, err)
		require.Empty(t, ids)
	}
	stale, err := cache.GetChatGPTTurn(ctx, key)
	require.NoError(t, err)
	_, err = cache.SetChatGPTTurnAsyncStatus(ctx, key, 3)
	require.NoError(t, err)
	images, err := json.Marshal(stale.Images)
	require.NoError(t, err)
	result, err := chatGPTSealBilling.Run(ctx, rdb, []string{key}, string(images), `{"Count":0,"Size":"","CostUSD":0}`).Text()
	require.NoError(t, err)
	require.Equal(t, "pending", result, "a stale reader must not seal after active work was observed")
	_, err = cache.SealChatGPTTurnBilling(ctx, key)
	require.ErrorIs(t, err, service.ErrChatGPTTurnPending)
	pending, err := cache.GetChatGPTTurn(ctx, key)
	require.NoError(t, err)
	require.False(t, pending.TerminalSeen)
	require.Nil(t, pending.ImageBilling)
	_, err = cache.SetChatGPTTurnAsyncStatus(ctx, key, 4)
	require.NoError(t, err)
	sealed, err := cache.SealChatGPTTurnBilling(ctx, key)
	require.NoError(t, err)
	require.True(t, sealed.TerminalSeen)
	late, err := cache.SetChatGPTTurnAsyncStatus(ctx, key, 3)
	require.NoError(t, err)
	require.False(t, late.AsyncPending)
	require.LessOrEqual(t, rdb.PTTL(ctx, key).Val(), ttl)
	require.LessOrEqual(t, rdb.PTTL(ctx, index).Val(), ttl)
	raw, err := rdb.Get(ctx, key).Result()
	require.NoError(t, err)
	require.NotContains(t, raw, "PRIVATE_CONVERSATION")
}
