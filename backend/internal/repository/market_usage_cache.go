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
// observation never overwrites a newer one: a window value is replaced only
// by a later window (larger reset) or by a later observation of the same
// window; the absence flag only by a later observation.
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
	local function newer_window(oldRaw, new)
		if not oldRaw then return true end
		local ok, old = pcall(cjson.decode, oldRaw)
		if not ok or type(old) ~= 'table' then return true end
		local oldReset = tonumber(old['reset']) or 0
		local oldAt = tonumber(old['at']) or 0
		if new['reset'] > oldReset then return true end
		if new['reset'] == oldReset and new['at'] >= oldAt then return true end
		return false
	end
	local function newer_flag(oldRaw, new)
		if not oldRaw then return true end
		local ok, old = pcall(cjson.decode, oldRaw)
		if not ok or type(old) ~= 'table' then return true end
		return new['at'] >= (tonumber(old['at']) or 0)
	end
	for i = 2, #ARGV, 2 do
		local field = ARGV[i]
		local raw = ARGV[i + 1]
		local new = cjson.decode(raw)
		local oldRaw = redis.call('HGET', key, field)
		if oldRaw == false then oldRaw = nil end
		local accept
		if field == '5h_absent' then
			accept = newer_flag(oldRaw, new)
		else
			accept = newer_window(oldRaw, new)
		end
		if accept then
			redis.call('HSET', key, field, raw)
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
