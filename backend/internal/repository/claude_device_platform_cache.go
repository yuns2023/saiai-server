package repository

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Redis layout (per site, global across upstream accounts):
//
//	claude_device_platform_v1:dev:<digest>   hash, TTL 30 d refreshed on write
//	    p                     display prefix (first 8 hex of the device id)
//	    fa:<family>|<arch>    "<first>,<last>"   (unix seconds)
//	    fm:<family>|<major>   "<first>,<last>"   (only known majors)
//	    u:<user_id>           "<last>,<api_key_id>"   (at most 20)
//	    ut                    "1" once a user was dropped by the 20-user cap
//	    r:<reason>            "<last_matched>,<evidence>"
//	claude_device_platform_v1:tracked        zset digest -> last seen (cap 20,000)
//	claude_device_platform_v1:flagged        zset digest -> max last_matched
//	claude_device_platform_v1:new:<uid>:<day> new devices of a user that UTC day
const (
	claudeDevicePlatformPrefix     = "claude_device_platform_v1:"
	claudeDevicePlatformDevPrefix  = claudeDevicePlatformPrefix + "dev:"
	claudeDevicePlatformTrackedKey = claudeDevicePlatformPrefix + "tracked"
	claudeDevicePlatformFlaggedKey = claudeDevicePlatformPrefix + "flagged"
	claudeDevicePlatformNewPrefix  = claudeDevicePlatformPrefix + "new:"

	// claudeDevicePlatformScanBudget bounds the zset members one admin page
	// inspects (the reason filter is applied to the details).
	claudeDevicePlatformScanBudget = 500
	claudeDevicePlatformScanBatch  = 100
)

