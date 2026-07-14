package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	fingerprintKeyPrefix       = "fingerprint:"
	slotFingerprintPrefix      = "fingerprint_slot_v2:"
	fingerprintTTL             = 7 * 24 * time.Hour // 7天，配合每24小时懒续期可保持活跃账号永不过期
	maskedSessionKeyPrefix     = "masked_session:"
	slotMaskedSessionPrefix    = "masked_session_slot_v2:"
	carpoolRecordedPrefix      = "claude_carpool_recorded_v1:"
	carpoolOverflowPrefix      = "claude_carpool_overflow_v1:"
	sharedBucketMetaPrefix     = "shared_bucket_meta_v1:"
	sharedBucketBindingPref    = "shared_bucket_binding_v1:"
	sharedBucketConfigPref     = "shared_bucket_config_v1:"
	singleDeviceSlotBindPrefix = "single_device_slot_binding_v1:"
	singleDeviceSlotMetaPrefix = "single_device_slot_meta_v1:"
	singleDeviceSlotNextPrefix = "single_device_slot_next_v1:"
	pinnedDeviceBindingsKey    = "pinned_device_bindings_v1:"
	pinnedAccountBindingKey    = "pinned_account_binding_v1:"
	maxSharedBucketSlots       = 32
	carpoolOverflowMaxItems    = 256
	// Session TTL 使用随机范围 [5, 60] 分钟
	// 避免固定 15 分钟 TTL 产生非自然的 session 存活分布
	maskedSessionTTLMin = 5 * time.Minute
	maskedSessionTTLMax = 60 * time.Minute
)

// fingerprintKey generates the Redis key for account fingerprint cache.
func fingerprintKey(accountID int64) string {
	return fmt.Sprintf("%s%d", fingerprintKeyPrefix, accountID)
}

func slotFingerprintKey(accountID int64, slot int) string {
	return fmt.Sprintf("%s%d:%d", slotFingerprintPrefix, accountID, slot)
}

// maskedSessionKey generates the Redis key for masked session ID cache.
func maskedSessionKey(accountID int64) string {
	return fmt.Sprintf("%s%d", maskedSessionKeyPrefix, accountID)
}

func slotMaskedSessionKey(accountID int64, slot int) string {
	return fmt.Sprintf("%s%d:%d", slotMaskedSessionPrefix, accountID, slot)
}

func sharedBucketBindingKey(accountID int64) string {
	return fmt.Sprintf("%s%d", sharedBucketBindingPref, accountID)
}

func sharedBucketConfigKey(accountID int64) string {
	return fmt.Sprintf("%s%d", sharedBucketConfigPref, accountID)
}

func singleDeviceSlotBindingKey(accountID int64) string {
	return fmt.Sprintf("%s%d", singleDeviceSlotBindPrefix, accountID)
}

func singleDeviceSlotCounterKey(accountID int64) string {
	return fmt.Sprintf("%s%d", singleDeviceSlotNextPrefix, accountID)
}

func carpoolRecordedKey(accountID int64) string {
	return fmt.Sprintf("%s%d", carpoolRecordedPrefix, accountID)
}

func carpoolOverflowKey(accountID int64) string {
	return fmt.Sprintf("%s%d", carpoolOverflowPrefix, accountID)
}

func carpoolDeviceField(originalDeviceID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(originalDeviceID)))
	return hex.EncodeToString(sum[:])
}

func sharedBucketMetaKey(accountID int64, slot int) string {
	return fmt.Sprintf("%s%d:%d", sharedBucketMetaPrefix, accountID, slot)
}

func singleDeviceSlotMetaKey(accountID int64, slot int) string {
	return fmt.Sprintf("%s%d:%d", singleDeviceSlotMetaPrefix, accountID, slot)
}

func pinnedDeviceBindingKey(groupID int64, originalDeviceID string) string {
	return fmt.Sprintf("%s%d:%s", pinnedDeviceBindingsKey, groupID, carpoolDeviceField(originalDeviceID))
}

func pinnedAccountKey(groupID, accountID int64) string {
	return fmt.Sprintf("%s%d:%d", pinnedAccountBindingKey, groupID, accountID)
}

type identityCache struct {
	rdb *redis.Client
}

const carpoolDeviceWriteMaxRetries = 16

func NewIdentityCache(rdb *redis.Client) service.IdentityCache {
	return &identityCache{rdb: rdb}
}

