package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const chatGPTSynchronousSnapshotFixture = `{"conversation_id":"TEST_ONLY_CONVERSATION","current_node":"final","mapping":{
"user":{"parent":null,"message":{"id":"user","author":{"role":"user"}}},
"reasoning":{"parent":"user","message":{"id":"reasoning","author":{"role":"assistant"},"status":"finished_successfully","content":{"content_type":"text","parts":["TEST_ONLY"]}}},
"final":{"parent":"reasoning","message":{"id":"final","author":{"role":"assistant"},"status":"finished_successfully","end_turn":true,"content":{"content_type":"text","parts":["TEST_ONLY"]},"metadata":{"model_slug":"gpt-5-6-thinking"}}}}}`

func TestChatGPTSynchronousSnapshotCompletesWithoutAsyncStatus(t *testing.T) {
	for _, status := range []string{"", `"async_status":null,`, `"async_status":4,`} {
		raw := strings.Replace(chatGPTSynchronousSnapshotFixture, `"current_node"`, status+`"current_node"`, 1)
		result, err := InspectChatGPTConversationDelivery([]byte(raw), "TEST_ONLY_CONVERSATION", ChatGPTMessageIDHash("user"))
		require.NoError(t, err)
		require.True(t, result.Completed)
		require.True(t, result.Summary.CompletionSeen)
		require.Equal(t, "gpt-5-6-thinking", result.Summary.ObservedModel)
		require.Empty(t, result.Images.AssetHashes)
	}
}

func TestChatGPTSynchronousSnapshotRejectsUnfinishedForeignOrToolBranches(t *testing.T) {
	for _, change := range []struct{ before, after string }{
		{`"current_node":"final"`, `"async_status":3,"current_node":"final"`},
		{`"current_node":"final"`, `"async_status":99,"current_node":"final"`},
		{`"end_turn":true`, `"end_turn":false`},
		{`"status":"finished_successfully"`, `"status":"in_progress"`},
		{`"role":"assistant"`, `"role":"tool"`},
		{`"content_type":"text"`, `"content_type":"multimodal_text"`},
		{`"id":"user"`, `"id":"other-user"`},
		{`"model_slug":"gpt-5-6-thinking"`, `"is_error":true`},
	} {
		result, _ := InspectChatGPTConversationDelivery([]byte(strings.ReplaceAll(chatGPTSynchronousSnapshotFixture, change.before, change.after)), "TEST_ONLY_CONVERSATION", ChatGPTMessageIDHash("user"))
		require.False(t, result.Completed, change.after)
	}
}

func TestChatGPTSynchronousImagesRequireOwnedFinalAssetsAndFinishedBranch(t *testing.T) {
	for _, leaf := range []string{"final", "image"} {
		for _, status := range []string{"", `"async_status":null,`} {
			raw := strings.Replace(chatGPTCompletedSnapshotFixture, `"async_status":4,`, status, 1)
			raw = strings.Replace(raw, `"current_node":"final"`, `"current_node":"`+leaf+`"`, 1)
			t.Run(leaf+status, func(t *testing.T) {
				result, err := InspectChatGPTConversationDelivery([]byte(raw), "TEST_ONLY_CONVERSATION", ChatGPTMessageIDHash("user"))
				require.NoError(t, err)
				require.True(t, result.Completed)
				require.Equal(t, "completed_synchronous_image", result.Outcome)
				require.Len(t, result.Images.AssetHashes, 1, "do not recharge the ancestor image")
				require.Equal(t, []string{"3840x2160"}, result.Images.AssetSizes)
			})
		}
	}
	base := strings.Replace(chatGPTCompletedSnapshotFixture, `"async_status":4,`, "", 1)
	for _, change := range []struct{ before, after string }{
		{`"id":"image","author":{"role":"tool","name":"image_gen"},"status":"finished_successfully"`, `"id":"image","author":{"role":"tool","name":"image_gen"},"status":"in_progress"`},
		{`"current_node":"final"`, `"async_status":3,"current_node":"final"`},
		{`"current_node":"final"`, `"async_status":99,"current_node":"final"`},
		{`"end_turn":true`, `"end_turn":false`},
		{`"status":"finished_successfully"`, `"status":"in_progress"`},
		{`"name":"image_gen"`, `"name":"browser"`},
		{`"height":2160`, `"height":2160,"metadata":{"generation":{"height":100}}`},
		{`"parts":[{`, `"parts":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://PREVIEW","width":3840,"height":2160,"metadata":{"generation":{"height":100}}},{`},
		{`"id":"user"`, `"id":"foreign"`},
		{`"parent":"image"`, `"parent":"old"`},
		{`"conversation_id":"TEST_ONLY_CONVERSATION"`, `"conversation_id":"FOREIGN"`},
		{`"model_slug":"gpt-5-6-thinking"`, `"is_error":true,"model_slug":"gpt-5-6-thinking"`},
		{`"end_turn":true`, `"end_turn":true,"content":{"content_type":"system_error"}`},
	} {
		t.Run(change.after, func(t *testing.T) {
			raw := strings.ReplaceAll(base, change.before, change.after)
			result, _ := InspectChatGPTConversationDelivery([]byte(raw), "TEST_ONLY_CONVERSATION", ChatGPTMessageIDHash("user"))
			require.False(t, result.Completed)
		})
	}
}
