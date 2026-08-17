package repository

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const inputModerationCooldownKeyPrefix = "input_moderation:cooldown:user:"

type inputModerationStateCache struct {
	rdb *redis.Client
}

func NewInputModerationStateCache(rdb *redis.Client) service.InputModerationStateCache {
	return &inputModerationStateCache{rdb: rdb}
}

func inputModerationCooldownKey(userID int64) string {
	return fmt.Sprintf("%s%d", inputModerationCooldownKeyPrefix, userID)
}

func (c *inputModerationStateCache) GetUserCooldown(ctx context.Context, userID int64) (*time.Time, bool, error) {
	if c == nil || c.rdb == nil || userID <= 0 {
		return nil, false, nil
	}
	value, err := c.rdb.Get(ctx, inputModerationCooldownKey(userID)).Result()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if value == "0" {
		return nil, true, nil
	}
	unixMilli, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, false, fmt.Errorf("parse input moderation cooldown: %w", err)
	}
	blockedUntil := time.UnixMilli(unixMilli)
	if !blockedUntil.After(time.Now()) {
		_ = c.rdb.Del(ctx, inputModerationCooldownKey(userID)).Err()
		return nil, false, nil
	}
	return &blockedUntil, true, nil
}

func (c *inputModerationStateCache) SetUserCooldown(ctx context.Context, userID int64, blockedUntil time.Time) error {
	if c == nil || c.rdb == nil || userID <= 0 {
		return nil
	}
	ttl := time.Until(blockedUntil)
	if ttl <= 0 {
		return c.ClearUserCooldown(ctx, userID)
	}
	return c.rdb.Set(ctx, inputModerationCooldownKey(userID), strconv.FormatInt(blockedUntil.UnixMilli(), 10), ttl).Err()
}

func (c *inputModerationStateCache) SetUserNoCooldown(ctx context.Context, userID int64, ttl time.Duration) error {
	if c == nil || c.rdb == nil || userID <= 0 {
		return nil
	}
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	return c.rdb.Set(ctx, inputModerationCooldownKey(userID), "0", ttl).Err()
}

func (c *inputModerationStateCache) ClearUserCooldown(ctx context.Context, userID int64) error {
	if c == nil || c.rdb == nil || userID <= 0 {
		return nil
	}
	return c.rdb.Del(ctx, inputModerationCooldownKey(userID)).Err()
}
