//go:build unit

package service

import (
	"context"
	"fmt"
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
	require.InDelta(t, 0.00125*2*0.8*0.5, billRepo.lastCmd.SubscriptionCost, 1e-12)
	require.Zero(t, billRepo.lastCmd.BalanceCost)
	require.NotNil(t, logRepo.lastLog.PricingSnapshot)
	require.Equal(t, 0.25e-6, logRepo.lastLog.PricingSnapshot.Effective.CacheReadPricePerToken)
	require.InDelta(t, 0.002, logRepo.lastLog.PricingSnapshot.ReferenceTotalCost, 1e-12)
	require.InDelta(t, 0.00125*2*0.8*0.5, logRepo.lastLog.ActualCost, 1e-12, "subscription quota and usage records use the same whole-request factors")
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

func TestSubscriptionAndBalanceRecordUsageShareBillingFactors(t *testing.T) {
	for _, route := range []string{"claude", "legacy-long-context", "openai-http", "openai-websocket"} {
		for _, factors := range []struct {
			name      string
			modelRate float64
			groupRate *float64
			discount  float64
		}{
			{name: "double-model", modelRate: 2, discount: 1},
			{name: "user-group-override-and-discount", modelRate: 2, groupRate: priceTestNumber(1.5), discount: 0.8},
			{name: "free-model", modelRate: 0, discount: 1},
			{name: "free-group", modelRate: 2, groupRate: priceTestNumber(0), discount: 1},
			{name: "free-user", modelRate: 2, discount: 0},
		} {
			var balanceCost float64
			for _, subscription := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/subscription=%t", route, factors.name, subscription), func(t *testing.T) {
					usageRepo := &openAIRecordUsageLogRepoStub{}
					billRepo := &openAIRecordUsageBillingRepoStub{}
					rateRepo := &openAIUserGroupRateRepoStub{rate: factors.groupRate}
					group := &Group{ID: 3, RateMultiplier: 1, ModelRateMultipliers: map[string]float64{"claude-sonnet-*": factors.modelRate, "gpt-*": factors.modelRate}}
					var sub *UserSubscription
					if subscription {
						group.SubscriptionType = SubscriptionTypeSubscription
						sub = &UserSubscription{ID: 5}
					}
					key := &APIKey{ID: 2, GroupID: i64p(3), Group: group, Quota: 100}
					user := &User{ID: 1, PaygDiscountMultiplier: &factors.discount}
					account := &Account{ID: 4, Type: AccountTypeAPIKey, Extra: map[string]any{"quota_limit": float64(100)}}
					quota := &openAIRecordUsageAPIKeyQuotaStub{}
					var err error
					if route == "openai-http" || route == "openai-websocket" {
						svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billRepo, nil, nil, rateRepo)
						err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
							Result: &OpenAIForwardResult{RequestID: "factors", Model: "gpt-5.1", Usage: OpenAIUsage{InputTokens: 300000, OutputTokens: 100, CacheReadInputTokens: 1000}, OpenAIWSMode: route == "openai-websocket", Duration: time.Second},
							APIKey: key, User: user, Account: account, Subscription: sub, APIKeyService: quota,
						})
					} else {
						svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billRepo, nil, nil)
						svc.userGroupRateResolver = newUserGroupRateResolver(rateRepo, nil, time.Minute, nil, "service.test")
						result := &ForwardResult{RequestID: "factors", Model: "claude-sonnet-4", Usage: ClaudeUsage{InputTokens: 300000, OutputTokens: 100, CacheReadInputTokens: 1000}, Duration: time.Second}
						if route == "legacy-long-context" {
							err = svc.RecordUsageWithLongContext(context.Background(), &RecordUsageLongContextInput{Result: result, APIKey: key, User: user, Account: account, Subscription: sub, APIKeyService: quota, LongContextThreshold: 200000, LongContextMultiplier: 2})
						} else {
							err = svc.RecordUsage(context.Background(), &RecordUsageInput{Result: result, APIKey: key, User: user, Account: account, Subscription: sub, APIKeyService: quota})
						}
					}
					require.NoError(t, err)
					require.NotNil(t, usageRepo.lastLog)
					log := usageRepo.lastLog
					require.Positive(t, log.TotalCost)
					effectiveGroupRate := 1.0
					if factors.groupRate != nil {
						effectiveGroupRate = *factors.groupRate
					}
					baseCharge := log.TotalCost
					if route == "legacy-long-context" {
						// The legacy calculator stores its excess-input surcharge in
						// ActualCost only: 301K input minus 200K, at $3/M extra.
						baseCharge += 101000 * 3e-6
					}
					expected := baseCharge * effectiveGroupRate * factors.modelRate * factors.discount
					require.InDelta(t, expected, log.ActualCost, 1e-12)
					require.Equal(t, factors.discount, *log.UserPaygDiscountMultiplier)
					require.InDelta(t, expected, billRepo.lastCmd.APIKeyQuotaCost, 1e-12)
					require.InDelta(t, log.TotalCost, billRepo.lastCmd.AccountQuotaCost, 1e-12, "upstream account quota retains its base-cost accounting")
					if subscription {
						require.InDelta(t, balanceCost, billRepo.lastCmd.SubscriptionCost, 1e-12)
						require.Zero(t, billRepo.lastCmd.BalanceCost)
					} else {
						balanceCost = billRepo.lastCmd.BalanceCost
						require.InDelta(t, expected, balanceCost, 1e-12)
					}
				})
			}
		}
	}
}
