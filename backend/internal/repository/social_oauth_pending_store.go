package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	socialOAuthPendingKeyPrefix     = "social_oauth:pending:"
	socialOAuthPendingFailKeyPrefix = "social_oauth:pending_fail:"
	socialOAuthHandoffKeyPrefix     = "social_oauth:handoff:"
)

// socialOAuthConsumeScript atomically reads and deletes a one-time entry (GETDEL works only
// on Redis >= 6.2; the script works on every supported version).
var socialOAuthConsumeScript = redis.NewScript(`
local v = redis.call('GET', KEYS[1])
if v then
  redis.call('DEL', KEYS[1])
end
return v
`)

// socialOAuthFailureScript counts a failure and deletes the pending entry once the budget is spent.
// KEYS[1] = pending key, KEYS[2] = failure counter; ARGV[1] = max failures, ARGV[2] = ttl ms.
var socialOAuthFailureScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[2])
if n == 1 then
  redis.call('PEXPIRE', KEYS[2], ARGV[2])
end
if n >= tonumber(ARGV[1]) then
  redis.call('DEL', KEYS[1])
  redis.call('DEL', KEYS[2])
  return 1
end
return 0
`)

// hashedKey never uses the raw handle as a Redis key.
func hashedKey(prefix, token string) string {
	sum := sha256.Sum256([]byte(token))
	return prefix + hex.EncodeToString(sum[:])
}

func socialOAuthPendingKey(token string) string { return hashedKey(socialOAuthPendingKeyPrefix, token) }

func setOnceJSON(ctx context.Context, rdb *redis.Client, key string, value any, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	ok, err := rdb.SetNX(ctx, key, data, ttl).Result()
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if !ok {
		return errors.New("one-time key collision")
	}
	return nil
}

func consumeJSON(ctx context.Context, rdb *redis.Client, key string, out any) (bool, error) {
	raw, err := socialOAuthConsumeScript.Run(ctx, rdb, []string{key}).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("consume: %w", err)
	}
	str, ok := raw.(string)
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal([]byte(str), out); err != nil {
		return false, fmt.Errorf("decode: %w", err)
	}
	return true, nil
}

type socialOAuthPendingStore struct {
	rdb *redis.Client
}

// NewSocialOAuthPendingStore stores one-time pending OAuth sign-ups in Redis.
func NewSocialOAuthPendingStore(rdb *redis.Client) service.SocialOAuthPendingStore {
	return &socialOAuthPendingStore{rdb: rdb}
}

func (s *socialOAuthPendingStore) Save(ctx context.Context, token string, pending *service.SocialOAuthPendingSignup, ttl time.Duration) error {
	if pending == nil {
		return errors.New("nil pending sign-up")
	}
	return setOnceJSON(ctx, s.rdb, socialOAuthPendingKey(token), pending, ttl)
}

func (s *socialOAuthPendingStore) Peek(ctx context.Context, token string) (*service.SocialOAuthPendingSignup, error) {
	data, err := s.rdb.Get(ctx, socialOAuthPendingKey(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pending sign-up: %w", err)
	}
	var pending service.SocialOAuthPendingSignup
	if err := json.Unmarshal(data, &pending); err != nil {
		return nil, fmt.Errorf("decode pending sign-up: %w", err)
	}
	return &pending, nil
}

func (s *socialOAuthPendingStore) Consume(ctx context.Context, token string) (*service.SocialOAuthPendingSignup, error) {
	var pending service.SocialOAuthPendingSignup
	ok, err := consumeJSON(ctx, s.rdb, socialOAuthPendingKey(token), &pending)
	if err != nil || !ok {
		return nil, err
	}
	return &pending, nil
}

func (s *socialOAuthPendingStore) RecordFailure(ctx context.Context, token string, maxFailures int, ttl time.Duration) (bool, error) {
	n, err := socialOAuthFailureScript.Run(ctx, s.rdb,
		[]string{socialOAuthPendingKey(token), hashedKey(socialOAuthPendingFailKeyPrefix, token)},
		maxFailures, ttl.Milliseconds(),
	).Int()
	if err != nil {
		return false, fmt.Errorf("record pending failure: %w", err)
	}
	return n == 1, nil
}

type socialOAuthHandoffStore struct {
	rdb *redis.Client
}

// NewSocialOAuthHandoffStore stores one-time OAuth callback handoffs in Redis.
func NewSocialOAuthHandoffStore(rdb *redis.Client) service.SocialOAuthHandoffStore {
	return &socialOAuthHandoffStore{rdb: rdb}
}

func (s *socialOAuthHandoffStore) Save(ctx context.Context, code string, handoff *service.SocialOAuthHandoff, ttl time.Duration) error {
	if handoff == nil {
		return errors.New("nil handoff")
	}
	return setOnceJSON(ctx, s.rdb, hashedKey(socialOAuthHandoffKeyPrefix, code), handoff, ttl)
}

func (s *socialOAuthHandoffStore) Consume(ctx context.Context, code string) (*service.SocialOAuthHandoff, error) {
	var handoff service.SocialOAuthHandoff
	ok, err := consumeJSON(ctx, s.rdb, hashedKey(socialOAuthHandoffKeyPrefix, code), &handoff)
	if err != nil || !ok {
		return nil, err
	}
	return &handoff, nil
}