func (c *identityCache) GetFingerprint(ctx context.Context, accountID int64) (*service.Fingerprint, error) {
	key := fingerprintKey(accountID)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var fp service.Fingerprint
	if err := json.Unmarshal([]byte(val), &fp); err != nil {
		return nil, err
	}
	return &fp, nil
}

func (c *identityCache) SetFingerprint(ctx context.Context, accountID int64, fp *service.Fingerprint) error {
	key := fingerprintKey(accountID)
	val, err := json.Marshal(fp)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, val, fingerprintTTL).Err()
}

func (c *identityCache) DeleteFingerprint(ctx context.Context, accountID int64) error {
	return c.rdb.Del(ctx, fingerprintKey(accountID)).Err()
}

func (c *identityCache) GetSlotFingerprint(ctx context.Context, accountID int64, slot int) (*service.Fingerprint, error) {
	key := slotFingerprintKey(accountID, slot)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var fp service.Fingerprint
	if err := json.Unmarshal([]byte(val), &fp); err != nil {
		return nil, err
	}
	return &fp, nil
}

func (c *identityCache) SetSlotFingerprint(ctx context.Context, accountID int64, slot int, fp *service.Fingerprint) error {
	key := slotFingerprintKey(accountID, slot)
	val, err := json.Marshal(fp)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, val, fingerprintTTL).Err()
}

func (c *identityCache) GetOrCreateCarpoolDevice(ctx context.Context, accountID int64, originalDeviceID string, hints service.ClientHints, limit int, nowUnix int64) (*service.CarpoolDeviceRecord, error) {
	trimmedDeviceID := strings.TrimSpace(originalDeviceID)
	if trimmedDeviceID == "" {
		return nil, nil
	}
	if limit <= 0 {
		return nil, service.ErrClaudeOAuthCarpoolDevicesFull
	}

	recordedKey := carpoolRecordedKey(accountID)
	overflowKey := carpoolOverflowKey(accountID)
	deviceField := carpoolDeviceField(trimmedDeviceID)
	if deviceField == "" {
		return nil, nil
	}

	for attempt := 0; attempt < carpoolDeviceWriteMaxRetries; attempt++ {
		var out *service.CarpoolDeviceRecord
		err := c.rdb.Watch(ctx, func(tx *redis.Tx) error {
			recordedRaw, err := tx.HGet(ctx, recordedKey, deviceField).Result()
			if err != nil && err != redis.Nil {
				return err
			}
			if err == nil {
				var record service.CarpoolDeviceRecord
				if unmarshalErr := json.Unmarshal([]byte(recordedRaw), &record); unmarshalErr != nil {
					return unmarshalErr
				}
				record.LastSeenAt = nowUnix
				applyCarpoolDeviceHints(&record, hints)
				payload, marshalErr := json.Marshal(&record)
				if marshalErr != nil {
					return marshalErr
				}
				_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
					pipe.HSet(ctx, recordedKey, deviceField, payload)
					pipe.HDel(ctx, overflowKey, deviceField)
					return nil
				})
				if err == nil {
					out = &record
				}
				return err
			}

			recordedCount, err := tx.HLen(ctx, recordedKey).Result()
			if err != nil {
				return err
			}
			if recordedCount < int64(limit) {
				record := &service.CarpoolDeviceRecord{
					DeviceKey:        deviceField,
					OriginalDeviceID: trimmedDeviceID,
					CreatedAt:        nowUnix,
					LastSeenAt:       nowUnix,
				}
				applyCarpoolDeviceHints(record, hints)
				payload, marshalErr := json.Marshal(record)
				if marshalErr != nil {
					return marshalErr
				}
				_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
					pipe.HSet(ctx, recordedKey, deviceField, payload)
					pipe.HDel(ctx, overflowKey, deviceField)
					return nil
				})
				if err == nil {
					out = record
				}
				return err
			}

			overflowRaw, overflowErr := tx.HGet(ctx, overflowKey, deviceField).Result()
			if overflowErr != nil && overflowErr != redis.Nil {
				return overflowErr
			}
			overflowDeleteFields := make([]string, 0)
			if overflowErr == redis.Nil {
				overflowCount, countErr := tx.HLen(ctx, overflowKey).Result()
				if countErr != nil {
					return countErr
				}
				if overflowCount >= int64(carpoolOverflowMaxItems) {
					overflowItems, itemsErr := tx.HGetAll(ctx, overflowKey).Result()
					if itemsErr != nil {
						return itemsErr
					}
					overflowDeleteFields, itemsErr = oldestCarpoolOverflowFields(overflowItems, int(overflowCount)-carpoolOverflowMaxItems+1)
					if itemsErr != nil {
						return itemsErr
					}
				}
			}
			record := &service.CarpoolOverflowRecord{
				DeviceKey:        deviceField,
				OriginalDeviceID: trimmedDeviceID,
				FirstRejectedAt:  nowUnix,
				LastRejectedAt:   nowUnix,
				RejectCount:      1,
				LastUserAgent:    hints.UserAgent,
			}
			if overflowErr == nil {
				if unmarshalErr := json.Unmarshal([]byte(overflowRaw), record); unmarshalErr != nil {
					return unmarshalErr
				}
				if record.FirstRejectedAt == 0 {
					record.FirstRejectedAt = nowUnix
				}
				record.LastRejectedAt = nowUnix
				record.RejectCount++
				record.LastUserAgent = hints.UserAgent
			}
			payload, marshalErr := json.Marshal(record)
			if marshalErr != nil {
				return marshalErr
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				if len(overflowDeleteFields) > 0 {
					pipe.HDel(ctx, overflowKey, overflowDeleteFields...)
				}
				pipe.HSet(ctx, overflowKey, deviceField, payload)
				return nil
			})
			if err != nil {
				return err
			}
			return service.ErrClaudeOAuthCarpoolDevicesFull
		}, recordedKey, overflowKey)
		if err == nil {
			return out, nil
		}
		if errors.Is(err, service.ErrClaudeOAuthCarpoolDevicesFull) {
			return nil, err
		}
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return nil, err
	}
	return nil, redis.TxFailedErr
}

