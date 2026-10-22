package service

import "strings"

// Reference-feed corrections run inside parsePricingData, so they apply to the
// remote download and the bundled fallback alike and survive
// tools/pricing_fallback_sync.sh. Each entry must cite an official pricing
// fact; administrator aliases and overrides still apply on top of the result.

// openAIModelsWithoutPriorityTier lists OpenAI models that the official
// pricing page (https://developers.openai.com/api/docs/pricing, checked
// 2026-10-22) shows in a Standard table but not in the matching Fast
// (formerly Priority) table. OpenAI serves Priority/Fast requests for them at
// Standard, so the Gateway bills Priority at Standard prices (see
// ModelPricing.NoPriorityTier) and drops any feed Priority price. Keys are
// bare feed names; dated snapshots (-YYYY-MM-DD / -YYYYMMDD) match too.
// Provider-prefixed entries such as azure/... are separate feed keys and are
// not affected.
var openAIModelsWithoutPriorityTier = map[string]struct{}{
	// Main text-token table.
	"gpt-5.5-pro":  {},
	"gpt-5.4-nano": {},
	"gpt-5.4-pro":  {},
	"gpt-5.2-pro":  {},
	// The feed copies gpt-5's 2.5 USD/M Priority input onto gpt-5-nano, 50x
	// its 0.05 USD/M Standard input.
	"gpt-5-nano":             {},
	"gpt-5-pro":              {},
	"o1":                     {},
	"o1-pro":                 {},
	"o3-pro":                 {},
	"o3-mini":                {},
	"gpt-4-turbo-2024-04-09": {},
	"gpt-4-0613":             {},
	"gpt-3.5-turbo":          {},
	"gpt-3.5-turbo-0125":     {},
	// Specialized models table (its Fast view lists only gpt-5.3-codex).
	"chat-latest":      {},
	"gpt-5-search-api": {},
}

func openAIModelHasNoPriorityTier(modelName string) bool {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if _, ok := openAIModelsWithoutPriorityTier[name]; ok {
		return true
	}
	_, ok := openAIModelsWithoutPriorityTier[openAIModelDatePattern.ReplaceAllString(name, "")]
	return ok
}

func applyReferencePricingCorrections(modelName string, pricing *LiteLLMModelPricing) {
	if pricing == nil {
		return
	}
	if openAIModelHasNoPriorityTier(modelName) {
		pricing.NoPriorityTier = true
		pricing.InputCostPerTokenPriority = 0
		pricing.OutputCostPerTokenPriority = 0
		pricing.CacheCreationInputTokenCostPriority = 0
		pricing.CacheReadInputTokenCostPriority = 0
	}
}
