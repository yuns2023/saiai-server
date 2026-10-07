package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

var chatGPTPutTurn = redis.NewScript("redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2], 'NX'); return redis.call('GET', KEYS[1])")
var chatGPTBindResume = redis.NewScript("local ttl = redis.call('PTTL', KEYS[2]); if ttl <= 0 then return 0 end; local old = redis.call('GET', KEYS[1]); if old and old ~= KEYS[2] then local raw = redis.call('GET', old); if raw then local prior = cjson.decode(raw); if not prior.Completed and not prior.TerminalSeen then return 0 end end end; redis.call('SET', KEYS[1], KEYS[2], 'PX', ttl); return 1")
var chatGPTReadResume = redis.NewScript("local key = redis.call('GET', KEYS[1]); if not key then return false end; return redis.call('GET', key)")
var chatGPTCompleteTurn = redis.NewScript("local raw = redis.call('GET', KEYS[1]); local ttl = redis.call('PTTL', KEYS[1]); if not raw or ttl <= 0 then return 0 end; local value = cjson.decode(raw); value.Completed = true; redis.call('SET', KEYS[1], cjson.encode(value), 'PX', ttl); return 1")
var chatGPTMarkTerminal = redis.NewScript("local raw = redis.call('GET', KEYS[1]); local ttl = redis.call('PTTL', KEYS[1]); if not raw or ttl <= 0 then return 0 end; local value = cjson.decode(raw); value.TerminalSeen = true; redis.call('SET', KEYS[1], cjson.encode(value), 'PX', ttl); return 1")

var _ service.ChatGPTTurnCache = (*gatewayCache)(nil)

func decodeChatGPTTurn(raw string, err error) (*service.ChatGPTTurnSnapshot, error) {
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot service.ChatGPTTurnSnapshot
	if len(raw) > 8192 || json.Unmarshal([]byte(raw), &snapshot) != nil || snapshot.AccountID <= 0 ||
		snapshot.Identity.RequestID == "" || snapshot.Identity.PayloadHash == "" || snapshot.StartedAt.IsZero() ||
		snapshot.BasePriceUSD < 0 {
		return nil, errors.New("invalid native Chat accounting snapshot")
	}
	return &snapshot, nil
}

func (c *gatewayCache) GetChatGPTTurn(ctx context.Context, key string) (*service.ChatGPTTurnSnapshot, error) {
	raw, err := c.rdb.Get(ctx, key).Result()
	return decodeChatGPTTurn(raw, err)
}

func (c *gatewayCache) PutChatGPTTurnIfAbsent(ctx context.Context, key string, snapshot *service.ChatGPTTurnSnapshot, ttl time.Duration) (*service.ChatGPTTurnSnapshot, error) {
	if snapshot == nil || ttl <= 0 {
		return nil, errors.New("invalid native Chat snapshot")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	result, err := chatGPTPutTurn.Run(ctx, c.rdb, []string{key}, string(raw), ttl.Milliseconds()).Text()
	return decodeChatGPTTurn(result, err)
}

func (c *gatewayCache) BindChatGPTResume(ctx context.Context, aliasKey, turnKey string) error {
	ok, err := chatGPTBindResume.Run(ctx, c.rdb, []string{aliasKey, turnKey}).Int()
	if err != nil {
		return err
	}
	if ok != 1 {
		return errors.New("native Chat resume context expired or conflicts")
	}
	return nil
}

func (c *gatewayCache) GetChatGPTResume(ctx context.Context, key string) (*service.ChatGPTTurnSnapshot, error) {
	raw, err := chatGPTReadResume.Run(ctx, c.rdb, []string{key}).Text()
	return decodeChatGPTTurn(raw, err)
}

func (c *gatewayCache) CompleteChatGPTTurn(ctx context.Context, key string) error {
	ok, err := chatGPTCompleteTurn.Run(ctx, c.rdb, []string{key}).Int()
	if err != nil {
		return err
	}
	if ok != 1 {
		return errors.New("native Chat turn context expired")
	}
	return nil
}

func (c *gatewayCache) MarkChatGPTTurnTerminal(ctx context.Context, key string) error {
	ok, err := chatGPTMarkTerminal.Run(ctx, c.rdb, []string{key}).Int()
	if err != nil {
		return err
	}
	if ok != 1 {
		return errors.New("native Chat turn context expired")
	}
	return nil
}
