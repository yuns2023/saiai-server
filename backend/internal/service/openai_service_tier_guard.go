package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	coderws "github.com/coder/websocket"
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

// openAIAmbiguousRequestKeys are request fields the Gateway reads for
// routing, turn accounting or billing. Duplicates are ambiguous: gjson (used
// by the handlers, the v1 WS parser and the v2 relay) returns the FIRST
// occurrence, sjson rewrites only the first, while encoding/json (used for the
// HTTP request map) and many upstream parsers keep the LAST. A frame such as
// {"type":"response.cancel","type":"response.create",...} could otherwise be
// relayed as a non-billable control frame yet executed as a new response.
var openAIAmbiguousRequestKeys = [...]string{"type", "model", "service_tier", "previous_response_id", "response", "stream"}

// OpenAIRequestPayloadCheck is the result of a single scan of an OpenAI
// Responses request body or WebSocket frame.
type OpenAIRequestPayloadCheck struct {
	// DuplicateKey is the first duplicated routing/billing key, prefixed with
	// "response." when it is inside a top-level response object.
	DuplicateKey string
	// UnsupportedServiceTier is set when any service_tier (top level or in a
	// top-level response object) is not billable, or is duplicated.
	UnsupportedServiceTier bool
}

// CheckOpenAIRequestPayload scans the top-level object, and a top-level
// "response" object, once. Absent, null and empty service_tier values are
// billable. Malformed or non-object payloads report no issue; the existing
// JSON validation of each path rejects them.
func CheckOpenAIRequestPayload(body []byte) OpenAIRequestPayloadCheck {
	var check OpenAIRequestPayloadCheck
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return check
	}
	root := gjson.ParseBytes(body)
	if root.IsObject() {
		scanOpenAIRequestObject(root, "", &check)
	}
	return check
}

func scanOpenAIRequestObject(object gjson.Result, prefix string, check *OpenAIRequestPayloadCheck) {
	var seen [len(openAIAmbiguousRequestKeys)]bool
	object.ForEach(func(key, value gjson.Result) bool {
		name := key.String()
		duplicate := false
		for index, watched := range openAIAmbiguousRequestKeys {
			if name == watched {
				duplicate = seen[index]
				seen[index] = true
				break
			}
		}
		if duplicate && check.DuplicateKey == "" {
			check.DuplicateKey = prefix + name
		}
		switch name {
		case "service_tier":
			if duplicate || !isOpenAIRequestServiceTierValueSupported(value) {
				check.UnsupportedServiceTier = true
			}
		case "response":
			if prefix == "" && value.IsObject() {
				scanOpenAIRequestObject(value, "response.", check)
			}
		}
		return true
	})
}

// IsOpenAIRequestServiceTierSupported reports whether every service_tier in
// the payload can be billed and none is duplicated.
func IsOpenAIRequestServiceTierSupported(body []byte) bool {
	return !CheckOpenAIRequestPayload(body).UnsupportedServiceTier
}

// OpenAIRequestDuplicateKey returns the first duplicated routing/billing key,
// or "" when the payload is unambiguous.
func OpenAIRequestDuplicateKey(body []byte) string {
	return CheckOpenAIRequestPayload(body).DuplicateKey
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

// OpenAIDuplicateKeyMessage is the client-facing rejection for an ambiguous
// payload. It fits the 123-byte WebSocket close-reason limit.
func OpenAIDuplicateKeyMessage(key string) string {
	return fmt.Sprintf("Ambiguous request: duplicate %q key.", key)
}

// WSRejection returns the policy-violation close error for the payload, or nil.
func (check OpenAIRequestPayloadCheck) WSRejection() error {
	if check.DuplicateKey != "" {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, OpenAIDuplicateKeyMessage(check.DuplicateKey), nil)
	}
	if check.UnsupportedServiceTier {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, OpenAIUnsupportedServiceTierMessage, nil)
	}
	return nil
}

// OpenAIWSPayloadRejection returns the policy-violation close error for a
// WebSocket payload that is ambiguous or carries an unbillable service_tier.
// The v1 ingress parser and the v2 relay call it once for every client frame
// before the frame can be written upstream.
func OpenAIWSPayloadRejection(payload []byte) error {
	return CheckOpenAIRequestPayload(payload).WSRejection()
}

func validateOpenAIWSV2ClientFrame(_ coderws.MessageType, payload []byte) error {
	return OpenAIWSPayloadRejection(payload)
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
