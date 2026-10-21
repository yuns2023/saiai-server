package service

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexUpstreamCapacityCode = "saiai_upstream_capacity_unavailable"
const codexUpstreamCapacityMessage = "SAIAI upstream capacity is temporarily unavailable. Please retry later or contact your administrator. This is not your SAIAI balance or quota."

func writeOpenAICodexCapacityError(ctx *gin.Context, account *Account, body []byte) bool {
	normalized := normalizeOpenAICodexCapacityError(account, body)
	if bytes.Equal(normalized, body) {
		return false
	}
	ctx.Writer.Header().Del("Content-Length")
	ctx.Data(http.StatusServiceUnavailable, "application/json", normalized)
	return true
}

func normalizeOpenAICodexCapacityError(account *Account, payload []byte) []byte {
	if !hideOpenAICodexQuota(account) || !gjson.ValidBytes(payload) {
		return payload
	}
	kind := gjson.GetBytes(payload, "type").String()
	path := "error"
	switch kind {
	case "response.failed":
		path = "response.error"
	case "response.done":
		if gjson.GetBytes(payload, "response.status").String() != "failed" {
			return payload
		}
		path = "response.error"
	case "", "error":
	default:
		return payload
	}
	errorObject := gjson.GetBytes(payload, path)
	quotaError := false
	for _, field := range []string{"type", "code"} {
		switch strings.ToLower(strings.TrimSpace(errorObject.Get(field).String())) {
		case "usage_limit_reached", "insufficient_quota":
			quotaError = true
		}
	}
	if !quotaError {
		return payload
	}
	replacement, _ := json.Marshal(map[string]string{
		"type": "server_error", "code": codexUpstreamCapacityCode, "message": codexUpstreamCapacityMessage,
	})
	normalized, err := sjson.SetRawBytes(payload, path, replacement)
	if err != nil {
		return payload
	}
	return normalized
}
