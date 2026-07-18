package service

import (
	"net/http"
	"strings"
)

// UpstreamFailureKind identifies account-scoped upstream failures whose
// client response must not expose provider-specific recovery instructions.
type UpstreamFailureKind string

const (
	UpstreamFailureNone                       UpstreamFailureKind = ""
	UpstreamFailureDeviceAuthorizationRevoked UpstreamFailureKind = "upstream_device_authorization_revoked"
	restrictedUpstreamIdentityReClaude                            = "reclaude"

	// DeviceAuthorizationUnavailableClientMessage is intentionally provider-neutral.
	// The original upstream detail remains available in restricted Ops logs.
	DeviceAuthorizationUnavailableClientMessage = "SAIAI 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。"
)

// ClientSafeUpstreamErrorMessage enforces the client-facing provider identity
// boundary. Keep the original upstream message in restricted Ops context before
// calling this helper; provider identities must never be returned to clients,
// including through configurable passthrough rules.
func ClientSafeUpstreamErrorMessage(message string) string {
	if containsRestrictedUpstreamIdentity(message) {
		return DeviceAuthorizationUnavailableClientMessage
	}
	return message
}

func containsRestrictedUpstreamIdentity(value string) bool {
	return strings.Contains(strings.ToLower(value), restrictedUpstreamIdentityReClaude)
}

func classifyUpstreamFailure(platform string, statusCode int, responseBody []byte) UpstreamFailureKind {
	if platform != PlatformAnthropic || statusCode != http.StatusBadRequest || len(responseBody) == 0 {
		return UpstreamFailureNone
	}

	message := strings.TrimSpace(extractUpstreamErrorMessage(responseBody))
	if message == "" {
		message = strings.TrimSpace(string(responseBody))
	}
	lowerMessage := strings.ToLower(message)

	chineseDeviceRevoked := strings.Contains(message, "此设备已被解绑") &&
		(strings.Contains(message, "登录") || strings.Contains(message, "终端"))
	englishDeviceRevoked := (strings.Contains(lowerMessage, "device has been unbound") ||
		strings.Contains(lowerMessage, "device was unbound") ||
		strings.Contains(lowerMessage, "device authorization has been revoked")) &&
		(strings.Contains(lowerMessage, "login") ||
			strings.Contains(lowerMessage, "log in") ||
			strings.Contains(lowerMessage, "reauth") ||
			strings.Contains(lowerMessage, "authenticate"))
	// Some upstream versions report the same account-scoped authorization outage
	// as a branded client-state failure instead of saying the device was unbound.
	// Reuse the historical failure kind so account isolation and failover behavior
	// remain compatible while the client response stays provider-neutral.
	chineseClientStateUnavailable := containsRestrictedUpstreamIdentity(message) &&
		strings.Contains(message, "客户端状态异常") &&
		strings.Contains(message, "重启")
	englishClientStateUnavailable := containsRestrictedUpstreamIdentity(message) &&
		(strings.Contains(lowerMessage, "client state") || strings.Contains(lowerMessage, "client status")) &&
		(strings.Contains(lowerMessage, "restart") || strings.Contains(lowerMessage, "relaunch"))
	if chineseDeviceRevoked || englishDeviceRevoked || chineseClientStateUnavailable || englishClientStateUnavailable {
		return UpstreamFailureDeviceAuthorizationRevoked
	}

	return UpstreamFailureNone
}
