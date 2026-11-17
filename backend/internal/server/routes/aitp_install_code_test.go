package routes

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The install code routes live next to the gateway and user routes without conflicts:
// redeem is public at /i/:code (POST only consumes), create is a JWT user route.
func TestAITPInstallCodeRoutesRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &handler.Handlers{
		AITPInstallCode: &handler.AITPInstallCodeHandler{},
		Gateway:         &handler.GatewayHandler{},
		OpenAIGateway:   &handler.OpenAIGatewayHandler{},
	}
	require.NotPanics(t, func() {
		RegisterCommonRoutes(router)
		RegisterAITPInstallCodeRoutes(router, h)
		RegisterUserRoutes(router.Group("/api/v1"), h, servermiddleware.JWTAuthMiddleware(func(c *gin.Context) { c.Next() }), nil)
		RegisterGatewayRoutes(router, h, servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) { c.Next() }), nil, nil, nil, nil, &config.Config{})
	})

	routes := map[string]string{}
	for _, r := range router.Routes() {
		routes[r.Method+" "+r.Path] = r.Handler
	}
	require.True(t, strings.HasSuffix(routes["POST /i/:code"], "AITPInstallCodeHandler).Redeem-fm"), routes["POST /i/:code"])
	require.Contains(t, routes["GET /i/:code"], "AITPInstallCodeHandler).RedeemMethodNotAllowed")
	require.Contains(t, routes["HEAD /i/:code"], "AITPInstallCodeHandler).RedeemMethodNotAllowed")
	require.Contains(t, routes["POST /api/v1/keys/:id/install-code"], "AITPInstallCodeHandler).Create")
	for _, r := range router.Routes() {
		if r.Path == "/i/:code" {
			require.Contains(t, []string{"POST", "GET", "HEAD"}, r.Method)
		}
	}
}