// claudeDevicePlatformRecordScript records one observation atomically.
//
// KEYS: 1 device hash, 2 tracked zset, 3 flagged zset, 4 per-user daily counter
// ARGV: 1 digest, 2 display prefix, 3 family, 4 arch, 5 major ("" unknown),
// 6 user id ("0" unknown), 7 api key id, 8 event time, 9 retention seconds,
// 10 cross-family window seconds, 11 max devices, 12 max new devices per user
// per day, 13 max users per device, 14 max flagged devices, 15 daily counter TTL
//
// Event times may arrive out of order: first/last are min/max merges and a
// reason's last_matched never moves backwards. Flags are only removed when
// their last match is older than the retention, so dropping a user at the
// 20-user cap never erases an earlier cross_user flag.
var claudeDevicePlatformRecordScript = redis.NewScript(`
local dev, tracked, flagged, daily = KEYS[1], KEYS[2], KEYS[3], KEYS[4]
local digest, prefix = ARGV[1], ARGV[2]
local family, arch, major = ARGV[3], ARGV[4], ARGV[5]
local uid, kid = ARGV[6], ARGV[7]
local now = tonumber(ARGV[8])
local retention = tonumber(ARGV[9])
local familyWindow = tonumber(ARGV[10])
local maxDevices = tonumber(ARGV[11])
local maxNew = tonumber(ARGV[12])
local maxUsers = tonumber(ARGV[13])
local flaggedCap = tonumber(ARGV[14])
local dailyTTL = tonumber(ARGV[15])
local cutoff = now - retention

-- Inclusive: a member at exactly the cutoff is gone, like its hash
-- (EXPIREAT score + retention).
redis.call('ZREMRANGEBYSCORE', tracked, '-inf', cutoff)
redis.call('ZREMRANGEBYSCORE', flagged, '-inf', cutoff)

local isNew = 0
if redis.call('EXISTS', dev) == 0 and not redis.call('ZSCORE', tracked, digest) then
  if redis.call('ZCARD', tracked) >= maxDevices then return {'global_cap'} end
  if uid == '0' then return {'no_owner'} end
  local used = tonumber(redis.call('GET', daily) or '0') or 0
  if used >= maxNew then return {'user_cap'} end
  redis.call('INCR', daily)
  redis.call('EXPIRE', daily, dailyTTL)
  isNew = 1
end

local fields = {}
local raw = redis.call('HGETALL', dev)
for i = 1, #raw, 2 do fields[raw[i]] = raw[i + 1] end

local function head(v)
  local a, b = string.match(v or '', '^(%d+),(.*)$')
  return tonumber(a), b
end
local function range(v)
  local a, b = string.match(v or '', '^(%d+),(%d+)$')
  return tonumber(a), tonumber(b)
end

-- Drop entries whose last activity left the retention window.
for k, v in pairs(fields) do
  local kind = string.sub(k, 1, 2)
  local last = nil
  if kind == 'fa' or kind == 'fm' then
    local _, l = range(v)
    last = l or 0
  elseif kind == 'u:' or kind == 'r:' then
    last = head(v) or 0
  end
  if last ~= nil and last < cutoff then
    redis.call('HDEL', dev, k)
    fields[k] = nil
  end
end

if fields['p'] == nil then
  redis.call('HSET', dev, 'p', prefix)
  fields['p'] = prefix
end

local function merge(field)
  local f, l = range(fields[field])
  if not f then
    f, l = now, now
  else
    if now < f then f = now end
    if now > l then l = now end
  end
  local v = string.format('%d,%d', f, l)
  fields[field] = v
  redis.call('HSET', dev, field, v)
end

if family ~= 'unknown' or arch ~= 'unknown' then merge('fa:' .. family .. '|' .. arch) end
if family ~= 'unknown' and major ~= '' then merge('fm:' .. family .. '|' .. major) end

if uid ~= '0' then
  local ufield = 'u:' .. uid
  local cur = fields[ufield]
  if cur then
    local l = head(cur)
    if not l or now >= l then
      local v = string.format('%d,%s', now, kid)
      fields[ufield] = v
      redis.call('HSET', dev, ufield, v)
    end
  else
    local count, oldestField, oldestLast = 0, nil, nil
    for k, v in pairs(fields) do
      if string.sub(k, 1, 2) == 'u:' then
        count = count + 1
        local l = head(v) or 0
        if oldestLast == nil or l < oldestLast or (l == oldestLast and k < oldestField) then
          oldestField, oldestLast = k, l
        end
      end
    end
    local add = true
    if count >= maxUsers then
      redis.call('HSET', dev, 'ut', '1')
      fields['ut'] = '1'
      if oldestLast ~= nil and oldestLast <= now then
        redis.call('HDEL', dev, oldestField)
        fields[oldestField] = nil
      else
        add = false
      end
    end
    if add then
      local v = string.format('%d,%s', now, kid)
      fields[ufield] = v
      redis.call('HSET', dev, ufield, v)
    end
  end
end

-- Evaluate the flag reasons on the current state.
local familyLast, familyArchs, majors, users = {}, {}, {}, 0
for k, v in pairs(fields) do
  local kind, a, b = string.match(k, '^(f[am]):([^|]+)|(.+)$')
  if kind == 'fa' and a ~= 'unknown' then
    local _, l = range(v)
    l = l or 0
    if (familyLast[a] or 0) < l then familyLast[a] = l end
    if b ~= 'unknown' then
      familyArchs[a] = familyArchs[a] or {}
      familyArchs[a][b] = true
    end
  elseif kind == 'fm' and a ~= 'unknown' then
    local f, l = range(v)
    if f then
      majors[a] = majors[a] or {}
      table.insert(majors[a], {major = b, n = tonumber(b) or 0, first = f, last = l})
    end
  elseif string.sub(k, 1, 2) == 'u:' then
    users = users + 1
  end
end

local matched = {}
-- The window is anchored at the newest family sighting (never before this
-- event), so a late older event is not paired with a much newer one.
local anchor = now
for _, l in pairs(familyLast) do
  if l > anchor then anchor = l end
end
local recent = {}
for f, l in pairs(familyLast) do
  if l >= anchor - familyWindow then table.insert(recent, f) end
end
if #recent >= 2 then
  table.sort(recent)
  matched['cross_family'] = table.concat(recent, '+')
end

local archOnly = {}
for f, set in pairs(familyArchs) do
  local list = {}
  for a in pairs(set) do table.insert(list, a) end
  if #list >= 2 then
    table.sort(list)
    table.insert(archOnly, f .. ':' .. table.concat(list, '+'))
  end
end
if #archOnly > 0 then
  table.sort(archOnly)
  matched['arch_only'] = table.concat(archOnly, ' ')
end

local alternation, sequential = {}, {}
for f, list in pairs(majors) do
  table.sort(list, function(x, y) return x.n < y.n end)
  for i = 1, #list do
    for j = i + 1, #list do
      local old, new = list[i], list[j]
      local pair = f .. ':' .. old.major .. '/' .. new.major
      if old.last > new.first then
        table.insert(alternation, pair)
      else
        table.insert(sequential, pair)
      end
    end
  end
end
if #alternation > 0 then
  table.sort(alternation)
  matched['version_alternation'] = table.concat(alternation, ' ')
end
if #sequential > 0 then
  table.sort(sequential)
  matched['sequential_upgrade'] = table.concat(sequential, ' ')
end
if users >= 2 then matched['cross_user'] = tostring(users) end

for reason, evidence in pairs(matched) do
  local field = 'r:' .. reason
  local l = head(fields[field])
  if not l or now >= l then
    local v = string.format('%d,%s', now, string.sub(evidence, 1, 48))
    fields[field] = v
    redis.call('HSET', dev, field, v)
  end
end

local maxMatched = 0
local reasonCount = 0
for k, v in pairs(fields) do
  if string.sub(k, 1, 2) == 'r:' then
    reasonCount = reasonCount + 1
    local l = head(v) or 0
    if l > maxMatched then maxMatched = l end
  end
end
if reasonCount > 0 then
  redis.call('ZADD', flagged, maxMatched, digest)
  local card = redis.call('ZCARD', flagged)
  if card > flaggedCap then
    redis.call('ZREMRANGEBYRANK', flagged, 0, card - flaggedCap - 1)
  end
else
  redis.call('ZREM', flagged, digest)
end

redis.call('ZADD', tracked, 'GT', now, digest)
-- The hash expires exactly when its tracked entry is pruned (same clock).
local seen = tonumber(redis.call('ZSCORE', tracked, digest)) or now
redis.call('EXPIREAT', dev, seen + retention)
return {'ok', isNew, reasonCount}
`)