// applyCarpoolDeviceHints 把最新一次请求的 ClientHints 写进 record。空字段
// 不覆盖已有值，保留历史记录不被 SDK 偶发缺 header 的请求抹掉。
func applyCarpoolDeviceHints(record *service.CarpoolDeviceRecord, hints service.ClientHints) {
	if record == nil {
		return
	}
	if hints.UserAgent != "" {
		record.LastUserAgent = hints.UserAgent
	}
	if hints.OS != "" {
		record.LastOS = hints.OS
	}
	if hints.Arch != "" {
		record.LastArch = hints.Arch
	}
	if hints.Runtime != "" {
		record.LastRuntime = hints.Runtime
	}
	if hints.RuntimeVersion != "" {
		record.LastRuntimeVersion = hints.RuntimeVersion
	}
	if hints.SDKVersion != "" {
		record.LastSDKVersion = hints.SDKVersion
	}
}

func (c *identityCache) ListCarpoolDevices(ctx context.Context, accountID int64) ([]*service.CarpoolDeviceRecord, error) {
	values, err := c.rdb.HVals(ctx, carpoolRecordedKey(accountID)).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	items := make([]*service.CarpoolDeviceRecord, 0, len(values))
	for _, raw := range values {
		var item service.CarpoolDeviceRecord
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, err
		}
		items = append(items, &item)
	}
	return items, nil
}

func (c *identityCache) ListCarpoolOverflowDevices(ctx context.Context, accountID int64) ([]*service.CarpoolOverflowRecord, error) {
	values, err := c.rdb.HVals(ctx, carpoolOverflowKey(accountID)).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	items := make([]*service.CarpoolOverflowRecord, 0, len(values))
	for _, raw := range values {
		var item service.CarpoolOverflowRecord
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, err
		}
		items = append(items, &item)
	}
	return items, nil
}

