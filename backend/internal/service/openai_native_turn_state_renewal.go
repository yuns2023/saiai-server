package service

import (
	"context"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Ownership is independent of scheduler stickiness. Only provider observations
// and successful uses renew it; validation and failed attempts never do.
// A turn can idle for hours on a long tool run before its next request; the
// owner must still be known then, or the request is rejected with a 409.
const openAINativeTurnStateOwnershipTTL = 24 * time.Hour
const openAINativeTurnStateRenewalInterval = time.Minute

// Implemented by Redis with an atomic owner comparison and conditional expiry.
// A non-positive remaining TTL means absent, expired, persistent or foreign:
// callers must not create or overwrite a binding in any of those cases.
type openAITurnStateExpiryCache interface {
	RefreshSessionTTLForAccount(context.Context, int64, string, int64, time.Duration, time.Duration) (time.Duration, error)
}

// Call only for a request that passed BOTH header and metadata validation.
// Keep digests, not raw client tokens, in per-turn bookkeeping.
func openAINativeTurnStateKeys(userID int64, headers http.Header, payload []byte) []string {
	keys := make([]string, 0, 2)
	add := func(state string) {
		key := openAITurnStateAccountKey(userID, state)
		if key == "" {
			return
		}
		for _, existing := range keys {
			if existing == key {
				return
			}
		}
		keys = append(keys, key)
	}
	if values := headers.Values(openAIWSTurnStateHeader); len(values) == 1 {
		add(values[0])
	}
	state := gjson.GetBytes(payload, "client_metadata.x-codex-turn-state")
	if state.Type == gjson.String {
		add(state.String())
	}
	return keys
}

func (s *defaultOpenAIWSStateStore) renewTurnStateAccount(ctx context.Context, key string, accountID int64) (string, error) {
	now := time.Now()
	deadline := now.Add(openAINativeTurnStateOwnershipTTL)
	if s.cache != nil {
		cache, ok := s.cache.(openAITurnStateExpiryCache)
		if !ok {
			return "unsupported_cache", nil
		}
		cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
		defer cancel()
		remaining, err := cache.RefreshSessionTTLForAccount(cacheCtx, 0, key, accountID, openAINativeTurnStateOwnershipTTL, openAINativeTurnStateRenewalInterval)
		if err != nil {
			return "shared_lookup_error", err
		}
		if remaining <= 0 {
			return "shared_missing_or_changed", nil
		}
		deadline = now.Add(remaining)
	}
	s.responseToAccountMu.Lock()
	defer s.responseToAccountMu.Unlock()
	binding, exists := s.responseToAccount[key]
	if s.cache == nil {
		if !exists || !now.Before(binding.expiresAt) || binding.accountID != accountID {
			return "local_missing_or_changed", nil
		}
		if binding.expiresAt.Sub(now) > openAINativeTurnStateOwnershipTTL-openAINativeTurnStateRenewalInterval {
			return "recent", nil
		}
	} else if exists && binding.accountID != accountID {
		// Do not overwrite a concurrent local observation of another owner.
		return "local_owner_changed", nil
	}
	ensureBindingCapacity(s.responseToAccount, key, openAIWSStateStoreMaxEntriesPerMap)
	s.responseToAccount[key] = openAIWSAccountBinding{accountID: accountID, expiresAt: deadline}
	return "renewed", nil
}

func (s *OpenAIGatewayService) renewOpenAINativeTurnStateAfterSuccess(ctx context.Context, account *Account, userID int64, keys []string) {
	if account == nil || !account.IsOpenAIOAuth() || len(keys) == 0 {
		return
	}
	store, ok := s.getOpenAIWSStateStore().(*defaultOpenAIWSStateStore)
	if !ok {
		return
	}
	for _, key := range keys {
		// A completed turn remains a success even if the downstream disconnected.
		// The store imposes its own short timeout on the detached cache operation.
		outcome, _ := store.renewTurnStateAccount(context.WithoutCancel(ctx), key, account.ID)
		if outcome != "renewed" && outcome != "recent" {
			logger.FromContext(ctx).Warn("openai.turn_state_renewal_skipped",
				zap.Int64("user_id", userID), zap.Int64("account_id", account.ID),
				zap.String("turn_state_digest", key), zap.String("renewal_outcome", outcome))
		}
	}
}
