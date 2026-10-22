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
// charge per returned image when a successful response for an official
// gpt-image model carries no image output usage. It is the largest documented
// gpt-image-1 output-token count (high quality, 1024x1536 or 1536x1024).
// Billing nothing would let a malformed or unexpected upstream response (for
// example SSE when JSON was expected) serve images for free.
const openAIImageMissingUsageOutputTokensPerImage = 6240

// openAIImageResponseUsage is the usage extracted from one image response.
type openAIImageResponseUsage struct {
	usage openAIImageUsage
	// images counts entries/events that actually carry image data.
	images int
	// imagesWithoutOutputUsage counts returned images not covered by any
	// reported image output usage.
	imagesWithoutOutputUsage int
	sse                      bool
}

// resolveOpenAIImageResponseUsage extracts usage and the image count from a
// successful direct or native Image API response and fails closed for missing
// image output usage; see applyOpenAIImageMissingUsageFloor.
func (s *OpenAIGatewayService) resolveOpenAIImageResponseUsage(ctx context.Context, account *Account, model, requestID, contentType string, body []byte) (openAIImageUsage, int) {
	var parsed openAIImageResponseUsage
	if isOpenAIImageSSEResponse(contentType, body) {
		parsed = parseOpenAIImageSSEUsage(body)
	} else {
		parsed.usage = parseOpenAIImageUsage(body)
		parsed.images = countOpenAIImageDataEntries(gjson.GetBytes(body, "data"))
		if parsed.usage.ImageOutputTokens == 0 {
			parsed.imagesWithoutOutputUsage = parsed.images
		}
	}
	return s.applyOpenAIImageMissingUsageFloor(ctx, account, model, requestID, parsed), parsed.images
}

// applyOpenAIImageMissingUsageFloor charges openAIImageMissingUsageOutputTokensPerImage
// for each returned image without output usage, but only when the billed
// model resolves to the official token-priced gpt-image family. Other models
// (custom or aliased compatible models) keep the previous behaviour of
// billing only the reported usage; a warning is always logged so a missing
// output charge is never silent.
func (s *OpenAIGatewayService) applyOpenAIImageMissingUsageFloor(ctx context.Context, account *Account, model, requestID string, parsed openAIImageResponseUsage) openAIImageUsage {
	usage := parsed.usage
	if parsed.imagesWithoutOutputUsage <= 0 {
		return usage
	}
	resolvedModel, floorApplies := s.openAIImageUsageFloorModel(model)
	fallbackTokens := 0
	if floorApplies {
		fallbackTokens = parsed.imagesWithoutOutputUsage * openAIImageMissingUsageOutputTokensPerImage
		usage.ImageOutputTokens += fallbackTokens
	}
	fields := []zap.Field{
		zap.String("model", unpricedModelLogValue(model)),
		zap.String("resolved_pricing_model", unpricedModelLogValue(resolvedModel)),
		zap.String("upstream_request_id", requestID),
		zap.Bool("sse_response", parsed.sse),
		zap.Int("image_count", parsed.images),
		zap.Int("images_without_output_usage", parsed.imagesWithoutOutputUsage),
		zap.Int("reported_output_tokens", parsed.usage.ImageOutputTokens),
		zap.Int("fallback_output_tokens", fallbackTokens),
	}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
	}
	message := "openai image response missing output usage; billing conservative gpt-image floor"
	if !floorApplies {
		message = "openai image response missing output usage; model is not an official gpt-image price, output not billed"
	}
	logger.FromContext(ctx).Warn(message, fields...)
	return usage
}

// openAIImageUsageFloorModel returns the model whose prices bill the request
// and whether it is the official token-priced gpt-image family.
func (s *OpenAIGatewayService) openAIImageUsageFloorModel(model string) (string, bool) {
	resolved := strings.ToLower(strings.TrimSpace(model))
	if s == nil || s.billingService == nil {
		return resolved, false
	}
	pricing, err := s.billingService.GetModelPricing(model)
	if err != nil || pricing == nil {
		return resolved, false
	}
	if pricing.Snapshot != nil && strings.TrimSpace(pricing.Snapshot.ResolvedModel) != "" {
		resolved = strings.ToLower(strings.TrimSpace(pricing.Snapshot.ResolvedModel))
	}
	if index := strings.LastIndex(resolved, "/"); index >= 0 {
		resolved = resolved[index+1:]
	}
	tokenPriced := pricing.InputImagePricePerToken > 0 && pricing.OutputImagePricePerToken > 0
	return resolved, tokenPriced && strings.HasPrefix(resolved, "gpt-image-")
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

func addOpenAIImageUsage(total *openAIImageUsage, add openAIImageUsage) {
	total.TextInputTokens += add.TextInputTokens
	total.ImageInputTokens += add.ImageInputTokens
	total.ImageOutputTokens += add.ImageOutputTokens
}

// parseOpenAIImageSSEUsage joins multi-line data fields per event and
// distinguishes two usage shapes:
//   - per-image: a "*.completed" event carrying image data (b64_json/url) and
//     its own usage. Per-image usages are summed; completed images without
//     output usage are counted as missing.
//   - aggregate: an event carrying usage but no single image (for example a
//     final summary event, or an event with a "data" array). It covers every
//     image, so it replaces only the fields it reports: input-only aggregate
//     usage keeps the per-image output usage, and an aggregate with output
//     usage covers all images.
//
// When an aggregate "data" array lists more images than per-image completed
// events, the uncovered images count as missing output unless an aggregate
// output covers them. A reported zero is treated as absent. Other shapes of
// non-standard upstreams (several conflicting aggregates, partial aggregates
// that cover only some images) are not modelled: the last aggregate wins.
func parseOpenAIImageSSEUsage(body []byte) openAIImageResponseUsage {
	parsed := openAIImageResponseUsage{sse: true}
	var perImage openAIImageUsage
	var aggregate openAIImageUsage
	completedImages := 0
	completedWithoutOutput := 0
	dataImages := 0
	forEachOpenAIImageSSEPayload(body, func(payload []byte) {
		if !gjson.ValidBytes(payload) {
			return
		}
		event := gjson.ParseBytes(payload)
		hasUsage := event.Get("usage").IsObject()
		if eventDataImages := countOpenAIImageDataEntries(event.Get("data")); eventDataImages > 0 {
			dataImages = eventDataImages
		}
		isImageEvent := strings.HasSuffix(event.Get("type").String(), ".completed") && openAIImageEntryHasData(event)
		switch {
		case isImageEvent:
			completedImages++
			var eventUsage openAIImageUsage
			if hasUsage {
				eventUsage = parseOpenAIImageUsage(payload)
				addOpenAIImageUsage(&perImage, eventUsage)
			}
			if eventUsage.ImageOutputTokens == 0 {
				completedWithoutOutput++
			}
		case hasUsage:
			aggregate = parseOpenAIImageUsage(payload)
		}
	})
	parsed.images = completedImages
	if dataImages > parsed.images {
		parsed.images = dataImages
	}
	parsed.usage = perImage
	if aggregate.totalInputTokens() > 0 {
		parsed.usage.TextInputTokens = aggregate.TextInputTokens
		parsed.usage.ImageInputTokens = aggregate.ImageInputTokens
	}
	if aggregate.ImageOutputTokens > 0 {
		parsed.usage.ImageOutputTokens = aggregate.ImageOutputTokens
		return parsed
	}
	parsed.imagesWithoutOutputUsage = completedWithoutOutput + (parsed.images - completedImages)
	return parsed
}
