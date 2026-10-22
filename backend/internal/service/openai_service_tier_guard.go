package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
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

// IsOpenAIRequestServiceTierSupported reports whether every service_tier in
// an OpenAI Responses request body (HTTP body or WebSocket response.create
// payload) can be billed. Absent, null and empty values are allowed. A
// duplicated service_tier key is rejected because JSON parsers disagree on
// which duplicate wins, so local billing and the upstream could read different
// tiers. A nested response.service_tier is checked as well so a wrapped
// WebSocket payload cannot carry an unbillable tier upstream.
func IsOpenAIRequestServiceTierSupported(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		// Malformed payloads are rejected by the existing JSON validation.
		return true
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return true
	}
	supported := openAIServiceTierObjectSupported(root)
	root.ForEach(func(key, value gjson.Result) bool {
		if supported && key.String() == "response" && value.IsObject() {
			supported = openAIServiceTierObjectSupported(value)
		}
		return supported
	})
	return supported
}

// openAIServiceTierObjectSupported checks every service_tier key of one JSON
// object and rejects duplicates.
func openAIServiceTierObjectSupported(object gjson.Result) bool {
	supported := true
	occurrences := 0
	object.ForEach(func(key, value gjson.Result) bool {
		if key.String() != "service_tier" {
			return true
		}
		occurrences++
		supported = occurrences == 1 && isOpenAIRequestServiceTierValueSupported(value)
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

// openAIServiceTierGuardFrameConn validates every client frame of a v2
// passthrough session before the relay can forward it. The relay only invokes
// per-turn hooks for frames it classifies as response.create (by the first
// "type" key), so ambiguous or non-turn frames are checked here.
type openAIServiceTierGuardFrameConn struct {
	openaiwsv2.FrameConn
}

func (c *openAIServiceTierGuardFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	msgType, payload, err := c.FrameConn.ReadFrame(ctx)
	if err == nil {
		if rejection := OpenAIWSPayloadRejection(payload); rejection != nil {
			return msgType, nil, rejection
		}
	}
	return msgType, payload, err
}

// openAIAmbiguousRequestKeys are request fields the Gateway reads for
// routing, turn accounting or billing. Duplicates are ambiguous: gjson (used
// by the handlers, the v1 WS parser and the v2 relay) returns the FIRST
// occurrence, sjson rewrites only the first, while encoding/json (used for the
// HTTP request map) and many upstream parsers keep the LAST. A frame such as
// {"type":"response.cancel","type":"response.create",...} could otherwise be
// relayed as a non-billable control frame yet executed as a new response.
var openAIAmbiguousRequestKeys = []string{"type", "model", "service_tier", "previous_response_id", "response", "stream"}

// OpenAIRequestDuplicateKey returns the first billing/routing key that occurs
// more than once in the top-level object, or in a top-level "response"
// object, of an OpenAI Responses request body or WebSocket frame. It returns
// "" when the payload is unambiguous or is not a JSON object.
func OpenAIRequestDuplicateKey(body []byte) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return ""
	}
	duplicate := openAIObjectDuplicateKey(root)
	if duplicate != "" {
		return duplicate
	}
	root.ForEach(func(key, value gjson.Result) bool {
		if key.String() == "response" && value.IsObject() {
			if nested := openAIObjectDuplicateKey(value); nested != "" {
				duplicate = "response." + nested
			}
		}
		return duplicate == ""
	})
	return duplicate
}

func openAIObjectDuplicateKey(object gjson.Result) string {
	seen := make(map[string]struct{}, len(openAIAmbiguousRequestKeys))
	duplicate := ""
	object.ForEach(func(key, _ gjson.Result) bool {
		name := key.String()
		for _, watched := range openAIAmbiguousRequestKeys {
			if name != watched {
				continue
			}
			if _, ok := seen[name]; ok {
				duplicate = name
				return false
			}
			seen[name] = struct{}{}
		}
		return true
	})
	return duplicate
}

// OpenAIDuplicateKeyMessage is the client-facing rejection for an ambiguous
// payload. It fits the 123-byte WebSocket close-reason limit.
func OpenAIDuplicateKeyMessage(key string) string {
	return fmt.Sprintf("Ambiguous request: duplicate %q key.", key)
}

// OpenAIWSPayloadRejection returns the policy-violation close error for a
// WebSocket payload that is ambiguous or carries an unbillable service_tier.
func OpenAIWSPayloadRejection(payload []byte) error {
	if key := OpenAIRequestDuplicateKey(payload); key != "" {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, OpenAIDuplicateKeyMessage(key), nil)
	}
	if !IsOpenAIRequestServiceTierSupported(payload) {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, OpenAIUnsupportedServiceTierMessage, nil)
	}
	return nil
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
