package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/tidwall/gjson"
)

const ChatGPTTurnContextTTL = time.Hour

// ChatGPTTurnSnapshot contains accounting metadata only. Message content,
// conversation IDs, credentials and resume tokens must never be stored here.
type ChatGPTTurnSnapshot struct {
	Identity       ChatGPTTurnBillingIdentity
	AccountID      int64
	BasePriceUSD   float64
	RequestedModel string
	ThinkingEffort string
	StartedAt      time.Time
	Completed      bool
	TerminalSeen   bool
	Images         ChatGPTImageEvidence
}

// ChatGPTTurnCache is implemented by the shared Redis Gateway cache. Operations
// are atomic across instances and retain the original expiry and price.
type ChatGPTTurnCache interface {
	GetChatGPTTurn(context.Context, string) (*ChatGPTTurnSnapshot, error)
	PutChatGPTTurnIfAbsent(context.Context, string, *ChatGPTTurnSnapshot, time.Duration) (*ChatGPTTurnSnapshot, error)
	BindChatGPTResume(context.Context, string, string) error
	GetChatGPTResume(context.Context, string) (*ChatGPTTurnSnapshot, error)
	CompleteChatGPTTurn(context.Context, string) error
	MarkChatGPTTurnTerminal(context.Context, string) error
	MergeChatGPTTurnImages(context.Context, string, ChatGPTImageEvidence) (*ChatGPTTurnSnapshot, error)
}

type ChatGPTTurnScope struct{ UserID, APIKeyID, GroupID int64 }

func (s ChatGPTTurnScope) namespace() string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("chatgpt-turn-scope-v1:%d:%d:%d", s.UserID, s.APIKeyID, s.GroupID)))
	return hex.EncodeToString(digest[:])
}

func (s ChatGPTTurnScope) TurnKey(identity ChatGPTTurnBillingIdentity) string {
	digest := sha256.Sum256([]byte(identity.RequestID))
	return fmt.Sprintf("chatgpt:turn:{%s}:%x", s.namespace(), digest)
}

func (s ChatGPTTurnScope) ResumeKey(conversationID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(conversationID)))
	return fmt.Sprintf("chatgpt:resume:{%s}:%x", s.namespace(), digest)
}

func ChatGPTRequestMetadata(body []byte) (model, effort string) {
	return strings.TrimSpace(gjson.GetBytes(body, "model").String()),
		strings.TrimSpace(gjson.GetBytes(body, "thinking_effort").String())
}

func ValidChatGPTMetadataValue(value string, limit int) bool {
	if len(value) > limit {
		return false
	}
	for _, c := range value {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("-_.:/", c) {
			continue
		}
		return false
	}
	return true
}

// ResolveChatGPTResumeConversation validates only delivery-control semantics.
// Unknown extension fields are preserved; new generation inputs are rejected.
func ResolveChatGPTResumeConversation(body []byte) (string, error) {
	for _, key := range []string{"messages", "input", "prompt", "tools", "model", "instructions"} {
		if gjson.GetBytes(body, key).Exists() {
			return "", errors.New("resume contains generation inputs")
		}
	}
	id := gjson.GetBytes(body, "conversation_id")
	if id.Type != gjson.String || strings.TrimSpace(id.String()) == "" || len(id.String()) > 512 {
		return "", errors.New("resume requires a conversation identity")
	}
	return strings.TrimSpace(id.String()), nil
}

func (s *OpenAIGatewayService) ChatGPTTurnCache() (ChatGPTTurnCache, error) {
	if s == nil {
		return nil, errors.New("native Chat turn cache unavailable")
	}
	if cache, ok := s.cache.(ChatGPTTurnCache); ok {
		return cache, nil
	}
	// Live gateways require shared storage. Redis errors never fall back locally.
	if s.cfg == nil || (s.cfg.RunMode != config.RunModeSimple && strings.TrimSpace(s.cfg.Gateway.OpenAIChatUpstreamBaseURL) == "") {
		return nil, errors.New("shared native Chat turn cache unavailable")
	}
	s.chatGPTTurnCacheOnce.Do(func() { s.chatGPTTurnMemoryCache = NewChatGPTMemoryTurnCache() })
	return s.chatGPTTurnMemoryCache, nil
}

// SelectChatGPTBoundAccount never fails over a resume to another provider
// account. The owner must still be schedulable in the authenticated group.
func (s *OpenAIGatewayService) SelectChatGPTBoundAccount(ctx context.Context, groupID *int64, ownerID int64) (*AccountSelectionResult, error) {
	if s == nil || s.accountRepo == nil || groupID == nil || ownerID <= 0 {
		return nil, ErrNoAvailableAccounts
	}
	accounts, err := s.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, *groupID, PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	for _, candidate := range accounts {
		if candidate.ID != ownerID {
			continue
		}
		account, err := s.accountRepo.GetByID(ctx, ownerID)
		if err != nil || account == nil || account.ID != ownerID || !account.IsOpenAIOAuth() || !account.IsSchedulable() {
			return nil, ErrNoAvailableAccounts
		}
		max := account.Concurrency
		if max < 1 {
			max = 1
		}
		selection := &AccountSelectionResult{Account: account, WaitPlan: &AccountWaitPlan{MaxConcurrency: max, Timeout: 30 * time.Second, MaxWaiting: 3}}
		if s.concurrencyService != nil {
			acquired, err := s.concurrencyService.AcquireAccountSlot(ctx, account.ID, max)
			if err != nil {
				return nil, err
			}
			if acquired.Acquired {
				selection.Acquired, selection.ReleaseFunc = true, acquired.ReleaseFunc
			}
		}
		return selection, nil
	}
	return nil, ErrNoAvailableAccounts
}

