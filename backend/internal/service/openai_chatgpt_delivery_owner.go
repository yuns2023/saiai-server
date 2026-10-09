package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Routing ownership must outlive the one-hour accounting/replay context.
// The lease contains only scoped identity digests and the original account.
// Verified activity may renew ownership, never an accounting snapshot.
const ChatGPTDeliveryOwnerTTL = 30 * 24 * time.Hour

type ChatGPTDeliveryOwner struct {
	AccountID       int64
	UserMessageHash string
	TurnStartedMS   int64
}

func (s ChatGPTTurnScope) ConversationOwnerKey(id string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(id)))
	return fmt.Sprintf("chatgpt:owner:{%s}:conversation:%x", s.namespace(), digest)
}

func (s ChatGPTTurnScope) AssetOwnerKey(digest string) string {
	return fmt.Sprintf("chatgpt:owner:{%s}:asset:%s", s.namespace(), digest)
}

func ValidChatGPTDeliveryOwnerKey(key string) bool {
	parts := strings.Split(key, ":")
	if len(parts) != 5 || parts[0] != "chatgpt" || parts[1] != "owner" ||
		(parts[3] != "conversation" && parts[3] != "asset") || len(parts[2]) != 66 ||
		parts[2][0] != '{' || parts[2][65] != '}' || len(parts[4]) != 64 {
		return false
	}
	_, scopeErr := hex.DecodeString(parts[2][1:65])
	_, idErr := hex.DecodeString(parts[4])
	return scopeErr == nil && idErr == nil
}

func ValidChatGPTDeliveryOwner(owner *ChatGPTDeliveryOwner) bool {
	if owner == nil || owner.AccountID <= 0 || owner.TurnStartedMS < 0 {
		return false
	}
	if owner.UserMessageHash == "" {
		return true
	}
	_, err := hex.DecodeString(owner.UserMessageHash)
	return len(owner.UserMessageHash) == 64 && err == nil
}

func chatGPTDeliveryOwnerFromTurn(turn *ChatGPTTurnSnapshot) *ChatGPTDeliveryOwner {
	owner := &ChatGPTDeliveryOwner{AccountID: turn.AccountID, UserMessageHash: turn.UserMessageHash}
	if !turn.StartedAt.IsZero() {
		owner.TurnStartedMS = turn.StartedAt.UnixMilli()
	}
	return owner
}

// A still-live legacy turn is authoritative evidence for lazy migration.
// Missing ownership never falls back to scheduler/account enumeration.
func ResolveChatGPTConversationOwner(ctx context.Context, cache ChatGPTTurnCache, scope ChatGPTTurnScope, id string) (*ChatGPTDeliveryOwner, *ChatGPTTurnSnapshot, error) {
	turn, err := cache.GetChatGPTResume(ctx, scope.ResumeKey(id))
	if err != nil {
		return nil, nil, err
	}
	owner, err := cache.GetChatGPTDeliveryOwner(ctx, scope.ConversationOwnerKey(id))
	if err != nil {
		return nil, nil, err
	}
	if turn != nil {
		if owner != nil && owner.AccountID != turn.AccountID {
			return nil, nil, errors.New("native Chat conversation owner conflicts")
		}
		owner = chatGPTDeliveryOwnerFromTurn(turn)
	}
	if owner != nil {
		if err := cache.BindChatGPTDeliveryOwner(ctx, scope.ConversationOwnerKey(id), owner); err != nil {
			return nil, nil, err
		}
	}
	return owner, turn, nil
}

func BindChatGPTConversationDelivery(ctx context.Context, cache ChatGPTTurnCache, scope ChatGPTTurnScope, turn *ChatGPTTurnSnapshot, id string) error {
	owner := chatGPTDeliveryOwnerFromTurn(turn)
	if err := cache.BindChatGPTDeliveryOwner(ctx, scope.ConversationOwnerKey(id), owner); err != nil {
		return err
	}
	return cache.BindChatGPTUpdates(ctx, scope.ResumeKey(id), scope.TurnKey(turn.Identity), scope.UpdatesKey())
}

func BindChatGPTOwnedDeliveryAssets(ctx context.Context, cache ChatGPTTurnCache, scope ChatGPTTurnScope, accountID int64, images ChatGPTImageEvidence) error {
	for _, digest := range images.AssetHashes {
		if err := cache.BindChatGPTDeliveryOwner(ctx, scope.AssetOwnerKey(digest), &ChatGPTDeliveryOwner{AccountID: accountID}); err != nil {
			return err
		}
	}
	return nil
}

func (s *chatGPTMemoryTurnCache) BindChatGPTDeliveryOwner(_ context.Context, key string, owner *ChatGPTDeliveryOwner) error {
	if !ValidChatGPTDeliveryOwnerKey(key) || !ValidChatGPTDeliveryOwner(owner) {
		return errors.New("invalid native Chat delivery owner")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	if old, ok := s.deliveryOwners[key]; ok && old.owner.AccountID != owner.AccountID {
		return errors.New("native Chat delivery owner conflicts")
	}
	copy := *owner
	if old, ok := s.deliveryOwners[key]; ok && old.owner.TurnStartedMS > copy.TurnStartedMS {
		copy = old.owner
	}
	if _, ok := s.deliveryOwners[key]; !ok && len(s.deliveryOwners) >= 16384 {
		return errors.New("native Chat delivery owner cache is full")
	}
	s.deliveryOwners[key] = chatGPTMemoryDeliveryOwner{owner: copy, expires: time.Now().Add(ChatGPTDeliveryOwnerTTL)}
	return nil
}

func (s *chatGPTMemoryTurnCache) GetChatGPTDeliveryOwner(_ context.Context, key string) (*ChatGPTDeliveryOwner, error) {
	if !ValidChatGPTDeliveryOwnerKey(key) {
		return nil, errors.New("invalid native Chat delivery owner key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	if entry, ok := s.deliveryOwners[key]; ok {
		owner := entry.owner
		return &owner, nil
	}
	return nil, nil
}

type chatGPTMemoryDeliveryOwner struct {
	owner   ChatGPTDeliveryOwner
	expires time.Time
}
