package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Synthetic fixture prices in USD per million tokens. "mult-*" models have no
// native Priority prices (Priority is the whole-tier x2 multiplier until a
// Priority field is overridden); "native-*" models publish Priority prices at
// a non-2x ratio, like the bundled feed. "-bd" models publish a separate 1h
// cache-write price (5m/1h breakdown); no bundled native-Priority model does.
const priorityMatrixPricingJSON = `{
"mult-bd":{"input_cost_per_token":10e-6,"output_cost_per_token":50e-6,"cache_read_input_token_cost":1e-6,"cache_creation_input_token_cost":12.5e-6,"cache_creation_input_token_cost_above_1hr":20e-6,"mode":"chat"},
"mult-zero":{"input_cost_per_token":10e-6,"output_cost_per_token":50e-6,"mode":"chat"},
"mult-nobd":{"input_cost_per_token":4e-6,"output_cost_per_token":20e-6,"cache_read_input_token_cost":0.4e-6,"cache_creation_input_token_cost":5e-6,"mode":"chat"},
"native-nobd":{"input_cost_per_token":4e-6,"input_cost_per_token_priority":7e-6,"output_cost_per_token":20e-6,"output_cost_per_token_priority":35e-6,"cache_read_input_token_cost":0.4e-6,"cache_read_input_token_cost_priority":0.7e-6,"cache_creation_input_token_cost":5e-6,"cache_creation_input_token_cost_priority":8.75e-6,"mode":"chat"},
"native-bd":{"input_cost_per_token":4e-6,"input_cost_per_token_priority":7e-6,"output_cost_per_token":20e-6,"output_cost_per_token_priority":35e-6,"cache_read_input_token_cost":0.4e-6,"cache_read_input_token_cost_priority":0.7e-6,"cache_creation_input_token_cost":5e-6,"cache_creation_input_token_cost_priority":8.75e-6,"cache_creation_input_token_cost_above_1hr":8e-6,"mode":"chat"},
"native-nocw":{"input_cost_per_token":4e-6,"input_cost_per_token_priority":8e-6,"output_cost_per_token":20e-6,"output_cost_per_token_priority":40e-6,"cache_read_input_token_cost":0.4e-6,"mode":"chat"}
}`

func priorityMatrixBilling(t *testing.T) (*PricingService, *BillingService) {
	t.Helper()
	p := NewPricingService(&config.Config{}, nil)
	data, err := p.parsePricingData([]byte(priorityMatrixPricingJSON))
	require.NoError(t, err)
	p.pricingData = data
	p.localHash = "priority-matrix"
	return p, NewBillingService(&config.Config{}, p)
}

func priorityMatrixOverrides() map[string]ModelPriceOverride {
	n := priceTestNumber
	return map[string]ModelPriceOverride{
		"none":      {},
		"stdall":    {Input: n(2), Output: n(11), CacheRead: n(3), CacheWrite5m: n(6), CacheWrite1h: n(9)},
		"std5m":     {CacheWrite5m: n(6)},
		"std1h":     {CacheWrite1h: n(9)},
		"pin":       {PriorityInput: n(30)},
		"pin+std":   {PriorityInput: n(30), CacheRead: n(3), CacheWrite5m: n(6), CacheWrite1h: n(9)},
		"pfull":     {PriorityInput: n(30), PriorityOutput: n(60), PriorityCacheRead: n(2), PriorityCacheWrite: n(15)},
		"pfull+std": {PriorityInput: n(30), PriorityOutput: n(60), PriorityCacheRead: n(2), PriorityCacheWrite: n(15), CacheRead: n(3), CacheWrite5m: n(6), CacheWrite1h: n(9)},
		"pcw0+std":  {PriorityCacheWrite: n(0), CacheWrite5m: n(6), CacheWrite1h: n(9)},
		"pcw+std5m": {PriorityCacheWrite: n(15), CacheWrite5m: n(6)},
		"pzero":     {PriorityInput: n(0), PriorityOutput: n(0), PriorityCacheRead: n(0), PriorityCacheWrite: n(0)},
	}
}

