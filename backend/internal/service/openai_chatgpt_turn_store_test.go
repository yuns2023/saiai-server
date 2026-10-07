package service

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestChatGPTTurnCacheSnapshotScopeAndExpiry(t *testing.T) {
	ctx := context.Background()
	cache := NewChatGPTMemoryTurnCache()
	scope := ChatGPTTurnScope{UserID: 1, APIKeyID: 2, GroupID: 3}
	first := &ChatGPTTurnSnapshot{Identity: ChatGPTTurnBillingIdentity{RequestID: "first", PayloadHash: "digest"}, AccountID: 19, BasePriceUSD: 0.05, RequestedModel: "gpt-6-pro", StartedAt: time.Now()}
	key := scope.TurnKey(first.Identity)
	copy, err := cache.PutChatGPTTurnIfAbsent(ctx, key, first, time.Minute)
	require.NoError(t, err)
	second := *first
	second.BasePriceUSD = 0.09
	second.AccountID = 20
	copy, err = cache.PutChatGPTTurnIfAbsent(ctx, key, &second, time.Hour)
	require.NoError(t, err)
	require.Equal(t, first, copy)
	alias := scope.ResumeKey("PRIVATE_CONVERSATION")
	require.NotContains(t, alias, "PRIVATE_CONVERSATION")
	require.NoError(t, cache.BindChatGPTResume(ctx, alias, key))
	for _, other := range []ChatGPTTurnScope{{2, 2, 3}, {1, 3, 3}, {1, 2, 4}} {
		resume, err := cache.GetChatGPTResume(ctx, other.ResumeKey("PRIVATE_CONVERSATION"))
		require.NoError(t, err)
		require.Nil(t, resume)
	}
	other := *first
	other.Identity.RequestID = "second-turn"
	_, err = cache.PutChatGPTTurnIfAbsent(ctx, scope.TurnKey(other.Identity), &other, time.Minute)
	require.NoError(t, err)
	require.Error(t, cache.BindChatGPTResume(ctx, alias, scope.TurnKey(other.Identity)), "overlapping turns must not overwrite an unsettled owner")
	require.NoError(t, cache.MarkChatGPTTurnTerminal(ctx, key))
	terminal, err := cache.GetChatGPTTurn(ctx, key)
	require.NoError(t, err)
	require.True(t, terminal.TerminalSeen)
	require.False(t, terminal.Completed)
	require.NoError(t, cache.BindChatGPTResume(ctx, alias, scope.TurnKey(other.Identity)), "a completed generation may advance while billing is queued")
	require.NoError(t, cache.CompleteChatGPTTurn(ctx, key))
	require.NoError(t, cache.BindChatGPTResume(ctx, alias, scope.TurnKey(other.Identity)))
	resume, err := cache.GetChatGPTResume(ctx, alias)
	require.NoError(t, err)
	require.Equal(t, "second-turn", resume.Identity.RequestID)
	raw, err := json.Marshal(resume)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "PRIVATE_CONVERSATION")
	mem := cache.(*chatGPTMemoryTurnCache)
	entry := mem.turns[scope.TurnKey(other.Identity)]
	entry.expires = time.Now().Add(-time.Second)
	mem.turns[scope.TurnKey(other.Identity)] = entry
	resume, err = cache.GetChatGPTResume(ctx, alias)
	require.NoError(t, err)
	require.Nil(t, resume)
}

func TestChatGPTResumeRejectsGenerationInputs(t *testing.T) {
	id, err := ResolveChatGPTResumeConversation([]byte(`{"conversation_id":"fixture","offset":0,"client_extension":true}`))
	require.NoError(t, err)
	require.Equal(t, "fixture", id)
	for _, body := range []string{`{}`, `{"conversation_id":2}`, `{"conversation_id":"fixture","messages":[]}`, `{"conversation_id":"fixture","model":"gpt-6-pro"}`, `{"conversation_id":"fixture","tools":[]}`} {
		_, err := ResolveChatGPTResumeConversation([]byte(body))
		require.Error(t, err)
	}
}
