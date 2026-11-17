package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/google/wire"
)

// ProvideAdminHandlers creates the AdminHandlers struct
func ProvideAdminHandlers(
	dashboardHandler *admin.DashboardHandler,
	userHandler *admin.UserHandler,
	groupHandler *admin.GroupHandler,
	accessLevelHandler *admin.AccessLevelHandler,
	accountHandler *admin.AccountHandler,
	announcementHandler *admin.AnnouncementHandler,
	dataManagementHandler *admin.DataManagementHandler,
	backupHandler *admin.BackupHandler,
	oauthHandler *admin.OAuthHandler,
	openaiOAuthHandler *admin.OpenAIOAuthHandler,
	geminiOAuthHandler *admin.GeminiOAuthHandler,
	proxyHandler *admin.ProxyHandler,
	redeemHandler *admin.RedeemHandler,
	promoHandler *admin.PromoHandler,
	settingHandler *admin.SettingHandler,
	opsHandler *admin.OpsHandler,
	subscriptionHandler *admin.SubscriptionHandler,
	usageHandler *admin.UsageHandler,
	evidenceHandler *admin.ModerationEvidenceHandler,
	userAttributeHandler *admin.UserAttributeHandler,
	errorPassthroughHandler *admin.ErrorPassthroughHandler,
	apiKeyHandler *admin.AdminAPIKeyHandler,
	paymentHandler *admin.PaymentHandler,
	cryptoPayHandler *admin.CryptoPayHandler,
	settlementHandler *admin.SettlementHandler,
	hostedOwnerService *service.HostedOwnerService,
) *AdminHandlers {
	accountHandler.SetHostedOwnerService(hostedOwnerService)
	openaiOAuthHandler.SetHostedOwnerService(hostedOwnerService)
	return &AdminHandlers{
		Dashboard:        dashboardHandler,
		User:             userHandler,
		Group:            groupHandler,
		AccessLevel:      accessLevelHandler,
		Account:          accountHandler,
		Announcement:     announcementHandler,
		DataManagement:   dataManagementHandler,
		Backup:           backupHandler,
		OAuth:            oauthHandler,
		OpenAIOAuth:      openaiOAuthHandler,
		GeminiOAuth:      geminiOAuthHandler,
		Proxy:            proxyHandler,
		Redeem:           redeemHandler,
		Promo:            promoHandler,
		Setting:          settingHandler,
		Ops:              opsHandler,
		Subscription:     subscriptionHandler,
		Usage:            usageHandler,
		Evidence:         evidenceHandler,
		UserAttribute:    userAttributeHandler,
		ErrorPassthrough: errorPassthroughHandler,
		APIKey:           apiKeyHandler,
		Payment:          paymentHandler,
		CryptoPay:        cryptoPayHandler,
		Settlement:       settlementHandler,
	}
}

// ProvideSettingHandler creates SettingHandler with version from BuildInfo
func ProvideSettingHandler(settingService *service.SettingService, buildInfo BuildInfo) *SettingHandler {
	return NewSettingHandler(settingService, buildInfo.Version)
}

// ProvideAdminSettingHandler creates the admin settings handler and keeps the
// runtime pricing alias cache connected when settings change.
func ProvideAdminSettingHandler(
	settingService *service.SettingService,
	emailService *service.EmailService,
	turnstileService *service.TurnstileService,
	opsService *service.OpsService,
	pricingService *service.PricingService,
	billingService *service.BillingService,
	pricingDrift *service.PricingDriftService,
	usageRepo service.UsageLogRepository,
	jevSettings *service.JevSettingsService,
	reservedEmailCounter service.ReservedEmailUserCounter,
) *admin.SettingHandler {
	handler := admin.NewSettingHandler(settingService, emailService, turnstileService, opsService)
	handler.SetPricingService(pricingService)
	handler.SetBillingService(billingService)
	handler.SetPricingDriftService(pricingDrift)
	handler.SetPricingUsageReader(usageRepo)
	handler.SetJevSettingsService(jevSettings)
	handler.SetReservedEmailCounter(reservedEmailCounter)
	return handler
}

