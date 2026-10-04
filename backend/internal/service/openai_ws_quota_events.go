package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

func parseOpenAIWSCodexRateLimitEvent(payload []byte, observedAt time.Time) *OpenAICodexUsageSnapshot {
	var event struct {
		Type             string `json:"type"`
		MeteredLimitName string `json:"metered_limit_name"`
		LimitName        string `json:"limit_name"`
		RateLimits       *struct {
			Primary   *openAIWSQuotaWindow `json:"primary"`
			Secondary *openAIWSQuotaWindow `json:"secondary"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(payload, &event) != nil || event.Type != "codex.rate_limits" || event.RateLimits == nil {
		return nil
	}
	limitName := strings.TrimSpace(event.MeteredLimitName)
	if limitName == "" {
		limitName = strings.TrimSpace(event.LimitName)
	}
	if limitName != "" && !strings.EqualFold(limitName, "codex") {
		return nil
	}
	observedAt = observedAt.UTC().Truncate(time.Second)
	snapshot := &OpenAICodexUsageSnapshot{UpdatedAt: observedAt.Format(time.RFC3339)}
	snapshot.PrimaryUsedPercent, snapshot.PrimaryWindowMinutes, snapshot.PrimaryResetAfterSeconds = openAIWSQuotaWindowValues(event.RateLimits.Primary, observedAt)
	snapshot.SecondaryUsedPercent, snapshot.SecondaryWindowMinutes, snapshot.SecondaryResetAfterSeconds = openAIWSQuotaWindowValues(event.RateLimits.Secondary, observedAt)
	if snapshot.PrimaryUsedPercent == nil && snapshot.SecondaryUsedPercent == nil {
		return nil
	}
	return snapshot
}

type openAIWSQuotaWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowMinutes *int     `json:"window_minutes"`
	ResetAt       *int64   `json:"reset_at"`
}

func openAIWSQuotaWindowValues(window *openAIWSQuotaWindow, observedAt time.Time) (*float64, *int, *int) {
	if window == nil || window.UsedPercent == nil || *window.UsedPercent < 0 || window.WindowMinutes == nil {
		return nil, nil, nil
	}
	if *window.WindowMinutes != 300 && *window.WindowMinutes != 10080 {
		return nil, nil, nil
	}
	var resetAfter *int
	if window.ResetAt != nil && *window.ResetAt > 0 {
		seconds := *window.ResetAt - observedAt.Unix()
		if seconds < 0 {
			seconds = 0
		}
		if seconds <= int64(^uint(0)>>1) {
			value := int(seconds)
			resetAfter = &value
		}
	}
	return window.UsedPercent, window.WindowMinutes, resetAfter
}

func openAIWSQuotaErrorPayload(payload []byte) []byte {
	if !gjson.ValidBytes(payload) {
		return nil
	}
	eventType := gjson.GetBytes(payload, "type").String()
	if eventType == "error" {
		return payload
	}
	if eventType != "response.failed" && (eventType != "response.done" || gjson.GetBytes(payload, "response.status").String() != "failed") {
		return nil
	}
	errorObject := gjson.GetBytes(payload, "response.error")
	if !errorObject.IsObject() {
		return nil
	}
	return []byte(`{"error":` + errorObject.Raw + `}`)
}

func openAIWSQuotaErrorHeaders(handshakeHeaders http.Header, payload []byte) http.Header {
	headers := cloneHeader(handshakeHeaders)
	if headers == nil {
		headers = make(http.Header)
	}
	gjson.GetBytes(payload, "headers").ForEach(func(name, value gjson.Result) bool {
		lowerName := strings.ToLower(name.String())
		if value.Type == gjson.String && (strings.HasPrefix(lowerName, "x-codex-") || lowerName == "retry-after") {
			headers.Set(name.String(), value.String())
		}
		return true
	})
	return headers
}

func isOpenAIWSQuotaReplaySafe(payload []byte) bool {
	output := gjson.GetBytes(payload, "response.output")
	return (!output.Exists() || (output.IsArray() && len(output.Array()) == 0)) && gjson.GetBytes(payload, "response.usage.output_tokens").Int() == 0
}
