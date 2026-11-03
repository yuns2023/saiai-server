package service

import infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

// Hosted metering is an explicit, zero-charge policy for an exclusive OpenAI
// or Anthropic group. A zero user/group multiplier alone never enables this
// policy.
func (g *Group) UsesHostedUsage() bool {
	return g != nil && g.HostedUsageEnabled && hostedUsagePlatform(g.Platform) &&
		g.IsExclusive && g.SubscriptionType == SubscriptionTypeStandard && g.RateMultiplier == 0
}

// hostedUsagePlatform lists the platforms whose usage recording applies
// applyHostedUsage. Adding one requires wiring its RecordUsage path first.
func hostedUsagePlatform(platform string) bool {
	return platform == PlatformOpenAI || platform == PlatformAnthropic
}

func (g *Group) ValidateHostedUsage() error {
	if g != nil && g.HostedUsageEnabled && !g.UsesHostedUsage() {
		return infraerrors.BadRequest("INVALID_HOSTED_USAGE", "Hosted usage requires an exclusive standard OpenAI or Anthropic group with zero billing multiplier")
	}
	if g.UsesHostedUsage() && (g.FallbackGroupID != nil || g.FallbackGroupIDOnInvalidRequest != nil) {
		return infraerrors.BadRequest("INVALID_HOSTED_USAGE", "Hosted groups cannot use fallback groups")
	}
	return nil
}

// applyHostedUsage freezes a per-request metering snapshot. The original model
// price remains in TotalCost, while actual charges and platform account cost
// are zero. Other groups sharing the same upstream account are unaffected.
func applyHostedUsage(group *Group, cost *CostBreakdown, accountRate *float64) *float64 {
	if !group.UsesHostedUsage() || cost == nil {
		return nil
	}
	cost.ActualCost = 0
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
