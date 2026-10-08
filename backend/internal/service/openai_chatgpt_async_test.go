package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChatGPTAsyncStatusIsNotAStreamCompletion(t *testing.T) {
	for _, status := range []string{"3", "5", "6", "7", "8", "null", `"3"`, `{}`} {
		t.Run(status, func(t *testing.T) {
			o := NewChatGPTConversationStreamObserver()
			require.NoError(t, o.Observe([]byte(fmt.Sprintf("data: {\"type\":\"conversation_async_status\",\"conversation_id\":\"TEST_ONLY\",\"async_status\":%s}\n\ndata: {\"type\":\"message_stream_complete\",\"conversation_id\":\"TEST_ONLY\"}\n\n", status))))
			summary, err := o.Finish()
			require.NoError(t, err)
			require.True(t, summary.CompletionSeen)
			require.True(t, summary.AsyncStatusSeen)
			require.True(t, ChatGPTAsyncPending(summary.AsyncStatus))
			ctx := context.Background()
			cache := NewChatGPTMemoryTurnCache()
			_, err = cache.PutChatGPTTurnIfAbsent(ctx, "turn", &ChatGPTTurnSnapshot{AccountID: 1, BasePriceUSD: .03, StartedAt: time.Now()}, time.Minute)
			require.NoError(t, err)
			_, err = cache.SetChatGPTTurnAsyncStatus(ctx, "turn", summary.AsyncStatus)
			require.NoError(t, err)
			_, err = cache.SealChatGPTTurnBilling(ctx, "turn")
			require.ErrorIs(t, err, ErrChatGPTTurnPending)
			pending, err := cache.GetChatGPTTurn(ctx, "turn")
			require.NoError(t, err)
			require.False(t, pending.TerminalSeen)
			require.Nil(t, pending.ImageBilling)
			_, err = cache.SetChatGPTTurnAsyncStatus(ctx, "turn", 4)
			require.NoError(t, err)
			sealed, err := cache.SealChatGPTTurnBilling(ctx, "turn")
			require.NoError(t, err)
			require.True(t, sealed.TerminalSeen)
			unchanged, err := cache.SetChatGPTTurnAsyncStatus(ctx, "turn", 3)
			require.NoError(t, err)
			require.False(t, unchanged.AsyncPending, "a late old delivery cannot reopen a sealed bill")
		})
	}
}

const chatGPTCompletedSnapshotFixture = `{"conversation_id":"TEST_ONLY_CONVERSATION","async_status":4,"current_node":"final","mapping":{
"user":{"parent":"old","message":{"id":"user","author":{"role":"user"}}},
"image":{"parent":"user","message":{"id":"image","author":{"role":"tool","name":"image_gen"},"status":"finished_successfully","content":{"content_type":"multimodal_text","parts":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://file_TEST_ONLY","width":3840,"height":2160}]}}},
"final":{"parent":"image","message":{"id":"final","author":{"role":"assistant"},"status":"finished_successfully","end_turn":true,"metadata":{"model_slug":"gpt-5-6-thinking"}}},
"old":{"parent":null,"message":{"id":"old","author":{"role":"tool","name":"image_gen"},"status":"finished_successfully","content":{"content_type":"multimodal_text","parts":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://file_OLD"}]}}}}}`

func TestChatGPTSnapshotRequiresCurrentOwnedCompletedBranch(t *testing.T) {
	summary, images, completed, err := InspectChatGPTConversationSnapshot([]byte(chatGPTCompletedSnapshotFixture), "TEST_ONLY_CONVERSATION", ChatGPTMessageIDHash("user"))
	require.NoError(t, err)
	require.True(t, completed)
	require.Equal(t, "gpt-5-6-thinking", summary.ObservedModel)
	require.Len(t, images.AssetHashes, 1, "historical images must not be charged again")
	require.Equal(t, []string{"3840x2160"}, images.AssetSizes)
	for _, change := range []struct{ old, next string }{
		{`"async_status":4`, `"async_status":3`}, {`"async_status":4`, `"async_status":null`},
		{`"end_turn":true`, `"end_turn":false`}, {`"status":"finished_successfully"`, `"status":"in_progress"`},
		{`"role":"assistant"`, `"role":"tool"`}, {`"parent":"image"`, `"parent":"old"`},
		{`"id":"user"`, `"id":"another-user"`}, {`"conversation_id":"TEST_ONLY_CONVERSATION"`, `"conversation_id":"OTHER"`},
	} {
		_, _, complete, _ := InspectChatGPTConversationSnapshot([]byte(strings.ReplaceAll(chatGPTCompletedSnapshotFixture, change.old, change.next)), "TEST_ONLY_CONVERSATION", ChatGPTMessageIDHash("user"))
		require.False(t, complete, change.old)
	}
}

