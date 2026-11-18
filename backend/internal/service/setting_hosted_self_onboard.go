package service

import (
	"math"
	"strconv"
	"strings"
)

// Hosted self-onboarding settings (P2c). Default: switch off.
const (
	SettingKeyHostedSelfOnboardEnabled                   = "hosted_self_onboard_enabled"
	SettingKeyHostedSelfOnboardExchangeProxyID           = "hosted_self_onboard_exchange_proxy_id"
	SettingKeyHostedSelfFeeRate                          = "hosted_self_fee_rate"
	SettingKeyHostedSelfOnboardDefaultConcurrency        = "hosted_self_onboard_default_concurrency"
	SettingKeyHostedSelfOnboardDefaultPriority           = "hosted_self_onboard_default_priority"
	SettingKeyHostedSelfOnboardDefaultDeviceLimit        = "hosted_self_onboard_default_device_limit"
	SettingKeyHostedSelfOnboardDefaultAdmissionsPerCycle = "hosted_self_onboard_default_admissions_per_cycle"
	SettingKeyHostedSelfOnboardDefaultIntervalHours      = "hosted_self_onboard_default_interval_hours"
)

const (
	DefaultHostedSelfFeeRate                    = 0.10
	DefaultHostedSelfOnboardConcurrency         = 3
	DefaultHostedSelfOnboardPriority            = 1
	DefaultHostedSelfOnboardDeviceLimit         = DefaultClaudeOAuthCarpoolDeviceLimit
	DefaultHostedSelfOnboardAdmissionsPerCycle  = 1
	DefaultHostedSelfOnboardIntervalHours       = 24 // = GetCarpoolMaintenanceIntervalHours default
	MaxHostedSelfFeeRate                        = 10.0
	MaxHostedSelfOnboardConcurrency             = 100
	MaxHostedSelfOnboardPriority                = 10000
	MaxHostedSelfOnboardCarpoolIntervalHours    = 720
	MaxHostedSelfOnboardCarpoolDeviceLimitValue = maxClaudeOAuthCarpoolDeviceLimit
)

// HostedSelfOnboardSettings is the parsed settings block. A missing or
// invalid stored value means its default.
type HostedSelfOnboardSettings struct {
	Enabled                   bool
	ExchangeProxyID           int64
	FeeRate                   float64
	DefaultConcurrency        int
	DefaultPriority           int
	DefaultDeviceLimit        int
	DefaultAdmissionsPerCycle int
	DefaultIntervalHours      int
}

// HostedSelfOnboardSettingKeys are the settings rows of the feature.
func HostedSelfOnboardSettingKeys() []string {
	return []string{
		SettingKeyHostedSelfOnboardEnabled,
		SettingKeyHostedSelfOnboardExchangeProxyID,
		SettingKeyHostedSelfFeeRate,
		SettingKeyHostedSelfOnboardDefaultConcurrency,
		SettingKeyHostedSelfOnboardDefaultPriority,
		SettingKeyHostedSelfOnboardDefaultDeviceLimit,
		SettingKeyHostedSelfOnboardDefaultAdmissionsPerCycle,
		SettingKeyHostedSelfOnboardDefaultIntervalHours,
	}
}

// ParseHostedSelfOnboardSettings turns stored values into settings.
func ParseHostedSelfOnboardSettings(values map[string]string) HostedSelfOnboardSettings {
	out := HostedSelfOnboardSettings{
		Enabled:                   values[SettingKeyHostedSelfOnboardEnabled] == "true",
		ExchangeProxyID:           0,
		FeeRate:                   DefaultHostedSelfFeeRate,
		DefaultConcurrency:        parseBoundedIntSetting(values[SettingKeyHostedSelfOnboardDefaultConcurrency], 1, MaxHostedSelfOnboardConcurrency, DefaultHostedSelfOnboardConcurrency),
		DefaultPriority:           parseBoundedIntSetting(values[SettingKeyHostedSelfOnboardDefaultPriority], 0, MaxHostedSelfOnboardPriority, DefaultHostedSelfOnboardPriority),
		DefaultDeviceLimit:        parseBoundedIntSetting(values[SettingKeyHostedSelfOnboardDefaultDeviceLimit], 1, MaxHostedSelfOnboardCarpoolDeviceLimitValue, DefaultHostedSelfOnboardDeviceLimit),
		DefaultAdmissionsPerCycle: DefaultHostedSelfOnboardAdmissionsPerCycle,
		DefaultIntervalHours:      parseBoundedIntSetting(values[SettingKeyHostedSelfOnboardDefaultIntervalHours], 1, MaxHostedSelfOnboardCarpoolIntervalHours, DefaultHostedSelfOnboardIntervalHours),
	}
	if id, err := strconv.ParseInt(strings.TrimSpace(values[SettingKeyHostedSelfOnboardExchangeProxyID]), 10, 64); err == nil && id > 0 {
		out.ExchangeProxyID = id
	}
	if rate, err := strconv.ParseFloat(strings.TrimSpace(values[SettingKeyHostedSelfFeeRate]), 64); err == nil && ValidHostedSelfFeeRate(rate) {
		out.FeeRate = rate
	}
	out.DefaultAdmissionsPerCycle = parseBoundedIntSetting(values[SettingKeyHostedSelfOnboardDefaultAdmissionsPerCycle], 0, out.DefaultDeviceLimit, minInt(DefaultHostedSelfOnboardAdmissionsPerCycle, out.DefaultDeviceLimit))
	return out
}

