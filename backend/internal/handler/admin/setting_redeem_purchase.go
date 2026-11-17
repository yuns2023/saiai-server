package admin

import (
	"fmt"
	"math"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

// redeemPurchaseSettingValues are the user code-purchase switches after
// merging an UpdateSettingsRequest into the stored values.
type redeemPurchaseSettingValues struct {
	enabled         bool
	minAmount       float64
	maxAmount       float64
	windowMaxAmount float64
	windowMaxCount  int
}

// resolveRedeemPurchaseSettings keeps every stored value whose field is
// absent from the request (admin clients that predate the feature) and
// validates the merged result. A non-empty string is the 400 message.
func resolveRedeemPurchaseSettings(req *UpdateSettingsRequest, prev *service.SystemSettings) (redeemPurchaseSettingValues, string) {
	v := redeemPurchaseSettingValues{
		enabled:         prev.RedeemPurchaseEnabled,
		minAmount:       prev.RedeemPurchaseMinAmount,
		maxAmount:       prev.RedeemPurchaseMaxAmount,
		windowMaxAmount: prev.RedeemPurchaseWindowMaxAmount,
		windowMaxCount:  prev.RedeemPurchaseWindowMaxCount,
	}
	if req.RedeemPurchaseEnabled != nil {
		v.enabled = *req.RedeemPurchaseEnabled
	}
	amounts := []struct {
		name string
		in   *float64
		out  *float64
	}{
		{"redeem_purchase_min_amount", req.RedeemPurchaseMinAmount, &v.minAmount},
		{"redeem_purchase_max_amount", req.RedeemPurchaseMaxAmount, &v.maxAmount},
		{"redeem_purchase_window_max_amount", req.RedeemPurchaseWindowMaxAmount, &v.windowMaxAmount},
	}
	for _, a := range amounts {
		if a.in == nil {
			continue
		}
		if msg := validateRedeemPurchaseAmountSetting(a.name, *a.in); msg != "" {
			return v, msg
		}
		*a.out = *a.in
	}
	if req.RedeemPurchaseWindowMaxCount != nil {
		n := *req.RedeemPurchaseWindowMaxCount
		if n < 0 || n > service.MaxRedeemPurchaseWindowMaxCount {
			return v, fmt.Sprintf("redeem_purchase_window_max_count must be between 0 and %d", service.MaxRedeemPurchaseWindowMaxCount)
		}
		v.windowMaxCount = n
	}
	if decimal.NewFromFloat(v.minAmount).GreaterThan(decimal.NewFromFloat(v.maxAmount)) {
		return v, "redeem_purchase_min_amount must not exceed redeem_purchase_max_amount"
	}
	return v, ""
}

func validateRedeemPurchaseAmountSetting(name string, v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) || !service.ValidRedeemPurchaseAmountSetting(decimal.NewFromFloat(v)) {
		return fmt.Sprintf("%s must be between 0 and %s with at most 2 decimal places", name, service.MaxRedeemPurchaseAmountSetting.String())
	}
	return ""
}

func (v redeemPurchaseSettingValues) apply(dst *service.SystemSettings) {
	dst.RedeemPurchaseEnabled = v.enabled
	dst.RedeemPurchaseMinAmount = v.minAmount
	dst.RedeemPurchaseMaxAmount = v.maxAmount
	dst.RedeemPurchaseWindowMaxAmount = v.windowMaxAmount
	dst.RedeemPurchaseWindowMaxCount = v.windowMaxCount
}

func diffRedeemPurchaseSettings(before, after *service.SystemSettings) []string {
	var changed []string
	if before.RedeemPurchaseEnabled != after.RedeemPurchaseEnabled {
		changed = append(changed, "redeem_purchase_enabled")
	}
	if before.RedeemPurchaseMinAmount != after.RedeemPurchaseMinAmount {
		changed = append(changed, "redeem_purchase_min_amount")
	}
	if before.RedeemPurchaseMaxAmount != after.RedeemPurchaseMaxAmount {
		changed = append(changed, "redeem_purchase_max_amount")
	}
	if before.RedeemPurchaseWindowMaxAmount != after.RedeemPurchaseWindowMaxAmount {
		changed = append(changed, "redeem_purchase_window_max_amount")
	}
	if before.RedeemPurchaseWindowMaxCount != after.RedeemPurchaseWindowMaxCount {
		changed = append(changed, "redeem_purchase_window_max_count")
	}
	return changed
}
