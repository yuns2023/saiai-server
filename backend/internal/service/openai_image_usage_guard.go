package service

import (
	"bytes"
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// openAIImageMissingUsageOutputTokensPerImage is the conservative output-token
// charge per returned image when a successful Image API response carries no
// usage. It is the largest documented gpt-image-1 output-token count (high
// quality, 1024x1536 or 1536x1024). Billing nothing would let a malformed or
// unexpected upstream response (for example SSE when JSON was expected) serve
// images for free; the fallback is logged so operators can review it.
const openAIImageMissingUsageOutputTokensPerImage = 6240

// resolveOpenAIImageResponseUsage extracts usage and the image count from a
// successful direct Image API response. It accepts the expected JSON body and
// an SSE body (usage from the last event carrying it; completed events that
// carry image data count as images). Only entries that actually carry image
// data (b64_json or url) are counted, so a 2xx response without images is
// never charged by the fallback.
func resolveOpenAIImageResponseUsage(ctx context.Context, account *Account, model, requestID, contentType string, body []byte) (openAIImageUsage, int) {
	var usage openAIImageUsage
	imageCount := 0
	sse := isOpenAIImageSSEResponse(contentType, body)
	if sse {
		usage, imageCount = parseOpenAIImageSSEUsage(body)
	} else {
		usage = parseOpenAIImageUsage(body)
		imageCount = countOpenAIImageDataEntries(gjson.GetBytes(body, "data"))
	}
	usage = applyOpenAIImageMissingUsageFallback(ctx, account, model, requestID, usage, imageCount, sse)
	return usage, imageCount
}

// applyOpenAIImageMissingUsageFallback fails closed when images were returned
// but no image output usage was reported: input usage alone does not cover
// the output charge, so the output tokens are filled in conservatively.
func applyOpenAIImageMissingUsageFallback(ctx context.Context, account *Account, model, requestID string, usage openAIImageUsage, imageCount int, sse bool) openAIImageUsage {
	if imageCount <= 0 || usage.ImageOutputTokens > 0 {
		return usage
	}
	usage.ImageOutputTokens = imageCount * openAIImageMissingUsageOutputTokensPerImage
	fields := []zap.Field{
		zap.String("model", unpricedModelLogValue(model)),
		zap.String("upstream_request_id", requestID),
		zap.Bool("sse_response", sse),
		zap.Int("image_count", imageCount),
		zap.Int("reported_input_tokens", usage.totalInputTokens()),
		zap.Int("fallback_output_tokens", usage.ImageOutputTokens),
	}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
	}
	logger.FromContext(ctx).Warn("openai image response has no output usage; billing conservative fallback", fields...)
	return usage
}

// isOpenAIImageSSEResponse decides by the body first: a valid JSON document is
// parsed as JSON whatever the Content-Type claims.
func isOpenAIImageSSEResponse(contentType string, body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	if gjson.ValidBytes(trimmed) {
		return false
	}
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return true
	}
	return bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte("data:"))
}

func openAIImageEntryHasData(entry gjson.Result) bool {
	return strings.TrimSpace(entry.Get("b64_json").String()) != "" || strings.TrimSpace(entry.Get("url").String()) != ""
}

func countOpenAIImageDataEntries(data gjson.Result) int {
	count := 0
	for _, entry := range data.Array() {
		if openAIImageEntryHasData(entry) {
			count++
		}
	}
	return count
}

// parseOpenAIImageSSEUsage joins multi-line data fields per event, uses the
// last event that carries usage, and counts image-bearing completed events
// (or a final image "data" array) as returned images.
func parseOpenAIImageSSEUsage(body []byte) (openAIImageUsage, int) {
	var usage openAIImageUsage
	completed := 0
	dataImages := 0
	forEachOpenAIImageSSEPayload(body, func(payload []byte) {
		if !gjson.ValidBytes(payload) {
			return
		}
		event := gjson.ParseBytes(payload)
		if event.Get("usage").IsObject() {
			usage = parseOpenAIImageUsage(payload)
		}
		if strings.HasSuffix(event.Get("type").String(), ".completed") && openAIImageEntryHasData(event) {
			completed++
		}
		if images := countOpenAIImageDataEntries(event.Get("data")); images > 0 {
			dataImages = images
		}
	})
	if completed > 0 {
		return usage, completed
	}
	return usage, dataImages
}
