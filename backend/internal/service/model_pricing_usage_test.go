//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestModelPricingSubscriptionRecordUsesCustomBaseAndStoresSnapshot(t *testing.T) {
	p, b := testModelPricing(t)
	p.SetModelPricingConfig(nil, map[string]ModelPriceOverride{"claude-fable-5": {CacheRead: priceTestNumber(0.25)}})
	logRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billRepo := &openAIRecordUsageBillingRepoStub{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logRepo, billRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	svc.billingService = b
	discount := 0.5
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{Result: &ForwardResult{RequestID: "priced-subscription", Model: "claude-fable-5", Usage: ClaudeUsage{InputTokens: 100, CacheReadInputTokens: 1000}, Duration: time.Second}, APIKey: &APIKey{ID: 2, GroupID: i64p(3), Group: &Group{ID: 3, SubscriptionType: SubscriptionTypeSubscription, RateMultiplier: 2, ModelRateMultipliers: map[string]float64{"claude-fable-5": 0.8}}}, User: &User{ID: 1, PaygDiscountMultiplier: &discount}, Account: &Account{ID: 4}, Subscription: &UserSubscription{ID: 5}})
	require.NoError(t, err)
	require.NotNil(t, billRepo.lastCmd)
	require.InDelta(t, 0.00125, billRepo.lastCmd.SubscriptionCost, 1e-12)
	require.Zero(t, billRepo.lastCmd.BalanceCost)
	require.NotNil(t, logRepo.lastLog.PricingSnapshot)
	require.Equal(t, 0.25e-6, logRepo.lastLog.PricingSnapshot.Effective.CacheReadPricePerToken)
	require.InDelta(t, 0.002, logRepo.lastLog.PricingSnapshot.ReferenceTotalCost, 1e-12)
	require.InDelta(t, 0.00125*2*0.8, logRepo.lastLog.ActualCost, 1e-12, "whole-request factors retain the existing subscription behavior")
}
func TestModelPricingOpenAIHTTPAndWebSocketRecordShareOverrideAndSnapshot(t *testing.T) {
	for _, ws := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "websocket"}[ws], func(t *testing.T) {
			logRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(logRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, &openAIUserGroupRateRepoStub{})
			svc.billingService.pricingService.SetModelPricingConfig(nil, map[string]ModelPriceOverride{"gpt-5.1": {CacheRead: priceTestNumber(0)}})
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: &OpenAIForwardResult{RequestID: "priced-openai", Model: "gpt-5.1", Usage: OpenAIUsage{InputTokens: 10, CacheReadInputTokens: 100}, Duration: time.Second, OpenAIWSMode: ws}, APIKey: &APIKey{ID: 2, Group: &Group{RateMultiplier: 1}}, User: &User{ID: 1}, Account: &Account{ID: 3}})
			require.NoError(t, err)
			require.NotNil(t, logRepo.lastLog.PricingSnapshot)
			require.Zero(t, logRepo.lastLog.CacheReadCost)
			require.Equal(t, "gpt-5.1", logRepo.lastLog.PricingSnapshot.BilledModel)
		})
	}
}
