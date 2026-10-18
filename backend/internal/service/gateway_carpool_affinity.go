package service

import (
	"context"
	"log/slog"
	"sort"
	"strings"
)

type carpoolAffinityContextKey struct{}

// Selection-local memoization: routed and fallback candidates share membership
// reads, but a later request observes removals/rotation. No registry writes here.
type carpoolAffinity struct {
	deviceID   string
	membership map[int64]bool
	failed     bool
}

func withCarpoolAffinity(ctx context.Context, metadataUserID string) context.Context {
	parsed := ParseMetadataUserID(strings.TrimSpace(metadataUserID))
	if parsed == nil || strings.TrimSpace(parsed.DeviceID) == "" {
		return ctx
	}
	return context.WithValue(ctx, carpoolAffinityContextKey{}, &carpoolAffinity{
		deviceID: strings.TrimSpace(parsed.DeviceID), membership: make(map[int64]bool),
	})
}

func (s *GatewayService) carpoolMembership(ctx context.Context, accounts []*Account) map[int64]bool {
	affinity, _ := ctx.Value(carpoolAffinityContextKey{}).(*carpoolAffinity)
	if affinity == nil || affinity.failed || s.identityService == nil || s.identityService.cache == nil {
		return nil
	}
	var missing []int64
	for _, account := range accounts {
		if account.GetClaudeOAuthMode() != ClaudeOAuthModeCarpool || account.IsClaudeOAuthCarpoolUnlimitedDevices() {
			continue
		}
		if _, known := affinity.membership[account.ID]; !known {
			missing = append(missing, account.ID)
		}
	}
	if len(missing) > 0 {
		membership, err := s.identityService.cache.GetCarpoolDeviceMembership(ctx, missing, affinity.deviceID)
		if err != nil {
			affinity.failed = true
			// Do not log metadata, device identity, or cache payloads.
			slog.Warn("carpool_affinity_lookup_failed", "candidate_count", len(missing))
			return nil
		}
		for _, id := range missing {
			affinity.membership[id] = membership[id]
		}
	}
	return affinity.membership
}

// Apply only after the account-priority tier has been selected. Availability,
// routing and fresh admission checks remain authoritative.
func (s *GatewayService) preferCarpoolWithLoad(ctx context.Context, accounts []accountWithLoad) []accountWithLoad {
	plain := make([]*Account, 0, len(accounts))
	for _, item := range accounts {
		plain = append(plain, item.account)
	}
	membership := s.carpoolMembership(ctx, plain)
	var preferred []accountWithLoad
	for _, item := range accounts {
		if membership[item.account.ID] {
			preferred = append(preferred, item)
		}
	}
	if len(preferred) > 0 {
		return preferred
	}
	return accounts
}

// Stable sorting preserves the existing load ordering and random tie breaks.
// Priority is always above affinity; other modes receive no affinity bonus.
func (s *GatewayService) prioritizeCarpoolAccounts(ctx context.Context, accounts []*Account) {
	membership := s.carpoolMembership(ctx, accounts)
	if len(membership) == 0 {
		return
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return membership[a.ID] && !membership[b.ID]
	})
}

func (s *GatewayService) prioritizeCarpoolWithLoad(ctx context.Context, accounts []accountWithLoad) {
	plain := make([]*Account, 0, len(accounts))
	for _, item := range accounts {
		plain = append(plain, item.account)
	}
	membership := s.carpoolMembership(ctx, plain)
	if len(membership) == 0 {
		return
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i].account, accounts[j].account
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return membership[a.ID] && !membership[b.ID]
	})
}