func TestChatGPTUpdateFilteringIsolatesScopeAccountCatchupsAndTopics(t *testing.T) {
	ctx := context.Background()
	cache := NewChatGPTMemoryTurnCache()
	scope := ChatGPTTurnScope{1, 2, 3}
	turn := &ChatGPTTurnSnapshot{Identity: ChatGPTTurnBillingIdentity{RequestID: "first", PayloadHash: "hash"}, AccountID: 11, StartedAt: time.Now()}
	_, err := cache.PutChatGPTTurnIfAbsent(ctx, scope.TurnKey(turn.Identity), turn, time.Minute)
	require.NoError(t, err)
	require.NoError(t, cache.BindChatGPTUpdates(ctx, scope.ResumeKey("owned"), scope.TurnKey(turn.Identity), scope.UpdatesKey()))
	topic := "conv-turn-low-ttl-TEST_ONLY"
	require.NoError(t, cache.BindChatGPTResume(ctx, scope.TopicKey(topic), scope.TurnKey(turn.Identity)))
	subscribed := func(topic string) bool { return topic == "conversations" || topic == "conv-turn-low-ttl-TEST_ONLY" }
	owned := `{"type":"message","topic_id":"conversations","offset":"ACCOUNT_CURSOR","payload":{"type":"conversation-update","payload":{"conversation_id":"owned","update_type":"async-task-completed","update_content":{"message":{"content":"OWNED_CONTENT"}}}}}`
	foreign := strings.ReplaceAll(owned, "owned", "foreign")
	stream := `{"type":"message","topic_id":"` + topic + `","offset":"TURN_CURSOR","payload":{"type":"conversation-turn-stream","payload":{"conversation_id":"owned","turn_id":"turn","type":"done"}}}`
	batch := []byte(` [` + foreign + `,` + owned + `,{"id":3,"reply":{"type":"subscribe","catchups":[` + foreign + `,` + owned + `]}},` + stream + `,{"type":"unknown","payload":{"conversation_id":"owned"}}] `)
	updates, err := FilterChatGPTUpdates(ctx, cache, scope, 11, batch, subscribed)
	require.NoError(t, err)
	require.Len(t, updates, 3)
	encoded, err := json.Marshal(updates)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "foreign")
	require.NotContains(t, string(encoded), "ACCOUNT_CURSOR")
	require.Contains(t, string(encoded), "TURN_CURSOR")
	require.Contains(t, string(encoded), "OWNED_CONTENT")
	for _, other := range []ChatGPTTurnScope{{2, 2, 3}, {1, 3, 3}, {1, 2, 4}} {
		updates, err := FilterChatGPTUpdates(ctx, cache, other, 11, batch, subscribed)
		require.NoError(t, err)
		require.Empty(t, updates)
	}
	updates, err = FilterChatGPTUpdates(ctx, cache, scope, 12, batch, subscribed)
	require.NoError(t, err)
	require.Empty(t, updates)
	updates, err = FilterChatGPTUpdates(ctx, cache, scope, 11, []byte(strings.ReplaceAll(stream, `"conversation_id":"owned"`, `"conversation_id":"foreign"`)), subscribed)
	require.NoError(t, err)
	require.Empty(t, updates)
	oldImage := strings.ReplaceAll(owned, `{"content":"OWNED_CONTENT"}`, `{"status":"finished_successfully","author":{"role":"tool","name":"image_gen"},"content":{"content_type":"multimodal_text","parts":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://file_HISTORY","width":1024,"height":1024}]}}`)
	_, err = FilterChatGPTUpdates(ctx, cache, scope, 11, []byte(`{"id":4,"reply":{"catchups":[`+oldImage+`]}}`), subscribed)
	require.NoError(t, err)
	pending, err := cache.GetChatGPTTurn(ctx, scope.TurnKey(turn.Identity))
	require.NoError(t, err)
	require.Empty(t, pending.Images.AssetHashes, "a replayed image from another branch cannot increase this turn's bill")
	asset, err := cache.GetChatGPTResume(ctx, scope.AssetKey(ChatGPTAssetLookupHashes("file_HISTORY")[0]))
	require.NoError(t, err)
	require.NotNil(t, asset, "owned image updates can still resolve downloads")
	accounts, err := cache.ListChatGPTUpdateAccounts(ctx, scope.UpdatesKey())
	require.NoError(t, err)
	require.Equal(t, []int64{11}, accounts)
	accounts, err = cache.ListChatGPTUpdateAccounts(ctx, (ChatGPTTurnScope{1, 3, 3}).UpdatesKey())
	require.NoError(t, err)
	require.Empty(t, accounts)
}

func TestChatGPTUpdateURLsStayOnProviderOrExplicitReplayOrigin(t *testing.T) {
	svc := &OpenAIGatewayService{}
	for _, value := range []string{"wss://ws.chatgpt.com/ws?access_token=TEST_ONLY", "wss://test.webpubsub.azure.com/client/hubs/test"} {
		require.True(t, svc.validChatGPTUpdatesURL(value))
	}
	for _, value := range []string{"ws://ws.chatgpt.com", "wss://127.0.0.1/", "wss://chatgpt.com.attacker.invalid/", "wss://chatgpt.com:444/", "wss://user:pass@chatgpt.com/", "wss://ws.chatgpt.com/#TEST_ONLY"} {
		require.False(t, svc.validChatGPTUpdatesURL(value))
	}
}
