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

	// DeviceAuthorizationUnavailableClientMessage is intentionally provider-neutral.
	// The original upstream detail remains available in restricted Ops logs.
	DeviceAuthorizationUnavailableClientMessage = "SAIAI 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。"
)

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
	if chineseDeviceRevoked || englishDeviceRevoked {
		return UpstreamFailureDeviceAuthorizationRevoked
	}

	return UpstreamFailureNone
}