// ValidHostedSelfFeeRate: 0 ≤ rate ≤ MaxHostedSelfFeeRate, finite, at most
// four decimals (groups.rate_multiplier is decimal(10,4)).
func ValidHostedSelfFeeRate(rate float64) bool {
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 || rate > MaxHostedSelfFeeRate {
		return false
	}
	return math.Abs(rate*10000-math.Round(rate*10000)) <= 1e-7
}

func parseBoundedIntSetting(raw string, lo, hi, def int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v < lo || v > hi {
		return def
	}
	return v
}

func applyHostedSelfOnboardSystemSettings(out *SystemSettings, values map[string]string) {
	out.HostedSelfOnboard = ParseHostedSelfOnboardSettings(values)
}

// hostedSelfOnboardSettingUpdates writes the settings back, re-validated: an
// invalid value is stored as its default.
func hostedSelfOnboardSettingUpdates(settings *SystemSettings, updates map[string]string) {
	in := settings.HostedSelfOnboard
	stored := map[string]string{
		SettingKeyHostedSelfOnboardEnabled:                   strconv.FormatBool(in.Enabled),
		SettingKeyHostedSelfOnboardExchangeProxyID:           strconv.FormatInt(in.ExchangeProxyID, 10),
		SettingKeyHostedSelfFeeRate:                          strconv.FormatFloat(in.FeeRate, 'f', -1, 64),
		SettingKeyHostedSelfOnboardDefaultConcurrency:        strconv.Itoa(in.DefaultConcurrency),
		SettingKeyHostedSelfOnboardDefaultPriority:           strconv.Itoa(in.DefaultPriority),
		SettingKeyHostedSelfOnboardDefaultDeviceLimit:        strconv.Itoa(in.DefaultDeviceLimit),
		SettingKeyHostedSelfOnboardDefaultAdmissionsPerCycle: strconv.Itoa(in.DefaultAdmissionsPerCycle),
		SettingKeyHostedSelfOnboardDefaultIntervalHours:      strconv.Itoa(in.DefaultIntervalHours),
	}
	parsed := ParseHostedSelfOnboardSettings(stored)
	updates[SettingKeyHostedSelfOnboardEnabled] = strconv.FormatBool(parsed.Enabled)
	updates[SettingKeyHostedSelfOnboardExchangeProxyID] = strconv.FormatInt(parsed.ExchangeProxyID, 10)
	updates[SettingKeyHostedSelfFeeRate] = strconv.FormatFloat(parsed.FeeRate, 'f', -1, 64)
	updates[SettingKeyHostedSelfOnboardDefaultConcurrency] = strconv.Itoa(parsed.DefaultConcurrency)
	updates[SettingKeyHostedSelfOnboardDefaultPriority] = strconv.Itoa(parsed.DefaultPriority)
	updates[SettingKeyHostedSelfOnboardDefaultDeviceLimit] = strconv.Itoa(parsed.DefaultDeviceLimit)
	updates[SettingKeyHostedSelfOnboardDefaultAdmissionsPerCycle] = strconv.Itoa(parsed.DefaultAdmissionsPerCycle)
	updates[SettingKeyHostedSelfOnboardDefaultIntervalHours] = strconv.Itoa(parsed.DefaultIntervalHours)
}
