package service

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func bundledPricingBilling(t *testing.T) *BillingService {
	t.Helper()
	p := NewPricingService(&config.Config{}, nil)
	require.NoError(t, p.loadPricingData(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json")))
	return NewBillingService(&config.Config{}, p)
}

func TestParsePricingData_RemovesPriorityForModelsWithoutOfficialPriorityTier(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"gpt-5-nano": {"input_cost_per_token": 5e-8, "input_cost_per_token_priority": 2.5e-6, "output_cost_per_token": 4e-7, "cache_read_input_token_cost": 5e-9, "litellm_provider": "openai", "mode": "chat"},
		"gpt-5-nano-2025-08-07": {"input_cost_per_token": 5e-8, "input_cost_per_token_priority": 2.5e-6, "output_cost_per_token_priority": 2e-5, "cache_read_input_token_cost_priority": 2.5e-7, "cache_creation_input_token_cost_priority": 1e-6, "output_cost_per_token": 4e-7, "litellm_provider": "openai", "mode": "chat"},
		"gpt-5-nano-20250807": {"input_cost_per_token": 5e-8, "input_cost_per_token_priority": 2.5e-6, "output_cost_per_token": 4e-7, "mode": "chat"},
		"azure/gpt-5-nano": {"input_cost_per_token": 5e-8, "input_cost_per_token_priority": 1e-7, "output_cost_per_token": 4e-7, "mode": "chat"},
		"gpt-5-nano-search": {"input_cost_per_token": 5e-8, "input_cost_per_token_priority": 1e-7, "output_cost_per_token": 4e-7, "mode": "chat"},
		"gpt-5-mini": {"input_cost_per_token": 2.5e-7, "input_cost_per_token_priority": 4.5e-7, "output_cost_per_token": 2e-6, "litellm_provider": "openai", "mode": "chat"}
	}`))
	require.NoError(t, err)

	for _, model := range []string{"gpt-5-nano", "gpt-5-nano-2025-08-07", "gpt-5-nano-20250807"} {
		p := data[model]
		require.NotNil(t, p, model)
		require.Zero(t, p.InputCostPerTokenPriority, model)
		require.Zero(t, p.OutputCostPerTokenPriority, model)
		require.Zero(t, p.CacheReadInputTokenCostPriority, model)
		require.Zero(t, p.CacheCreationInputTokenCostPriority, model)
		require.InDelta(t, 5e-8, p.InputCostPerToken, 1e-15, model)
		require.InDelta(t, 4e-7, p.OutputCostPerToken, 1e-15, model)
	}
	for model, want := range map[string]bool{"gpt-5-nano": true, "gpt-5-nano-2025-08-07": true, "gpt-5-nano-20250807": true, "azure/gpt-5-nano": false, "gpt-5-nano-search": false, "gpt-5-mini": false} {
		require.Equal(t, want, data[model].NoPriorityTier, model)
	}
	require.InDelta(t, 4.5e-7, data["gpt-5-mini"].InputCostPerTokenPriority, 1e-15)
	// Only bare OpenAI names and their dated snapshots are corrected.
	require.InDelta(t, 1e-7, data["azure/gpt-5-nano"].InputCostPerTokenPriority, 1e-15)
	require.InDelta(t, 1e-7, data["gpt-5-nano-search"].InputCostPerTokenPriority, 1e-15)
}

// Official OpenAI Priority ("Fast") text-token prices in USD per million tokens
// (input, cached input, output), from https://developers.openai.com/api/docs/pricing
// as of 2026-10-22. Cache-write Priority prices are checked where published.
func TestBundledPricing_PriorityMatchesOfficialFastPrices(t *testing.T) {
	svc := bundledPricingBilling(t)
	official := []struct {
		model                 string
		input, cached, output float64
		cacheWrite            float64 // 0: not published for this model
	}{
		{"gpt-6-astra", 20, 2, 100, 25},
		{"gpt-6.1-sol", 4, 0.2, 20, 5},
		{"gpt-6-sol", 4, 0.4, 20, 5},
		{"gpt-6-luna", 0.2, 0.02, 1, 0.25},
		{"gpt-5.6-sol", 8, 0.8, 40, 10},
		{"gpt-5.6-terra", 4, 0.4, 24, 5},
		{"gpt-5.6-luna", 0.4, 0.04, 2.4, 0.5},
		{"gpt-5.5", 12.5, 1.25, 75, 0},
		{"gpt-5.5-2026-04-23", 12.5, 1.25, 75, 0},
		{"gpt-5.4", 5, 0.5, 30, 0},
		{"gpt-5.4-mini", 1.5, 0.15, 9, 0},
		{"gpt-5.3-codex", 3.5, 0.35, 28, 0},
		{"gpt-5.2", 3.5, 0.35, 28, 0},
		{"gpt-5.1", 2.5, 0.25, 20, 0},
		{"gpt-5", 2.5, 0.25, 20, 0},
		{"gpt-5-mini", 0.45, 0.045, 3.6, 0},
		{"gpt-4.1", 3.5, 0.875, 14, 0},
		{"gpt-4.1-mini", 0.7, 0.175, 2.8, 0},
		{"gpt-4.1-nano", 0.2, 0.05, 0.8, 0},
		{"gpt-4o", 4.25, 2.125, 17, 0},
		{"gpt-4o-mini", 0.25, 0.125, 1, 0},
		{"o3", 3.5, 0.875, 14, 0},
		{"o4-mini", 2, 0.5, 8, 0},
	}
	for _, tc := range official {
		// The exact feed entry must carry the official prices itself; billing
		// policies (e.g. GPT-5.5's derived 2.5x Priority) must not mask drift.
		ref := svc.pricingService.pricingData[tc.model]
		require.NotNil(t, ref, tc.model)
		require.InDelta(t, tc.input*1e-6, ref.InputCostPerTokenPriority, 1e-15, tc.model)
		require.InDelta(t, tc.cached*1e-6, ref.CacheReadInputTokenCostPriority, 1e-15, tc.model)
		require.InDelta(t, tc.output*1e-6, ref.OutputCostPerTokenPriority, 1e-15, tc.model)
		if tc.cacheWrite > 0 {
			require.InDelta(t, tc.cacheWrite*1e-6, ref.CacheCreationInputTokenCostPriority, 1e-15, tc.model)
		}

		pricing, err := svc.GetModelPricing(tc.model)
		require.NoError(t, err, tc.model)
		require.InDelta(t, tc.input*1e-6, pricing.InputPricePerTokenPriority, 1e-15, tc.model)
		require.InDelta(t, tc.cached*1e-6, pricing.CacheReadPricePerTokenPriority, 1e-15, tc.model)
		require.InDelta(t, tc.output*1e-6, pricing.OutputPricePerTokenPriority, 1e-15, tc.model)
		if tc.cacheWrite > 0 {
			require.InDelta(t, tc.cacheWrite*1e-6, pricing.CacheCreationPricePerTokenPriority, 1e-15, tc.model)
		}
	}
}

// Models in the official Standard tables without a Fast (formerly Priority)
// row, https://developers.openai.com/api/docs/pricing as of 2026-10-22.
var officialOpenAIModelsWithoutFastTier = []string{
	"gpt-5.5-pro", "gpt-5.4-nano", "gpt-5.4-pro", "gpt-5.2-pro", "gpt-5-nano", "gpt-5-pro",
	"o1", "o1-pro", "o3-pro", "o3-mini", "gpt-4-turbo-2024-04-09", "gpt-4-0613",
	"gpt-3.5-turbo", "gpt-3.5-turbo-0125", "chat-latest", "gpt-5-search-api",
}

func TestOpenAIModelsWithoutPriorityTier_MatchesOfficialList(t *testing.T) {
	require.Len(t, openAIModelsWithoutPriorityTier, len(officialOpenAIModelsWithoutFastTier))
	for _, model := range officialOpenAIModelsWithoutFastTier {
		require.Contains(t, openAIModelsWithoutPriorityTier, model)
	}
	// Exact names and dated snapshots only; 4-digit suffixes are not dates.
	require.True(t, openAIModelHasNoPriorityTier(" O1-2024-12-17 "))
	require.True(t, openAIModelHasNoPriorityTier("gpt-4-turbo-2024-04-09"))
	require.False(t, openAIModelHasNoPriorityTier("gpt-4-turbo"))
	require.False(t, openAIModelHasNoPriorityTier("gpt-4-0314"))
	require.False(t, openAIModelHasNoPriorityTier("azure/gpt-5-nano"))
	require.False(t, openAIModelHasNoPriorityTier("gpt-5.4-mini"))
}

func noFastTierBundledModels(t *testing.T, svc *BillingService) []string {
	t.Helper()
	var models []string
	for name, p := range svc.pricingService.pricingData {
		if p.NoPriorityTier {
			require.Equal(t, "openai", p.LiteLLMProvider, name)
			models = append(models, name)
		}
	}
	for _, model := range officialOpenAIModelsWithoutFastTier {
		require.Contains(t, models, model, "bundled feed entry missing")
	}
	for _, dated := range []string{"gpt-5-nano-2025-08-07", "gpt-5.4-nano-2026-03-17", "gpt-5.4-pro-2026-03-05", "gpt-5.5-pro-2026-04-23", "o1-2024-12-17", "o3-mini-2025-01-31"} {
		require.Contains(t, models, dated)
	}
	return models
}

func TestBundledPricing_NoFastTierModelsBillPriorityAtStandard(t *testing.T) {
	svc := bundledPricingBilling(t)
	tokens := UsageTokens{InputTokens: 100_000, OutputTokens: 100_000, CacheReadTokens: 100_000, CacheCreationTokens: 10_000}
	for _, model := range noFastTierBundledModels(t, svc) {
		pricing, err := svc.GetModelPricing(model)
		require.NoError(t, err, model)
		require.True(t, pricing.NoPriorityTier, model)
		require.Zero(t, pricing.InputPricePerTokenPriority, model)

		standard, err := svc.CalculateCostWithServiceTier(model, tokens, 1, "")
		require.NoError(t, err, model)
		require.Positive(t, standard.TotalCost, model)
		for _, tier := range []string{"priority", " Priority "} {
			priority, err := svc.CalculateCostWithServiceTier(model, tokens, 1, tier)
			require.NoError(t, err, model)
			require.InDelta(t, standard.InputCost, priority.InputCost, 1e-12, model)
			require.InDelta(t, standard.OutputCost, priority.OutputCost, 1e-12, model)
			require.InDelta(t, standard.CacheReadCost, priority.CacheReadCost, 1e-12, model)
			require.InDelta(t, standard.CacheCreationCost, priority.CacheCreationCost, 1e-12, model)
			require.InDelta(t, standard.TotalCost, priority.TotalCost, 1e-12, model)
			require.Equal(t, "priority", priority.PricingSnapshot.ServiceTier, model)
			require.True(t, priority.PricingSnapshot.PriorityBilledAsStandard, model)
			require.InDelta(t, standard.TotalCost, priority.PricingSnapshot.ReferenceTotalCost, 1e-12, model)
		}
		require.False(t, standard.PricingSnapshot.PriorityBilledAsStandard, model)

		// Flex keeps the whole-tier x0.5 multiplier.
		flex, err := svc.CalculateCostWithServiceTier(model, tokens, 1, "flex")
		require.NoError(t, err, model)
		require.InDelta(t, standard.TotalCost*0.5, flex.TotalCost, 1e-12, model)
		require.False(t, flex.PricingSnapshot.PriorityBilledAsStandard, model)

		// The administrator pricing page shows the same effective Priority prices.
		require.Equal(t, svc.DisplayUnitPrices(pricing, "default"), svc.DisplayUnitPrices(pricing, "priority"), model)
		require.Equal(t, svc.DisplayUnitPrices(pricing.Reference, "default"), svc.DisplayUnitPrices(pricing.Reference, "priority"), model)
	}
}

// Priority billed at Standard follows the Standard long-context rule as well;
// otherwise a Priority request would be cheaper than the same Standard request.
func TestBundledPricing_NoFastTierPriorityKeepsStandardLongContext(t *testing.T) {
	svc := bundledPricingBilling(t)
	tokens := UsageTokens{InputTokens: 300_000, OutputTokens: 1_000}
	for _, model := range []string{"gpt-5.4-pro", "gpt-5.5-pro-2026-04-23"} {
		standard, err := svc.CalculateCostWithServiceTier(model, tokens, 1, "")
		require.NoError(t, err, model)
		require.True(t, standard.PricingSnapshot.LongContext.Applied, model)
		priority, err := svc.CalculateCostWithServiceTier(model, tokens, 1, "priority")
		require.NoError(t, err, model)
		require.True(t, priority.PricingSnapshot.LongContext.Applied, model)
		require.InDelta(t, standard.TotalCost, priority.TotalCost, 1e-12, model)

		pricing, err := svc.GetModelPricing(model)
		require.NoError(t, err, model)
		require.NotNil(t, svc.DisplayLongContextUnitPrices(pricing, "priority"), model)
		require.Equal(t, svc.DisplayLongContextUnitPrices(pricing, "default"), svc.DisplayLongContextUnitPrices(pricing, "priority"), model)
	}
}

func TestBundledPricing_PriorityUnchangedForModelsWithFastTierOrPrefix(t *testing.T) {
	svc := bundledPricingBilling(t)
	tokens := UsageTokens{InputTokens: 100_000, OutputTokens: 100_000}
	cases := []struct {
		model string
		ratio float64 // Priority total / Standard total
	}{
		{"gpt-5", 2},            // native official Fast prices (2x)
		{"gpt-5.4-mini", 2},     // native official Fast prices (2x)
		{"gpt-5.5", 2.5},        // GPT-5.5 policy-derived 2.5x Fast prices
		{"gpt-5.6-cyber", 2},    // no Priority price, not listed: generic x2
		{"azure/gpt-5-nano", 2}, // provider-prefixed entry: generic x2
		{"azure/o3-mini", 2},
	}
	for _, tc := range cases {
		pricing, err := svc.GetModelPricing(tc.model)
		require.NoError(t, err, tc.model)
		require.False(t, pricing.NoPriorityTier, tc.model)
		standard, err := svc.CalculateCostWithServiceTier(tc.model, tokens, 1, "")
		require.NoError(t, err, tc.model)
		priority, err := svc.CalculateCostWithServiceTier(tc.model, tokens, 1, "priority")
		require.NoError(t, err, tc.model)
		require.InDelta(t, tc.ratio*standard.TotalCost, priority.TotalCost, 1e-9, tc.model)
		require.False(t, priority.PricingSnapshot.PriorityBilledAsStandard, tc.model)
	}
}

// An administrator Priority override on a model without a Fast tier is
// intentional operator pricing and still applies; unedited Priority
// categories inherit Standard (not x2) prices.
func TestBundledPricing_NoFastTierHonoursAdminPriorityOverride(t *testing.T) {
	svc := bundledPricingBilling(t)
	svc.pricingService.setModelPricingConfig(nil, map[string]ModelPriceOverride{
		"gpt-5.4-nano": {PriorityInput: priceTestNumber(1)},
		"gpt-5-nano":   {PriorityInput: priceTestNumber(0.1), PriorityOutput: priceTestNumber(0.8), PriorityCacheRead: priceTestNumber(0.01)},
	})
	tokens := UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000}

	// gpt-5.4-nano Standard: 0.20 input / 1.25 output / 0.02 cached USD per 1M.
	priority, err := svc.CalculateCostWithServiceTier("gpt-5.4-nano", tokens, 1, "priority")
	require.NoError(t, err)
	require.InDelta(t, 1.0, priority.InputCost, 1e-9)
	require.InDelta(t, 1.25, priority.OutputCost, 1e-9)
	require.InDelta(t, 0.02, priority.CacheReadCost, 1e-9)
	require.False(t, priority.PricingSnapshot.PriorityBilledAsStandard)
	require.Equal(t, "priority", priority.PricingSnapshot.ServiceTier)

	priority, err = svc.CalculateCostWithServiceTier("gpt-5-nano-2025-08-07", tokens, 1, "priority")
	require.NoError(t, err)
	// Overrides are keyed by the billed model; the dated name has none.
	require.True(t, priority.PricingSnapshot.PriorityBilledAsStandard)
	priority, err = svc.CalculateCostWithServiceTier("gpt-5-nano", tokens, 1, "priority")
	require.NoError(t, err)
	require.InDelta(t, 0.1, priority.InputCost, 1e-9)
	require.InDelta(t, 0.8, priority.OutputCost, 1e-9)
	require.InDelta(t, 0.01, priority.CacheReadCost, 1e-9)
	require.False(t, priority.PricingSnapshot.PriorityBilledAsStandard)

	// Standard and Flex are unaffected by the Priority override.
	standard, err := svc.CalculateCostWithServiceTier("gpt-5-nano", tokens, 1, "")
	require.NoError(t, err)
	require.InDelta(t, 0.05+0.4+0.005, standard.TotalCost, 1e-9)
	flex, err := svc.CalculateCostWithServiceTier("gpt-5-nano", tokens, 1, "flex")
	require.NoError(t, err)
	require.InDelta(t, standard.TotalCost*0.5, flex.TotalCost, 1e-9)
}

// Aliases follow the resolved feed entry, and an explicit zero Priority
// override is honoured as free Priority pricing.
func TestBundledPricing_NoFastTierAliasesAndZeroOverride(t *testing.T) {
	svc := bundledPricingBilling(t)
	svc.pricingService.setModelPricingConfig(map[string]string{
		"my-nano":    "gpt-5-nano", // priced from a model without a Fast tier
		"gpt-5-pro":  "gpt-5",      // listed model priced from a Fast-tier model
		"free-o3pro": "o3-pro",
	}, map[string]ModelPriceOverride{
		"free-o3pro": {PriorityInput: priceTestNumber(0), PriorityOutput: priceTestNumber(0)},
	})
	tokens := UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000}

	standard, err := svc.CalculateCostWithServiceTier("my-nano", tokens, 1, "")
	require.NoError(t, err)
	priority, err := svc.CalculateCostWithServiceTier("my-nano", tokens, 1, "priority")
	require.NoError(t, err)
	require.InDelta(t, standard.TotalCost, priority.TotalCost, 1e-12)
	require.True(t, priority.PricingSnapshot.PriorityBilledAsStandard)

	// gpt-5 Fast: 2.5 input / 20 output USD per 1M.
	priority, err = svc.CalculateCostWithServiceTier("gpt-5-pro", tokens, 1, "priority")
	require.NoError(t, err)
	require.InDelta(t, 22.5, priority.TotalCost, 1e-9)
	require.False(t, priority.PricingSnapshot.PriorityBilledAsStandard)

	priority, err = svc.CalculateCostWithServiceTier("free-o3pro", tokens, 1, "priority")
	require.NoError(t, err)
	require.Zero(t, priority.TotalCost)
	require.False(t, priority.PricingSnapshot.PriorityBilledAsStandard)
}

func TestPricingSnapshot_PriorityBilledAsStandardJSON(t *testing.T) {
	svc := bundledPricingBilling(t)
	cost, err := svc.CalculateCostWithServiceTier("o3-mini-2025-01-31", UsageTokens{InputTokens: 10}, 1, "priority")
	require.NoError(t, err)
	raw, err := json.Marshal(cost.PricingSnapshot)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, "priority", decoded["service_tier"])
	require.Equal(t, true, decoded["priority_billed_as_standard"])
	require.Equal(t, true, decoded["effective"].(map[string]any)["no_priority_tier"])
	require.Equal(t, true, decoded["reference"].(map[string]any)["no_priority_tier"])
}
