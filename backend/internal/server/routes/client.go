package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// RegisterClientRoutes registers authenticated metadata endpoints used by the
// SAIAI CLI and desktop client. These routes never forward requests upstream.
func RegisterClientRoutes(
	v1 *gin.RouterGroup,
	h *handler.Handlers,
	apiKeyAuth middleware.APIKeyAuthMiddleware,
) {
	client := v1.Group("/client")
	client.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Vary", "Authorization")
		c.Next()
	})
	client.Use(gin.HandlerFunc(apiKeyAuth))
	client.GET("/bootstrap", h.Client.Bootstrap)
}
