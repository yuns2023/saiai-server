//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func hostedFeeGroup(platform string, rate float64) *Group {
	g := hostedUsageGroup()
	g.Platform = platform
	g.RateMultiplier = rate
	// A model multiplier that must NOT apply to hosted fees.
	g.ModelRateMultipliers = map[string]float64{"claude-sonnet-4": 2, "gpt-5": 2}
	return g
}

// discountedUser has a 0.8 PAYG discount that must NOT apply to hosted fees.
func discountedUser() *User {
	discount := 0.8
	return &User{ID: 1, PaygDiscountMultiplier: &discount, PaygDiscountOverrideEnabled: true}
}

func TestHostedFeePredicates(t *testing.T) {
	free := hostedUsageGroup()
	require.True(t, free.UsesHostedUsage())
	require.True(t, free.HostedChargeFree())
	require.Zero(t, free.HostedFeeRate())

	paid := hostedUsageGroup()
	paid.RateMultiplier = 0.1
	require.True(t, paid.UsesHostedUsage(), "a non-negative rate is the hosting fee")
	require.False(t, paid.HostedChargeFree())
	require.Equal(t, 0.1, paid.HostedFeeRate())
	require.NoError(t, paid.ValidateHostedUsage())

	ordinary := &Group{RateMultiplier: 0.1, Platform: PlatformAnthropic}
	require.False(t, ordinary.HostedChargeFree())
	require.Zero(t, ordinary.HostedFeeRate())
	var none *Group
	require.False(t, none.HostedChargeFree())
	require.Zero(t, none.HostedFeeRate())
}

func TestHostedFeeIgnoresModelRateAndUserDiscount(t *testing.T) {
	g := hostedFeeGroup(PlatformAnthropic, 0.1)
	cost := &CostBreakdown{TotalCost: 10, ActualCost: 1}
	modelRate, discount := applyUserBillingFactors(cost, g, discountedUser(), "claude-sonnet-4")
	require.Equal(t, 1.0, modelRate)
	require.Equal(t, 1.0, discount)
	require.Equal(t, 1.0, cost.ActualCost, "hosted ActualCost is standard × fee rate only")

	ordinary := hostedFeeGroup(PlatformAnthropic, 0.5)
	ordinary.HostedUsageEnabled = false
	cost = &CostBreakdown{TotalCost: 10, ActualCost: 5}
	modelRate, discount = applyUserBillingFactors(cost, ordinary, discountedUser(), "claude-sonnet-4")
	require.Equal(t, 2.0, modelRate)
	require.Equal(t, 0.8, discount)
	require.InDelta(t, 8.0, cost.ActualCost, 1e-12, "ordinary groups keep model rate × discount")
}

func TestHostedFeeAnthropicRecordUsage(t *testing.T) {
	for _, tc := range []struct {
		name string
		rate float64
	}{{"free", 0}, {"fee", 0.1}} {
		t.Run(tc.name, func(t *testing.T) {
			logs := &openAIRecordUsageLogRepoStub{inserted: true}
			billing := &openAIRecordUsageBillingRepoStub{}
			svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, nil)
			// A per-user override never changes a hosted fee (neither way).
			override := 2.0
			svc.userGroupRateResolver = newUserGroupRateResolver(&userGroupRateResolverRepoStub{rate: &override}, nil, time.Minute, nil, "service.test")
			g := hostedFeeGroup(PlatformAnthropic, tc.rate)
			accountRate := 1.5
			account := &Account{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, RateMultiplier: &accountRate}
			err := svc.RecordUsage(context.Background(), &RecordUsageInput{
				Result: &ForwardResult{RequestID: "TEST_ONLY_HOSTED_FEE_" + tc.name, Model: "claude-sonnet-4", Duration: time.Second,
					Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 100}},
				APIKey: &APIKey{ID: 2, GroupID: &g.ID, Group: g, Quota: 200, RateLimit5h: 200},
				User:   discountedUser(), Account: account, APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
			})
			require.NoError(t, err)
			log := logs.lastLog
			require.Positive(t, log.TotalCost)
			require.NotNil(t, log.MeteredCost)
			require.Equal(t, log.TotalCost, *log.MeteredCost)
			require.InDelta(t, log.TotalCost*tc.rate, log.ActualCost, 1e-12)
			require.Equal(t, tc.rate, log.RateMultiplier)
			require.Zero(t, *log.AccountRateMultiplier, "the account is the owner's own")
			require.Equal(t, log.ActualCost, billing.lastCmd.BalanceCost)
			require.Equal(t, log.TotalCost, billing.lastCmd.APIKeyQuotaCost, "Key limits count the standard price")
			require.Equal(t, log.TotalCost, billing.lastCmd.APIKeyRateLimitCost)
		})
	}
}

