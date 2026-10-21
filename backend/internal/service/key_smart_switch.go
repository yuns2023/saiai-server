package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrSmartSwitchInvalid     = infraerrors.BadRequest("SMART_SWITCH_INVALID", "invalid smart switching policy")
	ErrSmartSwitchConflict    = infraerrors.Conflict("SMART_SWITCH_CONFLICT", "key settings changed; reload before saving")
	ErrSmartSwitchUnavailable = infraerrors.ServiceUnavailable("SMART_SWITCH_UNAVAILABLE", "No eligible group in the smart switching policy")
)

func (s *APIKeyService) validateSmartSwitch(ctx context.Context, user *User, groupID *int64, policy *domain.KeySmartSwitchPolicy) error {
	if policy != nil && policy.PaygBudgetUSD != nil && (math.IsNaN(*policy.PaygBudgetUSD) || math.IsInf(*policy.PaygBudgetUSD, 0) || *policy.PaygBudgetUSD < 0) {
		return ErrSmartSwitchInvalid
	}
	if policy == nil {
		return nil
	}
	if len(policy.GroupIDs) > 10 || (policy.Enabled && len(policy.GroupIDs) == 0) || policy.Version < 0 {
		return ErrSmartSwitchInvalid
	}
	seen := make(map[int64]bool)
	for _, id := range policy.GroupIDs {
		if id <= 0 || seen[id] {
			return ErrSmartSwitchInvalid
		}
		seen[id] = true
	}
	if !policy.Enabled {
		return nil
	}
	if groupID == nil {
		return ErrSmartSwitchInvalid
	}
	base, err := s.groupRepo.GetByID(ctx, *groupID)
	if err != nil {
		return err
	}
	if !smartSwitchPlatform(base) {
		return ErrSmartSwitchInvalid
	}
	for _, id := range policy.GroupIDs {
		group, err := s.groupRepo.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if !group.IsActive() || !smartSwitchPlatform(group) || group.Platform != base.Platform {
			return ErrSmartSwitchInvalid
		}
		if !user.MeetsRequiredLevel(group.RequiredLevel) {
			return ErrGroupNotAllowed
		}
		if group.IsSubscriptionType() {
			if _, err := s.userSubRepo.GetActiveByUserIDAndGroupID(ctx, user.ID, id); err != nil {
				return err
			}
		} else {
			if !user.CanAccessGroup(group) {
				return ErrGroupNotAllowed
			}
			if !policy.PaygConfirmed {
				return ErrSmartSwitchInvalid
			}
		}
	}
	return nil
}

func smartSwitchPlatform(group *Group) bool {
	return group != nil && !group.UsesHostedUsage() && (group.Platform == PlatformAnthropic || group.Platform == PlatformOpenAI)
}

// SmartSwitchSelection is request-owned. It must never be written into auth caches
// or persisted as the key's binding. Reasons contain stable codes, not upstream data.
type SmartSwitchSelection struct {
	Key          *APIKey
	Subscription *UserSubscription
	Skipped      []string
}

