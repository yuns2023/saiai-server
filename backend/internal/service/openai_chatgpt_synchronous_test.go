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
	image := strings.Replace(chatGPTCompletedSnapshotFixture, `"async_status":4,`, "", 1)
	result, err := InspectChatGPTConversationDelivery([]byte(image), "TEST_ONLY_CONVERSATION", ChatGPTMessageIDHash("user"))
	require.NoError(t, err)
	require.False(t, result.Completed, "a missing async status must not settle an image branch")
}