type chatGPTMemoryEntry struct {
	snapshot ChatGPTTurnSnapshot
	expires  time.Time
}
type chatGPTMemoryAlias struct {
	turnKey string
	expires time.Time
}

func cloneChatGPTTurnSnapshot(input ChatGPTTurnSnapshot) *ChatGPTTurnSnapshot {
	copy := input
	copy.Images.AssetHashes = append([]string(nil), input.Images.AssetHashes...)
	return &copy
}

type chatGPTMemoryTurnCache struct {
	mu      sync.Mutex
	turns   map[string]chatGPTMemoryEntry
	aliases map[string]chatGPTMemoryAlias
}

func NewChatGPTMemoryTurnCache() ChatGPTTurnCache {
	return &chatGPTMemoryTurnCache{turns: make(map[string]chatGPTMemoryEntry), aliases: make(map[string]chatGPTMemoryAlias)}
}
func (s *chatGPTMemoryTurnCache) cleanup() {
	now := time.Now()
	for key, entry := range s.turns {
		if !now.Before(entry.expires) {
			delete(s.turns, key)
		}
	}
	for key, alias := range s.aliases {
		if !now.Before(alias.expires) {
			delete(s.aliases, key)
		}
	}
}
func (s *chatGPTMemoryTurnCache) GetChatGPTTurn(_ context.Context, key string) (*ChatGPTTurnSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	if entry, ok := s.turns[key]; ok {
		return cloneChatGPTTurnSnapshot(entry.snapshot), nil
	}
	return nil, nil
}
func (s *chatGPTMemoryTurnCache) PutChatGPTTurnIfAbsent(_ context.Context, key string, input *ChatGPTTurnSnapshot, ttl time.Duration) (*ChatGPTTurnSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	if entry, ok := s.turns[key]; ok {
		return cloneChatGPTTurnSnapshot(entry.snapshot), nil
	}
	if len(s.turns) >= 4096 {
		return nil, errors.New("native Chat replay cache is full")
	}
	snapshot := cloneChatGPTTurnSnapshot(*input)
	s.turns[key] = chatGPTMemoryEntry{*snapshot, time.Now().Add(ttl)}
	return cloneChatGPTTurnSnapshot(*snapshot), nil
}

func (s *chatGPTMemoryTurnCache) MergeChatGPTTurnImages(_ context.Context, key string, images ChatGPTImageEvidence) (*ChatGPTTurnSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	entry, ok := s.turns[key]
	if !ok {
		return nil, errors.New("native Chat turn context expired")
	}
	entry.snapshot.Images = MergeChatGPTImageEvidence(entry.snapshot.Images, images)
	s.turns[key] = entry
	return cloneChatGPTTurnSnapshot(entry.snapshot), nil
}
func (s *chatGPTMemoryTurnCache) BindChatGPTResume(_ context.Context, aliasKey, turnKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	entry, ok := s.turns[turnKey]
	if !ok {
		return errors.New("native Chat turn context expired")
	}
	if old, ok := s.aliases[aliasKey]; ok && old.turnKey != turnKey {
		if prior, ok := s.turns[old.turnKey]; ok && !prior.snapshot.Completed && !prior.snapshot.TerminalSeen {
			return errors.New("another Chat turn is pending")
		}
	}
	if _, ok := s.aliases[aliasKey]; !ok && len(s.aliases) >= 4096 {
		return errors.New("native Chat replay aliases are full")
	}
	s.aliases[aliasKey] = chatGPTMemoryAlias{turnKey, entry.expires}
	return nil
}
func (s *chatGPTMemoryTurnCache) GetChatGPTResume(_ context.Context, key string) (*ChatGPTTurnSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	alias, ok := s.aliases[key]
	if !ok {
		return nil, nil
	}
	entry, ok := s.turns[alias.turnKey]
	if !ok {
		return nil, nil
	}
	return cloneChatGPTTurnSnapshot(entry.snapshot), nil
}
func (s *chatGPTMemoryTurnCache) CompleteChatGPTTurn(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	entry, ok := s.turns[key]
	if !ok {
		return errors.New("native Chat turn context expired")
	}
	entry.snapshot.Completed = true
	s.turns[key] = entry
	return nil
}

func (s *chatGPTMemoryTurnCache) MarkChatGPTTurnTerminal(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	entry, ok := s.turns[key]
	if !ok {
		return errors.New("native Chat turn context expired")
	}
	entry.snapshot.TerminalSeen = true
	s.turns[key] = entry
	return nil
}
