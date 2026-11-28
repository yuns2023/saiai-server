package dto

import "github.com/Wei-Shaw/sub2api/internal/service"

// MarketSettings are the sharing-market admin settings, embedded
// (flattened) into SystemSettings. The reservation defaults were removed
// (reservations are always 0).
type MarketSettings struct {
	MarketEnabled                 bool    `json:"market_enabled"`
	MarketPlatformCutRate         float64 `json:"market_platform_cut_rate"`
	MarketEarningFreezeDays       int     `json:"market_earning_freeze_days"`
	MarketMinConvertUSD           float64 `json:"market_min_convert_usd"`
	MarketModeSwitchCooldownHours int     `json:"market_mode_switch_cooldown_hours"`
}

// MarketSettingsFromService converts the parsed block (defaults when the
// block was not loaded). Amounts have at most 4 decimals, so the float is
// exact in its shortest form.
func MarketSettingsFromService(s *service.SystemSettings) MarketSettings {
	in := service.ParseMarketSettings(nil)
	if s != nil && s.Market != nil {
		in = *s.Market
	}
	return MarketSettings{
		MarketEnabled:                 in.Enabled,
		MarketPlatformCutRate:         in.PlatformCutRate.InexactFloat64(),
		MarketEarningFreezeDays:       in.EarningFreezeDays,
		MarketMinConvertUSD:           in.MinConvertUSD.InexactFloat64(),
		MarketModeSwitchCooldownHours: in.ModeSwitchCooldownHours,
	}
}
