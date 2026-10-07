package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatGPTImagesCountCompletedToolAssetsAcrossChunkedSnapshots(t *testing.T) {
	observer := NewChatGPTConversationStreamObserver()
	message := `{"message":{"id":"tool-message","author":{"role":"tool","name":"image_gen.text2im"},"status":"finished_successfully","content":{"content_type":"multimodal_text","parts":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://PRIVATE_ASSET_ONE","height":1024,"metadata":{"generation":{"height":1024},"dalle":{"prompt":"PRIVATE_PROMPT"}}},{"content_type":"image_asset_pointer","image_asset_pointer":{"asset_pointer":"file-service://PRIVATE_ASSET_TWO","height":1024,"metadata":{"generation":{"height":1024}}}}]}}}`
	stream := "data: " + message + "\n\ndata: " + message + "\n\ndata: {\"type\":\"message_stream_complete\",\"conversation_id\":\"fixture\"}\n\n"
	for _, b := range []byte(stream) {
		require.NoError(t, observer.Observe([]byte{b}))
	}
	summary, err := observer.Finish()
	require.NoError(t, err)
	require.True(t, summary.ImageGenerationSeen)
	require.Equal(t, 2, summary.ImageCount)
	require.True(t, summary.CompletionSeen)
	evidence := observer.ImageEvidence()
	require.Len(t, evidence.AssetHashes, 2)
	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "PRIVATE")
	require.NotContains(t, string(observer.eventData[:cap(observer.eventData)]), "PRIVATE")
	require.NotContains(t, string(observer.lineBuffer[:cap(observer.lineBuffer)]), "PRIVATE")
}

func TestChatGPTImagesIgnoreUploadsPreviewsErrorsAndUnrelatedText(t *testing.T) {
	for _, tt := range []struct {
		name, event string
		seen        bool
	}{
		{"upload", `{"message":{"author":{"role":"user"},"content":{"content_type":"multimodal_text","parts":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://upload","metadata":{"generation":{"height":1024}}}]}}}`, false},
		{"unrelated-tool", `{"message":{"author":{"role":"tool","name":"browser"},"content":{"content_type":"multimodal_text","parts":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://screenshot"}]}}}`, false},
		{"preview", `{"message":{"author":{"role":"tool","name":"dalle.text2im"},"content":{"content_type":"multimodal_text","parts":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://preview","height":1024,"metadata":{"generation":{"height":256}}}]}}}`, true},
		{"error", `{"message":{"author":{"role":"tool","name":"image_gen"},"metadata":{"is_error":true},"content":{"content_type":"multimodal_text","parts":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://failed"}]}}}`, true},
		{"prose", `{"message":{"author":{"role":"assistant"},"content":{"content_type":"text","parts":["example sediment://file; image_generation_call"]}}}`, false},
		{"async", `{"message":{"author":{"role":"tool","name":"image_gen"},"metadata":{"image_gen_async":{"task_id":"PRIVATE_TASK"}},"content":{"content_type":"multimodal_text","parts":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://pending"}]}}}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := NewChatGPTConversationStreamObserver()
			require.NoError(t, o.Observe([]byte("data: "+tt.event+"\n\n")))
			summary, err := o.Finish()
			require.NoError(t, err)
			require.Equal(t, tt.seen, summary.ImageGenerationSeen)
			require.Zero(t, summary.ImageCount)
		})
	}
}

func TestChatGPTImagesExplicitDeltaPartsAndStructuredCalls(t *testing.T) {
	o := NewChatGPTConversationStreamObserver()
	stream := `data: {"v":{"message":{"id":"image-tool","status":"finished_successfully","author":{"role":"tool","name":"image_gen"}}}}

data: {"o":"patch","v":[{"p":"/message/content/parts","o":"append","v":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://one"}]}]}

data: {"message":{"id":"assistant","author":{"role":"assistant"},"content":{"content_type":"text","parts":["[{\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"sediment://one\"},{\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"file-service://two\"},{\"type\":\"image_generation_call\",\"status\":\"in_progress\",\"result\":\"sediment://three\"}]"]}}}

`
	require.NoError(t, o.Observe([]byte(stream)))
	summary, err := o.Finish()
	require.NoError(t, err)
	require.True(t, summary.ImageGenerationSeen)
	require.Equal(t, 2, summary.ImageCount)
}

func TestChatGPTImageEvidenceIsBoundedAndDeduplicated(t *testing.T) {
	evidence := ChatGPTImageEvidence{AssetHashes: []string{"PRIVATE_ASSET", strings.Repeat("z", 64)}}
	for i := 0; i < MaxChatGPTObservedImages+20; i++ {
		evidence.AssetHashes = append(evidence.AssetHashes, chatGPTImageDigest(fmt.Sprint(i)))
	}
	merged := MergeChatGPTImageEvidence(evidence, evidence)
	require.True(t, merged.GenerationSeen)
	require.Len(t, merged.AssetHashes, MaxChatGPTObservedImages)
	require.NotContains(t, merged.AssetHashes, "PRIVATE_ASSET")
}

func TestChatGPTImagesFailedMessageCannotRegainAssetsThroughDelta(t *testing.T) {
	o := NewChatGPTConversationStreamObserver()
	stream := `data: {"p":"/message","o":"replace","v":{"id":"failed-image-tool","status":"finished_successfully","author":{"role":"tool","name":"image_gen"},"content":{"content_type":"multimodal_text","parts":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://one"}]}}}

data: {"message":{"id":"failed-image-tool","status":"finished_successfully","author":{"role":"tool","name":"image_gen"},"metadata":{"is_error":true}}}

data: {"p":"/message/content/parts","o":"append","v":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://one"}]}

`
	require.NoError(t, o.Observe([]byte(stream)))
	summary, err := o.Finish()
	require.NoError(t, err)
	require.True(t, summary.ImageGenerationSeen)
	require.Zero(t, summary.ImageCount)
}