type claudeDevicePlatformCache struct {
	rdb *redis.Client
}

// NewClaudeDevicePlatformStore creates the Redis-backed observation store.
func NewClaudeDevicePlatformStore(rdb *redis.Client) service.ClaudeDevicePlatformStore {
	return &claudeDevicePlatformCache{rdb: rdb}
}

func claudeDevicePlatformDevKey(digest string) string { return claudeDevicePlatformDevPrefix + digest }

func claudeDevicePlatformNewKey(userID int64, at int64) string {
	day := time.Unix(at, 0).UTC().Format("20060102")
	return claudeDevicePlatformNewPrefix + strconv.FormatInt(userID, 10) + ":" + day
}

func (r *claudeDevicePlatformCache) RecordClaudeDevicePlatform(ctx context.Context, obs service.ClaudeDevicePlatformObservation, limits service.ClaudeDevicePlatformLimits) (service.ClaudeDevicePlatformWriteStatus, error) {
	if r == nil || r.rdb == nil {
		return "", fmt.Errorf("claude device platform store unavailable")
	}
	if !isClaudeDevicePlatformDigest(obs.Digest) {
		return "", fmt.Errorf("invalid device digest")
	}
	keys := []string{
		claudeDevicePlatformDevKey(obs.Digest),
		claudeDevicePlatformTrackedKey,
		claudeDevicePlatformFlaggedKey,
		claudeDevicePlatformNewKey(obs.UserID, obs.ObservedAt),
	}
	res, err := claudeDevicePlatformRecordScript.Run(ctx, r.rdb, keys,
		obs.Digest, obs.DisplayPrefix, obs.Family, obs.Arch, obs.Major,
		obs.UserID, obs.APIKeyID, obs.ObservedAt,
		int64(limits.Retention/time.Second), int64(limits.CrossFamilyWindow/time.Second),
		limits.MaxDevices, limits.MaxNewPerUserDay, limits.MaxUsers, limits.MaxFlaggedDevices,
		int64(limits.NewDeviceDayWindow/time.Second),
	).Slice()
	if err != nil {
		return "", err
	}
	if len(res) == 0 {
		return "", fmt.Errorf("empty claude device platform script result")
	}
	status, _ := res[0].(string)
	return service.ClaudeDevicePlatformWriteStatus(status), nil
}

