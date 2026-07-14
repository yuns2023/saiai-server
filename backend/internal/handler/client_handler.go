package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

const clientBootstrapSchemaVersion = 1

// ClientCapabilities describes routes that the authenticated API key can use
// without changing its group. Fields are additive within a schema version so
// older clients can safely ignore capabilities introduced by newer gateways.
type ClientCapabilities struct {
	Claude          bool `json:"claude"`
	Codex           bool `json:"codex"`
	CodexResponses  bool `json:"codex_responses"`
	CodexWebSockets bool `json:"codex_websockets"`
}

// ClientBootstrapData is the versioned contract returned to SAIAI clients.
type ClientBootstrapData struct {
	SchemaVersion  int                `json:"schema_version"`
	GatewayVersion string             `json:"gateway_version"`
	Capabilities   ClientCapabilities `json:"capabilities"`
}

// ClientHandler exposes authenticated, non-billable client metadata.
type ClientHandler struct {
	gatewayVersion string
}

func NewClientHandler(buildInfo BuildInfo) *ClientHandler {
	return &ClientHandler{gatewayVersion: buildInfo.Version}
}

// Bootstrap reports gateway compatibility without forwarding a model request.
// GET /api/v1/client/bootstrap
func (h *ClientHandler) Bootstrap(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Vary", "Authorization")
	apiKey, _ := middleware.GetAPIKeyFromContext(c)
	response.Success(c, ClientBootstrapData{
		SchemaVersion:  clientBootstrapSchemaVersion,
		GatewayVersion: h.gatewayVersion,
		Capabilities:   clientCapabilitiesForAPIKey(apiKey),
	})
}

func clientCapabilitiesForAPIKey(apiKey *service.APIKey) ClientCapabilities {
	if apiKey == nil || apiKey.Group == nil || !service.IsGroupContextValid(apiKey.Group) || !apiKey.Group.IsActive() {
		return ClientCapabilities{}
	}

	var capabilities ClientCapabilities
	switch apiKey.Group.Platform {
	case service.PlatformOpenAI:
		capabilities.Codex = true
		capabilities.CodexResponses = true
		capabilities.Claude = apiKey.Group.AllowMessagesDispatch
	case service.PlatformAnthropic, service.PlatformAntigravity:
		capabilities.Claude = true
	}

	// WebSocket availability also depends on global transport settings and on
	// the selected account's per-account mode. Bootstrap deliberately performs
	// no account selection, so Preview stays on the reliable HTTPS transport.
	capabilities.CodexWebSockets = false
	return capabilities
}
