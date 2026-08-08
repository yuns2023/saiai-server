package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func RegisterPaymentRoutes(
	v1 *gin.RouterGroup,
	h *handler.Handlers,
	jwtAuth middleware.JWTAuthMiddleware,
	adminAuth middleware.AdminAuthMiddleware,
	settingService *service.SettingService,
) {
	webhook := v1.Group("/payment/webhook")
	webhook.Use(middleware.RequestBodyLimit(64 << 10))
	webhook.GET("/:provider", h.PaymentWebhook.Notify)
	webhook.POST("/:provider", h.PaymentWebhook.Notify)

	user := v1.Group("/payment")
	user.Use(gin.HandlerFunc(jwtAuth))
	user.Use(middleware.BackendModeUserGuard(settingService))
	{
		user.GET("/config", h.Payment.GetConfig)
		user.GET("/plans", h.Payment.ListPlans)
		orders := user.Group("/orders")
		orders.POST("", h.Payment.CreateOrder)
		orders.GET("/my", h.Payment.ListMyOrders)
		orders.GET("/:id", h.Payment.GetOrder)
		orders.POST("/:id/cancel", h.Payment.CancelOrder)
	}

	admin := v1.Group("/admin/payment")
	admin.Use(gin.HandlerFunc(adminAuth))
	{
		admin.GET("/config", h.Admin.Payment.GetConfig)
		admin.PUT("/config", h.Admin.Payment.UpdateConfig)
		admin.GET("/provider-definitions", h.Admin.Payment.ListProviderDefinitions)
		admin.GET("/providers", h.Admin.Payment.ListProviders)
		admin.POST("/providers", h.Admin.Payment.CreateProvider)
		admin.PUT("/providers/:id", h.Admin.Payment.UpdateProvider)
		admin.DELETE("/providers/:id", h.Admin.Payment.DeleteProvider)
		admin.GET("/plans", h.Admin.Payment.ListPlans)
		admin.POST("/plans", h.Admin.Payment.CreatePlan)
		admin.PUT("/plans/:id", h.Admin.Payment.UpdatePlan)
		admin.DELETE("/plans/:id", h.Admin.Payment.DeletePlan)
		admin.GET("/orders", h.Admin.Payment.ListOrders)
		admin.POST("/orders/:id/retry", h.Admin.Payment.RetryFulfillment)
		admin.POST("/orders/:id/refund", h.Admin.Payment.RequestRefund)
		admin.POST("/orders/:id/refund/resolve", h.Admin.Payment.ResolveRefund)
	}
}
