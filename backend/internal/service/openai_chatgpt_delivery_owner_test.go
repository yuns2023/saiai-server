package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChatGPTDeliveryOwnershipSurvivesAccountingExpiryWithoutRevivingBill(t *testing.T) {
	ctx := context.Background()
	cache := NewChatGPTMemoryTurnCache()
	mem, ok := cache.(*chatGPTMemoryTurnCache)
	require.True(t, ok)
	scope := ChatGPTTurnScope{UserID: 1, APIKeyID: 2, GroupID: 3}
	turn := &ChatGPTTurnSnapshot{Identity: ChatGPTTurnBillingIdentity{RequestID: "TEST_ONLY_TURN", PayloadHash: "TEST_ONLY_HASH"}, AccountID: 19, BasePriceUSD: .01, StartedAt: time.Now(), UserMessageHash: ChatGPTMessageIDHash("TEST_ONLY_USER")}
	turnKey := scope.TurnKey(turn.Identity)
	_, err := cache.PutChatGPTTurnIfAbsent(ctx, turnKey, turn, time.Hour)
	require.NoError(t, err)
	require.NoError(t, BindChatGPTConversationDelivery(ctx, cache, scope, turn, "TEST_ONLY_CONVERSATION"))
	asset := chatGPTImageDigest("sediment://file_TEST_ONLY")
	require.NoError(t, BindChatGPTDeliveryAssets(ctx, cache, scope, turn, ChatGPTImageEvidence{AssetHashes: []string{asset}}))
	turnExpiry := mem.turns[turnKey].expires
	_, _, err = ResolveChatGPTConversationOwner(ctx, cache, scope, "TEST_ONLY_CONVERSATION")
	require.NoError(t, err)
	require.Equal(t, turnExpiry, mem.turns[turnKey].expires, "reading an owner must not extend accounting expiry")
	entry := mem.turns[turnKey]
	entry.expires = time.Now().Add(-time.Second)
	mem.turns[turnKey] = entry
	owner, expiredTurn, err := ResolveChatGPTConversationOwner(ctx, cache, scope, "TEST_ONLY_CONVERSATION")
	require.NoError(t, err)
	require.Nil(t, expiredTurn)
	require.EqualValues(t, 19, owner.AccountID)
	require.Equal(t, turn.UserMessageHash, owner.UserMessageHash)
	missing, err := cache.GetChatGPTTurn(ctx, turnKey)
	require.NoError(t, err)
	require.Nil(t, missing, "historical delivery must not create a replay/billing snapshot")
	assetOwner, err := cache.GetChatGPTDeliveryOwner(ctx, scope.AssetOwnerKey(asset))
	require.NoError(t, err)
	require.EqualValues(t, 19, assetOwner.AccountID)
	for _, other := range []ChatGPTTurnScope{{2, 2, 3}, {1, 3, 3}, {1, 2, 4}} {
		owner, _, err := ResolveChatGPTConversationOwner(ctx, cache, other, "TEST_ONLY_CONVERSATION")
		require.NoError(t, err)
		require.Nil(t, owner)
		owner, err = cache.GetChatGPTDeliveryOwner(ctx, other.AssetOwnerKey(asset))
		require.NoError(t, err)
		require.Nil(t, owner)
	}
	require.Error(t, cache.BindChatGPTDeliveryOwner(ctx, scope.ConversationOwnerKey("TEST_ONLY_CONVERSATION"), &ChatGPTDeliveryOwner{AccountID: 20}))
	owner, err = cache.GetChatGPTDeliveryOwner(ctx, scope.ConversationOwnerKey("TEST_ONLY_CONVERSATION"))
	require.NoError(t, err)
	require.EqualValues(t, 19, owner.AccountID)
	require.NotContains(t, scope.ConversationOwnerKey("TEST_ONLY_CONVERSATION"), "TEST_ONLY_CONVERSATION")
	newer := &ChatGPTDeliveryOwner{AccountID: 19, UserMessageHash: ChatGPTMessageIDHash("TEST_ONLY_NEW_USER"), TurnStartedMS: turn.StartedAt.Add(time.Minute).UnixMilli()}
	require.NoError(t, cache.BindChatGPTDeliveryOwner(ctx, scope.ConversationOwnerKey("TEST_ONLY_CONVERSATION"), newer))
	require.NoError(t, cache.BindChatGPTDeliveryOwner(ctx, scope.ConversationOwnerKey("TEST_ONLY_CONVERSATION"), &ChatGPTDeliveryOwner{AccountID: 19, UserMessageHash: turn.UserMessageHash, TurnStartedMS: turn.StartedAt.UnixMilli()}))
	owner, err = cache.GetChatGPTDeliveryOwner(ctx, scope.ConversationOwnerKey("TEST_ONLY_CONVERSATION"))
	require.NoError(t, err)
	require.Equal(t, newer, owner, "a delayed delivery must not rewind the latest branch identity")
}

func TestChatGPTDeliveryOwnerMigratesOnlyLiveLegacyContext(t *testing.T) {
	ctx := context.Background()
	cache := NewChatGPTMemoryTurnCache()
	scope := ChatGPTTurnScope{UserID: 1, APIKeyID: 2, GroupID: 3}
	turn := &ChatGPTTurnSnapshot{Identity: ChatGPTTurnBillingIdentity{RequestID: "TEST_ONLY_LEGACY", PayloadHash: "TEST_ONLY_HASH"}, AccountID: 17, StartedAt: time.Now()}
	_, err := cache.PutChatGPTTurnIfAbsent(ctx, scope.TurnKey(turn.Identity), turn, time.Hour)
	require.NoError(t, err)
	require.NoError(t, cache.BindChatGPTResume(ctx, scope.ResumeKey("TEST_ONLY_LEGACY"), scope.TurnKey(turn.Identity)))
	owner, live, err := ResolveChatGPTConversationOwner(ctx, cache, scope, "TEST_ONLY_LEGACY")
	require.NoError(t, err)
	require.EqualValues(t, 17, owner.AccountID)
	require.Equal(t, turn, live)
	owner, _, err = ResolveChatGPTConversationOwner(ctx, cache, scope, "UNKNOWN")
	require.NoError(t, err)
	require.Nil(t, owner)
	require.Error(t, cache.BindChatGPTDeliveryOwner(ctx, scope.ResumeKey("TEST_ONLY_LEGACY"), &ChatGPTDeliveryOwner{AccountID: 17}), "long ownership leases cannot extend old accounting or upload keys")
	require.Error(t, cache.BindChatGPTDeliveryOwner(ctx, scope.AssetOwnerKey(strings.Repeat("z", 64)), &ChatGPTDeliveryOwner{AccountID: 17}))
}
