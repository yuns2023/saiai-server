package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type fakePricingRemoteClient struct {
	pricingBody []byte
	hashText    string
	fetches     int
	hashFetches int
}

func (f *fakePricingRemoteClient) FetchPricingJSON(_ context.Context, _ string) ([]byte, error) {
	f.fetches++
	return append([]byte(nil), f.pricingBody...), nil
}

func (f *fakePricingRemoteClient) FetchHashText(_ context.Context, _ string) (string, error) {
	f.hashFetches++
	return f.hashText, nil
}

func TestParsePricingData_ParsesPriorityAndServiceTierFields(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"input_cost_per_token_priority": 0.000005,
			"output_cost_per_token": 0.000015,
			"output_cost_per_token_priority": 0.00003,
			"cache_creation_input_token_cost": 0.0000025,
			"cache_read_input_token_cost": 0.00000025,
			"cache_read_input_token_cost_priority": 0.0000005,
			"supports_service_tier": true,
			"supports_prompt_caching": true,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)
	pricing := data["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 5e-6, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 3e-5, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 5e-7, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

func TestParsePricingData_DerivesWholeRequestAbove272kPolicy(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"gpt-5.6-sol": {
			"input_cost_per_token": 0.000005,
			"input_cost_per_token_above_272k_tokens": 0.00001,
			"input_cost_per_token_priority": 0.00001,
			"output_cost_per_token": 0.00003,
			"output_cost_per_token_above_272k_tokens": 0.000045,
			"output_cost_per_token_priority": 0.00006,
			"cache_creation_input_token_cost": 0.00000625,
			"cache_creation_input_token_cost_above_272k_tokens": 0.0000125,
			"cache_creation_input_token_cost_priority": 0.0000125,
			"cache_read_input_token_cost": 0.0000005,
			"cache_read_input_token_cost_above_272k_tokens": 0.000001,
			"cache_read_input_token_cost_priority": 0.000001
		}
	}`))
	require.NoError(t, err)

	pricing := data["gpt-5.6-sol"]
	require.NotNil(t, pricing)
	require.Equal(t, 272000, pricing.LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, pricing.LongContextInputCostMultiplier, 1e-12)
	require.InDelta(t, 1.5, pricing.LongContextOutputCostMultiplier, 1e-12)
	require.InDelta(t, 2.0, pricing.LongContextCacheCreationCostMultiplier, 1e-12)
	require.InDelta(t, 2.0, pricing.LongContextCacheReadCostMultiplier, 1e-12)
	require.InDelta(t, 12.5e-6, pricing.CacheCreationInputTokenCostPriority, 1e-12)
}

func TestSyncWithRemote_NoHashURLRefreshesChangedFreshFile(t *testing.T) {
	dir := t.TempDir()
	pricingFile := filepath.Join(dir, "model_pricing.json")
	localBody := []byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"output_cost_per_token": 0.000015
		}
	}`)
	remoteBody := []byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"output_cost_per_token": 0.000015
		},
		"gpt-5.6-sol": {
			"input_cost_per_token": 0.000005,
			"output_cost_per_token": 0.00003
		}
	}`)
	require.NoError(t, os.WriteFile(pricingFile, localBody, 0644))

	remote := &fakePricingRemoteClient{pricingBody: remoteBody}
	svc := NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			RemoteURL:           "https://example.com/model_pricing.json",
			DataDir:             dir,
			UpdateIntervalHours: 24,
		},
	}, remote)
	require.NoError(t, svc.loadPricingData(pricingFile))

	require.NoError(t, svc.syncWithRemote())

	require.Equal(t, 1, remote.fetches)
	require.Zero(t, remote.hashFetches)
	got := svc.GetModelPricing("gpt-5.6-sol")
	require.NotNil(t, got)
	require.InDelta(t, 0.000005, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 0.00003, got.OutputCostPerToken, 1e-12)
	updated, err := os.ReadFile(pricingFile)
	require.NoError(t, err)
	require.Contains(t, string(updated), `"gpt-5.6-sol"`)
}

func TestSyncWithRemote_NoHashURLSkipsRewriteWhenContentUnchanged(t *testing.T) {
	dir := t.TempDir()
	pricingFile := filepath.Join(dir, "model_pricing.json")
	body := []byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"output_cost_per_token": 0.000015
		}
	}`)
	require.NoError(t, os.WriteFile(pricingFile, body, 0644))
	fixedTime := time.Now().Add(-time.Hour).Round(time.Second)
	require.NoError(t, os.Chtimes(pricingFile, fixedTime, fixedTime))

	remote := &fakePricingRemoteClient{pricingBody: body}
	svc := NewPricingService(&config.Config{
		Pricing: config.PricingConfig{
			RemoteURL:           "https://example.com/model_pricing.json",
			DataDir:             dir,
			UpdateIntervalHours: 24,
		},
	}, remote)
	require.NoError(t, svc.loadPricingData(pricingFile))

	require.NoError(t, svc.syncWithRemote())

	require.Equal(t, 1, remote.fetches)
	info, err := os.Stat(pricingFile)
	require.NoError(t, err)
	require.True(t, info.ModTime().Equal(fixedTime), "unchanged remote content should not rewrite local cache")
}

