package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// RegisterAITPInstallCodeRoutes registers the one-time install code redemption (no auth).
// Only POST consumes a code; GET/HEAD (browsers, link previews) get 405 and never a key.
// The embedded frontend bypasses /i/ (web.shouldBypassEmbeddedFrontend), and the access
// logs and HTTP trace redact the path (middleware.RedactedLogPath).
func RegisterAITPInstallCodeRoutes(r *gin.Engine, h *handler.Handlers) {
	r.POST("/i/:code", h.AITPInstallCode.Redeem)
	r.GET("/i/:code", h.AITPInstallCode.RedeemMethodNotAllowed)
	r.HEAD("/i/:code", h.AITPInstallCode.RedeemMethodNotAllowed)
}