func (c *identityCache) DeleteCarpoolDevice(ctx context.Context, accountID int64, deviceKey string) error {
	field := strings.TrimSpace(deviceKey)
	if field == "" {
		return nil
	}
	pipe := c.rdb.TxPipeline()
	pipe.HDel(ctx, carpoolRecordedKey(accountID), field)
	pipe.HDel(ctx, carpoolOverflowKey(accountID), field)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *identityCache) EnsureSharedBucketTopology(ctx context.Context, accountID int64, bucketCount int) error {
	if bucketCount <= 0 {
		return nil
	}

	configKey := sharedBucketConfigKey(accountID)
	if currentRaw, err := c.rdb.Get(ctx, configKey).Result(); err == nil {
		if current, parseErr := strconv.Atoi(strings.TrimSpace(currentRaw)); parseErr == nil && current == bucketCount {
			return nil
		}
	} else if err != nil && err != redis.Nil {
		return err
	}

	bindingKey := sharedBucketBindingKey(accountID)
	bindings, err := c.rdb.HGetAll(ctx, bindingKey).Result()
	if err != nil && err != redis.Nil {
		return err
	}

	deleteFields := make([]string, 0)
	for field, rawBucket := range bindings {
		bucket, parseErr := strconv.Atoi(strings.TrimSpace(rawBucket))
		if parseErr != nil || bucket < 0 || bucket >= bucketCount {
			deleteFields = append(deleteFields, field)
		}
	}

	pipe := c.rdb.TxPipeline()
	for bucket := bucketCount; bucket < maxSharedBucketSlots; bucket++ {
		pipe.Del(ctx, sharedBucketMetaKey(accountID, bucket))
		pipe.Del(ctx, slotFingerprintKey(accountID, bucket))
		pipe.Del(ctx, slotMaskedSessionKey(accountID, bucket))
	}
	if len(deleteFields) > 0 {
		pipe.HDel(ctx, bindingKey, deleteFields...)
	}
	pipe.Set(ctx, configKey, strconv.Itoa(bucketCount), 0)
	_, err = pipe.Exec(ctx)
	return err
}

func (c *identityCache) GetOrAssignSharedBucket(ctx context.Context, accountID int64, originalDeviceID string, bucketCount, preferredBucket int) (int, error) {
	trimmedDeviceID := strings.TrimSpace(originalDeviceID)
	if trimmedDeviceID == "" || bucketCount <= 0 {
		return 0, nil
	}
	if err := c.EnsureSharedBucketTopology(ctx, accountID, bucketCount); err != nil {
		return 0, err
	}
	if preferredBucket < 0 || preferredBucket >= bucketCount {
		preferredBucket = 0
	}

	bindingKey := sharedBucketBindingKey(accountID)
	field := carpoolDeviceField(trimmedDeviceID)
	if field == "" {
		return preferredBucket, nil
	}
	if rawBucket, err := c.rdb.HGet(ctx, bindingKey, field).Result(); err == nil {
		if bucket, parseErr := strconv.Atoi(strings.TrimSpace(rawBucket)); parseErr == nil && bucket >= 0 && bucket < bucketCount {
			return bucket, nil
		}
		if err := c.rdb.HDel(ctx, bindingKey, field).Err(); err != nil {
			return 0, err
		}
	} else if err != nil && err != redis.Nil {
		return 0, err
	}

	if err := c.rdb.HSet(ctx, bindingKey, field, strconv.Itoa(preferredBucket)).Err(); err != nil {
		return 0, err
	}
	return preferredBucket, nil
}

func (c *identityCache) GetSharedBucketState(ctx context.Context, accountID int64, bucket int) (*service.SharedBucketState, error) {
	val, err := c.rdb.Get(ctx, sharedBucketMetaKey(accountID, bucket)).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	var state service.SharedBucketState
	if err := json.Unmarshal([]byte(val), &state); err != nil {
		return nil, err
	}
	state.Bucket = bucket
	return &state, nil
}

func (c *identityCache) SetSharedBucketState(ctx context.Context, accountID int64, bucket int, state *service.SharedBucketState) error {
	if state == nil {
		return nil
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, sharedBucketMetaKey(accountID, bucket), payload, fingerprintTTL).Err()
}

func (c *identityCache) ListSharedBucketStates(ctx context.Context, accountID int64, bucketCount int) ([]*service.SharedBucketState, error) {
	if bucketCount <= 0 {
		return nil, nil
	}
	keys := make([]string, 0, bucketCount)
	for bucket := 0; bucket < bucketCount; bucket++ {
		keys = append(keys, sharedBucketMetaKey(accountID, bucket))
	}
	values, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	states := make([]*service.SharedBucketState, 0, bucketCount)
	for bucket, raw := range values {
		if raw == nil {
			continue
		}
		val, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected shared bucket state type %T", raw)
		}
		var state service.SharedBucketState
		if err := json.Unmarshal([]byte(val), &state); err != nil {
			return nil, err
		}
		state.Bucket = bucket
		states = append(states, &state)
	}
	return states, nil
}

func (c *identityCache) DeleteSharedBucketState(ctx context.Context, accountID int64, slot int) error {
	pipe := c.rdb.TxPipeline()
	pipe.Del(ctx, sharedBucketMetaKey(accountID, slot))
	pipe.Del(ctx, slotFingerprintKey(accountID, slot))
	pipe.Del(ctx, slotMaskedSessionKey(accountID, slot))
	_, err := pipe.Exec(ctx)
	return err
}

