package service

import (
	"encoding/json"
	"math"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func priceTestNumber(v float64) *float64 { return &v }
func testModelPricing(t *testing.T) (*PricingService, *BillingService) {
	t.Helper()
	p := NewPricingService(&config.Config{}, nil)
	data, err := p.parsePricingData([]byte(`{"claude-fable-5":{"input_cost_per_token":0.00001,"output_cost_per_token":0.00005,"cache_read_input_token_cost":0.000001,"cache_creation_input_token_cost":0.0000125,"cache_creation_input_token_cost_above_1hr":0.00002,"mode":"chat"}}`))
	require.NoError(t, err)
	p.pricingData = data
	p.localHash = "fixture"
	p.priceSource = "remote"
	return p, NewBillingService(&config.Config{}, p)
}
func TestModelPricingExactOverrideInheritsAliasAndPreservesOtherModels(t *testing.T) {
	p, b := testModelPricing(t)
	aliases := map[string]string{"alias-a": "claude-fable-5", "alias-b": "claude-fable-5"}
	p.SetModelPricingConfig(aliases, map[string]ModelPriceOverride{"alias-a": {CacheRead: priceTestNumber(0.25)}})
	cost, err := b.CalculateCost("alias-a", UsageTokens{InputTokens: 100, CacheReadTokens: 1000}, 1.5)
	require.NoError(t, err)
	require.InDelta(t, 0.00125, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.001875, cost.ActualCost, 1e-12)
	require.InDelta(t, 0.002, cost.PricingSnapshot.ReferenceTotalCost, 1e-12)
	require.Equal(t, "claude-fable-5", cost.PricingSnapshot.ResolvedModel)
	original, err := b.GetModelPricing("alias-b")
	require.NoError(t, err)
	require.Equal(t, 1e-6, original.CacheReadPricePerToken)
	// Later feed updates update inherited prices and cannot mutate old invoices.
	replacement := *p.pricingData["claude-fable-5"]
	replacement.InputCostPerToken = 20e-6
	p.mu.Lock()
	p.pricingData = map[string]*LiteLLMModelPricing{"claude-fable-5": &replacement}
	p.localHash = "fixture-v2"
	p.mu.Unlock()
	updated, err := b.GetModelPricing("alias-a")
	require.NoError(t, err)
	require.Equal(t, 20e-6, updated.InputPricePerToken)
	require.Equal(t, 0.25e-6, updated.CacheReadPricePerToken)
	require.Equal(t, 10e-6, cost.PricingSnapshot.Effective.InputPricePerToken)
	require.NotEqual(t, cost.PricingSnapshot.Version, updated.Snapshot.Version)
	body, err := json.Marshal(cost.PricingSnapshot)
	require.NoError(t, err)
	var snapshot PricingSnapshot
	require.NoError(t, json.Unmarshal(body, &snapshot))
	require.Equal(t, 0.25e-6, snapshot.Effective.CacheReadPricePerToken)
}
func TestModelPricingExplicitFreeAndEqualCacheWritePrices(t *testing.T) {
	p, b := testModelPricing(t)
	for _, price := range []float64{0, 3} {
		p.SetModelPricingConfig(nil, map[string]ModelPriceOverride{"claude-fable-5": {CacheRead: priceTestNumber(0), CacheWrite5m: &price, CacheWrite1h: &price}})
		cost, err := b.CalculateCost("claude-fable-5", UsageTokens{CacheReadTokens: 100, CacheCreationTokens: 50, CacheCreation5mTokens: 20, CacheCreation1hTokens: 30}, 1)
		require.NoError(t, err)
		require.Zero(t, cost.CacheReadCost)
		require.InDelta(t, float64(20)*price/1e6, cost.CacheCreation5mCost, 1e-12)
		require.InDelta(t, float64(30)*price/1e6, cost.CacheCreation1hCost, 1e-12)
	}
	p.SetModelPricingConfig(nil, nil)
	pricing, err := b.GetModelPricing("claude-fable-5")
	require.NoError(t, err)
	require.Equal(t, 1e-6, pricing.CacheReadPricePerToken)
}
func TestModelPricingFreePriorityPricesAndInheritedFallbackTier(t *testing.T) {
	p, b := testModelPricing(t)
	p.SetModelPricingConfig(nil, map[string]ModelPriceOverride{"claude-fable-5": {PriorityCacheRead: priceTestNumber(0)}})
	cost, err := b.CalculateCostWithServiceTier("claude-fable-5", UsageTokens{InputTokens: 100, OutputTokens: 100, CacheReadTokens: 100}, 1, "priority")
	require.NoError(t, err)
	require.InDelta(t, 0.002, cost.InputCost, 1e-12)
	require.InDelta(t, 0.01, cost.OutputCost, 1e-12)
	require.Zero(t, cost.CacheReadCost)
	require.InDelta(t, 0.0122, cost.PricingSnapshot.ReferenceTotalCost, 1e-12)
}
func TestModelPricingAliasRetainsTargetSpecificPolicies(t *testing.T) {
	p, b := testModelPricing(t)
	p.pricingData["gpt-5.5"] = &LiteLLMModelPricing{ResolvedModel: "gpt-5.5", InputCostPerToken: 5e-6, OutputCostPerToken: 30e-6, CacheReadInputTokenCost: 0.5e-6}
	p.SetModelPricingConfig(map[string]string{"custom-model": "gpt-5.5"}, nil)
	pricing, err := b.GetModelPricing("custom-model")
	require.NoError(t, err)
	require.Equal(t, 272000, pricing.LongContextInputThreshold)
	require.Equal(t, 12.5e-6, pricing.InputPricePerTokenPriority)
}
func TestModelPricingPreviewDoesNotMutateLiveConfiguration(t *testing.T) {
	p, b := testModelPricing(t)
	o := ModelPriceOverride{CacheRead: priceTestNumber(0)}
	alias := "claude-fable-5"
	preview, err := p.PreviewPricing("new-model", &alias, &o)
	require.NoError(t, err)
	cost, err := preview.CalculateCost("new-model", UsageTokens{CacheReadTokens: 100}, 1)
	require.NoError(t, err)
	require.Zero(t, cost.TotalCost)
	_, err = b.GetModelPricing("new-model")
	require.ErrorIs(t, err, ErrModelPricingUnavailable)
	require.Nil(t, p.modelOverrides["new-model"].CacheRead)
}
func TestModelPricingValidationAndImmutableConfig(t *testing.T) {
	for _, v := range []float64{-1, math.NaN(), math.Inf(1), 1000001} {
		require.Error(t, (ModelPriceOverride{CacheRead: &v}).Validate())
	}
	p, b := testModelPricing(t)
	v := 0.25
	o := map[string]ModelPriceOverride{"claude-fable-5": {CacheRead: &v}}
	p.SetModelPricingConfig(nil, o)
	v = 999
	price, err := b.GetModelPricing("claude-fable-5")
	require.NoError(t, err)
	require.Equal(t, 0.25e-6, price.CacheReadPricePerToken)
}
func TestModelPricingConcurrentReadsAndUpdates(t *testing.T) {
	p, b := testModelPricing(t)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				cost, err := b.CalculateCost("claude-fable-5", UsageTokens{InputTokens: 100, CacheReadTokens: 100}, 1)
				require.NoError(t, err)
				require.NotNil(t, cost.PricingSnapshot)
			}
		}()
	}
	for j := 0; j < 25; j++ {
		p.SetModelPricingConfig(nil, map[string]ModelPriceOverride{"claude-fable-5": {CacheRead: priceTestNumber(float64(j))}})
	}
	wg.Wait()
}

