package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// The runtime may only consume a previously armed budget. Missing, evicted,
// expired, malformed or policy-mismatched records fail closed. Keeping arming
// separate prevents cache loss or a Gateway restart from granting more calls.
var reserveOpenAIProviderAttemptScript = redis.NewScript(`
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
if now >= tonumber(ARGV[3]) then return {-2, 0} end
if redis.call('EXISTS', KEYS[1]) == 0 then return {-4, 0} end
local values = redis.call('HMGET', KEYS[1], 'api_key_id', 'limit', 'expires_ms', 'used')
if values[1] ~= ARGV[1] or values[2] ~= ARGV[2] or values[3] ~= ARGV[3] then return {-3, 0} end
local used = tonumber(values[4])
if not used or used < 0 or used ~= math.floor(used) then return {-3, 0} end
if used >= tonumber(ARGV[2]) then return {0, used} end
return {1, redis.call('HINCRBY', KEYS[1], 'used', 1)}
`)

// ArmOpenAIProviderAttemptBudget is an explicit operator action, never called
// by request forwarding. An existing ID cannot be overwritten or renewed.
var armOpenAIProviderAttemptScript = redis.NewScript(`
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
if now >= tonumber(ARGV[3]) then return -2 end
if redis.call('EXISTS', KEYS[1]) ~= 0 then return -3 end
redis.call('HSET', KEYS[1], 'api_key_id', ARGV[1], 'limit', ARGV[2], 'expires_ms', ARGV[3], 'used', 0)
redis.call('PEXPIREAT', KEYS[1], tonumber(ARGV[3]) + 86400000)
return 1
`)

func buildOpenAIProviderAttemptBudgetKey(id string) string {
	digest := sha256.Sum256([]byte(id))
	return fmt.Sprintf("openai_provider_attempt_budget:%x", digest)
}

func (c *gatewayCache) ReserveOpenAIProviderAttempt(ctx context.Context, id string, apiKeyID, limit int64, expiresAt time.Time) (int64, error) {
	if id == "" || apiKeyID <= 0 || limit <= 0 || expiresAt.IsZero() {
		return 0, service.ErrOpenAIProviderAttemptBudgetUnavailable
	}
	values, err := reserveOpenAIProviderAttemptScript.Run(ctx, c.rdb, []string{buildOpenAIProviderAttemptBudgetKey(id)},
		apiKeyID, limit, expiresAt.UnixMilli()).Int64Slice()
	if err != nil || len(values) != 2 {
		return 0, service.ErrOpenAIProviderAttemptBudgetUnavailable
	}
	switch values[0] {
	case 1:
		return values[1], nil
	case 0:
		return values[1], service.ErrOpenAIProviderAttemptBudgetExhausted
	case -2:
		return 0, service.ErrOpenAIProviderAttemptBudgetExpired
	case -4:
		return 0, service.ErrOpenAIProviderAttemptBudgetUnarmed
	default:
		return 0, service.ErrOpenAIProviderAttemptBudgetUnavailable
	}
}

func (c *gatewayCache) ArmOpenAIProviderAttemptBudget(ctx context.Context, id string, apiKeyID, limit int64, expiresAt time.Time) error {
	if id == "" || apiKeyID <= 0 || limit <= 0 || expiresAt.IsZero() {
		return service.ErrOpenAIProviderAttemptBudgetUnavailable
	}
	result, err := armOpenAIProviderAttemptScript.Run(ctx, c.rdb, []string{buildOpenAIProviderAttemptBudgetKey(id)},
		apiKeyID, limit, expiresAt.UnixMilli()).Int64()
	if err != nil {
		return service.ErrOpenAIProviderAttemptBudgetUnavailable
	}
	if result == -2 {
		return service.ErrOpenAIProviderAttemptBudgetExpired
	}
	if result != 1 {
		return service.ErrOpenAIProviderAttemptBudgetUnavailable
	}
	return nil
}
