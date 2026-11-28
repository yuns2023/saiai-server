package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
)

// validateMarketTierGroup runs on every update of a group that ends up as a
// market tier (promotion or a tier edit): the group rules, no group may fall
// back into it (claude_code_only or invalid-request fallback, which also
// covers a middle-of-chain node), and no stored user rate override on it
// may be negative (0 is allowed: free self-use; the dispatch never sells a
// listed account at a zero price). The repositories implement the guard interfaces; a missing
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
	users, err := rateGuard.ListNegativeRateUsers(ctx, group.ID)
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
// is negative at the stored DECIMAL(10,4) precision (or not finite). Zero is
// allowed (free self-use: a zero price never dispatches to a listed account,
// see computeMarketPrice / marketSnapshotGroupPriced). Values that are >= 0
// at that precision are valid on every group and need no lookup; the rest
// are checked with one batched query of the groups' market_tier flag.
// normalizeGroupMarketPlan trims an API plan; "" means no plan.
func normalizeGroupMarketPlan(plan *string) *string {
	if plan == nil {
		return nil
	}
	v := NormalizeMarketPlanInput(*plan)
	if v == "" {
		return nil
	}
	return &v
}

func (s *adminServiceImpl) validateMarketTierRateOverrides(ctx context.Context, rates []marketTierRateCheck) error {
	if s.groupRepo == nil {
		return nil
	}
	var suspect []int64
	for _, item := range rates {
		if !MarketTierOverrideStoredAllowed(item.rate) && !slices.Contains(suspect, item.groupID) {
			suspect = append(suspect, item.groupID)
		}
	}
	if len(suspect) == 0 {
		return nil
	}
	var tiers []int64
	if lookup, ok := s.groupRepo.(MarketTierLookup); ok {
		var err error
		if tiers, err = lookup.ListMarketTierIDs(ctx, suspect); err != nil {
			return err
		}
	} else {
		for _, id := range suspect {
			group, err := s.groupRepo.GetByIDLite(ctx, id)
			if err != nil {
				if errors.Is(err, ErrGroupNotFound) {
					continue
				}
				return err
			}
			if group.MarketTier {
				tiers = append(tiers, id)
			}
		}
	}
	if len(tiers) > 0 {
		sort.Slice(tiers, func(i, j int) bool { return tiers[i] < tiers[j] })
		return ErrMarketTierRateOverride.WithMetadata(map[string]string{"group_id": fmt.Sprint(tiers[0])})
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