func (c *identityCache) GetOrCreateSingleDeviceSlot(ctx context.Context, accountID int64, slotKey string) (int, error) {
	trimmedSlotKey := strings.TrimSpace(slotKey)
	if accountID <= 0 || trimmedSlotKey == "" {
		return 0, nil
	}
	bindingKey := singleDeviceSlotBindingKey(accountID)
	if rawSlot, err := c.rdb.HGet(ctx, bindingKey, trimmedSlotKey).Result(); err == nil {
		if slot, parseErr := strconv.Atoi(strings.TrimSpace(rawSlot)); parseErr == nil && slot >= 0 {
			return slot, nil
		}
	} else if err != nil && err != redis.Nil {
		return 0, err
	}

	next, err := c.rdb.Incr(ctx, singleDeviceSlotCounterKey(accountID)).Result()
	if err != nil {
		return 0, err
	}
	slot := int(next - 1)
	ok, err := c.rdb.HSetNX(ctx, bindingKey, trimmedSlotKey, strconv.Itoa(slot)).Result()
	if err != nil {
		return 0, err
	}
	if ok {
		return slot, nil
	}
	rawSlot, err := c.rdb.HGet(ctx, bindingKey, trimmedSlotKey).Result()
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(rawSlot))
	if err != nil {
		return 0, err
	}
	return parsed, nil
}

func (c *identityCache) GetSingleDeviceSlotState(ctx context.Context, accountID int64, slot int) (*service.SingleDeviceSlotState, error) {
	val, err := c.rdb.Get(ctx, singleDeviceSlotMetaKey(accountID, slot)).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	var state service.SingleDeviceSlotState
	if err := json.Unmarshal([]byte(val), &state); err != nil {
		return nil, err
	}
	state.Slot = slot
	return &state, nil
}

func (c *identityCache) SetSingleDeviceSlotState(ctx context.Context, accountID int64, slot int, state *service.SingleDeviceSlotState) error {
	if state == nil {
		return nil
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, singleDeviceSlotMetaKey(accountID, slot), payload, fingerprintTTL).Err()
}

func (c *identityCache) ListSingleDeviceSlotStates(ctx context.Context, accountID int64) ([]*service.SingleDeviceSlotState, error) {
	maxSlotRaw, err := c.rdb.Get(ctx, singleDeviceSlotCounterKey(accountID)).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	maxSlot := 0
	if err == nil {
		if parsed, parseErr := strconv.Atoi(strings.TrimSpace(maxSlotRaw)); parseErr == nil && parsed > 0 {
			maxSlot = parsed
		}
	}
	if maxSlot <= 0 {
		return nil, nil
	}
	keys := make([]string, 0, maxSlot)
	for slot := 0; slot < maxSlot; slot++ {
		keys = append(keys, singleDeviceSlotMetaKey(accountID, slot))
	}
	values, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	items := make([]*service.SingleDeviceSlotState, 0, len(values))
	for slot, raw := range values {
		if raw == nil {
			continue
		}
		val, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected single_device slot state type %T", raw)
		}
		var state service.SingleDeviceSlotState
		if err := json.Unmarshal([]byte(val), &state); err != nil {
			return nil, err
		}
		state.Slot = slot
		items = append(items, &state)
	}
	return items, nil
}

func (c *identityCache) GetPinnedDeviceBindings(ctx context.Context, groupID int64, originalDeviceID string) ([]*service.PinnedDeviceBinding, error) {
	key := pinnedDeviceBindingKey(groupID, originalDeviceID)
	values, err := c.rdb.HVals(ctx, key).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	items := make([]*service.PinnedDeviceBinding, 0, len(values))
	for _, raw := range values {
		var item service.PinnedDeviceBinding
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, err
		}
		items = append(items, &item)
	}
	return items, nil
}

func (c *identityCache) GetPinnedAccountBindings(ctx context.Context, groupID int64, accountIDs []int64) (map[int64]*service.PinnedAccountBinding, error) {
	if len(accountIDs) == 0 {
		return map[int64]*service.PinnedAccountBinding{}, nil
	}
	keys := make([]string, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		keys = append(keys, pinnedAccountKey(groupID, accountID))
	}
	values, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	result := make(map[int64]*service.PinnedAccountBinding, len(accountIDs))
	for idx, raw := range values {
		if raw == nil {
			continue
		}
		val, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected pinned account binding type %T", raw)
		}
		var item service.PinnedAccountBinding
		if err := json.Unmarshal([]byte(val), &item); err != nil {
			return nil, err
		}
		result[accountIDs[idx]] = &item
	}
	return result, nil
}

