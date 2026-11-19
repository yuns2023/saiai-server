package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
)

// validateMarketTierGroup runs on every update of a group that ends up as a
// market tier (promotion or a tier edit): the group rules, no group may fall
// back into it (claude_code_only or invalid-request fallback, which also
// covers a middle-of-chain node), and every stored user rate override on it
// must stay > 0. The repositories implement the guard interfaces; a missing
// guard refuses the save instead of skipping the check.
func (s *adminServiceImpl) validateMarketTierGroup(ctx context.Context, group *Group) error {
	if group == nil || !group.MarketTier {
		return nil
	}
	if err := group.ValidateMarketTier(); err != nil {
		return err
	}
	groupGuard, ok := s.groupRepo.(MarketTierGroupGuard)
	if !ok {
		return ErrMarketTierGuardMissing
	}
	referrers, err := groupGuard.ListFallbackReferrers(ctx, group.ID)
	if err != nil {
		return err
	}
	if len(referrers) > 0 {
		return ErrMarketTierFallback.WithMetadata(map[string]string{"referrer_group_ids": joinInt64s(referrers)})
	}
	if s.userGroupRateRepo == nil {
		return nil
	}
	rateGuard, ok := s.userGroupRateRepo.(MarketTierRateGuard)
	if !ok {
		return ErrMarketTierGuardMissing
	}
	users, err := rateGuard.ListNonPositiveRateUsers(ctx, group.ID)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return ErrMarketTierRateOverride.WithMetadata(map[string]string{"user_ids": joinInt64s(users)})
	}
	return nil
}

type marketTierRateCheck struct {
	groupID int64
	rate    float64
}

// userGroupRatesToCheck lists the overrides being set (nil = delete).
func userGroupRatesToCheck(rates map[int64]*float64) []marketTierRateCheck {
	out := make([]marketTierRateCheck, 0, len(rates))
	for groupID, rate := range rates {
		if rate != nil {
			out = append(out, marketTierRateCheck{groupID: groupID, rate: *rate})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].groupID < out[j].groupID })
	return out
}

// validateMarketTierRateOverrides refuses an override on a market tier that
// is not > 0 at the stored DECIMAL(10,4) precision (0, negative, NaN, or a
// value such as 0.00001 that rounds to 0). Other groups are unchanged.
func (s *adminServiceImpl) validateMarketTierRateOverrides(ctx context.Context, rates []marketTierRateCheck) error {
	if len(rates) == 0 || s.groupRepo == nil {
		return nil
	}
	tiers := make(map[int64]bool, len(rates))
	for _, item := range rates {
		isTier, seen := tiers[item.groupID]
		if !seen {
			group, err := s.groupRepo.GetByIDLite(ctx, item.groupID)
			if err != nil {
				if errors.Is(err, ErrGroupNotFound) {
					tiers[item.groupID] = false
					continue
				}
				return err
			}
			isTier = group.MarketTier
			tiers[item.groupID] = isTier
		}
		if isTier && (math.IsNaN(item.rate) || !MarketTierRateStoredPositive(item.rate)) {
			return ErrMarketTierRateOverride.WithMetadata(map[string]string{"group_id": fmt.Sprint(item.groupID)})
		}
	}
	return nil
}

func joinInt64s(ids []int64) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprint(id)
	}
	return out
}
