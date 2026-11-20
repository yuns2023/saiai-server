package service

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// SetClaudeDevicePlatformService enables device platform observation.
func (s *GatewayService) SetClaudeDevicePlatformService(svc *ClaudeDevicePlatformService) {
	if s != nil {
		s.claudeDevicePlatform = svc
	}
}

// observeClaudeDevicePlatform records the original device id of an inbound
// Anthropic OAuth / setup-token request once per request (an account
// failover runs prepareOAuthRequestIdentity again on the same gin context).
// It reads the request as the client sent it and never changes it.
func (s *GatewayService) observeClaudeDevicePlatform(c *gin.Context, account *Account, parsed *ParsedRequest, deviceID string) {
	if s == nil || s.claudeDevicePlatform == nil || c == nil || account == nil || parsed == nil || !account.IsAnthropicOAuthOrSetupToken() {
		return
	}
	if _, done := c.Get(claudeDevicePlatformObservedKey); done {
		return
	}
	c.Set(claudeDevicePlatformObservedKey, true)
	var key *APIKey
	if value, ok := c.Get("api_key"); ok {
		key, _ = value.(*APIKey)
	}
	var headers map[string][]string
	if c.Request != nil {
		headers = c.Request.Header
	}
	s.claudeDevicePlatform.ObserveRequest(deviceID, headers, parsed.System, key)
}

// observeClaudeDevicePlatformAux covers the count_tokens auxiliary shape
// that skips the metadata check: observed only when metadata.user_id parses
// to a device id (no substitute from session headers).
func (s *GatewayService) observeClaudeDevicePlatformAux(c *gin.Context, account *Account, parsed *ParsedRequest) {
	if s == nil || s.claudeDevicePlatform == nil || parsed == nil {
		return
	}
	raw := strings.TrimSpace(parsed.MetadataUserID)
	if raw == "" || len(raw) > claudeDevicePlatformMaxUserIDBytes {
		return
	}
	userID := ParseMetadataUserID(raw)
	if userID == nil {
		return
	}
	s.observeClaudeDevicePlatform(c, account, parsed, userID.DeviceID)
}
