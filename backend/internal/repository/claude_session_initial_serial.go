package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const sessionInitialSerialStateTTL = time.Hour

// One hash keeps the account generation, reservation counter and lease atomic,
// including on Redis Cluster. No request bodies or credentials are stored.
func sessionInitialSerialKey(groupID int64, session string) string {
	return fmt.Sprintf("claude_session_initial_serial:%d:%x", groupID, sha256.Sum256([]byte(session)))
}

var acquireSessionInitialSerialScript = redis.NewScript(`
local account = redis.call('HGET', KEYS[1], 'account')
local generation = redis.call('HGET', KEYS[1], 'generation')
if ARGV[2] ~= '' and (account ~= ARGV[1] or generation ~= ARGV[2]) then
  return {3, generation or '', 0, tonumber(account or '0')}
end
if account ~= ARGV[1] then
  generation = ARGV[3]
  redis.call('HSET', KEYS[1], 'account', ARGV[1], 'generation', generation, 'count', 0)
  redis.call('HDEL', KEYS[1], 'owner', 'lease_until')
end
redis.call('PEXPIRE', KEYS[1], ARGV[5])
local count = tonumber(redis.call('HGET', KEYS[1], 'count') or '0')
local t = redis.call('TIME')
local now = tonumber(t[1])*1000 + math.floor(tonumber(t[2])/1000)
local owner = redis.call('HGET', KEYS[1], 'owner')
local lease_until = tonumber(redis.call('HGET', KEYS[1], 'lease_until') or '0')
-- Check the lease BEFORE count: request four must wait for request three too.
if owner and lease_until > now then
  if owner == ARGV[3] then
    return {1, generation, count, tonumber(account)}
  end
  return {0, generation, count, tonumber(ARGV[1])}
end
redis.call('HDEL', KEYS[1], 'owner', 'lease_until')
if count >= tonumber(ARGV[6]) then
  return {2, generation, count, tonumber(ARGV[1])}
end
count = count + 1
redis.call('HSET', KEYS[1], 'count', count, 'owner', ARGV[3], 'lease_until', now + tonumber(ARGV[4]))
return {1, generation, count, tonumber(ARGV[1])}
`)

var releaseSessionInitialSerialScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'account') ~= ARGV[1]
  or redis.call('HGET', KEYS[1], 'generation') ~= ARGV[2]
  or redis.call('HGET', KEYS[1], 'owner') ~= ARGV[3] then
  return 0
end
if ARGV[4] == '0' then
  redis.call('HINCRBY', KEYS[1], 'count', -1)
end
redis.call('HDEL', KEYS[1], 'owner', 'lease_until')
redis.call('PEXPIRE', KEYS[1], ARGV[5])
return 1
`)

func (c *gatewayCache) TryAcquireSessionInitialSerial(ctx context.Context, groupID int64, session string, accountID int64, generation, owner string, lease time.Duration) (*service.SessionInitialSerialResult, error) {
	values, err := acquireSessionInitialSerialScript.Run(ctx, c.rdb, []string{sessionInitialSerialKey(groupID, session)},
		accountID, generation, owner, lease.Milliseconds(), sessionInitialSerialStateTTL.Milliseconds(), service.ClaudeSessionInitialSerialRequests).Slice()
	if err != nil {
		return nil, fmt.Errorf("session initial serial acquire: %w", err)
	}
	if len(values) != 4 {
		return nil, fmt.Errorf("unexpected session initial serial result length: %d", len(values))
	}
	decision, decisionOK := values[0].(int64)
	gen, generationOK := values[1].(string)
	ordinal, ordinalOK := values[2].(int64)
	account, accountOK := values[3].(int64)
	if !decisionOK || !generationOK || !ordinalOK || !accountOK || decision < 0 || decision > 3 {
		return nil, fmt.Errorf("invalid session initial serial result")
	}
	return &service.SessionInitialSerialResult{Decision: service.SessionInitialSerialDecision(decision), Generation: gen, Ordinal: int(ordinal), AccountID: account}, nil
}

func (c *gatewayCache) ReleaseSessionInitialSerial(ctx context.Context, groupID int64, session string, accountID int64, generation, owner string, forwarded bool) error {
	attempted := 0
	if forwarded {
		attempted = 1
	}
	_, err := releaseSessionInitialSerialScript.Run(ctx, c.rdb, []string{sessionInitialSerialKey(groupID, session)},
		accountID, generation, owner, attempted, sessionInitialSerialStateTTL.Milliseconds()).Result()
	return err
}
