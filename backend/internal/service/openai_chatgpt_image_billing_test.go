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

func TestChatGPTImagePricesUseObservedDimensionsAndUnknownRate(t *testing.T) {
	prices := map[string]float64{"1K": .02, "2K": .04, "4K": .08, "unknown": .015}
	images := ChatGPTImageEvidence{}
	for i, size := range []string{"1024x1024", "1536x1024", "2048x2048", "3840x2160", "", "auto", "8192x8192"} {
		images.AssetHashes = append(images.AssetHashes, chatGPTImageDigest(fmt.Sprint(i)))
		images.AssetSizes = append(images.AssetSizes, size)
	}
	billing, err := CalculateChatGPTImageBilling(images, prices)
	require.NoError(t, err)
	require.Equal(t, 7, billing.Count)
	require.Equal(t, "mixed", billing.Size)
	require.InDelta(t, .225, billing.CostUSD, 1e-12)
	free, err := CalculateChatGPTImageBilling(images, nil)
	require.NoError(t, err)
	require.Zero(t, free.CostUSD, "legacy settings preserve image-inclusive Chat pricing")
	for size, bracket := range map[string]string{"1024x1024": "1K", "1024x1536": "2K", "2048x2048": "2K", "3840x2160": "4K", "0x1024": "unknown", "auto": "unknown", "2K": "unknown"} {
		require.Equal(t, bracket, ChatGPTImageSizeClass(size), size)
	}
}

func TestChatGPTImageEvidenceDeduplicatesDimensionsAndTreatsConflictsAsUnknown(t *testing.T) {
	hash := chatGPTImageDigest("PRIVATE_ASSET")
	first := ChatGPTImageEvidence{AssetHashes: []string{hash}}
	known := ChatGPTImageEvidence{AssetHashes: []string{hash}, AssetSizes: []string{"2048x2048"}}
	merged := MergeChatGPTImageEvidence(first, known)
	require.Equal(t, []string{"2048x2048"}, merged.AssetSizes)
	merged = MergeChatGPTImageEvidence(merged, ChatGPTImageEvidence{AssetHashes: []string{hash}, AssetSizes: []string{"1024x1024"}})
	require.Equal(t, []string{"conflict"}, merged.AssetSizes)
	merged = MergeChatGPTImageEvidence(merged, known)
	price, err := CalculateChatGPTImageBilling(merged, map[string]float64{"1K": .02, "2K": .04, "4K": .08, "unknown": .01})
	require.NoError(t, err)
	require.Equal(t, 1, price.Count)
	require.Empty(t, price.Size)
	require.InDelta(t, .01, price.CostUSD, 1e-12)
}

func TestChatGPTImageBillingSnapshotFreezesPriceAndCompletionAcrossReplays(t *testing.T) {
	ctx := context.Background()
	cache := NewChatGPTMemoryTurnCache()
	prices := map[string]float64{"1K": .02, "2K": .04, "4K": .08, "unknown": .01}
	initial := &ChatGPTTurnSnapshot{Identity: ChatGPTTurnBillingIdentity{RequestID: "TEST_ONLY", PayloadHash: "TEST_ONLY"}, AccountID: 1,
		BasePriceUSD: .01, ImagePricesUSD: prices, StartedAt: time.Now()}
	_, err := cache.PutChatGPTTurnIfAbsent(ctx, "TEST_ONLY", initial, time.Minute)
	require.NoError(t, err)
	prices["2K"] = 99
	hash := chatGPTImageDigest("PRIVATE_ASSET")
	_, err = cache.MergeChatGPTTurnImages(ctx, "TEST_ONLY", ChatGPTImageEvidence{AssetHashes: []string{hash}, AssetSizes: []string{"2048x2048"}})
	require.NoError(t, err)
	sealed, err := cache.SealChatGPTTurnBilling(ctx, "TEST_ONLY")
	require.NoError(t, err)
	require.True(t, sealed.TerminalSeen)
	require.Equal(t, .04, sealed.ImageBilling.CostUSD)
	sealed.ImageBilling.CostUSD = 999
	sealed.ImagePricesUSD["2K"] = 999
	_, err = cache.MergeChatGPTTurnImages(ctx, "TEST_ONLY", ChatGPTImageEvidence{AssetHashes: []string{chatGPTImageDigest("LATER")}, AssetSizes: []string{"3840x2160"}})
	require.NoError(t, err)
	again, err := cache.SealChatGPTTurnBilling(ctx, "TEST_ONLY")
	require.NoError(t, err)
	require.Equal(t, .04, again.ImageBilling.CostUSD)
	require.Equal(t, 1, again.ImageBilling.Count)
	require.Equal(t, "2048x2048", again.ImageBilling.Size)
}

func TestChatGPTImageDimensionSnapshotRemainsBoundedAndContentFree(t *testing.T) {
	images := ChatGPTImageEvidence{}
	for i := 0; i < MaxChatGPTObservedImages; i++ {
		images.AssetHashes = append(images.AssetHashes, chatGPTImageDigest(fmt.Sprintf("PRIVATE_ASSET_%d", i)))
		images.AssetSizes = append(images.AssetSizes, "32768x32768")
	}
	initial := ChatGPTTurnSnapshot{Identity: ChatGPTTurnBillingIdentity{RequestID: strings.Repeat("x", 100), PayloadHash: strings.Repeat("a", 64)},
		AccountID: 1, BasePriceUSD: .05, ImagePricesUSD: map[string]float64{"1K": .02, "2K": .04, "4K": .08, "unknown": .02}, StartedAt: time.Now(),
		RequestedModel: strings.Repeat("m", 100), ThinkingEffort: strings.Repeat("e", 32), Images: MergeChatGPTImageEvidence(images, images)}
	initial.ImageBilling, _ = CalculateChatGPTImageBilling(initial.Images, initial.ImagePricesUSD)
	encoded, err := json.Marshal(initial)
	require.NoError(t, err)
	require.Less(t, len(encoded), 8192)
	require.NotContains(t, string(encoded), "PRIVATE_ASSET")
}

func TestChatGPTCompletedToolImagesExposeOnlyActualDimensions(t *testing.T) {
	observer := NewChatGPTConversationStreamObserver()
	stream := `data: {"message":{"id":"tool","author":{"role":"tool","name":"image_gen"},"status":"finished_successfully","content":{"content_type":"multimodal_text","parts":[{"content_type":"image_asset_pointer","asset_pointer":"sediment://TEST_ONLY_ONE","width":2048,"height":2048},{"content_type":"image_asset_pointer","asset_pointer":"sediment://TEST_ONLY_TWO","height":2048}]}}}

data: {"type":"message_stream_complete","conversation_id":"TEST_ONLY"}

`
	require.NoError(t, observer.Observe([]byte(stream)))
	_, err := observer.Finish()
	require.NoError(t, err)
	billing, err := CalculateChatGPTImageBilling(observer.ImageEvidence(), map[string]float64{"1K": .02, "2K": .04, "4K": .08, "unknown": .01})
	require.NoError(t, err)
	require.Equal(t, 2, billing.Count)
	require.InDelta(t, .05, billing.CostUSD, 1e-12, "height alone must not imply 2K output")
}