func isClaudeDevicePlatformDigest(v string) bool {
	if len(v) != 64 {
		return false
	}
	for i := 0; i < len(v); i++ {
		if (v[i] < '0' || v[i] > '9') && (v[i] < 'a' || v[i] > 'f') {
			return false
		}
	}
	return true
}

// claudeDevicePlatformCursor is the last returned position: ordering is
// score desc, then digest desc (Redis' order for equal scores in reverse).
type claudeDevicePlatformCursor struct {
	score  int64
	digest string
}

func parseClaudeDevicePlatformCursor(raw string) (*claudeDevicePlatformCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	scoreText, digest, ok := strings.Cut(raw, ":")
	if !ok || len(scoreText) == 0 || len(scoreText) > 12 || !isClaudeDevicePlatformDigest(digest) {
		return nil, service.ErrClaudeDevicePlatformInvalidQuery
	}
	score, err := strconv.ParseInt(scoreText, 10, 64)
	if err != nil || score < 0 {
		return nil, service.ErrClaudeDevicePlatformInvalidQuery
	}
	return &claudeDevicePlatformCursor{score: score, digest: digest}, nil
}

func (c claudeDevicePlatformCursor) String() string {
	return strconv.FormatInt(c.score, 10) + ":" + c.digest
}

// nextClaudeDevicePlatformMembers returns up to n members strictly after the
// cursor in (score desc, member desc) order with score >= minScore.
func (r *claudeDevicePlatformCache) nextMembers(ctx context.Context, cursor *claudeDevicePlatformCursor, minScore int64, n int) ([]redis.Z, error) {
	minText := strconv.FormatInt(minScore, 10)
	if cursor == nil {
		return r.rdb.ZRevRangeByScoreWithScores(ctx, claudeDevicePlatformFlaggedKey, &redis.ZRangeBy{
			Max: "+inf", Min: minText, Count: int64(n),
		}).Result()
	}
	if cursor.score < minScore {
		return nil, nil
	}
	// Fast path: the cursor member still has its score, so the next members
	// in (score desc, member desc) order follow its reverse rank.
	if score, err := r.rdb.ZScore(ctx, claudeDevicePlatformFlaggedKey, cursor.digest).Result(); err == nil && int64(score) == cursor.score {
		rank, err := r.rdb.ZRevRank(ctx, claudeDevicePlatformFlaggedKey, cursor.digest).Result()
		if err == nil {
			members, err := r.rdb.ZRevRangeWithScores(ctx, claudeDevicePlatformFlaggedKey, rank+1, rank+int64(n)).Result()
			if err != nil {
				return nil, err
			}
			// The three reads are not atomic: keep only members strictly
			// after the cursor (a concurrent insert can at worst skip one).
			out := members[:0]
			for _, m := range members {
				member, _ := m.Member.(string)
				score := int64(m.Score)
				if score > cursor.score || score == cursor.score && member >= cursor.digest {
					// Shifted ahead of the cursor meanwhile: use the slow path.
					return r.nextMembersSlow(ctx, cursor, minScore, n)
				}
				if score >= minScore {
					out = append(out, m)
				}
			}
			return out, nil
		}
	} else if err != nil && err != redis.Nil {
		return nil, err
	}
	return r.nextMembersSlow(ctx, cursor, minScore, n)
}