// Expected unit prices {input, output, cache read, 5m write, 1h write}. Rows
// marked "changed" differ from the calculator before this table was added;
// every Standard/Flex price and every row without overrides is unchanged.
func TestModelPricingPriorityFallbackMatrix(t *testing.T) {
	type pm = [5]float64
	cases := []struct {
		model, override    string
		priority, standard pm
	}{
		{"mult-bd", "none", pm{20, 100, 2, 25, 40}, pm{10, 50, 1, 12.5, 20}},
		{"mult-bd", "stdall", pm{4, 22, 6, 12, 18}, pm{2, 11, 3, 6, 9}},
		{"mult-bd", "std5m", pm{20, 100, 2, 12, 40}, pm{10, 50, 1, 6, 20}},
		{"mult-bd", "std1h", pm{20, 100, 2, 25, 18}, pm{10, 50, 1, 12.5, 9}},
		{"mult-bd", "pin", pm{30, 100, 2, 25, 40}, pm{10, 50, 1, 12.5, 20}},
		{"mult-bd", "pin+std", pm{30, 100, 2, 25, 40}, pm{10, 50, 3, 6, 9}},
		{"mult-bd", "pfull", pm{30, 60, 2, 15, 40}, pm{10, 50, 1, 12.5, 20}},
		{"mult-bd", "pfull+std", pm{30, 60, 2, 15, 40}, pm{10, 50, 3, 6, 9}},
		{"mult-bd", "pcw0+std", pm{20, 100, 2, 0, 40}, pm{10, 50, 1, 6, 9}},
		{"mult-bd", "pcw+std5m", pm{20, 100, 2, 15, 40}, pm{10, 50, 1, 6, 20}},
		{"mult-bd", "pzero", pm{0, 0, 0, 0, 40}, pm{10, 50, 1, 12.5, 20}},
		{"mult-zero", "none", pm{20, 100, 0, 0, 0}, pm{10, 50, 0, 0, 0}},
		{"mult-zero", "stdall", pm{4, 22, 6, 12, 18}, pm{2, 11, 3, 6, 9}},
		{"mult-zero", "std5m", pm{20, 100, 0, 12, 0}, pm{10, 50, 0, 6, 0}},
		{"mult-zero", "std1h", pm{20, 100, 0, 0, 18}, pm{10, 50, 0, 0, 9}},
		{"mult-zero", "pin", pm{30, 100, 0, 0, 0}, pm{10, 50, 0, 0, 0}},
		{"mult-zero", "pin+std", pm{30, 100, 0, 0, 0}, pm{10, 50, 3, 6, 9}}, // changed Priority: cache_read 3->0, 5m 6->0
		{"mult-zero", "pfull", pm{30, 60, 2, 15, 15}, pm{10, 50, 0, 0, 0}},
		{"mult-zero", "pfull+std", pm{30, 60, 2, 15, 15}, pm{10, 50, 3, 6, 9}}, // changed Priority: 1h 0->15
		{"mult-zero", "pcw0+std", pm{20, 100, 0, 0, 0}, pm{10, 50, 0, 6, 9}},
		{"mult-zero", "pcw+std5m", pm{20, 100, 0, 15, 15}, pm{10, 50, 0, 6, 0}}, // changed Priority: 1h 0->15
		{"mult-zero", "pzero", pm{0, 0, 0, 0, 0}, pm{10, 50, 0, 0, 0}},
		{"mult-nobd", "none", pm{8, 40, 0.8, 10, 10}, pm{4, 20, 0.4, 5, 5}},
		{"mult-nobd", "stdall", pm{4, 22, 6, 12, 18}, pm{2, 11, 3, 6, 9}},
		{"mult-nobd", "std5m", pm{8, 40, 0.8, 12, 10}, pm{4, 20, 0.4, 6, 5}},
		{"mult-nobd", "std1h", pm{8, 40, 0.8, 10, 18}, pm{4, 20, 0.4, 5, 9}},
		{"mult-nobd", "pin", pm{30, 40, 0.8, 10, 10}, pm{4, 20, 0.4, 5, 5}},
		{"mult-nobd", "pin+std", pm{30, 40, 0.8, 10, 10}, pm{4, 20, 3, 6, 9}},
		{"mult-nobd", "pfull", pm{30, 60, 2, 15, 15}, pm{4, 20, 0.4, 5, 5}},
		{"mult-nobd", "pfull+std", pm{30, 60, 2, 15, 15}, pm{4, 20, 3, 6, 9}},    // changed Priority: 1h 10->15
		{"mult-nobd", "pcw0+std", pm{8, 40, 0.8, 0, 0}, pm{4, 20, 0.4, 6, 9}},    // changed Priority: 1h 10->0
		{"mult-nobd", "pcw+std5m", pm{8, 40, 0.8, 15, 15}, pm{4, 20, 0.4, 6, 5}}, // changed Priority: 1h 10->15
		{"mult-nobd", "pzero", pm{0, 0, 0, 0, 0}, pm{4, 20, 0.4, 5, 5}},
		{"native-nobd", "none", pm{7, 35, 0.7, 8.75, 8.75}, pm{4, 20, 0.4, 5, 5}},
		{"native-nobd", "stdall", pm{7, 35, 0.7, 8.75, 8.75}, pm{2, 11, 3, 6, 9}},  // changed Priority: 1h 9->8.75
		{"native-nobd", "std5m", pm{7, 35, 0.7, 8.75, 8.75}, pm{4, 20, 0.4, 6, 5}}, // changed Priority: 1h 5->8.75
		{"native-nobd", "std1h", pm{7, 35, 0.7, 8.75, 8.75}, pm{4, 20, 0.4, 5, 9}}, // changed Priority: 1h 9->8.75
		{"native-nobd", "pin", pm{30, 35, 0.7, 8.75, 8.75}, pm{4, 20, 0.4, 5, 5}},
		{"native-nobd", "pin+std", pm{30, 35, 0.7, 8.75, 8.75}, pm{4, 20, 3, 6, 9}}, // changed Priority: 1h 9->8.75
		{"native-nobd", "pfull", pm{30, 60, 2, 15, 15}, pm{4, 20, 0.4, 5, 5}},
		{"native-nobd", "pfull+std", pm{30, 60, 2, 15, 15}, pm{4, 20, 3, 6, 9}},    // changed Priority: 1h 9->15
		{"native-nobd", "pcw0+std", pm{7, 35, 0.7, 0, 0}, pm{4, 20, 0.4, 6, 9}},    // changed Priority: 1h 9->0
		{"native-nobd", "pcw+std5m", pm{7, 35, 0.7, 15, 15}, pm{4, 20, 0.4, 6, 5}}, // changed Priority: 1h 5->15
		{"native-nobd", "pzero", pm{0, 0, 0, 0, 0}, pm{4, 20, 0.4, 5, 5}},
		{"native-bd", "none", pm{7, 35, 0.7, 8.75, 8}, pm{4, 20, 0.4, 5, 8}},
		{"native-bd", "stdall", pm{7, 35, 0.7, 8.75, 8}, pm{2, 11, 3, 6, 9}}, // changed Priority: 1h 9->8
		{"native-bd", "std5m", pm{7, 35, 0.7, 8.75, 8}, pm{4, 20, 0.4, 6, 8}},
		{"native-bd", "std1h", pm{7, 35, 0.7, 8.75, 8}, pm{4, 20, 0.4, 5, 9}}, // changed Priority: 1h 9->8
		{"native-bd", "pin", pm{30, 35, 0.7, 8.75, 8}, pm{4, 20, 0.4, 5, 8}},
		{"native-bd", "pin+std", pm{30, 35, 0.7, 8.75, 8}, pm{4, 20, 3, 6, 9}}, // changed Priority: 1h 9->8
		{"native-bd", "pfull", pm{30, 60, 2, 15, 8}, pm{4, 20, 0.4, 5, 8}},
		{"native-bd", "pfull+std", pm{30, 60, 2, 15, 8}, pm{4, 20, 3, 6, 9}},  // changed Priority: 1h 9->8
		{"native-bd", "pcw0+std", pm{7, 35, 0.7, 0, 8}, pm{4, 20, 0.4, 6, 9}}, // changed Priority: 1h 9->8
		{"native-bd", "pcw+std5m", pm{7, 35, 0.7, 15, 8}, pm{4, 20, 0.4, 6, 8}},
		{"native-bd", "pzero", pm{0, 0, 0, 0, 8}, pm{4, 20, 0.4, 5, 8}},
		{"native-nocw", "none", pm{8, 40, 0.4, 0, 0}, pm{4, 20, 0.4, 0, 0}},
		{"native-nocw", "stdall", pm{8, 40, 3, 6, 0}, pm{2, 11, 3, 6, 9}}, // changed Priority: 1h 9->0
		{"native-nocw", "std5m", pm{8, 40, 0.4, 6, 0}, pm{4, 20, 0.4, 6, 0}},
		{"native-nocw", "std1h", pm{8, 40, 0.4, 0, 0}, pm{4, 20, 0.4, 0, 9}}, // changed Priority: 1h 9->0
		{"native-nocw", "pin", pm{30, 40, 0.4, 0, 0}, pm{4, 20, 0.4, 0, 0}},
		{"native-nocw", "pin+std", pm{30, 40, 3, 6, 0}, pm{4, 20, 3, 6, 9}}, // changed Priority: 1h 9->0
		{"native-nocw", "pfull", pm{30, 60, 2, 15, 15}, pm{4, 20, 0.4, 0, 0}},
		{"native-nocw", "pfull+std", pm{30, 60, 2, 15, 15}, pm{4, 20, 3, 6, 9}},    // changed Priority: 1h 9->15
		{"native-nocw", "pcw0+std", pm{8, 40, 0.4, 0, 0}, pm{4, 20, 0.4, 6, 9}},    // changed Priority: 1h 9->0
		{"native-nocw", "pcw+std5m", pm{8, 40, 0.4, 15, 15}, pm{4, 20, 0.4, 6, 0}}, // changed Priority: 1h 0->15
		{"native-nocw", "pzero", pm{0, 0, 0, 0, 0}, pm{4, 20, 0.4, 0, 0}},
	}
	p, b := priorityMatrixBilling(t)
	overrides := priorityMatrixOverrides()
	split := UsageTokens{InputTokens: 1e6, OutputTokens: 1e6, CacheReadTokens: 1e6, CacheCreationTokens: 2e6, CacheCreation5mTokens: 1e6, CacheCreation1hTokens: 1e6}
	unsplit := UsageTokens{CacheCreationTokens: 1e6}
	for _, tc := range cases {
		t.Run(tc.model+"/"+tc.override, func(t *testing.T) {
			override, ok := overrides[tc.override]
			require.True(t, ok)
			p.SetModelPricingConfig(nil, map[string]ModelPriceOverride{tc.model: override})
			for _, tier := range []struct {
				name  string
				price [5]float64
			}{
				{"priority", tc.priority},
				{"default", tc.standard},
				{"flex", [5]float64{tc.standard[0] / 2, tc.standard[1] / 2, tc.standard[2] / 2, tc.standard[3] / 2, tc.standard[4] / 2}},
			} {
				cost, err := b.CalculateCostWithServiceTier(tc.model, split, 1, tier.name)
				require.NoError(t, err)
				got := [5]float64{cost.InputCost, cost.OutputCost, cost.CacheReadCost, cost.CacheCreation5mCost, cost.CacheCreation1hCost}
				for i := range got {
					require.InDelta(t, tier.price[i], got[i], 1e-9, "%s category %d", tier.name, i)
				}
				if tc.override == "none" {
					require.InDelta(t, cost.TotalCost, cost.PricingSnapshot.ReferenceTotalCost, 1e-9)
				}

				// Free billing keeps the base charge but bills nothing.
				free, err := b.CalculateCostWithServiceTier(tc.model, split, 0, tier.name)
				require.NoError(t, err)
				require.InDelta(t, cost.TotalCost, free.TotalCost, 1e-9)
				require.Zero(t, free.ActualCost)

				// Without a 5m/1h usage split every cache write uses the 5m price.
				whole, err := b.CalculateCostWithServiceTier(tc.model, unsplit, 1, tier.name)
				require.NoError(t, err)
				require.InDelta(t, tier.price[3], whole.CacheCreationCost, 1e-9)

				// Historical display uses only the persisted effective prices.
				body, err := json.Marshal(cost.PricingSnapshot)
				require.NoError(t, err)
				var saved PricingSnapshot
				require.NoError(t, json.Unmarshal(body, &saved))
				display := b.DisplayUnitPrices(saved.Effective, tier.name)
				for i, key := range []string{"input", "output", "cache_read", "cache_write_5m", "cache_write_1h"} {
					require.InDelta(t, tier.price[i], display[key], 1e-9, "%s display %s", tier.name, key)
				}
			}
		})
	}
}