func TestHostedFeeOpenAIRecordUsage(t *testing.T) {
	for _, rate := range []float64{0, 0.1} {
		logs := &openAIRecordUsageLogRepoStub{inserted: true}
		billing := &openAIRecordUsageBillingRepoStub{}
		override := 2.0
		svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, nil, &openAIUserGroupRateRepoStub{rate: &override})
		g := hostedFeeGroup(PlatformOpenAI, rate)
		accountRate := 1.0
		account := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth, RateMultiplier: &accountRate}
		err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
			Result: &OpenAIForwardResult{RequestID: "TEST_ONLY_HOSTED_FEE_OAI", Model: "gpt-5",
				Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 100}},
			APIKey: &APIKey{ID: 2, GroupID: &g.ID, Group: g, Quota: 200, RateLimit5h: 200},
			User:   discountedUser(), Account: account, APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
		})
		require.NoError(t, err)
		log := logs.lastLog
		require.Positive(t, log.TotalCost)
		require.InDelta(t, log.TotalCost*rate, log.ActualCost, 1e-12, "rate=%v", rate)
		require.Equal(t, log.ActualCost, billing.lastCmd.BalanceCost)
		require.Equal(t, log.TotalCost, billing.lastCmd.APIKeyQuotaCost)
		require.Zero(t, *log.AccountRateMultiplier)
	}
}

func TestHostedFeeChatGPTTurn(t *testing.T) {
	logs := &chatGPTFixedUsageLogRepo{}
	billing := &chatGPTFixedBillingRepo{}
	cache := NewBillingCacheService(nil, nil, nil, nil, &config.Config{})
	t.Cleanup(cache.Stop)
	svc := NewOpenAIGatewayService(nil, logs, billing, nil, nil, nil, nil, &config.Config{}, nil, nil, nil, nil, cache, nil, &DeferredService{}, nil)
	g := hostedFeeGroup(PlatformOpenAI, 0.1)
	err := svc.RecordChatGPTTurnUsage(context.Background(), &OpenAIChatGPTTurnUsageInput{
		RequestID: "TEST_ONLY_HOSTED_FEE_CHAT", BasePriceUSD: .05,
		APIKey: &APIKey{ID: 2, GroupID: &g.ID, Group: g, Quota: 200, RateLimit5h: 200},
		User:   discountedUser(), Account: &Account{ID: 3, Type: AccountTypeOAuth}, APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
	})
	require.NoError(t, err)
	require.Len(t, logs.logs, 1)
	require.Equal(t, .05, *logs.logs[0].MeteredCost)
	require.InDelta(t, .005, logs.logs[0].ActualCost, 1e-12)
	require.InDelta(t, .005, billing.commands[0].BalanceCost, 1e-12)
	require.Equal(t, .05, billing.commands[0].APIKeyRateLimitCost)
}

func TestHostedFeeEligibilityNeedsWallet(t *testing.T) {
	now := time.Now()
	loader := &hostedKeyRepo{data: &APIKeyRateLimitData{Usage5h: 0, Window5hStart: &now}}
	users := &hostedUserRepo{user: &User{ID: 1, Balance: 0}}
	svc := &BillingCacheService{cfg: &config.Config{}, apiKeyRateLimitLoader: loader, userRepo: users}
	paid := hostedUsageGroup()
	paid.RateMultiplier = 0.1
	err := svc.CheckBillingEligibility(context.Background(), &User{ID: 1}, &APIKey{ID: 2}, paid, nil)
	require.ErrorIs(t, err, ErrInsufficientBalance, "a hosted group with a fee needs a positive balance")
	require.NoError(t, svc.CheckBillingEligibility(context.Background(), &User{ID: 1}, &APIKey{ID: 2}, hostedUsageGroup(), nil),
		"a free hosted group still needs no wallet")
}
