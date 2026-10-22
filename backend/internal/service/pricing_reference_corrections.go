package service

import "strings"

// Reference-feed corrections run inside parsePricingData, so they apply to the
// remote download and the bundled fallback alike and survive
// tools/pricing_fallback_sync.sh. Each entry must cite an official pricing
// fact; administrator aliases and overrides still apply on top of the result.

// openAIModelsWithoutPriorityTier lists OpenAI models (bare names; dated
// snapshots match too) for which the official pricing page publishes no
// Priority/Fast price, although the community feed carries one. Removing the
// feed price makes Priority requests use the Gateway's generic service-tier
// behaviour for models without native Priority prices.
var openAIModelsWithoutPriorityTier = map[string]struct{}{
	// The feed copies gpt-5's 2.5 USD/M Priority input onto gpt-5-nano, 50x
	// its 0.05 USD/M Standard input; OpenAI lists no Priority row for it.
	"gpt-5-nano": {},
}

func applyReferencePricingCorrections(modelName string, pricing *LiteLLMModelPricing) {
	if pricing == nil {
		return
	}
	base := openAIModelDatePattern.ReplaceAllString(strings.ToLower(strings.TrimSpace(modelName)), "")
	if _, ok := openAIModelsWithoutPriorityTier[base]; ok {
		pricing.InputCostPerTokenPriority = 0
		pricing.OutputCostPerTokenPriority = 0
		pricing.CacheCreationInputTokenCostPriority = 0
		pricing.CacheReadInputTokenCostPriority = 0
	}
}
