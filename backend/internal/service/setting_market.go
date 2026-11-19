package service

import (
	"math"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// Sharing market settings (P3a). Default: switch off. A missing or invalid
// stored value means its default; an explicit 0 is a valid value wherever
// the range includes 0 (it never falls back to the default).
const (
	SettingKeyMarketEnabled                     = "market_enabled"
	SettingKeyMarketPlatformCutRate             = "market_platform_cut_rate"
	SettingKeyMarketEarningFreezeDays           = "market_earning_freeze_days"
	SettingKeyMarketMinConvertUSD               = "market_min_convert_usd"
	SettingKeyMarketDefaultReservedConcurrency  = "market_default_reserved_concurrency"
	SettingKeyMarketDefaultReservedDevices      = "market_default_reserved_devices"
	SettingKeyMarketDefaultReservedAdmissions   = "market_default_reserved_admissions"
	SettingKeyMarketDefaultCap5hPct             = "market_default_cap_5h_pct"
	SettingKeyMarketDefaultCap7dPct             = "market_default_cap_7d_pct"
	SettingKeyMarketDefaultSampleMaxAgeMinutes  = "market_default_sample_max_age_minutes"
	MaxMarketEarningFreezeDays                  = 90
	DefaultMarketEarningFreezeDays              = 7
	DefaultMarketDefaultReservedConcurrency     = 1
	DefaultMarketDefaultReservedDevices         = 2
	DefaultMarketDefaultReservedAdmissions      = 1
	DefaultMarketDefaultCap5hPct                = 70
	DefaultMarketDefaultCap7dPct                = 60
	DefaultMarketDefaultSampleMaxAgeMinutes     = 30
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
	// DefaultProtection seeds new listings.
	DefaultProtection MarketProtectionParams
}

// MarketSettingKeys are the settings rows of the feature.
func MarketSettingKeys() []string {
	return []string{
		SettingKeyMarketEnabled,
		SettingKeyMarketPlatformCutRate,
		SettingKeyMarketEarningFreezeDays,
		SettingKeyMarketMinConvertUSD,
		SettingKeyMarketDefaultReservedConcurrency,
		SettingKeyMarketDefaultReservedDevices,
		SettingKeyMarketDefaultReservedAdmissions,
		SettingKeyMarketDefaultCap5hPct,
		SettingKeyMarketDefaultCap7dPct,
		SettingKeyMarketDefaultSampleMaxAgeMinutes,
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

// ParseMarketSettings turns stored values into settings.
func ParseMarketSettings(values map[string]string) MarketSettings {
	out := MarketSettings{
		Enabled:           values[SettingKeyMarketEnabled] == "true",
		PlatformCutRate:   DefaultMarketPlatformCutRate,
		EarningFreezeDays: parseBoundedIntSetting(values[SettingKeyMarketEarningFreezeDays], 0, MaxMarketEarningFreezeDays, DefaultMarketEarningFreezeDays),
		MinConvertUSD:     DefaultMarketMinConvertUSD,
		DefaultProtection: MarketProtectionParams{
			ReservedConcurrency: parseBoundedIntSetting(values[SettingKeyMarketDefaultReservedConcurrency], 0, MaxMarketReservedConcurrency, DefaultMarketDefaultReservedConcurrency),
			ReservedDevices:     parseBoundedIntSetting(values[SettingKeyMarketDefaultReservedDevices], 0, MaxMarketReservedDevices, DefaultMarketDefaultReservedDevices),
			ReservedAdmissions:  parseBoundedIntSetting(values[SettingKeyMarketDefaultReservedAdmissions], 0, MaxMarketReservedAdmissions, DefaultMarketDefaultReservedAdmissions),
			Cap5hPct:            parseBoundedIntSetting(values[SettingKeyMarketDefaultCap5hPct], 1, 100, DefaultMarketDefaultCap5hPct),
			Cap7dPct:            parseBoundedIntSetting(values[SettingKeyMarketDefaultCap7dPct], 1, 100, DefaultMarketDefaultCap7dPct),
			SampleMaxAgeMinutes: parseBoundedIntSetting(values[SettingKeyMarketDefaultSampleMaxAgeMinutes], 1, MaxMarketSampleMaxAgeMinutes, DefaultMarketDefaultSampleMaxAgeMinutes),
		},
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
	in := *settings.Market
	stored := map[string]string{
		SettingKeyMarketEnabled:                    strconv.FormatBool(in.Enabled),
		SettingKeyMarketPlatformCutRate:            in.PlatformCutRate.String(),
		SettingKeyMarketEarningFreezeDays:          strconv.Itoa(in.EarningFreezeDays),
		SettingKeyMarketMinConvertUSD:              in.MinConvertUSD.String(),
		SettingKeyMarketDefaultReservedConcurrency: strconv.Itoa(in.DefaultProtection.ReservedConcurrency),
		SettingKeyMarketDefaultReservedDevices:     strconv.Itoa(in.DefaultProtection.ReservedDevices),
		SettingKeyMarketDefaultReservedAdmissions:  strconv.Itoa(in.DefaultProtection.ReservedAdmissions),
		SettingKeyMarketDefaultCap5hPct:            strconv.Itoa(in.DefaultProtection.Cap5hPct),
		SettingKeyMarketDefaultCap7dPct:            strconv.Itoa(in.DefaultProtection.Cap7dPct),
		SettingKeyMarketDefaultSampleMaxAgeMinutes: strconv.Itoa(in.DefaultProtection.SampleMaxAgeMinutes),
	}
	parsed := ParseMarketSettings(stored)
	updates[SettingKeyMarketEnabled] = strconv.FormatBool(parsed.Enabled)
	updates[SettingKeyMarketPlatformCutRate] = parsed.PlatformCutRate.String()
	updates[SettingKeyMarketEarningFreezeDays] = strconv.Itoa(parsed.EarningFreezeDays)
	updates[SettingKeyMarketMinConvertUSD] = parsed.MinConvertUSD.String()
	updates[SettingKeyMarketDefaultReservedConcurrency] = strconv.Itoa(parsed.DefaultProtection.ReservedConcurrency)
	updates[SettingKeyMarketDefaultReservedDevices] = strconv.Itoa(parsed.DefaultProtection.ReservedDevices)
	updates[SettingKeyMarketDefaultReservedAdmissions] = strconv.Itoa(parsed.DefaultProtection.ReservedAdmissions)
	updates[SettingKeyMarketDefaultCap5hPct] = strconv.Itoa(parsed.DefaultProtection.Cap5hPct)
	updates[SettingKeyMarketDefaultCap7dPct] = strconv.Itoa(parsed.DefaultProtection.Cap7dPct)
	updates[SettingKeyMarketDefaultSampleMaxAgeMinutes] = strconv.Itoa(parsed.DefaultProtection.SampleMaxAgeMinutes)
}
