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

const socialOAuthPendingKeyPrefix = "social_oauth:pending:"

// socialOAuthConsumeScript atomically reads and deletes a pending sign-up (GETDEL works only
// on Redis >= 6.2; the script works on every supported version).
var socialOAuthConsumeScript = redis.NewScript(`
local v = redis.call('GET', KEYS[1])
if v then
  redis.call('DEL', KEYS[1])
end
return v
`)

type socialOAuthPendingStore struct {
	rdb *redis.Client
}

// NewSocialOAuthPendingStore stores one-time pending OAuth sign-ups in Redis.
// Only a hash of the handle is used as the key.
func NewSocialOAuthPendingStore(rdb *redis.Client) service.SocialOAuthPendingStore {
	return &socialOAuthPendingStore{rdb: rdb}
}

func socialOAuthPendingKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return socialOAuthPendingKeyPrefix + hex.EncodeToString(sum[:])
}

func (s *socialOAuthPendingStore) Save(ctx context.Context, token string, pending *service.SocialOAuthPendingSignup, ttl time.Duration) error {
	if pending == nil {
		return errors.New("nil pending sign-up")
	}
	data, err := json.Marshal(pending)
	if err != nil {
		return fmt.Errorf("marshal pending sign-up: %w", err)
	}
	ok, err := s.rdb.SetNX(ctx, socialOAuthPendingKey(token), data, ttl).Result()
	if err != nil {
		return fmt.Errorf("store pending sign-up: %w", err)
	}
	if !ok {
		return errors.New("pending sign-up token collision")
	}
	return nil
}

func (s *socialOAuthPendingStore) Peek(ctx context.Context, token string) (*service.SocialOAuthPendingSignup, error) {
	data, err := s.rdb.Get(ctx, socialOAuthPendingKey(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pending sign-up: %w", err)
	}
	return decodeSocialOAuthPending(data)
}

func (s *socialOAuthPendingStore) Consume(ctx context.Context, token string) (*service.SocialOAuthPendingSignup, error) {
	raw, err := socialOAuthConsumeScript.Run(ctx, s.rdb, []string{socialOAuthPendingKey(token)}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("consume pending sign-up: %w", err)
	}
	str, ok := raw.(string)
	if !ok {
		return nil, nil
	}
	return decodeSocialOAuthPending([]byte(str))
}

func decodeSocialOAuthPending(data []byte) (*service.SocialOAuthPendingSignup, error) {
	var pending service.SocialOAuthPendingSignup
	if err := json.Unmarshal(data, &pending); err != nil {
		return nil, fmt.Errorf("decode pending sign-up: %w", err)
	}
	return &pending, nil
}