// nextMembersSlow is used when the cursor member was removed or re-scored:
// members tied with the cursor score that sort after it (the tie group is
// bounded by the zset cap), then lower scores.
func (r *claudeDevicePlatformCache) nextMembersSlow(ctx context.Context, cursor *claudeDevicePlatformCursor, minScore int64, n int) ([]redis.Z, error) {
	minText := strconv.FormatInt(minScore, 10)
	scoreText := strconv.FormatInt(cursor.score, 10)
	ties, err := r.rdb.ZRevRangeByScore(ctx, claudeDevicePlatformFlaggedKey, &redis.ZRangeBy{
		Max: scoreText, Min: scoreText,
	}).Result()
	if err != nil {
		return nil, err
	}
	out := make([]redis.Z, 0, n)
	for _, member := range ties {
		if member < cursor.digest {
			out = append(out, redis.Z{Score: float64(cursor.score), Member: member})
			if len(out) >= n {
				return out, nil
			}
		}
	}
	rest, err := r.rdb.ZRevRangeByScoreWithScores(ctx, claudeDevicePlatformFlaggedKey, &redis.ZRangeBy{
		Max: "(" + scoreText, Min: minText, Count: int64(n - len(out)),
	}).Result()
	if err != nil {
		return nil, err
	}
	return append(out, rest...), nil
}

func (r *claudeDevicePlatformCache) ListFlaggedClaudeDevicePlatforms(ctx context.Context, query service.ClaudeDevicePlatformFlaggedQuery) (service.ClaudeDevicePlatformFlaggedPage, error) {
	page := service.ClaudeDevicePlatformFlaggedPage{Items: []service.ClaudeDevicePlatformDevice{}}
	if r == nil || r.rdb == nil {
		return page, nil
	}
	cursor, err := parseClaudeDevicePlatformCursor(query.Cursor)
	if err != nil {
		return page, err
	}
	limit := query.Limit
	if limit <= 0 || limit > service.ClaudeDevicePlatformListMaxLimit {
		limit = service.ClaudeDevicePlatformListMaxLimit
	}
	wanted := map[string]bool{}
	for _, reason := range query.Reasons {
		wanted[reason] = true
	}
	now := query.Now
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.Add(-service.ClaudeDevicePlatformRetention).Unix()

	scanned := 0
	for len(page.Items) < limit && scanned < claudeDevicePlatformScanBudget {
		batch := min(claudeDevicePlatformScanBatch, claudeDevicePlatformScanBudget-scanned)
		members, err := r.nextMembers(ctx, cursor, cutoff, batch)
		if err != nil {
			return page, err
		}
		if len(members) == 0 {
			return page, nil
		}
		pipe := r.rdb.Pipeline()
		cmds := make([]*redis.MapStringStringCmd, len(members))
		for i, m := range members {
			digest, _ := m.Member.(string)
			cmds[i] = pipe.HGetAll(ctx, claudeDevicePlatformDevKey(digest))
		}
		if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
			return page, err
		}
		consumed := 0
		for i, m := range members {
			digest, _ := m.Member.(string)
			scanned++
			consumed++
			cursor = &claudeDevicePlatformCursor{score: int64(m.Score), digest: digest}
			fields, err := cmds[i].Result()
			if err != nil || len(fields) == 0 {
				continue // expired or missing details
			}
			device, ok := decodeClaudeDevicePlatformDevice(fields, cutoff)
			if !ok || !claudeDevicePlatformHasReason(device, wanted) {
				continue
			}
			page.Items = append(page.Items, device)
			if len(page.Items) >= limit {
				break
			}
		}
		if consumed == len(members) && len(members) < batch {
			return page, nil // the zset is exhausted
		}
	}
	if cursor != nil {
		page.NextCursor = cursor.String()
	}
	return page, nil
}

func claudeDevicePlatformHasReason(device service.ClaudeDevicePlatformDevice, wanted map[string]bool) bool {
	if len(wanted) == 0 {
		return len(device.Reasons) > 0
	}
	for _, r := range device.Reasons {
		if wanted[r.Reason] {
			return true
		}
	}
	return false
}

