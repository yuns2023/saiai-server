package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// RegisterCryptoPayRoutes registers the stablecoin top-up API (user + admin).
func RegisterCryptoPayRoutes(
	v1 *gin.RouterGroup,
	h *handler.Handlers,
	jwtAuth middleware.JWTAuthMiddleware,
	adminAuth middleware.AdminAuthMiddleware,
	settingService *service.SettingService,
) {
	user := v1.Group("/crypto-pay")
	user.Use(gin.HandlerFunc(jwtAuth))
	user.Use(middleware.BackendModeUserGuard(settingService))
	{
		user.GET("/config", h.CryptoPay.GetConfig)
		user.POST("/orders", h.CryptoPay.CreateOrder)
		user.GET("/orders/my", h.CryptoPay.ListMyOrders)
		user.GET("/orders/:id", h.CryptoPay.GetOrder)
		user.GET("/deposits", h.CryptoPay.ListDeposits)
		user.GET("/addresses", h.CryptoPay.ListAddresses)
	}

	admin := v1.Group("/admin/crypto-pay")
	admin.Use(gin.HandlerFunc(adminAuth))
	{
		admin.GET("/settings", h.Admin.CryptoPay.GetSettings)
		admin.PUT("/settings", h.Admin.CryptoPay.UpdateSettings)
		admin.GET("/keysets", h.Admin.CryptoPay.ListKeysets)
		admin.POST("/keysets/evm", h.Admin.CryptoPay.ImportEVMKeyset)
		admin.POST("/keysets/solana", middleware.RequestBodyLimit(16<<20), h.Admin.CryptoPay.ImportSolanaKeyset)
		admin.GET("/deposits", h.Admin.CryptoPay.ListDeposits)
		admin.POST("/deposits/:id/release", h.Admin.CryptoPay.ReleaseDeposit)
		admin.POST("/rescan", h.Admin.CryptoPay.Rescan)
		admin.GET("/reconciliation", h.Admin.CryptoPay.Reconciliation)
	}
}