func TestModelPricingStaleVersionNeverPersistsAndReadersRemainAvailable(t *testing.T) {
	p, b := testModelPricing(t)
	old := p.ResolvePricing("claude-fable-5").Version
	p.SetModelAliases(nil)
	err := p.ApplyPricingConfiguration(old, nil, nil, func() error { t.Error("stale edit persisted"); return nil })
	require.ErrorIs(t, err, ErrPricingChanged)
	current := p.ResolvePricing("claude-fable-5").Version
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- p.ApplyPricingConfiguration(current, nil, map[string]ModelPriceOverride{"claude-fable-5": {CacheRead: priceTestNumber(0)}}, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	// Configuration persistence holds a writer guard, not the billing read lock.
	cost, err := b.CalculateCost("claude-fable-5", UsageTokens{CacheReadTokens: 1}, 1)
	require.NoError(t, err)
	require.Equal(t, 1e-6, cost.TotalCost)
	close(release)
	require.NoError(t, <-done)
	cost, err = b.CalculateCost("claude-fable-5", UsageTokens{CacheReadTokens: 1}, 1)
	require.NoError(t, err)
	require.Zero(t, cost.TotalCost)
}

func TestModelPricingLongContextEvidenceAndDisplayUseActualCalculator(t *testing.T) {
	p, b := testModelPricing(t)
	p.pricingData["gpt-6-astra"] = &LiteLLMModelPricing{InputCostPerToken: 10e-6, OutputCostPerToken: 50e-6, CacheReadInputTokenCost: 1e-6, CacheCreationInputTokenCost: 12.5e-6,
		LongContextInputTokenThreshold: 272000, LongContextInputCostMultiplier: 2, LongContextOutputCostMultiplier: 1.5, LongContextCacheReadCostMultiplier: 2, LongContextCacheCreationCostMultiplier: 2}
	p.SetModelPricingConfig(map[string]string{"alias-astra": "gpt-6-astra"}, map[string]ModelPriceOverride{"alias-astra": {CacheRead: priceTestNumber(0)}})
	pricing, err := b.GetModelPricing("alias-astra")
	require.NoError(t, err)
	long := b.DisplayLongContextUnitPrices(pricing, "default")
	require.InDelta(t, 20, long["input"], 1e-12)
	require.InDelta(t, 75, long["output"], 1e-12)
	require.Zero(t, long["cache_read"])
	require.InDelta(t, 25, long["cache_write_5m"], 1e-12)
	require.InDelta(t, 10, b.DisplayUnitPrices(pricing, "default")["input"], 1e-12)
	require.Nil(t, b.DisplayLongContextUnitPrices(pricing, "priority"))
	require.InDelta(t, 10, b.DisplayLongContextUnitPrices(pricing, "flex")["input"], 1e-12)
	for _, tier := range []string{"default", "flex", "priority"} {
		for _, total := range []int{272000, 272001} {
			tokens := UsageTokens{InputTokens: 1000, OutputTokens: 424, CacheReadTokens: total - 2000, CacheCreationTokens: 1000, CacheCreation5mTokens: 1000}
			cost, err := b.CalculateCostWithServiceTier("alias-astra", tokens, .4, tier)
			require.NoError(t, err)
			info := cost.PricingSnapshot.LongContext
			require.Equal(t, total, info.TotalInputTokens)
			require.Equal(t, total > 272000 && tier != "priority", info.Applied)
			require.Equal(t, "whole_request", info.Mode)
			require.Equal(t, 272000, info.Threshold)
			require.Equal(t, 1.5, info.OutputMultiplier)
			require.InDelta(t, cost.TotalCost*.4, cost.ActualCost, 1e-12)
			if info.Applied {
				pair := b.DisplayLongContextUnitPrices(pricing, tier)
				require.InDelta(t, pair["input"]*1000/1e6, cost.InputCost, 1e-12)
				require.InDelta(t, pair["output"]*424/1e6, cost.OutputCost, 1e-12)
			}
		}
	}
	cost, err := b.CalculateCost("alias-astra", UsageTokens{InputTokens: 272001}, 1)
	require.NoError(t, err)
	saved, err := json.Marshal(cost.PricingSnapshot)
	require.NoError(t, err)
	p.SetModelPricingConfig(nil, nil)
	var restored PricingSnapshot
	require.NoError(t, json.Unmarshal(saved, &restored))
	require.True(t, restored.LongContext.Applied)
	require.Zero(t, restored.Effective.CacheReadPricePerToken)
}

func TestModelPricingSplitLongContextEvidenceIsSeparateFromWholeRequest(t *testing.T) {
	_, b := testModelPricing(t)
	cost, err := b.CalculateCostWithLongContext("claude-fable-5", UsageTokens{InputTokens: 10000, CacheReadTokens: 210000}, 1, 200000, 2)
	require.NoError(t, err)
	info := cost.PricingSnapshot.LongContext
	require.True(t, info.Applied)
	require.Equal(t, "excess_input", info.Mode)
	require.Equal(t, 220000, info.TotalInputTokens)
	require.Equal(t, 1.0, info.OutputMultiplier)
	require.Equal(t, 1.0, info.CacheWriteMultiplier)
}
