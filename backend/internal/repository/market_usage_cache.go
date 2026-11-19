package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Per-window usage observations of listed accounts (market share caps).
// One hash per account: field "5h" / "7d" = {"u","reset","at"}, field
// "5h_absent" = {"absent","at"}. Writes are conditional in Lua so an older
// observation never lowers a newer one: a window value is replaced by a later
// window (larger reset); within one window the utilization keeps its maximum;
// the absence flag is replaced only by a later observation ("has a 5h
// window" wins a same-second tie).
const (
	marketUsageObsKeyPrefix = "market:usage_obs:"
	marketUsageObsTTL       = 8 * 24 * 60 * 60 // seconds; longer than the weekly window
)

func marketUsageObsKey(accountID int64) string {
	return fmt.Sprintf("%s%d", marketUsageObsKeyPrefix, accountID)
}

var recordMarketUsageScript = redis.NewScript(`
	local key = KEYS[1]
	local ttl = tonumber(ARGV[1])
	local written = 0
	local stale = false
	local function decode(raw)
		if not raw then return nil end
		local ok, v = pcall(cjson.decode, raw)
		if not ok or type(v) ~= 'table' then return nil end
		return v
	end
	for i = 2, #ARGV, 2 do
		local field = ARGV[i]
		local raw = ARGV[i + 1]
		local new = cjson.decode(raw)
		local oldRaw = redis.call('HGET', key, field)
		if oldRaw == false then oldRaw = nil end
		local old = decode(oldRaw)
		local out = nil
		if field == '5h_absent' then
			-- Later observation wins; at the same second "has a 5h window"
			-- (absent=false) wins. A response whose 7d belongs to an older
			-- window than the stored one is late: it never sets the flag.
			-- A known 5h window still running at the absence's time
			-- contradicts it: such an absence is late and never stored
			-- (the 5h field is written before this one in the same call).
			local live5h = false
			if new['absent'] == true then
				local five = decode(redis.call('HGET', key, '5h') or nil)
				if five ~= nil and (tonumber(five['reset']) or 0) > new['at'] then
					live5h = true
				end
			end
			if (stale or live5h) and new['absent'] == true then
				out = nil
			elseif old == nil then
				out = raw
			else
				local oldAt = tonumber(old['at']) or 0
				if new['at'] > oldAt or (new['at'] == oldAt and old['absent'] == true and new['absent'] == false) then
					out = raw
				end
			end
		else
			-- A later window (larger reset) replaces; within one window the
			-- utilization only grows, so keep the maximum (an out-of-order low
			-- value never lowers it).
			if old == nil then
				out = raw
			else
				local oldReset = tonumber(old['reset']) or 0
				if new['reset'] < oldReset and field == '7d' then
					stale = true
				end
				if new['reset'] > oldReset then
					out = raw
				elseif new['reset'] == oldReset then
					local oldU = tonumber(old['u']) or 0
					local oldAt = tonumber(old['at']) or 0
					local merged = {u = math.max(oldU, new['u']), reset = oldReset, at = math.max(oldAt, new['at'])}
					if merged['u'] ~= oldU or merged['at'] ~= oldAt then
						out = cjson.encode(merged)
					end
				end
			end
		end
		if out then
			redis.call('HSET', key, field, out)
			written = written + 1
		end
	end
	if written > 0 then
		redis.call('EXPIRE', key, ttl)
	end
	return written
`)

type marketUsageCache struct {
	rdb *redis.Client
}

// NewMarketUsageCache stores the market share-cap observations.
func NewMarketUsageCache(rdb *redis.Client) service.MarketUsageObservationCache {
	return &marketUsageCache{rdb: rdb}
}

type marketWindowObsJSON struct {
	U     float64 `json:"u"`
	Reset int64   `json:"reset"`
	At    int64   `json:"at"`
}

type marketAbsentObsJSON struct {
	Absent bool  `json:"absent"`
	At     int64 `json:"at"`
}

func (c *marketUsageCache) RecordMarketUsageObservations(ctx context.Context, accountID int64, obs service.MarketUsageObservations) error {
	if c == nil || c.rdb == nil || accountID <= 0 {
		return nil
	}
	args := []any{marketUsageObsTTL}
	add := func(field string, v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		args = append(args, field, string(raw))
		return nil
	}
	if w := obs.FiveHour; w != nil {
		if err := add("5h", marketWindowObsJSON{U: w.Utilization, Reset: w.ResetAt, At: w.ObservedAt}); err != nil {
			return err
		}
	}
	if w := obs.SevenDay; w != nil {
		if err := add("7d", marketWindowObsJSON{U: w.Utilization, Reset: w.ResetAt, At: w.ObservedAt}); err != nil {
			return err
		}
	}
	if a := obs.FiveHourAbsent; a != nil {
		if err := add("5h_absent", marketAbsentObsJSON{Absent: a.Absent, At: a.ObservedAt}); err != nil {
			return err
		}
	}
	if len(args) == 1 {
		return nil
	}
	return recordMarketUsageScript.Run(ctx, c.rdb, []string{marketUsageObsKey(accountID)}, args...).Err()
}

func (c *marketUsageCache) GetMarketUsageObservations(ctx context.Context, accountIDs []int64) (map[int64]*service.MarketUsageObservations, error) {
	out := make(map[int64]*service.MarketUsageObservations, len(accountIDs))
	if c == nil || c.rdb == nil || len(accountIDs) == 0 {
		return out, nil
	}
	pipe := c.rdb.Pipeline()
	cmds := make(map[int64]*redis.MapStringStringCmd, len(accountIDs))
	for _, id := range accountIDs {
		cmds[id] = pipe.HGetAll(ctx, marketUsageObsKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	for id, cmd := range cmds {
		fields := cmd.Val()
		obs := &service.MarketUsageObservations{}
		for field, raw := range fields {
			switch field {
			case "5h", "7d":
				var w marketWindowObsJSON
				if json.Unmarshal([]byte(raw), &w) != nil {
					continue
				}
				v := &service.MarketUsageObservation{Utilization: w.U, ResetAt: w.Reset, ObservedAt: w.At}
				if field == "5h" {
					obs.FiveHour = v
				} else {
					obs.SevenDay = v
				}
			case "5h_absent":
				var a marketAbsentObsJSON
				if json.Unmarshal([]byte(raw), &a) != nil {
					continue
				}
				obs.FiveHourAbsent = &service.MarketFiveHourAbsence{Absent: a.Absent, ObservedAt: a.At}
			}
		}
		out[id] = obs
	}
	return out, nil
}
