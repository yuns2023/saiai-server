package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// A 200 SSE transport can contain an application failure. Observe only error
// metadata; never rewrite the provider event or treat it as a completed turn.
func observeOpenAIStreamFailure(c *gin.Context, account *Account, resp *http.Response, payload []byte) error {
	if c == nil || account == nil || resp == nil || (!account.IsOpenAIOAuth() && !account.IsOpenAICodexNativeRelay()) {
		return nil
	}
	kind := gjson.GetBytes(payload, "type").String()
	if kind != "error" && kind != "response.failed" {
		return nil
	}
	errObject := gjson.GetBytes(payload, "error")
	if !errObject.IsObject() {
		errObject = gjson.GetBytes(payload, "response.error")
	}
	redact := func(value string) string {
		secrets := []string{account.GetCredential("access_token"), account.GetCredential("api_key")}
		for _, request := range []*http.Request{c.Request, resp.Request} {
			if request != nil {
				secrets = append(secrets, strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
			}
		}
		for _, secret := range secrets {
			if secret != "" {
				value = strings.ReplaceAll(value, secret, "[REDACTED]")
			}
		}
		return sanitizeUpstreamErrorMessage(value)
	}
	code := redact(errObject.Get("code").String())
	errType := redact(errObject.Get("type").String())
	status := int(gjson.GetBytes(payload, "status").Int())
	statusSource := "event_status"
	if status < 400 || status > 599 {
		status = int(errObject.Get("status").Int())
		statusSource = "error_status"
	}
	if status < 400 || status > 599 {
		statusSource = "error_classification"
		if code == "server_is_overloaded" || errType == "service_unavailable_error" {
			status = http.StatusServiceUnavailable
		} else {
			status = openAIWSErrorHTTPStatusFromRaw(code, errType)
		}
	}
	// No provider message or arbitrary extra fields are captured: they may echo
	// prompt content or credentials. The original event still goes to the client.
	message := "Upstream Responses stream returned an error"
	detail := map[string]any{
		"error":                 map[string]string{"code": code, "type": errType, "message": message},
		"transport_status_code": resp.StatusCode, "upstream_status_code": status,
		"stream_event_type": kind, "application_status_source": statusSource,
	}
	if requestID := redact(resp.Header.Get("x-request-id")); requestID != "" {
		detail["upstream_request_id"] = requestID
	}
	encoded, _ := json.Marshal(detail)
	setOpsUpstreamError(c, status, message, string(encoded))
	return fmt.Errorf("upstream Responses stream application failure: %d", status)
}
