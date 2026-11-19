package admin

import (
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// marketSettingsRequest fields are pointers: omitted keeps the stored value.
type marketSettingsRequest struct {
	MarketEnabled                    *bool    `json:"market_enabled"`
	MarketPlatformCutRate            *float64 `json:"market_platform_cut_rate"`
	MarketEarningFreezeDays          *int     `json:"market_earning_freeze_days"`
	MarketMinConvertUSD              *float64 `json:"market_min_convert_usd"`
	MarketDefaultReservedConcurrency *int     `json:"market_default_reserved_concurrency"`
	MarketDefaultReservedDevices     *int     `json:"market_default_reserved_devices"`
	MarketDefaultReservedAdmissions  *int     `json:"market_default_reserved_admissions"`
	MarketDefaultCap5hPct            *int     `json:"market_default_cap_5h_pct"`
	MarketDefaultCap7dPct            *int     `json:"market_default_cap_7d_pct"`
	MarketDefaultSampleMaxAgeMinutes *int     `json:"market_default_sample_max_age_minutes"`
}

func (r *marketSettingsRequest) present() bool {
	return len(r.providedKeys()) > 0
}

// providedKeys lists the setting keys present in the request.
func (r *marketSettingsRequest) providedKeys() []string {
	var keys []string
	add := func(set bool, key string) {
		if set {
			keys = append(keys, key)
		}
	}
	add(r.MarketEnabled != nil, service.SettingKeyMarketEnabled)
	add(r.MarketPlatformCutRate != nil, service.SettingKeyMarketPlatformCutRate)
	add(r.MarketEarningFreezeDays != nil, service.SettingKeyMarketEarningFreezeDays)
	add(r.MarketMinConvertUSD != nil, service.SettingKeyMarketMinConvertUSD)
	add(r.MarketDefaultReservedConcurrency != nil, service.SettingKeyMarketDefaultReservedConcurrency)
	add(r.MarketDefaultReservedDevices != nil, service.SettingKeyMarketDefaultReservedDevices)
	add(r.MarketDefaultReservedAdmissions != nil, service.SettingKeyMarketDefaultReservedAdmissions)
	add(r.MarketDefaultCap5hPct != nil, service.SettingKeyMarketDefaultCap5hPct)
	add(r.MarketDefaultCap7dPct != nil, service.SettingKeyMarketDefaultCap7dPct)
	add(r.MarketDefaultSampleMaxAgeMinutes != nil, service.SettingKeyMarketDefaultSampleMaxAgeMinutes)
	return keys
}

// resolveMarketSettings merges the request into the stored values and
// validates them. An explicit 0 is kept wherever 0 is in range (cut rate,
// freeze days, reservations). A non-empty string is the 400 message.
func resolveMarketSettings(req *marketSettingsRequest, prev *service.SystemSettings) (service.MarketSettings, string) {
	v := service.ParseMarketSettings(nil)
	if prev != nil && prev.Market != nil {
		v = *prev.Market
	}
	if req.MarketEnabled != nil {
		v.Enabled = *req.MarketEnabled
	}
	if req.MarketPlatformCutRate != nil {
		rate, ok := service.MarketDecimalFromFloat(*req.MarketPlatformCutRate)
		if !ok || !service.ValidMarketPlatformCutRate(rate) {
			return v, "market_platform_cut_rate must be between 0 and 1 with at most 4 decimal places"
		}
		v.PlatformCutRate = rate
	}
	if req.MarketMinConvertUSD != nil {
		amount, ok := service.MarketDecimalFromFloat(*req.MarketMinConvertUSD)
		if !ok || !service.ValidMarketMinConvertUSD(amount) {
			return v, fmt.Sprintf("market_min_convert_usd must be between %s and %s with at most 2 decimal places",
				service.MinMarketMinConvertUSD, service.MaxMarketMinConvertUSD)
		}
		v.MinConvertUSD = amount
	}
	ints := []struct {
		name   string
		in     *int
		out    *int
		lo, hi int
	}{
		{"market_earning_freeze_days", req.MarketEarningFreezeDays, &v.EarningFreezeDays, 0, service.MaxMarketEarningFreezeDays},
		{"market_default_reserved_concurrency", req.MarketDefaultReservedConcurrency, &v.DefaultProtection.ReservedConcurrency, 0, service.MaxMarketReservedConcurrency},
		{"market_default_reserved_devices", req.MarketDefaultReservedDevices, &v.DefaultProtection.ReservedDevices, 0, service.MaxMarketReservedDevices},
		{"market_default_reserved_admissions", req.MarketDefaultReservedAdmissions, &v.DefaultProtection.ReservedAdmissions, 0, service.MaxMarketReservedAdmissions},
		{"market_default_cap_5h_pct", req.MarketDefaultCap5hPct, &v.DefaultProtection.Cap5hPct, 1, 100},
		{"market_default_cap_7d_pct", req.MarketDefaultCap7dPct, &v.DefaultProtection.Cap7dPct, 1, 100},
		{"market_default_sample_max_age_minutes", req.MarketDefaultSampleMaxAgeMinutes, &v.DefaultProtection.SampleMaxAgeMinutes, 1, service.MaxMarketSampleMaxAgeMinutes},
	}
	for _, f := range ints {
		if f.in == nil {
			continue
		}
		if *f.in < f.lo || *f.in > f.hi {
			return v, fmt.Sprintf("%s must be between %d and %d", f.name, f.lo, f.hi)
		}
		*f.out = *f.in
	}
	return v, ""
}

func diffMarketSettings(before, after *service.SystemSettings) []string {
	// after is the written object: no market block = nothing written.
	if before == nil || before.Market == nil || after == nil || after.Market == nil {
		return nil
	}
	b, a := *before.Market, *after.Market
	var changed []string
	if b.Enabled != a.Enabled {
		changed = append(changed, "market_enabled")
	}
	if !b.PlatformCutRate.Equal(a.PlatformCutRate) {
		changed = append(changed, "market_platform_cut_rate")
	}
	if b.EarningFreezeDays != a.EarningFreezeDays {
		changed = append(changed, "market_earning_freeze_days")
	}
	if !b.MinConvertUSD.Equal(a.MinConvertUSD) {
		changed = append(changed, "market_min_convert_usd")
	}
	if b.DefaultProtection != a.DefaultProtection {
		changed = append(changed, "market_default_protection")
	}
	return changed
}
