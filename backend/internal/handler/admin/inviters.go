package admin

import (
	"context"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// resolveInviters looks up the users behind invitation relationships for admin views.
// It is an additive label: a lookup failure is logged and leaves the ids unresolved
// instead of failing the page.
func resolveInviters(ctx context.Context, invites *service.UserInviteService, ids []int64) map[int64]*dto.InviterSummary {
	out := map[int64]*dto.InviterSummary{}
	if invites == nil || len(ids) == 0 {
		return out
	}
	seen := make(map[int64]struct{}, len(ids))
	uniq := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok || id <= 0 {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	refs, err := invites.InviterRefs(ctx, uniq)
	if err != nil {
		slog.Warn("admin_inviter_lookup_failed", "error", err)
		return out
	}
	for id, ref := range refs {
		out[id] = &dto.InviterSummary{ID: ref.ID, Email: ref.Email, Deleted: ref.Deleted}
	}
	return out
}

func attachUserInviters(ctx context.Context, invites *service.UserInviteService, users ...*dto.AdminUser) {
	ids := make([]int64, 0, len(users))
	for _, u := range users {
		if u != nil && u.InvitedByUserID != nil {
			ids = append(ids, *u.InvitedByUserID)
		}
	}
	refs := resolveInviters(ctx, invites, ids)
	for _, u := range users {
		if u != nil && u.InvitedByUserID != nil {
			u.InvitedBy = refs[*u.InvitedByUserID]
		}
	}
}

func attachCodeOwners(ctx context.Context, invites *service.UserInviteService, codes ...*dto.AdminRedeemCode) {
	ids := make([]int64, 0, len(codes))
	for _, c := range codes {
		if c != nil && c.OwnerUserID != nil {
			ids = append(ids, *c.OwnerUserID)
		}
	}
	refs := resolveInviters(ctx, invites, ids)
	for _, c := range codes {
		if c != nil && c.OwnerUserID != nil {
			c.Owner = refs[*c.OwnerUserID]
		}
	}
}
