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
// an SSE body (event payloads carrying usage, "*.completed" events counted as
// images). When images were returned but no usage could be parsed it fails
// closed with a conservative output-token charge instead of billing zero.
func resolveOpenAIImageResponseUsage(ctx context.Context, account *Account, model, requestID, contentType string, body []byte) (openAIImageUsage, int) {
	var usage openAIImageUsage
	imageCount := 0
	sse := isOpenAIImageSSEResponse(contentType, body)
	if sse {
		usage, imageCount = parseOpenAIImageSSEUsage(body)
	} else {
		usage = parseOpenAIImageUsage(body)
		imageCount = len(gjson.GetBytes(body, "data").Array())
	}
	if usage.totalInputTokens() > 0 || usage.ImageOutputTokens > 0 || imageCount == 0 {
		return usage, imageCount
	}
	usage.ImageOutputTokens = imageCount * openAIImageMissingUsageOutputTokensPerImage
	fields := []zap.Field{
		zap.String("model", unpricedModelLogValue(model)),
		zap.String("upstream_request_id", requestID),
		zap.Bool("sse_response", sse),
		zap.Int("image_count", imageCount),
		zap.Int("fallback_output_tokens", usage.ImageOutputTokens),
	}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
	}
	logger.FromContext(ctx).Warn("openai image response has no usage; billing conservative fallback", fields...)
	return usage, imageCount
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

// parseOpenAIImageSSEUsage uses the last event that carries usage, and counts
// completed-image events (or a final "data" array) as returned images.
func parseOpenAIImageSSEUsage(body []byte) (openAIImageUsage, int) {
	var usage openAIImageUsage
	completed := 0
	dataImages := 0
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || !gjson.ValidBytes(payload) {
			continue
		}
		if gjson.GetBytes(payload, "usage").IsObject() {
			usage = parseOpenAIImageUsage(payload)
		}
		if strings.HasSuffix(gjson.GetBytes(payload, "type").String(), ".completed") {
			completed++
		}
		if images := len(gjson.GetBytes(payload, "data").Array()); images > 0 {
			dataImages = images
		}
	}
	if completed > 0 {
		return usage, completed
	}
	return usage, dataImages
}
