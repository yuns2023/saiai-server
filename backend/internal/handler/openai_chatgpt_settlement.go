package handler

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

// All delivery transports settle the same original identity and frozen price.
// Call only after verifying provider completion and merging its evidence.
func (h *OpenAIGatewayHandler) settleChatGPTTurn(ctx context.Context, cache service.ChatGPTTurnCache, scope service.ChatGPTTurnScope, turn *service.ChatGPTTurnSnapshot, input *service.OpenAIChatGPTTurnUsageInput) error {
	if turn == nil || cache == nil || input == nil {
		return errors.New("native Chat settlement context unavailable")
	}
	sealed, err := cache.SealChatGPTTurnBilling(ctx, scope.TurnKey(turn.Identity))
	if err != nil {
		return err
	}
	if sealed == nil || sealed.ImageBilling == nil {
		return errors.New("native Chat billing evidence unavailable")
	}
	if sealed.Completed {
		return nil
	}
	if !service.IsValidOpenAIChatGPTTurnPrice(sealed.BasePriceUSD) {
		return cache.CompleteChatGPTTurn(ctx, scope.TurnKey(sealed.Identity))
	}
	value := *input
	if sealed.BillingSnapshotVersion == 1 {
		value.Subscription = nil
		if sealed.SubscriptionID > 0 {
			value.Subscription = &service.UserSubscription{ID: sealed.SubscriptionID, UserID: scope.UserID, GroupID: scope.GroupID}
			if value.APIKey != nil && value.APIKey.Group != nil {
				keyCopy, groupCopy := *value.APIKey, *value.APIKey.Group
				groupCopy.SubscriptionType = service.SubscriptionTypeSubscription
				keyCopy.Group = &groupCopy
				value.APIKey = &keyCopy
			}
		}
	}
	value.BasePriceUSD = sealed.BasePriceUSD
	value.RequestedModel, value.ThinkingEffort = sealed.RequestedModel, sealed.ThinkingEffort
	value.RequestID, value.RequestPayloadHash = sealed.Identity.RequestID, sealed.Identity.PayloadHash
	value.ImageGenerationSeen = sealed.Images.GenerationSeen
	value.ImageCount, value.ImageSize, value.ImageCostUSD = sealed.ImageBilling.Count, sealed.ImageBilling.Size, sealed.ImageBilling.CostUSD
	value.Duration = time.Since(sealed.StartedAt)
	value.APIKeyService = h.apiKeyService
	h.submitChatGPTUsageRecordTask(func(ctx context.Context) {
		if err := h.gatewayService.RecordChatGPTTurnUsage(ctx, &value); err != nil {
			logger.L().Error("openai.chatgpt_record_turn_failed", zap.Int64("api_key_id", scope.APIKeyID), zap.Int64("account_id", sealed.AccountID), zap.Error(err))
			return
		}
		if err := cache.CompleteChatGPTTurn(ctx, scope.TurnKey(sealed.Identity)); err != nil {
			logger.L().Error("openai.chatgpt_complete_context_failed", zap.Error(err))
		}
	})
	return nil
}
