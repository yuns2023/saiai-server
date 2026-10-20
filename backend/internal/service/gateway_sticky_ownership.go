package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// stickyOwnershipCache fences selection and success across Gateway processes.
// The owner is a reservation, not a confirmed successful sticky binding.
type stickyOwnershipCache interface {
	GetStickyOwnership(context.Context, int64, string) (string, error)
	ClaimStickyOwnership(context.Context, int64, string, string, string, int64, time.Duration) (bool, error)
	ConfirmStickyOwnership(context.Context, int64, string, string, int64, time.Duration) (bool, error)
	RenewStickyOwnership(context.Context, int64, string, string, time.Duration) (bool, error)
}

type stickyOwnershipContextKey struct{}
type stickyOwnershipSelectionKey struct{}

func WithStickyOwnership(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, stickyOwnershipContextKey{}, token)
}
func stickyOwnershipFromContext(ctx context.Context) string {
	token, _ := ctx.Value(stickyOwnershipContextKey{}).(string)
	return token
}
func stickyOwnershipSelection(ctx context.Context) bool {
	active, _ := ctx.Value(stickyOwnershipSelectionKey{}).(bool)
	return active
}

func (s *GatewayService) SelectAccountWithLoadAwareness(ctx context.Context, groupID *int64, sessionHash, model string, excluded map[int64]struct{}, metadata string) (*AccountSelectionResult, error) {
	cache, ok := s.cache.(stickyOwnershipCache)
	if !ok || sessionHash == "" || !IsClaudeCodeClient(ctx) {
		return s.selectAccountWithLoadAwareness(ctx, groupID, sessionHash, model, excluded, metadata)
	}
	// Resolve restriction routing before accessing the ownership key.
	group, resolvedGroupID, err := s.checkClaudeCodeRestriction(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if group != nil && group.Platform != PlatformAnthropic {
		return s.selectAccountWithLoadAwareness(ctx, resolvedGroupID, sessionHash, model, excluded, metadata)
	}
	groupID = resolvedGroupID
	ctx = s.withGroupContext(ctx, group)
	for attempt := 0; attempt < 3; attempt++ {
		previous, err := cache.GetStickyOwnership(ctx, derefGroupID(groupID), sessionHash)
		if err != nil {
			return nil, fmt.Errorf("read session ownership: %w", err)
		}
		ownerID := int64(0)
		if previous != "" {
			ownerID, err = strconv.ParseInt(strings.SplitN(previous, ":", 2)[0], 10, 64)
			if err != nil || ownerID <= 0 {
				return nil, errors.New("invalid session ownership")
			}
		}
		// Ignore the handler's earlier prefetch: another request may have migrated.
		selectionCtx := WithPrefetchedStickySession(ctx, ownerID, derefGroupID(groupID), false)
		selectionCtx = context.WithValue(selectionCtx, stickyOwnershipSelectionKey{}, true)
		result, err := s.selectAccountWithLoadAwareness(selectionCtx, groupID, sessionHash, model, excluded, metadata)
		if err != nil {
			return nil, err
		}
		if !s.accountUsesSuccessOnlyStickyForAccount(ctx, result.Account) {
			s.setStickySessionAccount(ctx, groupID, sessionHash, result.Account.ID, ownerID)
			return result, nil
		}
		token := previous
		if ownerID != result.Account.ID {
			token = fmt.Sprintf("%d:%s", result.Account.ID, uuid.NewString())
		}
		claimed, err := cache.ClaimStickyOwnership(ctx, derefGroupID(groupID), sessionHash, previous, token, result.Account.ID, pendingStickySessionTTL)
		if err == nil && claimed {
			result.StickyOwnership = token
			return result, nil
		}
		if result.Acquired && result.ReleaseFunc != nil {
			result.ReleaseFunc()
		}
		if err != nil {
			return nil, fmt.Errorf("reserve session ownership: %w", err)
		}
	}
	return nil, errors.New("session ownership changed; retry request")
}

// KeepStickyOwnership reserves the same generation while waiting or streaming.
// It cannot revive an expired owner or extend a superseded generation.
func (s *GatewayService) KeepStickyOwnership(ctx context.Context, groupID *int64, session, token string) func() {
	cache, ok := s.cache.(stickyOwnershipCache)
	if !ok || token == "" {
		return func() {}
	}
	leaseCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(pendingStickySessionTTL / 4)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				renewed, err := cache.RenewStickyOwnership(leaseCtx, derefGroupID(groupID), session, token, pendingStickySessionTTL)
				if err == nil && !renewed {
					return
				}
			}
		}
	}()
	return cancel
}

// StickyOwnershipCurrent prevents a request that waited through a migration
// from starting a new upstream request on the superseded account.
func (s *GatewayService) StickyOwnershipCurrent(ctx context.Context, groupID *int64, session, token string) (bool, error) {
	if token == "" {
		return true, nil
	}
	cache, ok := s.cache.(stickyOwnershipCache)
	if !ok {
		return false, errors.New("sticky ownership cache unavailable")
	}
	current, err := cache.GetStickyOwnership(ctx, derefGroupID(groupID), session)
	return current == token, err
}
