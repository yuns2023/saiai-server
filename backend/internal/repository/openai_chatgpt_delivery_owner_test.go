//go:build unit

package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestNativeChatRedisDeliveryOwnershipSurvivesExpiredTurn(t *testing.T) {
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
	turn := &service.ChatGPTTurnSnapshot{Identity: service.ChatGPTTurnBillingIdentity{RequestID: "TEST_ONLY_OWNER", PayloadHash: "TEST_ONLY_HASH"}, AccountID: 19, StartedAt: time.Now(), BasePriceUSD: .01, UserMessageHash: service.ChatGPTMessageIDHash("TEST_ONLY_USER")}
	turnKey, resumeKey, ownerKey := scope.TurnKey(turn.Identity), scope.ResumeKey("TEST_ONLY_CONVERSATION"), scope.ConversationOwnerKey("TEST_ONLY_CONVERSATION")
	assetKey := scope.AssetOwnerKey(service.ChatGPTAssetLookupHashes("file_TEST_ONLY")[0])
	t.Cleanup(func() { _ = rdb.Del(ctx, turnKey, resumeKey, ownerKey, assetKey, scope.UpdatesKey()).Err() })
	_, err := cache.PutChatGPTTurnIfAbsent(ctx, turnKey, turn, time.Hour)
	require.NoError(t, err)
	require.NoError(t, service.BindChatGPTConversationDelivery(ctx, cache, scope, turn, "TEST_ONLY_CONVERSATION"))
	initialTTL := rdb.PTTL(ctx, turnKey).Val()
	_, _, err = service.ResolveChatGPTConversationOwner(ctx, cache, scope, "TEST_ONLY_CONVERSATION")
	require.NoError(t, err)
	require.LessOrEqual(t, rdb.PTTL(ctx, turnKey).Val(), initialTTL)
	require.Greater(t, rdb.PTTL(ctx, ownerKey).Val(), 29*24*time.Hour)
	require.NoError(t, cache.BindChatGPTDeliveryOwner(ctx, assetKey, &service.ChatGPTDeliveryOwner{AccountID: 19}))
	// Expire the actual Redis snapshot; the resume alias may still exist.
	require.NoError(t, rdb.PExpire(ctx, turnKey, time.Millisecond).Err())
	require.Eventually(t, func() bool { return rdb.Exists(ctx, turnKey).Val() == 0 }, time.Second, 5*time.Millisecond)
	owner, expired, err := service.ResolveChatGPTConversationOwner(ctx, cache, scope, "TEST_ONLY_CONVERSATION")
	require.NoError(t, err)
	require.Nil(t, expired)
	require.EqualValues(t, 19, owner.AccountID)
	require.Zero(t, rdb.Exists(ctx, turnKey).Val(), "reading history must not recreate billing state")
	assetOwner, err := cache.GetChatGPTDeliveryOwner(ctx, assetKey)
	require.NoError(t, err)
	require.EqualValues(t, 19, assetOwner.AccountID)
	require.Error(t, cache.BindChatGPTDeliveryOwner(ctx, ownerKey, &service.ChatGPTDeliveryOwner{AccountID: 20}))
	for _, other := range []service.ChatGPTTurnScope{{scope.UserID + 1, 2, 3}, {scope.UserID, 4, 3}, {scope.UserID, 2, 4}} {
		foreign, _, err := service.ResolveChatGPTConversationOwner(ctx, cache, other, "TEST_ONLY_CONVERSATION")
		require.NoError(t, err)
		require.Nil(t, foreign)
	}
	newer := &service.ChatGPTDeliveryOwner{AccountID: 19, UserMessageHash: service.ChatGPTMessageIDHash("TEST_ONLY_NEW_USER"), TurnStartedMS: turn.StartedAt.Add(time.Minute).UnixMilli()}
	require.NoError(t, cache.BindChatGPTDeliveryOwner(ctx, ownerKey, newer))
	require.NoError(t, cache.BindChatGPTDeliveryOwner(ctx, ownerKey, &service.ChatGPTDeliveryOwner{AccountID: 19, UserMessageHash: turn.UserMessageHash, TurnStartedMS: turn.StartedAt.UnixMilli()}))
	owner, err = cache.GetChatGPTDeliveryOwner(ctx, ownerKey)
	require.NoError(t, err)
	require.Equal(t, newer, owner)
}