func TestGetModelPricing_OpenAIDateVariantUsesSameModelFamilyPricing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.4": {
				InputCostPerToken:               2.5e-6,
				OutputCostPerToken:              1.5e-5,
				CacheReadInputTokenCost:         2.5e-7,
				LongContextInputTokenThreshold:  272000,
				LongContextInputCostMultiplier:  2.0,
				LongContextOutputCostMultiplier: 1.5,
			},
		},
	}

	got := svc.GetModelPricing("gpt-5.4-20260305")
	require.NotNil(t, got)
	require.InDelta(t, 2.5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.5e-5, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2.5e-7, got.CacheReadInputTokenCost, 1e-12)
	require.Equal(t, 272000, got.LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, got.LongContextInputCostMultiplier, 1e-12)
	require.InDelta(t, 1.5, got.LongContextOutputCostMultiplier, 1e-12)
}

func TestGetModelPricing_OpenAIHyphenatedDateVariantUsesSameModelFamilyPricing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.4": {
				InputCostPerToken:               2.5e-6,
				OutputCostPerToken:              1.5e-5,
				CacheReadInputTokenCost:         2.5e-7,
				LongContextInputTokenThreshold:  272000,
				LongContextInputCostMultiplier:  2.0,
				LongContextOutputCostMultiplier: 1.5,
			},
		},
	}

	got := svc.GetModelPricing("gpt-5.4-2026-03-05")
	require.NotNil(t, got)
	require.InDelta(t, 2.5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.5e-5, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2.5e-7, got.CacheReadInputTokenCost, 1e-12)
	require.Equal(t, 272000, got.LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, got.LongContextInputCostMultiplier, 1e-12)
	require.InDelta(t, 1.5, got.LongContextOutputCostMultiplier, 1e-12)
}

func TestGetModelPricing_Gpt54MiniDateVariantUsesBasePricing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.4-mini": {
				InputCostPerToken:       7.5e-7,
				OutputCostPerToken:      4.5e-6,
				CacheReadInputTokenCost: 7.5e-8,
			},
		},
	}

	got := svc.GetModelPricing("gpt-5.4-mini-20260317")
	require.NotNil(t, got)
	require.InDelta(t, 7.5e-7, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 4.5e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 7.5e-8, got.CacheReadInputTokenCost, 1e-12)
	require.Zero(t, got.LongContextInputTokenThreshold)
}

func TestGetModelPricing_Gpt54NanoDateVariantUsesBasePricing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.4-nano": {
				InputCostPerToken:       2e-7,
				OutputCostPerToken:      1.25e-6,
				CacheReadInputTokenCost: 2e-8,
			},
		},
	}

	got := svc.GetModelPricing("gpt-5.4-nano-20260317")
	require.NotNil(t, got)
	require.InDelta(t, 2e-7, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.25e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2e-8, got.CacheReadInputTokenCost, 1e-12)
	require.Zero(t, got.LongContextInputTokenThreshold)
}

