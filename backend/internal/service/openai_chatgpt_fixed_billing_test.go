package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type chatGPTFixedUsageLogRepo struct {
	UsageLogRepository
	logs []*UsageLog
}

func (r *chatGPTFixedUsageLogRepo) Create(_ context.Context, log *UsageLog) (bool, error) {
	r.logs = append(r.logs, log)
	return true, nil
}

func (r *chatGPTFixedUsageLogRepo) CreateBestEffort(ctx context.Context, log *UsageLog) error {
	_, err := r.Create(ctx, log)
	return err
}

type chatGPTFixedBillingRepo struct {
	UsageBillingRepository
	commands []*UsageBillingCommand
}

func (r *chatGPTFixedBillingRepo) Apply(_ context.Context, command *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	r.commands = append(r.commands, command)
	return &UsageBillingApplyResult{Applied: true}, nil
}

func TestRecordChatGPTTurnUsageUsesFixedPriceWithoutTokens(t *testing.T) {
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	usageRepo := &chatGPTFixedUsageLogRepo{}
	billingRepo := &chatGPTFixedBillingRepo{}
	billingCache := NewBillingCacheService(nil, nil, nil, nil, cfg)
	t.Cleanup(billingCache.Stop)
	svc := NewOpenAIGatewayService(
		nil, usageRepo, billingRepo, nil, nil, nil, nil, cfg,
		nil, nil, nil, nil, billingCache, nil, &DeferredService{}, nil,
	)
	groupID := int64(7)
	group := &Group{ID: groupID, Platform: PlatformOpenAI, RateMultiplier: 1.5}
	apiKey := &APIKey{ID: 8, GroupID: &groupID, Group: group}
	user := &User{ID: 9}
	account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "turn-request")

	err := svc.RecordChatGPTTurnUsage(ctx, &OpenAIChatGPTTurnUsageInput{
		BasePriceUSD:       0.02,
		APIKey:             apiKey,
		User:               user,
		Account:            account,
		InboundEndpoint:    "/chatgpt/backend-api/f/conversation",
		UpstreamEndpoint:   "/backend-api/f/conversation",
		UserAgent:          "ChatGPT fixture",
		IPAddress:          "127.0.0.1",
		RequestPayloadHash: "payload-hash",
		Duration:           1500 * time.Millisecond,
	})
	require.NoError(t, err)
	require.Len(t, billingRepo.commands, 1)
	command := billingRepo.commands[0]
	require.Equal(t, "local:turn-request", command.RequestID)
	require.Equal(t, OpenAIChatGPTTurnBillingModel, command.Model)
	require.Equal(t, "payload-hash", command.RequestPayloadHash)
	require.Zero(t, command.InputTokens)
	require.Zero(t, command.OutputTokens)
	require.InDelta(t, 0.03, command.BalanceCost, 1e-12)
	require.NotEmpty(t, command.RequestFingerprint)

	require.Len(t, usageRepo.logs, 1)
	log := usageRepo.logs[0]
	require.Equal(t, OpenAIChatGPTTurnBillingModel, log.Model)
	require.Equal(t, RequestTypeStream, log.RequestType)
	require.True(t, log.Stream)
	require.Zero(t, log.TotalTokens())
	require.InDelta(t, 0.02, log.TotalCost, 1e-12)
	require.InDelta(t, 0.03, log.ActualCost, 1e-12)
	require.Equal(t, 1500, *log.DurationMs)
	require.Equal(t, "/chatgpt/backend-api/f/conversation", *log.InboundEndpoint)
	require.Equal(t, "/backend-api/f/conversation", *log.UpstreamEndpoint)
}

func TestRecordChatGPTTurnUsageRejectsInvalidPriceAndDependencies(t *testing.T) {
	svc := &OpenAIGatewayService{}
	validInput := &OpenAIChatGPTTurnUsageInput{
		BasePriceUSD: 0.01,
		APIKey:       &APIKey{},
		User:         &User{},
		Account:      &Account{},
	}
	for _, price := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		input := *validInput
		input.BasePriceUSD = price
		require.ErrorContains(t, svc.RecordChatGPTTurnUsage(context.Background(), &input), "base price")
	}
	require.ErrorContains(t, svc.RecordChatGPTTurnUsage(context.Background(), &OpenAIChatGPTTurnUsageInput{BasePriceUSD: 0.01}), "API key")

	cfg := &config.Config{}
	cfg.Default.RateMultiplier = math.Inf(1)
	svc = &OpenAIGatewayService{cfg: cfg}
	require.ErrorContains(t, svc.RecordChatGPTTurnUsage(context.Background(), validInput), "multiplier")
}

func TestRecordChatGPTTurnUsageUsesSubscriptionCostContract(t *testing.T) {
	cfg := &config.Config{}
	usageRepo := &chatGPTFixedUsageLogRepo{}
	billingRepo := &chatGPTFixedBillingRepo{}
	billingCache := NewBillingCacheService(nil, nil, nil, nil, cfg)
	t.Cleanup(billingCache.Stop)
	svc := NewOpenAIGatewayService(
		nil, usageRepo, billingRepo, nil, nil, nil, nil, cfg,
		nil, nil, nil, nil, billingCache, nil, &DeferredService{}, nil,
	)
	groupID := int64(31)
	group := &Group{
		ID: groupID, Platform: PlatformOpenAI,
		SubscriptionType: SubscriptionTypeSubscription, RateMultiplier: 3,
	}
	subscription := &UserSubscription{ID: 32}
	err := svc.RecordChatGPTTurnUsage(context.Background(), &OpenAIChatGPTTurnUsageInput{
		BasePriceUSD: 0.04,
		APIKey:       &APIKey{ID: 33, GroupID: &groupID, Group: group},
		User:         &User{ID: 34},
		Account:      &Account{ID: 35, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		Subscription: subscription,
	})
	require.NoError(t, err)
	require.Len(t, billingRepo.commands, 1)
	require.InDelta(t, 0.04, billingRepo.commands[0].SubscriptionCost, 1e-12)
	require.Zero(t, billingRepo.commands[0].BalanceCost)
	require.Equal(t, subscription.ID, *billingRepo.commands[0].SubscriptionID)
	require.Len(t, usageRepo.logs, 1)
	require.Equal(t, BillingTypeSubscription, usageRepo.logs[0].BillingType)
	require.InDelta(t, 0.04, usageRepo.logs[0].TotalCost, 1e-12)
	require.InDelta(t, 0.12, usageRepo.logs[0].ActualCost, 1e-12)
}
