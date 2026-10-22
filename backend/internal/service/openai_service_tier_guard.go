package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// OpenAI service-tier admission and reported-tier fallback.
//
// The Gateway forwards the client's service_tier unchanged and bills by the
// more expensive of the requested and upstream-reported tiers. The billing
// resolver only knows default, flex and priority (fast is the SAIAI alias of
// priority). Any other request value used to normalize to "no tier" and was
// billed as Standard while the upstream could still honor it (for example a
// premium "ultrafast" tier). Such requests are therefore rejected before an
// upstream is contacted, and an unrecognized upstream report is billed
// conservatively at the highest tier the Gateway can price.

// OpenAIUnsupportedServiceTierMessage is returned to clients whose request
// carries a service_tier value the Gateway cannot bill. It must stay within the
// 123-byte WebSocket close-reason limit.
const OpenAIUnsupportedServiceTierMessage = "Unsupported service_tier. Supported values: auto, default, flex, priority, fast."

// OpenAIServiceTierParam is the request parameter named in rejection errors.
const OpenAIServiceTierParam = "service_tier"

// openAIUnrecognizedReportedServiceTierBilledAs is the tier used to bill an
// upstream-reported tier the Gateway does not recognize. Priority is the most
// expensive tier with configured prices; billing it below the tier the
// upstream actually used would be an unbounded under-charge, whereas the
// over-charge is bounded and the warning log makes it visible to operators.
const openAIUnrecognizedReportedServiceTierBilledAs = "priority"

const openAIServiceTierLogMaxLen = 64

// IsOpenAIRequestServiceTierSupported reports whether every service_tier in
// an OpenAI Responses request body (HTTP body or WebSocket response.create
// payload) can be billed. Absent, null and empty values are allowed. Every
// top-level occurrence is checked because JSON parsers disagree on which
// duplicate key wins, and a nested response.service_tier is checked as well
// so a wrapped WebSocket payload cannot carry an unbillable tier upstream.
func IsOpenAIRequestServiceTierSupported(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		// Malformed payloads are rejected by the existing JSON validation.
		return true
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return true
	}
	supported := true
	root.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "service_tier":
			supported = isOpenAIRequestServiceTierValueSupported(value)
		case "response":
			if value.IsObject() {
				value.ForEach(func(nestedKey, nestedValue gjson.Result) bool {
					if nestedKey.String() == "service_tier" {
						supported = isOpenAIRequestServiceTierValueSupported(nestedValue)
					}
					return supported
				})
			}
		}
		return supported
	})
	return supported
}

func isOpenAIRequestServiceTierValueSupported(value gjson.Result) bool {
	switch value.Type {
	case gjson.Null:
		return true
	case gjson.String:
		switch strings.ToLower(strings.TrimSpace(value.String())) {
		case "", "auto", "default", "flex", "priority", "fast":
			return true
		}
	}
	return false
}

// unrecognizedOpenAIReportedServiceTier returns the trimmed raw value when an
// upstream reports a tier the billing resolver does not know, or "".
func unrecognizedOpenAIReportedServiceTier(raw string) string {
	value := strings.TrimSpace(raw)
	switch strings.ToLower(value) {
	case "", "auto", "default", "flex", "priority", "fast":
		return ""
	}
	return value
}

func unrecognizedOpenAIReportedServiceTierFromBody(body []byte) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	value := gjson.GetBytes(body, "service_tier")
	if !value.Exists() {
		value = gjson.GetBytes(body, "response.service_tier")
	}
	return unrecognizedOpenAIReportedServiceTier(value.String())
}

func logUnrecognizedOpenAIReportedServiceTier(ctx context.Context, result *OpenAIForwardResult, account *Account, billingModel, billedTier string) {
	if result == nil || result.Usage.UnrecognizedReportedServiceTier == "" {
		return
	}
	fields := []zap.Field{
		zap.String("reported_service_tier", truncateOpenAIWSLogValue(result.Usage.UnrecognizedReportedServiceTier, openAIServiceTierLogMaxLen)),
		zap.String("billed_service_tier", billedTier),
		zap.String("model", unpricedModelLogValue(billingModel)),
		zap.String("upstream_request_id", result.RequestID),
		zap.Bool("openai_ws_mode", result.OpenAIWSMode),
	}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
	}
	logger.FromContext(ctx).Warn("openai upstream reported an unrecognized service tier; billing it as priority", fields...)
}
