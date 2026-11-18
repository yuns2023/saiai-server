package dto

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// HostedApplication is a hosting application as its owner sees it. Explicit
// allowlist: never credentials, proxy username/password or upstream UUIDs.
type HostedApplication struct {
	ID            int64      `json:"id"`
	Status        string     `json:"status"`
	DisplayName   string     `json:"display_name"`
	UpstreamEmail string     `json:"upstream_email"`
	ProxyDisplay  string     `json:"proxy_display"`
	RejectReason  string     `json:"reject_reason"`
	AccountID     *int64     `json:"account_id"`
	CreatedAt     time.Time  `json:"created_at"`
	ReviewedAt    *time.Time `json:"reviewed_at"`
}

// HostedApplicationList is GET /hosted/applications.
type HostedApplicationList struct {
	Items []HostedApplication `json:"items"`
}

// AdminHostedApplication adds the review fields (still no secrets).
type AdminHostedApplication struct {
	HostedApplication
	UserID        int64  `json:"user_id"`
	UserEmail     string `json:"user_email"`
	ProxyProtocol string `json:"proxy_protocol"`
	ProxyHost     string `json:"proxy_host"`
	ProxyPort     int    `json:"proxy_port"`
	HasProxyAuth  bool   `json:"has_proxy_auth"`
}

// AdminHostedApproveResult is POST /admin/hosted-applications/:id/approve.
type AdminHostedApproveResult struct {
	Application AdminHostedApplication `json:"application"`
	AccountID   int64                  `json:"account_id"`
	GroupID     int64                  `json:"group_id"`
}

func HostedApplicationFromService(a *service.HostedApplication) HostedApplication {
	if a == nil {
		return HostedApplication{}
	}
	return HostedApplication{
		ID:            a.ID,
		Status:        a.Status,
		DisplayName:   a.DisplayName,
		UpstreamEmail: a.UpstreamEmail,
		ProxyDisplay:  a.ProxyDisplay(),
		RejectReason:  a.RejectReason,
		AccountID:     copyInt64Pointer(a.AccountID),
		CreatedAt:     a.CreatedAt,
		ReviewedAt:    a.ReviewedAt,
	}
}

func HostedApplicationListFromService(items []service.HostedApplication) HostedApplicationList {
	out := HostedApplicationList{Items: make([]HostedApplication, 0, len(items))}
	for i := range items {
		out.Items = append(out.Items, HostedApplicationFromService(&items[i]))
	}
	return out
}

func AdminHostedApplicationFromService(a *service.HostedApplication) AdminHostedApplication {
	if a == nil {
		return AdminHostedApplication{}
	}
	return AdminHostedApplication{
		HostedApplication: HostedApplicationFromService(a),
		UserID:            a.UserID,
		UserEmail:         a.UserEmail,
		ProxyProtocol:     a.ProxyProtocol,
		ProxyHost:         a.ProxyHost,
		ProxyPort:         a.ProxyPort,
		HasProxyAuth:      a.HasProxyAuth,
	}
}

// HostedSelfOnboardSettings are the admin settings, embedded (flattened)
// into SystemSettings.
type HostedSelfOnboardSettings struct {
	HostedSelfOnboardEnabled                   bool    `json:"hosted_self_onboard_enabled"`
	HostedSelfOnboardExchangeProxyID           int64   `json:"hosted_self_onboard_exchange_proxy_id"`
	HostedSelfFeeRate                          float64 `json:"hosted_self_fee_rate"`
	HostedSelfOnboardDefaultConcurrency        int     `json:"hosted_self_onboard_default_concurrency"`
	HostedSelfOnboardDefaultPriority           int     `json:"hosted_self_onboard_default_priority"`
	HostedSelfOnboardDefaultDeviceLimit        int     `json:"hosted_self_onboard_default_device_limit"`
	HostedSelfOnboardDefaultAdmissionsPerCycle int     `json:"hosted_self_onboard_default_admissions_per_cycle"`
	HostedSelfOnboardDefaultIntervalHours      int     `json:"hosted_self_onboard_default_interval_hours"`
}

func HostedSelfOnboardSettingsFromService(s *service.SystemSettings) HostedSelfOnboardSettings {
	in := s.HostedSelfOnboard
	return HostedSelfOnboardSettings{
		HostedSelfOnboardEnabled:                   in.Enabled,
		HostedSelfOnboardExchangeProxyID:           in.ExchangeProxyID,
		HostedSelfFeeRate:                          in.FeeRate,
		HostedSelfOnboardDefaultConcurrency:        in.DefaultConcurrency,
		HostedSelfOnboardDefaultPriority:           in.DefaultPriority,
		HostedSelfOnboardDefaultDeviceLimit:        in.DefaultDeviceLimit,
		HostedSelfOnboardDefaultAdmissionsPerCycle: in.DefaultAdmissionsPerCycle,
		HostedSelfOnboardDefaultIntervalHours:      in.DefaultIntervalHours,
	}
}