// Regression: a Standard price edit must not change a Priority price that the
// unedited reference calculation derives on its own.
func TestModelPricingPriorityIgnoresStandardOverridesWhenExplicit(t *testing.T) {
	p, b := priorityMatrixBilling(t)
	tokens := UsageTokens{CacheReadTokens: 1e6, CacheCreationTokens: 2e6, CacheCreation5mTokens: 1e6, CacheCreation1hTokens: 1e6}

	// Reference cache read is zero; editing Priority input switches to
	// synthesized Priority prices, which keep the inherited zero.
	p.SetModelPricingConfig(nil, map[string]ModelPriceOverride{"mult-zero": {CacheRead: priceTestNumber(1), PriorityInput: priceTestNumber(30)}})
	cost, err := b.CalculateCostWithServiceTier("mult-zero", tokens, 1, "priority")
	require.NoError(t, err)
	require.Zero(t, cost.CacheReadCost)
	standard, err := b.CalculateCostWithServiceTier("mult-zero", tokens, 1, "default")
	require.NoError(t, err)
	require.InDelta(t, 1, standard.CacheReadCost, 1e-9)

	// A native Priority model publishes no 1h Priority price; its 1h tokens
	// keep the reference Priority cache-write price after a Standard 1h edit.
	p.SetModelPricingConfig(nil, nil)
	before, err := b.CalculateCostWithServiceTier("native-nobd", tokens, 1, "priority")
	require.NoError(t, err)
	p.SetModelPricingConfig(nil, map[string]ModelPriceOverride{"native-nobd": {CacheWrite1h: priceTestNumber(3)}})
	after, err := b.CalculateCostWithServiceTier("native-nobd", tokens, 1, "priority")
	require.NoError(t, err)
	require.InDelta(t, before.CacheCreation1hCost, after.CacheCreation1hCost, 1e-9)
	require.InDelta(t, 8.75, after.CacheCreation1hCost, 1e-9)
	standard, err = b.CalculateCostWithServiceTier("native-nobd", tokens, 1, "default")
	require.NoError(t, err)
	require.InDelta(t, 3, standard.CacheCreation1hCost, 1e-9)
}

