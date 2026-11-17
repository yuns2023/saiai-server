package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpdateSettings_RedeemPurchase(t *testing.T) {
	h, _, repo, router := modelPriceHandlerFixture(t)
	router.PUT("/settings", h.UpdateSettings)
	router.GET("/settings", h.GetSettings)

	// Defaults before anything is stored.
	w := priceHandlerRequest(router, http.MethodGet, "/settings", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	type purchaseSettings struct {
		Enabled      bool    `json:"redeem_purchase_enabled"`
		Min          float64 `json:"redeem_purchase_min_amount"`
		Max          float64 `json:"redeem_purchase_max_amount"`
		WindowAmount float64 `json:"redeem_purchase_window_max_amount"`
		WindowCount  int     `json:"redeem_purchase_window_max_count"`
	}
	var got struct {
		Data purchaseSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, purchaseSettings{Enabled: false, Min: 1, Max: 500, WindowAmount: 1000, WindowCount: 20}, got.Data)

	w = priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{
		"redeem_purchase_enabled": true, "redeem_purchase_min_amount": 0.5, "redeem_purchase_max_amount": 19.99,
		"redeem_purchase_window_max_amount": 100, "redeem_purchase_window_max_count": 3,
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "true", repo.values[service.SettingKeyRedeemPurchaseEnabled])
	require.Equal(t, "0.5", repo.values[service.SettingKeyRedeemPurchaseMinAmount])
	require.Equal(t, "19.99", repo.values[service.SettingKeyRedeemPurchaseMaxAmount])
	require.Equal(t, "100", repo.values[service.SettingKeyRedeemPurchaseWindowMaxAmount])
	require.Equal(t, "3", repo.values[service.SettingKeyRedeemPurchaseWindowMaxCount])
	got.Data = purchaseSettings{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), "the PUT response carries the new values")
	require.Equal(t, purchaseSettings{Enabled: true, Min: 0.5, Max: 19.99, WindowAmount: 100, WindowCount: 3}, got.Data)

	// Omitted fields keep their stored values (admin clients that predate the feature).
	w = priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{"site_name": "x"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "true", repo.values[service.SettingKeyRedeemPurchaseEnabled])
	require.Equal(t, "19.99", repo.values[service.SettingKeyRedeemPurchaseMaxAmount])
	require.Equal(t, "3", repo.values[service.SettingKeyRedeemPurchaseWindowMaxCount])

	for _, bad := range []map[string]any{
		{"redeem_purchase_min_amount": -1},
		{"redeem_purchase_max_amount": 1.005},
		{"redeem_purchase_max_amount": 1000000.01},
		{"redeem_purchase_window_max_amount": -0.01},
		{"redeem_purchase_window_max_count": -1},
		{"redeem_purchase_window_max_count": service.MaxRedeemPurchaseWindowMaxCount + 1},
		{"redeem_purchase_min_amount": 20},                                     // above the stored max 19.99
		{"redeem_purchase_min_amount": 10, "redeem_purchase_max_amount": 9.99}, // min > max in one request
	} {
		w = priceHandlerRequest(router, http.MethodPut, "/settings", bad)
		require.Equal(t, http.StatusBadRequest, w.Code, bad)
	}
	require.Equal(t, "0.5", repo.values[service.SettingKeyRedeemPurchaseMinAmount], "rejected requests change nothing")
	require.Equal(t, "19.99", repo.values[service.SettingKeyRedeemPurchaseMaxAmount])

	// 0 is a real limit, not "unlimited".
	w = priceHandlerRequest(router, http.MethodPut, "/settings", map[string]any{"redeem_purchase_window_max_count": 0, "redeem_purchase_min_amount": 0})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "0", repo.values[service.SettingKeyRedeemPurchaseWindowMaxCount])
	require.Equal(t, "0", repo.values[service.SettingKeyRedeemPurchaseMinAmount])

	require.ElementsMatch(t,
		[]string{"redeem_purchase_enabled", "redeem_purchase_window_max_count"},
		diffRedeemPurchaseSettings(
			&service.SystemSettings{RedeemPurchaseEnabled: false, RedeemPurchaseWindowMaxCount: 1, RedeemPurchaseMaxAmount: 5},
			&service.SystemSettings{RedeemPurchaseEnabled: true, RedeemPurchaseWindowMaxCount: 2, RedeemPurchaseMaxAmount: 5},
		))
}
