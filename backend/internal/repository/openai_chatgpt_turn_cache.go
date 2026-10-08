package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

var chatGPTPutTurn = redis.NewScript("redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2], 'NX'); return redis.call('GET', KEYS[1])")
var chatGPTBindResume = redis.NewScript("local ttl = redis.call('PTTL', KEYS[2]); if ttl <= 0 then return 0 end; local old = redis.call('GET', KEYS[1]); if old and old ~= KEYS[2] then local raw = redis.call('GET', old); if raw then local prior = cjson.decode(raw); if not prior.Completed and not prior.TerminalSeen then return 0 end end end; redis.call('SET', KEYS[1], KEYS[2], 'PX', ttl); return 1")
var chatGPTReadResume = redis.NewScript("local key = redis.call('GET', KEYS[1]); if not key then return false end; return redis.call('GET', key)")
var chatGPTBindUpdates = redis.NewScript(`
local ttl = redis.call('PTTL', KEYS[2])
local raw = redis.call('GET', KEYS[2])
if not raw or ttl <= 0 then return 0 end
local value = cjson.decode(raw)
local now = redis.call('TIME')
now = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', now)
local member = tostring(value.AccountID)
if not redis.call('ZSCORE', KEYS[3], member) and redis.call('ZCARD', KEYS[3]) >= 16 then return 0 end
local old = redis.call('GET', KEYS[1])
if old and old ~= KEYS[2] then
  local prior = redis.call('GET', old)
  if prior then
    prior = cjson.decode(prior)
    if not prior.Completed and not prior.TerminalSeen then return 0 end
  end
end
redis.call('SET', KEYS[1], KEYS[2], 'PX', ttl)
local previous = tonumber(redis.call('ZSCORE', KEYS[3], member) or '0')
redis.call('ZADD', KEYS[3], math.max(previous, now + ttl), member)
local last = redis.call('ZREVRANGE', KEYS[3], 0, 0, 'WITHSCORES')
redis.call('PEXPIRE', KEYS[3], math.max(1, tonumber(last[2]) - now))
return 1
`)
var chatGPTListUpdateAccounts = redis.NewScript(`
local now = redis.call('TIME')
now = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
return redis.call('ZRANGE', KEYS[1], 0, 15)
`)
var chatGPTCompleteTurn = redis.NewScript("local raw = redis.call('GET', KEYS[1]); local ttl = redis.call('PTTL', KEYS[1]); if not raw or ttl <= 0 then return 0 end; local value = cjson.decode(raw); value.Completed = true; redis.call('SET', KEYS[1], cjson.encode(value), 'PX', ttl); return 1")
var chatGPTMarkTerminal = redis.NewScript("local raw = redis.call('GET', KEYS[1]); local ttl = redis.call('PTTL', KEYS[1]); if not raw or ttl <= 0 then return 0 end; local value = cjson.decode(raw); value.TerminalSeen = true; redis.call('SET', KEYS[1], cjson.encode(value), 'PX', ttl); return 1")
var chatGPTSetAsyncStatus = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
local ttl = redis.call('PTTL', KEYS[1])
if not raw or ttl <= 0 then return false end
local value = cjson.decode(raw)
if type(value.ImageBilling) ~= 'table' then
  value.AsyncPending = ARGV[1] == 'true'
  raw = cjson.encode(value)
  redis.call('SET', KEYS[1], raw, 'PX', ttl)
end
return raw
`)
var chatGPTMergeImages = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
local ttl = redis.call('PTTL', KEYS[1])
if not raw or ttl <= 0 then return false end
local value = cjson.decode(raw)
local incoming = cjson.decode(ARGV[1])
local previous = type(value.Images) == 'table' and value.Images or {}
local hashes = {}
local seen = {}
local sizes = {}
local function merge(list, dimensions)
  if type(list) ~= 'table' then return end
  for i, hash in ipairs(list) do
    local size = type(dimensions) == 'table' and dimensions[i] or ''
    if not seen[hash] and #hashes < 64 then
      seen[hash] = true
      sizes[hash] = size
      table.insert(hashes, hash)
    elseif seen[hash] then
      local old = sizes[hash] or ''
      if old ~= '' and size ~= '' and old ~= size then
        sizes[hash] = 'conflict'
      elseif old == '' then
        sizes[hash] = size
      end
    end
  end
end
merge(previous.AssetHashes, previous.AssetSizes)
merge(incoming.AssetHashes, incoming.AssetSizes)
table.sort(hashes)
local dimensions = {}
for _, hash in ipairs(hashes) do table.insert(dimensions, sizes[hash] or '') end
value.Images = {GenerationSeen = previous.GenerationSeen == true or incoming.GenerationSeen == true or #hashes > 0,
                AssetHashes = #hashes > 0 and hashes or cjson.null,
                AssetSizes = #dimensions > 0 and dimensions or cjson.null}
local result = cjson.encode(value)
redis.call('SET', KEYS[1], result, 'PX', ttl)
return result
`)

