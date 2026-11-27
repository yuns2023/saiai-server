package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Priority/Fast requests above the long-context threshold pay the published
// Fast long-context prices (Fast price x the same whole-request multipliers).
func TestBundledPricing_PriorityAppliesLongContext(t *testing.T) {
	svc := bundledPricingBilling(t)
	tokens := UsageTokens{InputTokens: 806, OutputTokens: 165, CacheReadTokens: 592128}
	priority, err := svc.CalculateCostWithServiceTier("gpt-6-sol", tokens, 1, "priority")
	require.NoError(t, err)
	require.True(t, priority.PricingSnapshot.LongContext.Applied)
	require.InDelta(t, 806*8e-6, priority.InputCost, 1e-12)
	require.InDelta(t, 165*30e-6, priority.OutputCost, 1e-12)
	require.InDelta(t, 592128*0.8e-6, priority.CacheReadCost, 1e-12)

	standard, err := svc.CalculateCostWithServiceTier("gpt-6-sol", tokens, 1, "")
	require.NoError(t, err)
	require.True(t, standard.PricingSnapshot.LongContext.Applied)
	require.InDelta(t, standard.TotalCost*2, priority.TotalCost, 1e-12)
	flex, err := svc.CalculateCostWithServiceTier("gpt-6-sol", tokens, 1, "flex")
	require.NoError(t, err)
	require.InDelta(t, standard.TotalCost*0.5, flex.TotalCost, 1e-12)

	pricing, err := svc.GetModelPricing("gpt-6-sol")
	require.NoError(t, err)
	long := svc.DisplayLongContextUnitPrices(pricing, "priority")
	require.InDelta(t, 8, long["input"], 1e-9)
	require.InDelta(t, 30, long["output"], 1e-9)
	require.InDelta(t, 0.8, long["cache_read"], 1e-9)
}

// The threshold counts net input + cache read + cache write once; output does
// not count. 272000 is not long context, 272001 is.
func TestPriorityLongContextThresholdCountsCacheWrite(t *testing.T) {
	svc := bundledPricingBilling(t)
	at := UsageTokens{InputTokens: 1000, OutputTokens: 5000, CacheReadTokens: 271000}
	cost, err := svc.CalculateCostWithServiceTier("gpt-6-sol", at, 1, "priority")
	require.NoError(t, err)
	require.False(t, cost.PricingSnapshot.LongContext.Applied)
	require.Equal(t, 272000, cost.PricingSnapshot.LongContext.TotalInputTokens)
	require.InDelta(t, 1000*4e-6, cost.InputCost, 1e-12)

	over := at
	over.CacheCreationTokens, over.CacheCreation5mTokens = 1, 1
	cost, err = svc.CalculateCostWithServiceTier("gpt-6-sol", over, 1, "priority")
	require.NoError(t, err)
	require.True(t, cost.PricingSnapshot.LongContext.Applied)
	require.Equal(t, 272001, cost.PricingSnapshot.LongContext.TotalInputTokens)
	require.InDelta(t, 1000*8e-6, cost.InputCost, 1e-12)
	require.InDelta(t, 5000*30e-6, cost.OutputCost, 1e-12)
	require.InDelta(t, cost.CacheCreation5mCost+cost.CacheCreation1hCost, cost.CacheCreationCost, 1e-15)
}

// A category without a published long-context price keeps its short price in
// every tier instead of becoming free above the threshold.
func TestLongContextMissingCategoryKeepsShortPrice(t *testing.T) {
	p, b := testModelPricing(t)
	data, err := p.parsePricingData([]byte(`{"lc-partial":{"input_cost_per_token":0.000002,"input_cost_per_token_above_272k_tokens":0.000004,
		"output_cost_per_token":0.00001,"output_cost_per_token_above_272k_tokens":0.000015,"cache_read_input_token_cost":0.0000002,
		"input_cost_per_token_priority":0.000004,"output_cost_per_token_priority":0.00002,"cache_read_input_token_cost_priority":0.0000004,"mode":"chat"}}`))
	require.NoError(t, err)
	p.pricingData["lc-partial"] = data["lc-partial"]
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 300000}
	for tier, cacheRead := range map[string]float64{"default": 0.2e-6, "priority": 0.4e-6, "flex": 0.1e-6} {
		cost, err := b.CalculateCostWithServiceTier("lc-partial", tokens, 1, tier)
		require.NoError(t, err, tier)
		require.True(t, cost.PricingSnapshot.LongContext.Applied, tier)
		require.InDelta(t, 300000*cacheRead, cost.CacheReadCost, 1e-12, tier)
	}
}

// An administrator zero Priority price stays zero above the threshold, other
// categories still pay the long-context price, and the override does not leak
// to another alias of the same model.
func TestPriorityLongContextZeroOverrideAndAlias(t *testing.T) {
	svc := bundledPricingBilling(t)
	svc.pricingService.SetModelPricingConfig(map[string]string{"sol-a": "gpt-6-sol", "sol-b": "gpt-6-sol"},
		map[string]ModelPriceOverride{"sol-a": {PriorityCacheRead: priceTestNumber(0)}})
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 300000}
	a, err := svc.CalculateCostWithServiceTier("sol-a", tokens, 1, "priority")
	require.NoError(t, err)
	require.True(t, a.PricingSnapshot.LongContext.Applied)
	require.Zero(t, a.CacheReadCost)
	require.InDelta(t, 1000*8e-6, a.InputCost, 1e-12)
	b, err := svc.CalculateCostWithServiceTier("sol-b", tokens, 1, "priority")
	require.NoError(t, err)
	require.InDelta(t, 300000*0.8e-6, b.CacheReadCost, 1e-12)
	require.InDelta(t, b.TotalCost, a.PricingSnapshot.ReferenceTotalCost, 1e-12)
}

// Settlement (shared by HTTP and WebSocket) bills Fast long context.
func TestOpenAIGatewayServiceRecordUsage_PriorityLongContext(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	serviceTier := "priority"
	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{RequestID: "resp_priority_long", ServiceTier: &serviceTier, Model: "gpt-5.4", Duration: time.Second,
			Usage: OpenAIUsage{InputTokens: 300000, OutputTokens: 50, CacheReadInputTokens: 280000}},
		APIKey: &APIKey{ID: 1017}, User: &User{ID: 2017}, Account: &Account{ID: 3017},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	want, err := svc.billingService.CalculateCostWithServiceTier("gpt-5.4", UsageTokens{InputTokens: 20000, OutputTokens: 50, CacheReadTokens: 280000}, 1, "priority")
	require.NoError(t, err)
	require.True(t, want.PricingSnapshot.LongContext.Applied)
	short, err := svc.billingService.CalculateCostWithServiceTier("gpt-5.4", UsageTokens{InputTokens: 20000, OutputTokens: 50, CacheReadTokens: 280000 - 30000}, 1, "priority")
	require.NoError(t, err)
	require.False(t, short.PricingSnapshot.LongContext.Applied)
	require.InDelta(t, want.TotalCost, usageRepo.lastLog.TotalCost, 1e-10)
	require.Greater(t, usageRepo.lastLog.InputCost, short.InputCost*1.99)
}
