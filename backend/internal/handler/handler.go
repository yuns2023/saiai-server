package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
)

// AdminHandlers contains all admin-related HTTP handlers
type AdminHandlers struct {
	Dashboard        *admin.DashboardHandler
	User             *admin.UserHandler
	Group            *admin.GroupHandler
	AccessLevel      *admin.AccessLevelHandler
	Account          *admin.AccountHandler
	Announcement     *admin.AnnouncementHandler
	DataManagement   *admin.DataManagementHandler
	Backup           *admin.BackupHandler
	OAuth            *admin.OAuthHandler
	OpenAIOAuth      *admin.OpenAIOAuthHandler
	GeminiOAuth      *admin.GeminiOAuthHandler
	Proxy            *admin.ProxyHandler
	Redeem           *admin.RedeemHandler
	Promo            *admin.PromoHandler
	Setting          *admin.SettingHandler
	Ops              *admin.OpsHandler
	Subscription     *admin.SubscriptionHandler
	Usage            *admin.UsageHandler
	Evidence         *admin.ModerationEvidenceHandler
	UserAttribute    *admin.UserAttributeHandler
	ErrorPassthrough *admin.ErrorPassthroughHandler
	APIKey           *admin.AdminAPIKeyHandler
	Payment          *admin.PaymentHandler
	CryptoPay        *admin.CryptoPayHandler
	Settlement       *admin.SettlementHandler
}

// Handlers contains all HTTP handlers
type Handlers struct {
	Auth           *AuthHandler
	Client         *ClientHandler
	User           *UserHandler
	APIKey         *APIKeyHandler
	HostedAccount  *HostedAccountHandler
	HostedOwner    *HostedOwnerHandler
	Usage          *UsageHandler
	Redeem         *RedeemHandler
	UserInvite     *UserInviteHandler
	RedeemPurchase *RedeemPurchaseHandler
	Subscription   *SubscriptionHandler
	Announcement   *AnnouncementHandler
	Admin          *AdminHandlers
	Gateway        *GatewayHandler
	OpenAIGateway  *OpenAIGatewayHandler
	Setting        *SettingHandler
	Totp           *TotpHandler
	Payment        *PaymentHandler
	PaymentWebhook *PaymentWebhookHandler
	CryptoPay      *CryptoPayHandler
	// AITPInstallCode serves one-time install codes (/api/v1/keys/:id/install-code, /i/:code).
	AITPInstallCode *AITPInstallCodeHandler
}

// BuildInfo contains build-time information
type BuildInfo struct {
	Version string
}
