package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Preserve application headers and the raw query on the already-selected
// Messages/count_tokens route. Never let the inbound URL select an upstream.
func copyAnthropicPassthroughRequest(req *http.Request, c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	if c.Request.URL != nil {
		req.URL.RawQuery = c.Request.URL.RawQuery
		req.URL.ForceQuery = c.Request.URL.ForceQuery
	}
	hop := make(map[string]bool)
	for _, value := range c.Request.Header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			hop[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	for name, values := range c.Request.Header {
		lower := strings.ToLower(name)
		if hop[lower] || strings.HasPrefix(lower, "x-saiai-") || strings.HasPrefix(lower, "x-sub2api-") {
			continue
		}
		// Unlike pooled OAuth identity, API-key passthrough retains the
		// application's own request ID. Other credential/proxy exclusions apply.
		if lower != "x-client-request-id" && isDeniedClaudeOAuthHeader(lower) {
			continue
		}
		switch lower {
		case "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "upgrade", "cookie2":
			continue
		}
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
}
