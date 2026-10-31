package middleware

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/tidwall/gjson"
)

func smartSwitchRequest(c *gin.Context, key *service.APIKey) bool {
	if key.SmartSwitch == nil || !key.SmartSwitch.Enabled || c.Request.Method != http.MethodPost {
		return false
	}
	// Responses subpaths such as /responses/compact share the Responses handler
	// and billing, so they must not fall back to the Key's bound group.
	path := c.Request.URL.Path
	return strings.HasSuffix(path, "/messages") || strings.HasSuffix(path, "/responses") || strings.Contains(path, "/responses/")
}

// smartSwitchDecodedBodyLimit mirrors the OpenAI handler's decoded request-body
// cap (server.max_request_body_size, then gateway.max_body_size), so a
// compressed body is not rejected only because smart switching is enabled.
func smartSwitchDecodedBodyLimit(cfg *config.Config) int64 {
	if cfg.Server.MaxRequestBodySize > 0 {
		return cfg.Server.MaxRequestBodySize
	}
	if cfg.Gateway.MaxBodySize > 0 {
		return cfg.Gateway.MaxBodySize
	}
	return 256 << 20
}

func applySmartSwitch(c *gin.Context, key *service.APIKey, keys *service.APIKeyService, subscriptions *service.SubscriptionService, cfg *config.Config) (*service.APIKey, bool) {
	if key.IsExpired() || key.Status == service.StatusAPIKeyExpired {
		AbortWithError(c, 403, "API_KEY_EXPIRED", "API key has expired")
		return nil, false
	}
	if key.IsQuotaExhausted() || key.Status == service.StatusAPIKeyQuotaExhausted {
		abortAPIKeyQuotaExhausted(c)
		return nil, false
	}
	limit := cfg.Gateway.MaxBodySize
	if limit <= 0 {
		limit = 64 << 20
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, limit+1))
	if err != nil {
		AbortWithError(c, 400, "INVALID_REQUEST", "Failed to read request body")
		return nil, false
	}
	if int64(len(body)) > limit {
		AbortWithError(c, 413, "REQUEST_TOO_LARGE", "Request body too large")
		return nil, false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	// Inspect a bounded decoded copy while preserving the original wire body.
	encoding := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Encoding")))
	if encoding == "zstd" {
		decodedLimit := smartSwitchDecodedBodyLimit(cfg)
		reader, decodeErr := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(uint64(decodedLimit)))
		if decodeErr != nil {
			AbortWithError(c, 400, "INVALID_REQUEST", "Invalid compressed body")
			return nil, false
		}
		body, decodeErr = io.ReadAll(io.LimitReader(reader, decodedLimit+1))
		reader.Close()
		if decodeErr != nil || int64(len(body)) > decodedLimit {
			AbortWithError(c, 400, "INVALID_REQUEST", "Invalid or oversized compressed body")
			return nil, false
		}
	} else if encoding != "" && encoding != "identity" {
		AbortWithError(c, 415, "INVALID_REQUEST", "Unsupported content encoding")
		return nil, false
	}
	if !gjson.ValidBytes(body) {
		AbortWithError(c, 400, "INVALID_REQUEST", "Invalid JSON body")
		return nil, false
	}
	model := gjson.GetBytes(body, "model")
	if model.Type != gjson.String || strings.TrimSpace(model.String()) == "" {
		AbortWithError(c, 400, "INVALID_REQUEST", "model is required")
		return nil, false
	}
	// Billing for background jobs cannot be attributed to a completed HTTP request.
	if gjson.GetBytes(body, "background").Bool() {
		AbortWithError(c, 400, "SMART_SWITCH_BACKGROUND_UNSUPPORTED", "Smart switching does not support background requests")
		return nil, false
	}
	// Stateful continuation needs a persistent routing binding before it can switch.
	if gjson.GetBytes(body, "previous_response_id").String() != "" {
		AbortWithError(c, 400, "SMART_SWITCH_CONTINUATION_UNSUPPORTED", "Smart switching requires an independent request with complete context")
		return nil, false
	}
	selection, err := keys.SelectSmartSwitchGroup(c.Request.Context(), key, gjson.GetBytes(body, "model").String(), nil, subscriptions)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrAPIKeyQuotaExhausted):
			abortAPIKeyQuotaExhausted(c)
			return nil, false
		case errors.Is(err, service.ErrAPIKeyExpired):
			AbortWithError(c, 403, "API_KEY_EXPIRED", "API key has expired")
			return nil, false
		case errors.Is(err, service.ErrAPIKeyRateLimit5hExceeded), errors.Is(err, service.ErrAPIKeyRateLimit1dExceeded), errors.Is(err, service.ErrAPIKeyRateLimit7dExceeded):
			AbortWithError(c, 429, "API_KEY_RATE_LIMIT_EXCEEDED", "API key usage limit exceeded")
			return nil, false
		}
		var exhausted *service.SmartSwitchExhaustedError
		if errors.As(err, &exhausted) {
			abortSmartSwitchExhausted(c, exhausted)
		} else if errors.Is(err, service.ErrSmartSwitchUnavailable) {
			AbortWithError(c, 503, "SMART_SWITCH_UNAVAILABLE", "No eligible group in the smart switching policy")
		} else {
			AbortWithError(c, 503, "SMART_SWITCH_CHECK_FAILED", "Unable to validate smart switching eligibility")
		}
		return nil, false
	}
	c.Set("smart_switch_next", SmartSwitchNext(func(excluded map[int64]bool) (*service.SmartSwitchSelection, error) {
		return keys.SelectSmartSwitchGroup(c.Request.Context(), key, gjson.GetBytes(body, "model").String(), excluded, subscriptions)
	}))
	c.Request = c.Request.WithContext(service.WithSmartSwitchContext(c.Request.Context()))
	SetSmartSwitchSelection(c, selection)
	return selection.Key, true
}

