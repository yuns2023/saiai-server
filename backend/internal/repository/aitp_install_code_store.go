package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	// The code itself never appears in a Redis key (hashedKey stores sha256(code)).
	aitpInstallCodeKeyPrefix     = "aitp_install:code:"
	aitpInstallIssueKeyPrefix    = "aitp_install:issued:"
	aitpInstallRedeemIPKeyPrefix = "aitp_install:redeem_ip:"
)

// aitpInstallCounterScript is a fixed-window counter: INCR, and set the window on the first
// hit (or repair a counter that lost its TTL). Returns the new count.
var aitpInstallCounterScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 or redis.call('PTTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return n
`)

type aitpInstallCodeStore struct {
	rdb *redis.Client
}

// NewAITPInstallCodeStore stores one-time AITP install codes and their rate limits in Redis.
func NewAITPInstallCodeStore(rdb *redis.Client) service.AITPInstallCodeStore {
	return &aitpInstallCodeStore{rdb: rdb}
}

func aitpInstallCodeKey(code string) string { return hashedKey(aitpInstallCodeKeyPrefix, code) }

func (s *aitpInstallCodeStore) Save(ctx context.Context, code string, rec *service.AITPInstallCodeRecord, ttl time.Duration) (bool, error) {
	if rec == nil {
		return false, errors.New("nil install code record")
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return false, fmt.Errorf("marshal install code: %w", err)
	}
	ok, err := s.rdb.SetNX(ctx, aitpInstallCodeKey(code), data, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("store install code: %w", err)
	}
	return ok, nil
}

// Consume uses the GET+DEL script (GETDEL needs Redis >= 6.2).
func (s *aitpInstallCodeStore) Consume(ctx context.Context, code string) (*service.AITPInstallCodeRecord, error) {
	var rec service.AITPInstallCodeRecord
	ok, err := consumeJSON(ctx, s.rdb, aitpInstallCodeKey(code), &rec)
	if err != nil {
		return nil, fmt.Errorf("consume install code: %w", err)
	}
	if !ok {
		return nil, nil
	}
	return &rec, nil
}

func (s *aitpInstallCodeStore) CountIssued(ctx context.Context, userID int64, window time.Duration) (int64, error) {
	return s.count(ctx, aitpInstallIssueKeyPrefix+strconv.FormatInt(userID, 10), window)
}

func (s *aitpInstallCodeStore) CountRedeemAttempt(ctx context.Context, clientIP string, window time.Duration) (int64, error) {
	return s.count(ctx, aitpInstallRedeemIPKeyPrefix+clientIP, window)
}

func (s *aitpInstallCodeStore) count(ctx context.Context, key string, window time.Duration) (int64, error) {
	ms := window.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	n, err := aitpInstallCounterScript.Run(ctx, s.rdb, []string{key}, ms).Int64()
	if err != nil {
		return 0, fmt.Errorf("install code counter: %w", err)
	}
	return n, nil
}
