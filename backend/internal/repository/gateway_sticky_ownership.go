package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

func stickyOwnershipKey(group int64, session string) string {
	return fmt.Sprintf("sticky_owner:%d:%s", group, session)
}

func (c *gatewayCache) GetStickyOwnership(ctx context.Context, group int64, session string) (string, error) {
	value, err := c.rdb.Get(ctx, stickyOwnershipKey(group, session)).Result()
	if err == redis.Nil {
		return "", nil
	}
	return value, err
}

var claimStickyOwnershipScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1]) or ''
if current ~= ARGV[1] then return 0 end
local ttl = tonumber(ARGV[4])
if current == ARGV[2] then
  ttl = math.max(ttl, redis.call('PTTL', KEYS[1]))
end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ttl)
local confirmed = redis.call('GET', KEYS[2])
if confirmed ~= ARGV[3] then
  redis.call('DEL', KEYS[2])
  redis.call('SET', KEYS[3], ARGV[3], 'PX', ARGV[4])
end
return 1
`)

func (c *gatewayCache) ClaimStickyOwnership(ctx context.Context, group int64, session, previous, token string, account int64, ttl time.Duration) (bool, error) {
	value, err := claimStickyOwnershipScript.Run(ctx, c.rdb, []string{stickyOwnershipKey(group, session), buildSessionKey(group, session), buildPendingSessionKey(group, session)}, previous, token, account, ttl.Milliseconds()).Int()
	return value == 1, err
}

var confirmStickyOwnershipScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[3])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
redis.call('DEL', KEYS[3])
return 1
`)

func (c *gatewayCache) ConfirmStickyOwnership(ctx context.Context, group int64, session, token string, account int64, ttl time.Duration) (bool, error) {
	value, err := confirmStickyOwnershipScript.Run(ctx, c.rdb, []string{stickyOwnershipKey(group, session), buildSessionKey(group, session), buildPendingSessionKey(group, session)}, token, account, ttl.Milliseconds()).Int()
	return value == 1, err
}

var renewStickyOwnershipScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
local ttl = math.max(tonumber(ARGV[2]), redis.call('PTTL', KEYS[1]))
redis.call('PEXPIRE', KEYS[1], ttl)
return 1
`)

func (c *gatewayCache) RenewStickyOwnership(ctx context.Context, group int64, session, token string, ttl time.Duration) (bool, error) {
	value, err := renewStickyOwnershipScript.Run(ctx, c.rdb, []string{stickyOwnershipKey(group, session)}, token, ttl.Milliseconds()).Int()
	return value == 1, err
}