// A snapshot persisted before priority_synthesized existed keeps the
// calculator decision it was billed with: the legacy fallback of a zero
// synthesized Priority price to the Standard override. Fixture: mult-zero with
// Priority input 30 and Standard cache read 3 / 5m 6 / 1h 9 (USD per million),
// billed at 30 / 100 / 3 / 6 / 0 for Priority.
func TestModelPricingLegacySnapshotWithoutPrioritySynthesizedRecomputesUnchanged(t *testing.T) {
	const legacy = `{"version":"v","override":{"priority_input":30,"cache_read":3,"cache_write_5m":6,"cache_write_1h":9},"billed_model":"mult-zero","resolved_model":"mult-zero","source":"remote","service_tier":"priority",
"effective":{"priority_cache_write_1h":0,"priority_overrides":{"priority_input":true},"input":0.00001,"priority_input":0.00003,"output":0.00005,"priority_output":0.0001,"cache_write":0.000006,"priority_cache_write":0,"cache_read":0.000003,"priority_cache_read":0,"cache_write_5m":0.000006,"cache_write_1h":0.000009,"supports_cache_breakdown":true,"long_context_threshold":0,"long_context_input_multiplier":0,"long_context_output_multiplier":0,"long_context_cache_write_multiplier":0,"long_context_cache_read_multiplier":0,"image_input":0,"image_output":0},
"reference_total_cost":120}`
	var saved PricingSnapshot
	require.NoError(t, json.Unmarshal([]byte(legacy), &saved))
	require.False(t, saved.Effective.PrioritySynthesized)
	b := NewBillingService(&config.Config{}, nil)
	display := b.DisplayUnitPrices(saved.Effective, "priority")
	for key, want := range map[string]float64{"input": 30, "output": 100, "cache_read": 3, "cache_write_5m": 6, "cache_write_1h": 0} {
		require.InDelta(t, want, display[key], 1e-9, key)
	}
	tokens := UsageTokens{InputTokens: 1e6, OutputTokens: 1e6, CacheReadTokens: 1e6, CacheCreationTokens: 2e6, CacheCreation5mTokens: 1e6, CacheCreation1hTokens: 1e6}
	cost := b.calculateTokenCost(tokens, 1, "priority", saved.Effective)
	require.InDelta(t, 139, cost.TotalCost, 1e-9)
	standard := b.DisplayUnitPrices(saved.Effective, "default")
	require.InDelta(t, 3, standard["cache_read"], 1e-9)
}
