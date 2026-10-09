package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

var chatGPTBindDeliveryOwner = redis.NewScript(`
local incoming = cjson.decode(ARGV[1])
local raw = ARGV[1]
local prior = redis.call('GET', KEYS[1])
if prior then
  local previous = cjson.decode(prior)
  if previous.AccountID ~= incoming.AccountID then return 0 end
  if (previous.TurnStartedMS or 0) > incoming.TurnStartedMS then raw = prior end
end
redis.call('SET', KEYS[1], raw, 'PX', ARGV[2])
return 1
`)

func (c *gatewayCache) BindChatGPTDeliveryOwner(ctx context.Context, key string, owner *service.ChatGPTDeliveryOwner) error {
	if !service.ValidChatGPTDeliveryOwnerKey(key) || !service.ValidChatGPTDeliveryOwner(owner) {
		return errors.New("invalid native Chat delivery owner")
	}
	raw, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	ok, err := chatGPTBindDeliveryOwner.Run(ctx, c.rdb, []string{key}, string(raw), service.ChatGPTDeliveryOwnerTTL.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if ok != 1 {
		return errors.New("native Chat delivery owner conflicts")
	}
	return nil
}

func (c *gatewayCache) GetChatGPTDeliveryOwner(ctx context.Context, key string) (*service.ChatGPTDeliveryOwner, error) {
	if !service.ValidChatGPTDeliveryOwnerKey(key) {
		return nil, errors.New("invalid native Chat delivery owner key")
	}
	raw, err := c.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var owner service.ChatGPTDeliveryOwner
	if len(raw) > 256 || json.Unmarshal([]byte(raw), &owner) != nil || !service.ValidChatGPTDeliveryOwner(&owner) {
		return nil, errors.New("invalid native Chat delivery owner")
	}
	return &owner, nil
}
