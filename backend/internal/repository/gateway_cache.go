package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const stickySessionPrefix = "sticky_session:"
const pendingStickySessionPrefix = "pending_sticky_session:"
const openAIUnpricedModelSuccessPrefix = "openai_unpriced_model_success:"

var incrementOpenAIUnpricedModelSuccessScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('TTL', KEYS[1])
return {count, ttl}
`)

type gatewayCache struct {
	rdb *redis.Client
}

func NewGatewayCache(rdb *redis.Client) service.GatewayCache {
	return &gatewayCache{rdb: rdb}
}

// buildSessionKey 构建 session key，包含 groupID 实现分组隔离
// 格式: sticky_session:{groupID}:{sessionHash}
func buildSessionKey(groupID int64, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s", stickySessionPrefix, groupID, sessionHash)
}

func buildPendingSessionKey(groupID int64, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s", pendingStickySessionPrefix, groupID, sessionHash)
}

func buildOpenAIUnpricedModelSuccessKey(model string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(model))))
	return fmt.Sprintf("%s%x", openAIUnpricedModelSuccessPrefix, digest)
}

func (c *gatewayCache) GetUnpricedModelSuccessCount(ctx context.Context, model string) (int64, time.Duration, error) {
	key := buildOpenAIUnpricedModelSuccessKey(model)
	count, err := c.rdb.Get(ctx, key).Int64()
	if err == redis.Nil {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	ttl, err := c.rdb.TTL(ctx, key).Result()
	if err != nil {
		return 0, 0, err
	}
	return count, ttl, nil
}

func (c *gatewayCache) IncrementUnpricedModelSuccessCount(ctx context.Context, model string, window time.Duration) (int64, time.Duration, error) {
	windowSeconds := int64(window / time.Second)
	if windowSeconds < 1 {
		windowSeconds = 1
	}
	values, err := incrementOpenAIUnpricedModelSuccessScript.Run(ctx, c.rdb, []string{buildOpenAIUnpricedModelSuccessKey(model)}, windowSeconds).Slice()
	if err != nil {
		return 0, 0, err
	}
	if len(values) != 2 {
		return 0, 0, fmt.Errorf("unexpected unpriced-model counter result length: %d", len(values))
	}
	count, ok := values[0].(int64)
	if !ok {
		return 0, 0, fmt.Errorf("unexpected unpriced-model counter type %T", values[0])
	}
	ttlSeconds, ok := values[1].(int64)
	if !ok {
		return 0, 0, fmt.Errorf("unexpected unpriced-model ttl type %T", values[1])
	}
	return count, time.Duration(ttlSeconds) * time.Second, nil
}

func (c *gatewayCache) GetSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Get(ctx, key).Int64()
}

func (c *gatewayCache) SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Set(ctx, key, accountID, ttl).Err()
}

func (c *gatewayCache) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Expire(ctx, key, ttl).Err()
}

// DeleteSessionAccountID 删除粘性会话与账号的绑定关系。
// 当检测到绑定的账号不可用（如状态错误、禁用、不可调度等）时调用，
// 以便下次请求能够重新选择可用账号。
//
// DeleteSessionAccountID removes the sticky session binding for the given session.
// Called when the bound account becomes unavailable (e.g., error status, disabled,
// or unschedulable), allowing subsequent requests to select a new available account.
func (c *gatewayCache) DeleteSessionAccountID(ctx context.Context, groupID int64, sessionHash string) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Del(ctx, key).Err()
}

func (c *gatewayCache) GetPendingSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	key := buildPendingSessionKey(groupID, sessionHash)
	return c.rdb.Get(ctx, key).Int64()
}

func (c *gatewayCache) SetPendingSessionAccountIDNX(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) (bool, error) {
	key := buildPendingSessionKey(groupID, sessionHash)
	return c.rdb.SetNX(ctx, key, accountID, ttl).Result()
}

func (c *gatewayCache) DeletePendingSessionAccountID(ctx context.Context, groupID int64, sessionHash string) error {
	key := buildPendingSessionKey(groupID, sessionHash)
	return c.rdb.Del(ctx, key).Err()
}
