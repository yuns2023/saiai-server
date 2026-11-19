package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Market (P3a) buyer slots. The account's total slot set stays
// concurrency:account:{id} (accountSlotKey); buyer leases are additionally
// members of concurrency:account_buyer:{id} under the same request id, added,
// refreshed, expired and removed together with their total-set member. Owner
// and platform leases only touch the total set (acquireScript, unchanged), so
// accounts that are not on the market behave exactly as before.
const accountBuyerSlotKeyPrefix = "concurrency:account_buyer:"

func accountBuyerSlotKey(accountID int64) string {
	return fmt.Sprintf("%s%d", accountBuyerSlotKeyPrefix, accountID)
}

var (
	// acquireBuyerScript takes a buyer lease iff total < C AND buyer < B
	// (B = C - reserved, always >= 1: the service never calls it with a
	// non-positive buyer cap).
	// KEYS[1] = total set, KEYS[2] = buyer set
	// ARGV[1] = C, ARGV[2] = B, ARGV[3] = TTL seconds, ARGV[4] = requestID
	// Returns 1 = acquired, 0 = full.
	acquireBuyerScript = redis.NewScript(`
		local totalKey = KEYS[1]
		local buyerKey = KEYS[2]
		local totalMax = tonumber(ARGV[1])
		local buyerMax = tonumber(ARGV[2])
		local ttl = tonumber(ARGV[3])
		local requestID = ARGV[4]

		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl
		redis.call('ZREMRANGEBYSCORE', totalKey, '-inf', expireBefore)
		redis.call('ZREMRANGEBYSCORE', buyerKey, '-inf', expireBefore)

		local inTotal = redis.call('ZSCORE', totalKey, requestID) ~= false
		local inBuyer = redis.call('ZSCORE', buyerKey, requestID) ~= false
		if inTotal and inBuyer then
			-- Retry of the same acquire (both members were added atomically).
			redis.call('ZADD', totalKey, now, requestID)
			redis.call('ZADD', buyerKey, now, requestID)
			redis.call('EXPIRE', totalKey, ttl)
			redis.call('EXPIRE', buyerKey, ttl)
			return 1
		end
		-- A half lease never exists after this script; drop it and decide anew.
		if inTotal then redis.call('ZREM', totalKey, requestID) end
		if inBuyer then redis.call('ZREM', buyerKey, requestID) end

		if totalMax < 1 or buyerMax < 1 then
			return 0
		end
		if redis.call('ZCARD', totalKey) < totalMax and redis.call('ZCARD', buyerKey) < buyerMax then
			redis.call('ZADD', totalKey, now, requestID)
			redis.call('ZADD', buyerKey, now, requestID)
			redis.call('EXPIRE', totalKey, ttl)
			redis.call('EXPIRE', buyerKey, ttl)
			return 1
		end
		return 0
	`)

	// renewBuyerScript refreshes an existing buyer lease in both sets (XX:
	// never re-adds a member that already expired or was released).
	// Returns 1 when both members were still present.
	// Only a lease present (and unexpired) in BOTH sets is refreshed; a
	// half lease is never refreshed (its remaining member just expires).
	renewBuyerScript = redis.NewScript(`
		local ttl = tonumber(ARGV[1])
		local requestID = ARGV[2]
		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl
		local a = redis.call('ZSCORE', KEYS[1], requestID)
		local b = redis.call('ZSCORE', KEYS[2], requestID)
		if a == false or b == false or tonumber(a) <= expireBefore or tonumber(b) <= expireBefore then
			return 0
		end
		redis.call('ZADD', KEYS[1], 'XX', now, requestID)
		redis.call('ZADD', KEYS[2], 'XX', now, requestID)
		redis.call('EXPIRE', KEYS[1], ttl)
		redis.call('EXPIRE', KEYS[2], ttl)
		return 1
	`)

	// heldBuyerScript: 1 iff the lease is a live member of both sets.
	heldBuyerScript = redis.NewScript(`
		local ttl = tonumber(ARGV[1])
		local requestID = ARGV[2]
		local timeResult = redis.call('TIME')
		local expireBefore = tonumber(timeResult[1]) - ttl
		local a = redis.call('ZSCORE', KEYS[1], requestID)
		local b = redis.call('ZSCORE', KEYS[2], requestID)
		if a == false or b == false or tonumber(a) <= expireBefore or tonumber(b) <= expireBefore then
			return 0
		end
		return 1
	`)
)

var _ service.MarketSlotCache = (*concurrencyCache)(nil)

func (c *concurrencyCache) AcquireAccountBuyerSlot(ctx context.Context, accountID int64, totalMax, buyerMax int, requestID string) (bool, error) {
	keys := []string{accountSlotKey(accountID), accountBuyerSlotKey(accountID)}
	result, err := acquireBuyerScript.Run(ctx, c.rdb, keys, totalMax, buyerMax, c.slotTTLSeconds, requestID).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// ReleaseAccountBuyerSlot removes the lease from both sets in one MULTI.
func (c *concurrencyCache) ReleaseAccountBuyerSlot(ctx context.Context, accountID int64, requestID string) error {
	_, err := c.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.ZRem(ctx, accountSlotKey(accountID), requestID)
		pipe.ZRem(ctx, accountBuyerSlotKey(accountID), requestID)
		return nil
	})
	return err
}

func (c *concurrencyCache) AccountBuyerLeaseHeld(ctx context.Context, accountID int64, requestID string) (bool, error) {
	keys := []string{accountSlotKey(accountID), accountBuyerSlotKey(accountID)}
	result, err := heldBuyerScript.Run(ctx, c.rdb, keys, c.slotTTLSeconds, requestID).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) RenewAccountBuyerSlot(ctx context.Context, accountID int64, requestID string) (bool, error) {
	keys := []string{accountSlotKey(accountID), accountBuyerSlotKey(accountID)}
	result, err := renewBuyerScript.Run(ctx, c.rdb, keys, c.slotTTLSeconds, requestID).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// GetAccountBuyerConcurrencyBatch counts live buyer leases (expired members
// are trimmed first, like GetAccountConcurrencyBatch).
func (c *concurrencyCache) GetAccountBuyerConcurrencyBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(accountIDs))
	if len(accountIDs) == 0 {
		return out, nil
	}
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	cutoff := strconv.FormatInt(now.Unix()-int64(c.slotTTLSeconds), 10)
	pipe := c.rdb.Pipeline()
	cmds := make(map[int64]*redis.IntCmd, len(accountIDs))
	for _, id := range accountIDs {
		key := accountBuyerSlotKey(id)
		pipe.ZRemRangeByScore(ctx, key, "-inf", cutoff)
		cmds[id] = pipe.ZCard(ctx, key)
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}
	for id, cmd := range cmds {
		out[id] = int(cmd.Val())
	}
	return out, nil
}

func (c *concurrencyCache) SlotTTLSeconds() int {
	return c.slotTTLSeconds
}