// SelectSmartSwitchGroup always starts at the highest priority, so recovered
// subscriptions are used on the next independent request. excluded is bounded by
// the policy and lets a handler advance after exhausting a group's accounts.
func (s *APIKeyService) SelectSmartSwitchGroup(ctx context.Context, key *APIKey, model string, excluded map[int64]bool, subscriptions *SubscriptionService) (*SmartSwitchSelection, error) {
	if key == nil || key.SmartSwitch == nil || !key.SmartSwitch.Enabled || key.GroupID == nil {
		return nil, ErrSmartSwitchInvalid
	}
	// Refresh policy and counters before any paid routing decision.
	fresh, err := s.apiKeyRepo.GetByID(ctx, key.ID)
	if err != nil {
		return nil, err
	}
	if fresh.SmartSwitch == nil || !fresh.SmartSwitch.Enabled || fresh.SmartSwitchVersion != key.SmartSwitchVersion {
		return nil, ErrSmartSwitchConflict
	}
	refreshed := *key
	refreshed.Quota = fresh.Quota
	refreshed.QuotaUsed = fresh.QuotaUsed
	refreshed.Status = fresh.Status
	refreshed.ExpiresAt = fresh.ExpiresAt
	refreshed.SmartSwitchPaygUsed = fresh.SmartSwitchPaygUsed
	refreshed.SmartSwitch = fresh.SmartSwitch.Clone()
	key = &refreshed
	// These guards apply to the identity, never to individual candidates.
	if !key.IsActive() {
		return nil, ErrSmartSwitchInvalid
	}
	if key.IsExpired() {
		return nil, ErrAPIKeyExpired
	}
	if key.IsQuotaExhausted() {
		return nil, ErrAPIKeyQuotaExhausted
	}
	if fresh.RateLimit5h > 0 && fresh.EffectiveUsage5h() >= fresh.RateLimit5h {
		return nil, ErrAPIKeyRateLimit5hExceeded
	}
	if fresh.RateLimit1d > 0 && fresh.EffectiveUsage1d() >= fresh.RateLimit1d {
		return nil, ErrAPIKeyRateLimit1dExceeded
	}
	if fresh.RateLimit7d > 0 && fresh.EffectiveUsage7d() >= fresh.RateLimit7d {
		return nil, ErrAPIKeyRateLimit7dExceeded
	}
	user, err := s.userRepo.GetByID(ctx, key.UserID)
	if err != nil {
		return nil, err
	}
	if !user.IsActive() {
		return nil, ErrInsufficientPerms
	}
	baseID := key.GroupID
	if key.SmartSwitchOriginalGroupID != nil {
		baseID = key.SmartSwitchOriginalGroupID
	}
	base, err := s.groupRepo.GetByID(ctx, *baseID)
	if err != nil {
		return nil, err
	}
	if !smartSwitchPlatform(base) {
		return nil, ErrSmartSwitchInvalid
	}
	if len(key.SmartSwitch.GroupIDs) == 0 || len(key.SmartSwitch.GroupIDs) > 10 {
		return nil, ErrSmartSwitchInvalid
	}
	skipped := []string{}
	visited := map[int64]bool{}
	for _, id := range key.SmartSwitch.GroupIDs {
		if id <= 0 || visited[id] {
			return nil, ErrSmartSwitchInvalid
		}
		visited[id] = true
		if excluded[id] {
			skipped = append(skipped, "group_unavailable")
			continue
		}
		group, err := s.groupRepo.GetByID(ctx, id)
		if errors.Is(err, ErrGroupNotFound) {
			skipped = append(skipped, "group_missing")
			continue
		}
		if err != nil {
			return nil, err
		}
		if !group.IsActive() {
			skipped = append(skipped, "group_inactive")
			continue
		}
		if !smartSwitchPlatform(group) || group.Platform != base.Platform {
			skipped = append(skipped, "incompatible_group")
			continue
		}
		if !user.MeetsRequiredLevel(group.RequiredLevel) {
			skipped = append(skipped, "access_revoked")
			continue
		}
		if model != "" && group.IsModelBlocked(model) {
			skipped = append(skipped, "model_unavailable")
			continue
		}
		var sub *UserSubscription
		if group.IsSubscriptionType() {
			if subscriptions == nil {
				return nil, fmt.Errorf("smart switching requires subscription validation")
			}
			// Fetch fresh state: dependency failures must not cause paid failover.
			sub, err = s.userSubRepo.GetActiveByUserIDAndGroupID(ctx, user.ID, group.ID)
			if errors.Is(err, ErrSubscriptionNotFound) || errors.Is(err, ErrSubscriptionExpired) {
				skipped = append(skipped, "subscription_unavailable")
				continue
			}
			if err != nil {
				return nil, err
			}
			ownedSub := *sub
			sub = &ownedSub
			maintenance, validateErr := subscriptions.ValidateAndCheckLimits(sub, group)
			if validateErr != nil {
				if smartSwitchSubscriptionUnavailable(validateErr) {
					skipped = append(skipped, "subscription_limit_or_expiry")
					continue
				}
				return nil, validateErr
			}
			if sub.ExhaustedLimitError(group) != nil {
				skipped = append(skipped, "subscription_limit_or_expiry")
				continue
			}
			if maintenance {
				copy := *sub
				subscriptions.DoWindowMaintenance(&copy)
			}
		} else {
			if !user.CanAccessGroup(group) {
				skipped = append(skipped, "access_revoked")
				continue
			}
			if !key.SmartSwitch.PaygConfirmed {
				return nil, ErrSmartSwitchInvalid
			}
			if key.SmartSwitch.PaygBudgetUSD != nil && key.SmartSwitchPaygUsed >= *key.SmartSwitch.PaygBudgetUSD {
				skipped = append(skipped, "payg_budget_exhausted")
				continue
			}
			if user.Balance <= 0 {
				skipped = append(skipped, "insufficient_balance")
				continue
			}
		}
		copy := *key
		groupCopy := *group
		// Explicit user choices must not enter an administrator's hidden fallback chain.
		groupCopy.FallbackGroupID = nil
		groupCopy.FallbackGroupIDOnInvalidRequest = nil
		copy.Group = &groupCopy
		effectiveID := group.ID
		copy.GroupID = &effectiveID
		originalID := *baseID
		copy.SmartSwitchOriginalGroupID = &originalID
		copy.SmartSwitch = key.SmartSwitch.Clone()
		copy.SmartSwitchReason = strings.Join(skipped, ",")
		copy.User = user
		return &SmartSwitchSelection{Key: &copy, Subscription: sub, Skipped: skipped}, nil
	}
	return nil, ErrSmartSwitchUnavailable
}

func smartSwitchSubscriptionUnavailable(err error) bool {
	return errors.Is(err, ErrSubscriptionExpired) || errors.Is(err, ErrSubscriptionSuspended) ||
		errors.Is(err, ErrFiveHourLimitExceeded) || errors.Is(err, ErrDailyLimitExceeded) ||
		errors.Is(err, ErrWeeklyLimitExceeded) || errors.Is(err, ErrMonthlyLimitExceeded)
}

type smartSwitchContextKey struct{}

func WithSmartSwitchContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, smartSwitchContextKey{}, true)
}

func smartSwitchContext(ctx context.Context) bool {
	enabled, _ := ctx.Value(smartSwitchContextKey{}).(bool)
	return enabled
}
