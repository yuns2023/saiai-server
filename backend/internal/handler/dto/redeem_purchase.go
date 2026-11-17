package dto

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// RedeemPurchase is one code the user bought, as the buyer sees it. Explicit
// whitelist: never who redeemed the code, never notes or user objects.
type RedeemPurchase struct {
	ID     int64   `json:"id"`
	Code   string  `json:"code"`
	Amount float64 `json:"amount"`
	// unused | used | revoked | unknown
	Status    string     `json:"status"`
	UsedAt    *time.Time `json:"used_at"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

// RedeemPurchaseResult is POST /redeem/purchases. Replayed: the request_id was
// already used with this amount and the original purchase is returned.
type RedeemPurchaseResult struct {
	RedeemPurchase
	Replayed bool `json:"replayed"`
}

// RedeemPurchaseConfig is the purchase form state. Remaining values are a
// display snapshot, floored at 0.
type RedeemPurchaseConfig struct {
	Enabled            bool    `json:"enabled"`
	MinAmount          float64 `json:"min_amount"`
	MaxAmount          float64 `json:"max_amount"`
	RemainingAmount24h float64 `json:"remaining_amount_24h"`
	RemainingCount24h  int     `json:"remaining_count_24h"`
}

// RedeemPurchasePage is the list's paging block (pagination.PaginationResult has no JSON tags).
type RedeemPurchasePage struct {
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Pages    int   `json:"pages"`
}

// RedeemPurchaseOverview is GET /redeem/purchases.
type RedeemPurchaseOverview struct {
	Config     RedeemPurchaseConfig `json:"config"`
	Items      []RedeemPurchase     `json:"items"`
	Pagination RedeemPurchasePage   `json:"pagination"`
}

// RedeemPurchaseSettings are the admin switches, embedded (flattened) into SystemSettings.
type RedeemPurchaseSettings struct {
	RedeemPurchaseEnabled         bool    `json:"redeem_purchase_enabled"`
	RedeemPurchaseMinAmount       float64 `json:"redeem_purchase_min_amount"`
	RedeemPurchaseMaxAmount       float64 `json:"redeem_purchase_max_amount"`
	RedeemPurchaseWindowMaxAmount float64 `json:"redeem_purchase_window_max_amount"`
	RedeemPurchaseWindowMaxCount  int     `json:"redeem_purchase_window_max_count"`
}

func RedeemPurchaseSettingsFromService(s *service.SystemSettings) RedeemPurchaseSettings {
	return RedeemPurchaseSettings{
		RedeemPurchaseEnabled:         s.RedeemPurchaseEnabled,
		RedeemPurchaseMinAmount:       s.RedeemPurchaseMinAmount,
		RedeemPurchaseMaxAmount:       s.RedeemPurchaseMaxAmount,
		RedeemPurchaseWindowMaxAmount: s.RedeemPurchaseWindowMaxAmount,
		RedeemPurchaseWindowMaxCount:  s.RedeemPurchaseWindowMaxCount,
	}
}

// Amounts have at most two decimals, so float64 carries them exactly in
// their shortest form.
func RedeemPurchaseFromService(v *service.RedeemPurchaseView) RedeemPurchase {
	return RedeemPurchase{
		ID:        v.ID,
		Code:      v.Code,
		Amount:    v.Amount.InexactFloat64(),
		Status:    v.Status,
		UsedAt:    v.UsedAt,
		CreatedAt: v.CreatedAt,
		RevokedAt: v.RevokedAt,
	}
}

func RedeemPurchaseOverviewFromService(o *service.RedeemPurchaseOverview) RedeemPurchaseOverview {
	out := RedeemPurchaseOverview{
		Config: RedeemPurchaseConfig{
			Enabled:            o.Config.Enabled,
			MinAmount:          o.Config.MinAmount.InexactFloat64(),
			MaxAmount:          o.Config.MaxAmount.InexactFloat64(),
			RemainingAmount24h: o.Config.RemainingAmount24h.InexactFloat64(),
			RemainingCount24h:  o.Config.RemainingCount24h,
		},
		Items: make([]RedeemPurchase, 0, len(o.Items)),
		Pagination: RedeemPurchasePage{
			Total:    o.Pagination.Total,
			Page:     o.Pagination.Page,
			PageSize: o.Pagination.PageSize,
			Pages:    o.Pagination.Pages,
		},
	}
	for i := range o.Items {
		out.Items = append(out.Items, RedeemPurchaseFromService(&o.Items[i]))
	}
	return out
}
