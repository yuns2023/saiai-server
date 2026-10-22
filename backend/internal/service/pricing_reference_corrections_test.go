package service

import (
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

func TestBundledPricing_GPT5NanoPriorityUsesStandardTierMultiplier(t *testing.T) {
	svc := bundledPricingBilling(t)
	tokens := UsageTokens{InputTokens: 100_000, CacheReadTokens: 100_000, OutputTokens: 100_000}

	for _, model := range []string{"gpt-5-nano", "gpt-5-nano-2025-08-07"} {
		pricing, err := svc.GetModelPricing(model)
		require.NoError(t, err, model)
		require.Zero(t, pricing.InputPricePerTokenPriority, model)
		require.Zero(t, pricing.OutputPricePerTokenPriority, model)
		require.Zero(t, pricing.CacheReadPricePerTokenPriority, model)

		standard, err := svc.CalculateCostWithServiceTier(model, tokens, 1, "")
		require.NoError(t, err, model)
		// Standard 0.05 / 0.005 / 0.40 USD per million tokens.
		require.InDelta(t, 0.005, standard.InputCost, 1e-12, model)
		require.InDelta(t, 0.0005, standard.CacheReadCost, 1e-12, model)
		require.InDelta(t, 0.04, standard.OutputCost, 1e-12, model)

		priority, err := svc.CalculateCostWithServiceTier(model, tokens, 1, "priority")
		require.NoError(t, err, model)
		// No native Priority price: the generic x2 Priority tier multiplier
		// applies instead of the feed's 2.5 USD/M (50x) input price.
		require.InDelta(t, 2*standard.InputCost, priority.InputCost, 1e-12, model)
		require.InDelta(t, 2*standard.CacheReadCost, priority.CacheReadCost, 1e-12, model)
		require.InDelta(t, 2*standard.OutputCost, priority.OutputCost, 1e-12, model)
		require.InDelta(t, 2*standard.TotalCost, priority.TotalCost, 1e-12, model)
	}
}
