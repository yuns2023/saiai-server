//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBillingExplicitRateMultiplier(t *testing.T) {
	svc := newTestBillingService()
	imageBilling := NewBillingService(&config.Config{}, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-image-2": {
				InputCostPerToken: 5e-6, OutputCostPerToken: 10e-6,
				InputCostPerImageToken: 8e-6, OutputCostPerImageToken: 30e-6,
			},
		},
	})
	unitPrice := 0.25
	calculators := []struct {
		name string
		calc func(float64) (*CostBreakdown, error)
	}{
		{"text", func(rate float64) (*CostBreakdown, error) {
			return svc.CalculateCost("claude-sonnet-4", UsageTokens{
				InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 2000,
				CacheCreation5mTokens: 100, CacheCreation1hTokens: 200,
			}, rate)
		}},
		{"priority", func(rate float64) (*CostBreakdown, error) {
			return newOpenAITestBillingService().CalculateCostWithServiceTier("gpt-5.1", UsageTokens{InputTokens: 1000, OutputTokens: 500}, rate, "priority")
		}},
		{"long_context", func(rate float64) (*CostBreakdown, error) {
			return svc.CalculateCostWithLongContext("claude-sonnet-4", UsageTokens{InputTokens: 10000, CacheReadTokens: 210000, OutputTokens: 500}, rate, 200000, 2)
		}},
		{"image_tokens", func(rate float64) (*CostBreakdown, error) {
			return imageBilling.CalculateImageTokenCost("gpt-image-2", ImageUsageTokens{TextInputTokens: 10, ImageInputTokens: 20, ImageOutputTokens: 30}, rate)
		}},
		{"image_count", func(rate float64) (*CostBreakdown, error) {
			return svc.CalculateImageCost("gpt-image-1", "1K", 2, &ImagePriceConfig{Price1K: &unitPrice}, rate), nil
		}},
		{"sora_image", func(rate float64) (*CostBreakdown, error) {
			return svc.CalculateSoraImageCost("360", 2, &SoraPriceConfig{ImagePrice360: &unitPrice}, rate), nil
		}},
		{"sora_video", func(rate float64) (*CostBreakdown, error) {
			return svc.CalculateSoraVideoCost("sora-video", &SoraPriceConfig{VideoPricePerRequest: &unitPrice}, rate), nil
		}},
	}
	for _, calculator := range calculators {
		t.Run(calculator.name, func(t *testing.T) {
			base, err := calculator.calc(1)
			require.NoError(t, err)
			require.Positive(t, base.TotalCost)
			for _, rate := range []float64{0, 0.5, 1, 2} {
				cost, err := calculator.calc(rate)
				require.NoError(t, err)
				require.Equal(t, base.TotalCost, cost.TotalCost, "base cost at rate %v", rate)
				require.InDelta(t, base.ActualCost*rate, cost.ActualCost, 1e-12, "actual cost at rate %v", rate)
			}
		})
	}
}