// Compare the image snapshot before sealing. A concurrent delivery may merge
// new evidence between GET and EVAL; retry instead of freezing a stale price.
var chatGPTSealBilling = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
local ttl = redis.call('PTTL', KEYS[1])
if not raw or ttl <= 0 then return false end
local value = cjson.decode(raw)
if type(value.ImageBilling) ~= 'table' then
  if value.AsyncPending then return 'pending' end
  local images = type(value.Images) == 'table' and value.Images or {}
  local expected = cjson.decode(ARGV[1])
  local hashes = type(images.AssetHashes) == 'table' and images.AssetHashes or {}
  local prior = type(images.AssetSizes) == 'table' and images.AssetSizes or {}
  local compared = type(expected.AssetHashes) == 'table' and expected.AssetHashes or {}
  local dimensions = type(expected.AssetSizes) == 'table' and expected.AssetSizes or {}
  if #hashes ~= #compared then return 'retry' end
  for i, hash in ipairs(hashes) do
    if hash ~= compared[i] or (prior[i] or '') ~= (dimensions[i] or '') then return 'retry' end
  end
  value.ImageBilling = cjson.decode(ARGV[2])
end
value.TerminalSeen = true
local result = cjson.encode(value)
redis.call('SET', KEYS[1], result, 'PX', ttl)
return result
`)

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
		!service.IsValidOpenAIChatGPTSnapshotPrice(snapshot.BasePriceUSD) || !service.ValidChatGPTImagePrices(snapshot.ImagePricesUSD) {
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
	if len(raw) > 8192 {
		return nil, errors.New("native Chat snapshot exceeds accounting bound")
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

func (c *gatewayCache) BindChatGPTUpdates(ctx context.Context, aliasKey, turnKey, indexKey string) error {
	ok, err := chatGPTBindUpdates.Run(ctx, c.rdb, []string{aliasKey, turnKey, indexKey}).Int()
	if err != nil {
		return err
	}
	if ok != 1 {
		return errors.New("native Chat update context expired, full or conflicts")
	}
	return nil
}

func (c *gatewayCache) ListChatGPTUpdateAccounts(ctx context.Context, key string) ([]int64, error) {
	values, err := chatGPTListUpdateAccounts.Run(ctx, c.rdb, []string{key}).StringSlice()
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("invalid native Chat update owner")
		}
		ids = append(ids, id)
	}
	return ids, nil
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

func (c *gatewayCache) MergeChatGPTTurnImages(ctx context.Context, key string, images service.ChatGPTImageEvidence) (*service.ChatGPTTurnSnapshot, error) {
	images = service.MergeChatGPTImageEvidence(service.ChatGPTImageEvidence{}, images)
	raw, err := json.Marshal(images)
	if err != nil {
		return nil, err
	}
	result, err := chatGPTMergeImages.Run(ctx, c.rdb, []string{key}, string(raw)).Text()
	snapshot, err := decodeChatGPTTurn(result, err)
	if err == nil && snapshot == nil {
		return nil, errors.New("native Chat turn context expired")
	}
	return snapshot, err
}

func (c *gatewayCache) SealChatGPTTurnBilling(ctx context.Context, key string) (*service.ChatGPTTurnSnapshot, error) {
	for attempt := 0; attempt < 3; attempt++ {
		snapshot, err := c.GetChatGPTTurn(ctx, key)
		if err != nil || snapshot == nil {
			return nil, errors.New("native Chat turn context unavailable for billing")
		}
		billing, err := service.CalculateChatGPTImageBilling(snapshot.Images, snapshot.ImagePricesUSD)
		if err != nil {
			return nil, err
		}
		imagesJSON, err := json.Marshal(snapshot.Images)
		if err != nil {
			return nil, err
		}
		billingJSON, err := json.Marshal(billing)
		if err != nil {
			return nil, err
		}
		result, err := chatGPTSealBilling.Run(ctx, c.rdb, []string{key}, string(imagesJSON), string(billingJSON)).Text()
		if err == nil && result == "pending" {
			return nil, service.ErrChatGPTTurnPending
		}
		if err == nil && result == "retry" {
			continue
		}
		sealed, err := decodeChatGPTTurn(result, err)
		if err == nil && sealed == nil {
			return nil, errors.New("native Chat turn context expired")
		}
		return sealed, err
	}
	return nil, errors.New("native Chat image evidence changed while sealing billing")
}

func (c *gatewayCache) SetChatGPTTurnAsyncStatus(ctx context.Context, key string, status int) (*service.ChatGPTTurnSnapshot, error) {
	pending := "false"
	if service.ChatGPTAsyncPending(status) {
		pending = "true"
	}
	raw, err := chatGPTSetAsyncStatus.Run(ctx, c.rdb, []string{key}, pending).Text()
	snapshot, err := decodeChatGPTTurn(raw, err)
	if err == nil && snapshot == nil {
		return nil, errors.New("native Chat turn context expired")
	}
	return snapshot, err
}