func (c *identityCache) BindPinnedDeviceAccount(ctx context.Context, groupID int64, originalDeviceID string, deviceBinding *service.PinnedDeviceBinding, accountBinding *service.PinnedAccountBinding) error {
	if groupID <= 0 || strings.TrimSpace(originalDeviceID) == "" || deviceBinding == nil || accountBinding == nil {
		return nil
	}
	devicePayload, err := json.Marshal(deviceBinding)
	if err != nil {
		return err
	}
	accountPayload, err := json.Marshal(accountBinding)
	if err != nil {
		return err
	}
	pipe := c.rdb.TxPipeline()
	pipe.HSet(ctx, pinnedDeviceBindingKey(groupID, originalDeviceID), strconv.FormatInt(deviceBinding.AccountID, 10), devicePayload)
	pipe.Set(ctx, pinnedAccountKey(groupID, accountBinding.AccountID), accountPayload, 0)
	_, err = pipe.Exec(ctx)
	return err
}

func (c *identityCache) DeletePinnedDeviceAccountBinding(ctx context.Context, groupID int64, originalDeviceID string, accountID int64) error {
	if groupID <= 0 || accountID <= 0 || strings.TrimSpace(originalDeviceID) == "" {
		return nil
	}
	pipe := c.rdb.TxPipeline()
	pipe.HDel(ctx, pinnedDeviceBindingKey(groupID, originalDeviceID), strconv.FormatInt(accountID, 10))
	pipe.Del(ctx, pinnedAccountKey(groupID, accountID))
	_, err := pipe.Exec(ctx)
	return err
}

func (c *identityCache) GetMaskedSessionID(ctx context.Context, accountID int64) (string, error) {
	key := maskedSessionKey(accountID)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return "", nil
		}
		return "", err
	}
	return val, nil
}

func (c *identityCache) SetMaskedSessionID(ctx context.Context, accountID int64, sessionID string) error {
	key := maskedSessionKey(accountID)
	return c.rdb.Set(ctx, key, sessionID, randomMaskedSessionTTL()).Err()
}

func (c *identityCache) GetSlotMaskedSessionID(ctx context.Context, accountID int64, slot int) (string, error) {
	key := slotMaskedSessionKey(accountID, slot)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return "", nil
		}
		return "", err
	}
	return val, nil
}

func (c *identityCache) SetSlotMaskedSessionID(ctx context.Context, accountID int64, slot int, sessionID string) error {
	key := slotMaskedSessionKey(accountID, slot)
	return c.rdb.Set(ctx, key, sessionID, randomMaskedSessionTTL()).Err()
}

// randomMaskedSessionTTL 生成 [5, 60] 分钟范围内的随机 TTL
func randomMaskedSessionTTL() time.Duration {
	rangeNanos := big.NewInt(int64(maskedSessionTTLMax - maskedSessionTTLMin))
	jitter, err := rand.Int(rand.Reader, rangeNanos)
	if err != nil {
		return maskedSessionTTLMin + 10*time.Minute // fallback ~15min
	}
	return maskedSessionTTLMin + time.Duration(jitter.Int64())
}

func oldestCarpoolOverflowFields(items map[string]string, deleteCount int) ([]string, error) {
	if deleteCount <= 0 || len(items) == 0 {
		return nil, nil
	}

	records := make([]*service.CarpoolOverflowRecord, 0, len(items))
	for field, raw := range items {
		var record service.CarpoolOverflowRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return nil, err
		}
		if strings.TrimSpace(record.DeviceKey) == "" {
			record.DeviceKey = field
		}
		records = append(records, &record)
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].LastRejectedAt == records[j].LastRejectedAt {
			return records[i].DeviceKey < records[j].DeviceKey
		}
		return records[i].LastRejectedAt < records[j].LastRejectedAt
	})

	if deleteCount > len(records) {
		deleteCount = len(records)
	}
	fields := make([]string, 0, deleteCount)
	for i := 0; i < deleteCount; i++ {
		fields = append(fields, records[i].DeviceKey)
	}
	return fields, nil
}
