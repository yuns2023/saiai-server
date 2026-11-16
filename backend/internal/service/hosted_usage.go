package service

import (
	"math"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Hosted metering is an explicit policy for an exclusive OpenAI or Anthropic
// group whose upstream accounts belong to the users it serves. The group's
// rate multiplier is the hosting service fee: the charge is the standard price
// times that rate, with no user override, model multiplier or user discount
// (P0 rule hosted_self). A zero rate keeps the original zero-charge policy.
// A zero user/group multiplier alone never enables this policy.
func (g *Group) UsesHostedUsage() bool {
	return g != nil && g.HostedUsageEnabled && hostedUsagePlatform(g.Platform) &&
		g.IsExclusive && g.SubscriptionType == SubscriptionTypeStandard && validHostedFeeRate(g.RateMultiplier)
}

func validHostedFeeRate(rate float64) bool {
	return rate >= 0 && !math.IsNaN(rate) && !math.IsInf(rate, 0)
}

// HostedFeeRate is the service fee of a hosted group as a multiple of the
// standard price (0 for non-hosted groups).
func (g *Group) HostedFeeRate() float64 {
	if !g.UsesHostedUsage() {
		return 0
	}
	return g.RateMultiplier
}

// HostedChargeFree reports a hosted group with a zero fee: requests are never
// charged, so admission skips the wallet balance check. Because hosted fees
// ignore user overrides, this group-level answer is exact for every user.
func (g *Group) HostedChargeFree() bool {
	return g.UsesHostedUsage() && g.RateMultiplier == 0
}

// hostedUsagePlatform lists the platforms whose usage recording applies
// applyHostedUsage. Adding one requires wiring its RecordUsage path first.
func hostedUsagePlatform(platform string) bool {
	return platform == PlatformOpenAI || platform == PlatformAnthropic
}

func (g *Group) ValidateHostedUsage() error {
	if g != nil && g.HostedUsageEnabled && !g.UsesHostedUsage() {
		return infraerrors.BadRequest("INVALID_HOSTED_USAGE", "Hosted usage requires an exclusive standard OpenAI or Anthropic group with a finite non-negative billing multiplier (the hosting fee rate)")
	}
	if g.UsesHostedUsage() && (g.FallbackGroupID != nil || g.FallbackGroupIDOnInvalidRequest != nil) {
		return infraerrors.BadRequest("INVALID_HOSTED_USAGE", "Hosted groups cannot use fallback groups")
	}
	return nil
}

// applyHostedUsage freezes a per-request metering snapshot. The original model
// price remains in TotalCost (and in the returned metered cost used by Key
// limits); ActualCost is the hosting fee already computed with
// HostedFeeRate (zero for a free hosted group); the logged platform account
// cost is zero because the upstream account is the user's own. Other groups
// sharing the same upstream account are unaffected.
func applyHostedUsage(group *Group, cost *CostBreakdown, accountRate *float64) *float64 {
	if !group.UsesHostedUsage() || cost == nil {
		return nil
	}
	if group.HostedChargeFree() {
		cost.ActualCost = 0
	}
	if accountRate != nil {
		*accountRate = 0
	}
	metered := cost.TotalCost
	return &metered
}

func apiKeyUsageCost(p *postUsageBillingParams) float64 {
	if p == nil || p.Cost == nil {
		return 0
	}
	if p.MeteredCost != nil {
		return *p.MeteredCost
	}
	return p.Cost.ActualCost
}