// abortSmartSwitchExhausted uses the non-switching path's business codes when
// every group was skipped for balance, budget or subscription, so Ops keeps
// them out of the SLA and the user sees what to fix. The skip codes are stable
// and contain no upstream data.
const smartSwitchLimitedMessage = "Every group in the smart switching policy is blocked by a usage limit, the Key budget or the subscription status"

func abortSmartSwitchExhausted(c *gin.Context, exhausted *service.SmartSwitchExhaustedError) {
	reasons := " (" + strings.Join(exhausted.UniqueReasons(), ", ") + ")"
	switch {
	case !exhausted.BusinessLimited():
		AbortWithError(c, 503, "SMART_SWITCH_UNAVAILABLE", "No eligible group in the smart switching policy"+reasons)
	case exhausted.Has("insufficient_balance"):
		// Topping up is the action that can unblock a pay-as-you-go group.
		AbortWithError(c, 403, "INSUFFICIENT_BALANCE", "Insufficient account balance; no other group in the smart switching policy is available"+reasons)
	case exhausted.Has("subscription_limit_or_expiry"):
		// Matches the non-switching subscription limit. This legacy reason also
		// covers an expired or suspended subscription.
		AbortWithError(c, 429, "USAGE_LIMIT_EXCEEDED", smartSwitchLimitedMessage+reasons)
	default:
		// The Key budget or a missing/expired subscription does not recover by retrying.
		AbortWithError(c, 403, "USAGE_LIMIT_EXCEEDED", smartSwitchLimitedMessage+reasons)
	}
}

type SmartSwitchNext func(map[int64]bool) (*service.SmartSwitchSelection, error)

func SetSmartSwitchSelection(c *gin.Context, selection *service.SmartSwitchSelection) {
	c.Set(string(ContextKeyAPIKey), selection.Key)
	c.Set("smart_switch_reason", selection.Key.SmartSwitchReason)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, selection.Key.Group))
	if selection.Subscription != nil {
		c.Set(string(ContextKeySubscription), selection.Subscription)
	} else {
		delete(c.Keys, string(ContextKeySubscription))
	}
}
