package repository

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

var chatGPTClaimUpload = redis.NewScript("redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2], 'NX'); return redis.call('GET', KEYS[1])")
var chatGPTBindUpload = redis.NewScript(`
for _, key in ipairs(KEYS) do
  local old = redis.call('GET', key)
  if old and old ~= ARGV[1] then return 0 end
end
for _, key in ipairs(KEYS) do
  redis.call('SET', key, ARGV[1], 'PX', ARGV[2], 'NX')
end
return 1
`)

func (c *gatewayCache) GetChatGPTUploadOwner(ctx context.Context, key string) (int64, error) {
	raw, err := c.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	owner, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || owner <= 0 {
		return 0, errors.New("invalid native Chat upload owner")
	}
	return owner, nil
}
func (c *gatewayCache) ClaimChatGPTUploadOwner(ctx context.Context, key string, owner int64, ttl time.Duration) (int64, error) {
	if owner <= 0 || ttl <= 0 || ttl > service.ChatGPTUploadTTL {
		return 0, errors.New("invalid native Chat upload claim")
	}
	raw, err := chatGPTClaimUpload.Run(ctx, c.rdb, []string{key}, strconv.FormatInt(owner, 10), ttl.Milliseconds()).Text()
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("invalid native Chat upload owner")
	}
	return value, nil
}
func (c *gatewayCache) BindChatGPTUploadOwner(ctx context.Context, keys []string, owner int64, ttl time.Duration) error {
	if owner <= 0 || ttl <= 0 || ttl > service.ChatGPTUploadTTL || len(keys) == 0 || len(keys) > 4 {
		return errors.New("invalid native Chat upload binding")
	}
	result, err := chatGPTBindUpload.Run(ctx, c.rdb, keys, strconv.FormatInt(owner, 10), ttl.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return errors.New("native Chat upload ownership conflict")
	}
	return nil
}