func splitClaudeDevicePlatformValue(v string) (int64, string, bool) {
	head, rest, ok := strings.Cut(v, ",")
	if !ok {
		return 0, "", false
	}
	n, err := strconv.ParseInt(head, 10, 64)
	if err != nil {
		return 0, "", false
	}
	return n, rest, true
}

func decodeClaudeDevicePlatformDevice(fields map[string]string, cutoff int64) (service.ClaudeDevicePlatformDevice, bool) {
	device := service.ClaudeDevicePlatformDevice{
		DisplayPrefix: fields["p"],
		Reasons:       []service.ClaudeDevicePlatformReasonMatch{},
		Platforms:     []service.ClaudeDevicePlatformArchEntry{},
		Versions:      []service.ClaudeDevicePlatformVersionEntry{},
		Users:         []service.ClaudeDevicePlatformUser{},
	}
	device.UsersTruncated = fields["ut"] == "1"
	var maxMatched int64
	for k, v := range fields {
		switch {
		case strings.HasPrefix(k, "r:"):
			at, evidence, ok := splitClaudeDevicePlatformValue(v)
			if !ok || at < cutoff {
				continue
			}
			if at > maxMatched {
				maxMatched = at
			}
			device.Reasons = append(device.Reasons, service.ClaudeDevicePlatformReasonMatch{
				Reason: strings.TrimPrefix(k, "r:"), LastMatchedAt: time.Unix(at, 0).UTC(), Evidence: evidence,
			})
		case strings.HasPrefix(k, "fa:") || strings.HasPrefix(k, "fm:"):
			first, lastText, ok := splitClaudeDevicePlatformValue(v)
			last, err := strconv.ParseInt(lastText, 10, 64)
			if !ok || err != nil {
				continue
			}
			family, second, ok := strings.Cut(k[3:], "|")
			if !ok {
				continue
			}
			if last < cutoff {
				continue // left the retention window since the last write
			}
			firstAt, lastAt := time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
			if k[1] == 'a' {
				device.Platforms = append(device.Platforms, service.ClaudeDevicePlatformArchEntry{Family: family, Arch: second, FirstSeenAt: firstAt, LastSeenAt: lastAt})
			} else {
				device.Versions = append(device.Versions, service.ClaudeDevicePlatformVersionEntry{Family: family, Major: second, FirstSeenAt: firstAt, LastSeenAt: lastAt})
			}
		case strings.HasPrefix(k, "u:"):
			uid, err := strconv.ParseInt(k[2:], 10, 64)
			last, keyText, ok := splitClaudeDevicePlatformValue(v)
			if err != nil || !ok || last < cutoff {
				continue
			}
			keyID, _ := strconv.ParseInt(keyText, 10, 64)
			device.Users = append(device.Users, service.ClaudeDevicePlatformUser{UserID: uid, APIKeyID: keyID, LastSeenAt: time.Unix(last, 0).UTC()})
		}
	}
	if len(device.Reasons) == 0 {
		return device, false
	}
	device.LastMatchedAt = time.Unix(maxMatched, 0).UTC()
	sort.Slice(device.Reasons, func(i, j int) bool { return device.Reasons[i].Reason < device.Reasons[j].Reason })
	sort.Slice(device.Platforms, func(i, j int) bool {
		a, b := device.Platforms[i], device.Platforms[j]
		if !a.LastSeenAt.Equal(b.LastSeenAt) {
			return a.LastSeenAt.After(b.LastSeenAt)
		}
		return a.Family+a.Arch < b.Family+b.Arch
	})
	sort.Slice(device.Versions, func(i, j int) bool {
		a, b := device.Versions[i], device.Versions[j]
		if !a.LastSeenAt.Equal(b.LastSeenAt) {
			return a.LastSeenAt.After(b.LastSeenAt)
		}
		return a.Family+a.Major < b.Family+b.Major
	})
	sort.Slice(device.Users, func(i, j int) bool {
		a, b := device.Users[i], device.Users[j]
		if !a.LastSeenAt.Equal(b.LastSeenAt) {
			return a.LastSeenAt.After(b.LastSeenAt)
		}
		return a.UserID < b.UserID
	})
	return device, true
}
