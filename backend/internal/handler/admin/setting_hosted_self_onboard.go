package admin

import (
	"fmt"
	"math"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// resolveHostedSelfOnboardSettings keeps every stored value whose field is
// absent from the request (admin clients that predate the feature) and
// validates the merged result. A non-empty string is the 400 message.
func resolveHostedSelfOnboardSettings(req *UpdateSettingsRequest, prev *service.SystemSettings) (service.HostedSelfOnboardSettings, string) {
	v := prev.HostedSelfOnboard
	if req.HostedSelfOnboardEnabled != nil {
		v.Enabled = *req.HostedSelfOnboardEnabled
	}
	if req.HostedSelfOnboardExchangeProxyID != nil {
		if *req.HostedSelfOnboardExchangeProxyID < 0 {
			return v, "hosted_self_onboard_exchange_proxy_id must be 0 (none) or a proxy id"
		}
		v.ExchangeProxyID = *req.HostedSelfOnboardExchangeProxyID
	}
	if req.HostedSelfFeeRate != nil {
		rate := *req.HostedSelfFeeRate
		if math.IsNaN(rate) || !service.ValidHostedSelfFeeRate(rate) {
			return v, fmt.Sprintf("hosted_self_fee_rate must be between 0 and %g with at most 4 decimal places", service.MaxHostedSelfFeeRate)
		}
		v.FeeRate = rate
	}
	ints := []struct {
		name   string
		in     *int
		out    *int
		lo, hi int
	}{
		{"hosted_self_onboard_default_concurrency", req.HostedSelfOnboardDefaultConcurrency, &v.DefaultConcurrency, 1, service.MaxHostedSelfOnboardConcurrency},
		{"hosted_self_onboard_default_priority", req.HostedSelfOnboardDefaultPriority, &v.DefaultPriority, 0, service.MaxHostedSelfOnboardPriority},
		{"hosted_self_onboard_default_device_limit", req.HostedSelfOnboardDefaultDeviceLimit, &v.DefaultDeviceLimit, 1, service.MaxHostedSelfOnboardCarpoolDeviceLimitValue},
		{"hosted_self_onboard_default_interval_hours", req.HostedSelfOnboardDefaultIntervalHours, &v.DefaultIntervalHours, 1, service.MaxHostedSelfOnboardCarpoolIntervalHours},
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
	if req.HostedSelfOnboardDefaultAdmissionsPerCycle != nil {
		v.DefaultAdmissionsPerCycle = *req.HostedSelfOnboardDefaultAdmissionsPerCycle
	}
	if v.DefaultAdmissionsPerCycle < 0 || v.DefaultAdmissionsPerCycle > v.DefaultDeviceLimit {
		return v, "hosted_self_onboard_default_admissions_per_cycle must be between 0 and hosted_self_onboard_default_device_limit"
	}
	return v, ""
}

func diffHostedSelfOnboardSettings(before, after *service.SystemSettings) []string {
	b, a := before.HostedSelfOnboard, after.HostedSelfOnboard
	var changed []string
	if b.Enabled != a.Enabled {
		changed = append(changed, "hosted_self_onboard_enabled")
	}
	if b.ExchangeProxyID != a.ExchangeProxyID {
		changed = append(changed, "hosted_self_onboard_exchange_proxy_id")
	}
	if b.FeeRate != a.FeeRate {
		changed = append(changed, "hosted_self_fee_rate")
	}
	if b.DefaultConcurrency != a.DefaultConcurrency {
		changed = append(changed, "hosted_self_onboard_default_concurrency")
	}
	if b.DefaultPriority != a.DefaultPriority {
		changed = append(changed, "hosted_self_onboard_default_priority")
	}
	if b.DefaultDeviceLimit != a.DefaultDeviceLimit {
		changed = append(changed, "hosted_self_onboard_default_device_limit")
	}
	if b.DefaultAdmissionsPerCycle != a.DefaultAdmissionsPerCycle {
		changed = append(changed, "hosted_self_onboard_default_admissions_per_cycle")
	}
	if b.DefaultIntervalHours != a.DefaultIntervalHours {
		changed = append(changed, "hosted_self_onboard_default_interval_hours")
	}
	return changed
}