// ProvideHandlers creates the Handlers struct
func ProvideHandlers(
	authHandler *AuthHandler,
	clientHandler *ClientHandler,
	userHandler *UserHandler,
	apiKeyHandler *APIKeyHandler,
	hostedAccountHandler *HostedAccountHandler,
	hostedOwnerHandler *HostedOwnerHandler,
	usageHandler *UsageHandler,
	redeemHandler *RedeemHandler,
	userInviteHandler *UserInviteHandler,
	redeemPurchaseHandler *RedeemPurchaseHandler,
	subscriptionHandler *SubscriptionHandler,
	announcementHandler *AnnouncementHandler,
	adminHandlers *AdminHandlers,
	gatewayHandler *GatewayHandler,
	openaiGatewayHandler *OpenAIGatewayHandler,
	settingHandler *SettingHandler,
	totpHandler *TotpHandler,
	paymentHandler *PaymentHandler,
	paymentWebhookHandler *PaymentWebhookHandler,
	cryptoPayHandler *CryptoPayHandler,
	_ *service.IdempotencyCoordinator,
	_ *service.IdempotencyCleanupService,
) *Handlers {
	return &Handlers{
		Auth:           authHandler,
		Client:         clientHandler,
		User:           userHandler,
		APIKey:         apiKeyHandler,
		HostedAccount:  hostedAccountHandler,
		HostedOwner:    hostedOwnerHandler,
		Usage:          usageHandler,
		Redeem:         redeemHandler,
		UserInvite:     userInviteHandler,
		RedeemPurchase: redeemPurchaseHandler,
		Subscription:   subscriptionHandler,
		Announcement:   announcementHandler,
		Admin:          adminHandlers,
		Gateway:        gatewayHandler,
		OpenAIGateway:  openaiGatewayHandler,
		Setting:        settingHandler,
		Totp:           totpHandler,
		Payment:        paymentHandler,
		PaymentWebhook: paymentWebhookHandler,
		CryptoPay:      cryptoPayHandler,
	}
}

// ProviderSet is the Wire provider set for all handlers
var ProviderSet = wire.NewSet(
	// Top-level handlers
	NewAuthHandler,
	NewClientHandler,
	NewUserHandler,
	NewAPIKeyHandler,
	NewHostedAccountHandler,
	NewHostedOwnerHandler,
	NewUsageHandler,
	NewRedeemHandler,
	NewUserInviteHandler,
	NewRedeemPurchaseHandler,
	NewSubscriptionHandler,
	NewAnnouncementHandler,
	NewGatewayHandler,
	NewOpenAIGatewayHandler,
	NewTotpHandler,
	NewPaymentHandler,
	NewPaymentWebhookHandler,
	NewCryptoPayHandler,
	ProvideSettingHandler,

	// Admin handlers
	admin.NewDashboardHandler,
	admin.NewUserHandler,
	admin.NewGroupHandler,
	admin.NewAccessLevelHandler,
	admin.NewAccountHandler,
	admin.NewAnnouncementHandler,
	admin.NewDataManagementHandler,
	admin.NewBackupHandler,
	admin.NewOAuthHandler,
	admin.NewOpenAIOAuthHandler,
	admin.NewGeminiOAuthHandler,
	admin.NewProxyHandler,
	admin.NewRedeemHandler,
	admin.NewPromoHandler,
	ProvideAdminSettingHandler,
	admin.NewOpsHandler,
	admin.NewSubscriptionHandler,
	admin.NewUsageHandler,
	admin.NewModerationEvidenceHandler,
	admin.NewUserAttributeHandler,
	admin.NewErrorPassthroughHandler,
	admin.NewAdminAPIKeyHandler,
	admin.NewPaymentHandler,
	admin.NewCryptoPayHandler,
	admin.NewSettlementHandler,

	// AdminHandlers and Handlers constructors
	ProvideAdminHandlers,
	ProvideHandlers,
)
