package service

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// Sharing market settings (P3a). Default: switch off. A missing or invalid
// stored value means its default; an explicit 0 is a valid value wherever
// the range includes 0 (it never falls back to the default).
const (
	SettingKeyMarketEnabled           = "market_enabled"
	SettingKeyMarketPlatformCutRate   = "market_platform_cut_rate"
	SettingKeyMarketEarningFreezeDays = "market_earning_freeze_days"
	SettingKeyMarketMinConvertUSD     = "market_min_convert_usd"
	// SettingKeyMarketModeSwitchCooldownHours: minimum hours between two
	// effective owner switches 共享 ↔ 自用 (0..168, default 24, 0 = none).
	SettingKeyMarketModeSwitchCooldownHours     = "market_mode_switch_cooldown_hours"
	MaxMarketEarningFreezeDays                  = 90
	DefaultMarketEarningFreezeDays              = 7
	marketPlatformCutRateDecimals               = 4
	marketMinConvertDecimals                    = 2
	marketSettingDefaultPlatformCutRateText     = "0.2"
	marketSettingDefaultMinConvertUSDText       = "1"
	marketSettingMaxMinConvertUSDText           = "10000"
	marketSettingSmallestMinConvertUSDTextValue = "0.01"
)

var (
	DefaultMarketPlatformCutRate = decimal.RequireFromString(marketSettingDefaultPlatformCutRateText)
	DefaultMarketMinConvertUSD   = decimal.RequireFromString(marketSettingDefaultMinConvertUSDText)
	MaxMarketMinConvertUSD       = decimal.RequireFromString(marketSettingMaxMinConvertUSDText)
	MinMarketMinConvertUSD       = decimal.RequireFromString(marketSettingSmallestMinConvertUSDTextValue)
)

// MarketSettings is the parsed settings block.
type MarketSettings struct {
	Enabled bool
	// PlatformCutRate: share of the actual charge kept by the platform,
	// 0 ≤ rate ≤ 1 with at most 4 decimals.
	PlatformCutRate decimal.Decimal
	// EarningFreezeDays: 0..90.
	EarningFreezeDays int
	// MinConvertUSD: smallest earnings → balance conversion, 0.01..10000,
	// at most 2 decimals.
	MinConvertUSD decimal.Decimal
	// ModeSwitchCooldownHours: 0..168 (0 = no cooldown).
	ModeSwitchCooldownHours int
}

// MarketSettingKeys are the settings rows of the feature.
func MarketSettingKeys() []string {
	return []string{
		SettingKeyMarketEnabled,
		SettingKeyMarketPlatformCutRate,
		SettingKeyMarketEarningFreezeDays,
		SettingKeyMarketMinConvertUSD,
		SettingKeyMarketModeSwitchCooldownHours,
	}
}

// ValidMarketPlatformCutRate: 0 ≤ v ≤ 1 with at most 4 decimal places.
func ValidMarketPlatformCutRate(v decimal.Decimal) bool {
	return !v.IsNegative() && !v.GreaterThan(decimal.NewFromInt(1)) && v.Equal(v.Truncate(marketPlatformCutRateDecimals))
}

// ValidMarketMinConvertUSD: 0.01 ≤ v ≤ 10000 with at most 2 decimal places.
func ValidMarketMinConvertUSD(v decimal.Decimal) bool {
	return !v.LessThan(MinMarketMinConvertUSD) && !v.GreaterThan(MaxMarketMinConvertUSD) && v.Equal(v.Truncate(marketMinConvertDecimals))
}

// MarketDecimalFromFloat converts an API float exactly as written (shortest
// representation), so 0.12345 stays 5 decimals and is rejected, and 1.00001
// stays above 1. Non-finite values are rejected.
func MarketDecimalFromFloat(v float64) (decimal.Decimal, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return decimal.Zero, false
	}
	return decimal.NewFromFloat(v), true
}

// ParseMarketSettings turns stored values into settings. The removed
// reservation defaults (market_default_reserved_*) are not read.
func ParseMarketSettings(values map[string]string) MarketSettings {
	out := MarketSettings{
		Enabled:           values[SettingKeyMarketEnabled] == "true",
		PlatformCutRate:   DefaultMarketPlatformCutRate,
		EarningFreezeDays: parseBoundedIntSetting(values[SettingKeyMarketEarningFreezeDays], 0, MaxMarketEarningFreezeDays, DefaultMarketEarningFreezeDays),
		MinConvertUSD:     DefaultMarketMinConvertUSD,
		ModeSwitchCooldownHours: parseBoundedIntSetting(values[SettingKeyMarketModeSwitchCooldownHours], 0,
			MaxMarketModeSwitchCooldownHours, DefaultMarketModeSwitchCooldownHours),
	}
	if v, err := decimal.NewFromString(strings.TrimSpace(values[SettingKeyMarketPlatformCutRate])); err == nil && ValidMarketPlatformCutRate(v) {
		out.PlatformCutRate = v
	}
	if v, err := decimal.NewFromString(strings.TrimSpace(values[SettingKeyMarketMinConvertUSD])); err == nil && ValidMarketMinConvertUSD(v) {
		out.MinConvertUSD = v
	}
	return out
}

func applyMarketSystemSettings(out *SystemSettings, values map[string]string) {
	parsed := ParseMarketSettings(values)
	out.Market = &parsed
}

// marketSettingUpdates writes the settings back, re-validated: an invalid
// value is stored as its default. A nil block (a caller that did not load
// the market settings) writes nothing, so it can never reset them.
func marketSettingUpdates(settings *SystemSettings, updates map[string]string) {
	if settings.Market == nil {
		return
	}
	defer restrictMarketUpdates(settings.MarketFields, updates)
	in := *settings.Market
	stored := map[string]string{
		SettingKeyMarketEnabled:                 strconv.FormatBool(in.Enabled),
		SettingKeyMarketPlatformCutRate:         in.PlatformCutRate.String(),
		SettingKeyMarketEarningFreezeDays:       strconv.Itoa(in.EarningFreezeDays),
		SettingKeyMarketMinConvertUSD:           in.MinConvertUSD.String(),
		SettingKeyMarketModeSwitchCooldownHours: strconv.Itoa(in.ModeSwitchCooldownHours),
	}
	parsed := ParseMarketSettings(stored)
	updates[SettingKeyMarketEnabled] = strconv.FormatBool(parsed.Enabled)
	updates[SettingKeyMarketPlatformCutRate] = parsed.PlatformCutRate.String()
	updates[SettingKeyMarketEarningFreezeDays] = strconv.Itoa(parsed.EarningFreezeDays)
	updates[SettingKeyMarketMinConvertUSD] = parsed.MinConvertUSD.String()
	updates[SettingKeyMarketModeSwitchCooldownHours] = strconv.Itoa(parsed.ModeSwitchCooldownHours)
}

// restrictMarketUpdates keeps only the market keys the caller provided
// (MarketFields; nil = every key), so a PUT that changes one market field
// cannot write back a stale value of another (e.g. the switch).
func restrictMarketUpdates(fields []string, updates map[string]string) {
	if fields == nil {
		return
	}
	for _, key := range MarketSettingKeys() {
		if !slices.Contains(fields, key) {
			delete(updates, key)
		}
	}
}
