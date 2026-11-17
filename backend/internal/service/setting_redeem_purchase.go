package service

import (
	"math"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

var redeemPurchaseSettingKeys = []string{
	SettingKeyRedeemPurchaseEnabled,
	SettingKeyRedeemPurchaseMinAmount,
	SettingKeyRedeemPurchaseMaxAmount,
	SettingKeyRedeemPurchaseWindowMaxAmount,
	SettingKeyRedeemPurchaseWindowMaxCount,
}

// RedeemPurchaseSettingKeys are the settings rows of the code-purchase switches.
func RedeemPurchaseSettingKeys() []string {
	return append([]string(nil), redeemPurchaseSettingKeys...)
}

// ParseRedeemPurchaseSettings turns stored values into the switches: a
// missing or invalid value means its default ("off" for the switch).
func ParseRedeemPurchaseSettings(values map[string]string) RedeemPurchaseSettings {
	return parseRedeemPurchaseSettings(values)
}

func parseRedeemPurchaseSettings(values map[string]string) RedeemPurchaseSettings {
	return RedeemPurchaseSettings{
		Enabled:         values[SettingKeyRedeemPurchaseEnabled] == "true",
		MinAmount:       parseRedeemPurchaseAmountSetting(values[SettingKeyRedeemPurchaseMinAmount], DefaultRedeemPurchaseMinAmount),
		MaxAmount:       parseRedeemPurchaseAmountSetting(values[SettingKeyRedeemPurchaseMaxAmount], DefaultRedeemPurchaseMaxAmount),
		WindowMaxAmount: parseRedeemPurchaseAmountSetting(values[SettingKeyRedeemPurchaseWindowMaxAmount], DefaultRedeemPurchaseWindowMaxAmount),
		WindowMaxCount:  parseRedeemPurchaseWindowMaxCount(values[SettingKeyRedeemPurchaseWindowMaxCount]),
	}
}

// ValidRedeemPurchaseAmountSetting: 0 ≤ v ≤ MaxRedeemPurchaseAmountSetting
// with at most two decimal places.
func ValidRedeemPurchaseAmountSetting(v decimal.Decimal) bool {
	return !v.IsNegative() && !v.GreaterThan(MaxRedeemPurchaseAmountSetting) && v.Equal(v.Truncate(2))
}

// parseRedeemPurchaseAmountSetting: missing / unparsable / out of range /
// finer than a cent → the default. A bad stored value never widens a limit
// beyond MaxRedeemPurchaseAmountSetting.
func parseRedeemPurchaseAmountSetting(raw string, def decimal.Decimal) decimal.Decimal {
	v, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil || !ValidRedeemPurchaseAmountSetting(v) {
		return def
	}
	return v
}

// parseRedeemPurchaseWindowMaxCount: missing / invalid / negative / above the
// cap → the default. 0 is a valid value (no purchases).
func parseRedeemPurchaseWindowMaxCount(raw string) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v < 0 || v > MaxRedeemPurchaseWindowMaxCount {
		return DefaultRedeemPurchaseWindowMaxCount
	}
	return v
}

// applyRedeemPurchaseSystemSettings copies the stored switches into the admin
// settings view (amounts as float64 for the JSON API; every value has at most
// two decimals, so the conversion is exact in its shortest form).
func applyRedeemPurchaseSystemSettings(out *SystemSettings, values map[string]string) {
	cfg := parseRedeemPurchaseSettings(values)
	out.RedeemPurchaseEnabled = cfg.Enabled
	out.RedeemPurchaseMinAmount = cfg.MinAmount.InexactFloat64()
	out.RedeemPurchaseMaxAmount = cfg.MaxAmount.InexactFloat64()
	out.RedeemPurchaseWindowMaxAmount = cfg.WindowMaxAmount.InexactFloat64()
	out.RedeemPurchaseWindowMaxCount = cfg.WindowMaxCount
}

// redeemPurchaseSettingUpdates writes the admin settings back, re-validated:
// an invalid value is stored as its default.
func redeemPurchaseSettingUpdates(settings *SystemSettings, updates map[string]string) {
	amount := func(v float64, def decimal.Decimal) string {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return def.String()
		}
		return parseRedeemPurchaseAmountSetting(decimal.NewFromFloat(v).String(), def).String()
	}
	updates[SettingKeyRedeemPurchaseEnabled] = strconv.FormatBool(settings.RedeemPurchaseEnabled)
	updates[SettingKeyRedeemPurchaseMinAmount] = amount(settings.RedeemPurchaseMinAmount, DefaultRedeemPurchaseMinAmount)
	updates[SettingKeyRedeemPurchaseMaxAmount] = amount(settings.RedeemPurchaseMaxAmount, DefaultRedeemPurchaseMaxAmount)
	updates[SettingKeyRedeemPurchaseWindowMaxAmount] = amount(settings.RedeemPurchaseWindowMaxAmount, DefaultRedeemPurchaseWindowMaxAmount)
	updates[SettingKeyRedeemPurchaseWindowMaxCount] = strconv.Itoa(parseRedeemPurchaseWindowMaxCount(strconv.Itoa(settings.RedeemPurchaseWindowMaxCount)))
}