func TestGetModelPricing_OpenAIUnknownModelDoesNotUseApproximateFallback(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.2-codex": {InputCostPerToken: 2},
			"gpt-5.4":       {InputCostPerToken: 3},
		},
	}

	require.Nil(t, svc.GetModelPricing("gpt-5.3-codex"))
	require.Nil(t, svc.GetModelPricing("gpt-5.3-codex-spark"))
	require.Nil(t, svc.GetModelPricing("gpt-5.4-mini"))
}

func TestGetModelPricing_ClaudeFableFamilyMatchesBasePricing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-fable-5": {
				InputCostPerToken:                   10e-6,
				OutputCostPerToken:                  50e-6,
				CacheCreationInputTokenCost:         12.5e-6,
				CacheCreationInputTokenCostAbove1hr: 20e-6,
				CacheReadInputTokenCost:             1e-6,
			},
		},
	}

	got := svc.GetModelPricing("claude-fable-5-20260610")
	require.NotNil(t, got)
	require.InDelta(t, 10e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 50e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 12.5e-6, got.CacheCreationInputTokenCost, 1e-12)
	require.InDelta(t, 20e-6, got.CacheCreationInputTokenCostAbove1hr, 1e-12)
	require.InDelta(t, 1e-6, got.CacheReadInputTokenCost, 1e-12)
}

func TestGetModelPricing_ClaudeMythosFamilyMatchesBasePricing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"anthropic.claude-mythos-preview": {
				InputCostPerToken:                   10e-6,
				OutputCostPerToken:                  50e-6,
				CacheCreationInputTokenCost:         12.5e-6,
				CacheCreationInputTokenCostAbove1hr: 20e-6,
				CacheReadInputTokenCost:             1e-6,
			},
		},
	}

	got := svc.GetModelPricing("claude-mythos-5")
	require.NotNil(t, got)
	require.InDelta(t, 10e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 50e-6, got.OutputCostPerToken, 1e-12)
}

func TestParsePricingData_PreservesPriorityAndServiceTierFields(t *testing.T) {
	raw := map[string]any{
		"gpt-5.4": map[string]any{
			"input_cost_per_token":                 2.5e-6,
			"input_cost_per_token_priority":        5e-6,
			"output_cost_per_token":                15e-6,
			"output_cost_per_token_priority":       30e-6,
			"cache_read_input_token_cost":          0.25e-6,
			"cache_read_input_token_cost_priority": 0.5e-6,
			"supports_service_tier":                true,
			"supports_prompt_caching":              true,
			"litellm_provider":                     "openai",
			"mode":                                 "chat",
		},
	}
	body, err := json.Marshal(raw)
	require.NoError(t, err)

	svc := &PricingService{}
	pricingMap, err := svc.parsePricingData(body)
	require.NoError(t, err)

	pricing := pricingMap["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 2.5e-6, pricing.InputCostPerToken, 1e-12)
	require.InDelta(t, 5e-6, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 15e-6, pricing.OutputCostPerToken, 1e-12)
	require.InDelta(t, 30e-6, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.25e-6, pricing.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 0.5e-6, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

func TestParsePricingData_PreservesServiceTierPriorityFields(t *testing.T) {
	svc := &PricingService{}
	pricingData, err := svc.parsePricingData([]byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"input_cost_per_token_priority": 0.000005,
			"output_cost_per_token": 0.000015,
			"output_cost_per_token_priority": 0.00003,
			"cache_read_input_token_cost": 0.00000025,
			"cache_read_input_token_cost_priority": 0.0000005,
			"supports_service_tier": true,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`))
	require.NoError(t, err)

	pricing := pricingData["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 0.0000025, pricing.InputCostPerToken, 1e-12)
	require.InDelta(t, 0.000005, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.000015, pricing.OutputCostPerToken, 1e-12)
	require.InDelta(t, 0.00003, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.00000025, pricing.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 0.0000005, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}
