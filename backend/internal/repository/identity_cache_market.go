package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

var _ service.MarketCarpoolAdmissionCache = (*identityCache)(nil)

// GetOrCreateCarpoolDeviceMarket is the carpool device admission of a listed
// account (market owner or buyer path), inside one WATCH transaction. There
// are no owner reservations (共享 and 自用 are exclusive), so the limits are
// the account's own, for the whole account:
//
//   - a new device needs total devices < limit;
//   - with a cycle budget (policy != nil, Admissions > 0) a new device needs
//     Used < Quota (Admissions = 0 is the "unlimited cycle" state);
//   - an existing device is reused by either class without consuming a new
//     device admission (a buyer request on a device the owner registered —
//     e.g. the owner buying from the same machine after listing — passes);
//     it keeps its class, and an owner request on an unclassified
//     (pre-market) device claims it for the owner.
//
// The class and BuyerUsed are bookkeeping only (kept for a rollback to the
// previous binary, which reads them). Counts are derived from the records on
// every admission, so deletes, evictions and limit changes need no separate
// bookkeeping.
func (c *identityCache) GetOrCreateCarpoolDeviceMarket(ctx context.Context, accountID int64, originalDeviceID string, hints service.ClientHints, limit int, nowUnix int64, policy *service.CarpoolAdmissionPolicy, adm service.MarketDeviceAdmission) (*service.CarpoolDeviceRecord, error) {
	fullErr := service.ErrClaudeOAuthCarpoolDevicesFull
	trimmed := strings.TrimSpace(originalDeviceID)
	if trimmed == "" {
		return nil, service.ErrMarketDeviceUnidentified
	}
	if adm.Class != service.MarketDeviceClassOwner && adm.Class != service.MarketDeviceClassBuyer {
		return nil, errors.New("invalid market device class")
	}
	if limit <= 0 {
		return nil, fullErr
	}
	field := carpoolDeviceField(trimmed)
	if field == "" {
		return nil, service.ErrMarketDeviceUnidentified
	}
	if policy != nil && (policy.Generation == "" || policy.Admissions < 0) {
		return nil, errors.New("invalid carpool admission policy")
	}
	useBudget := policy != nil && policy.Admissions > 0
	recordedKey, overflowKey, budgetKey := carpoolRecordedKey(accountID), carpoolOverflowKey(accountID), carpoolAdmissionBudgetKey(accountID)
	watch := []string{recordedKey, overflowKey}
	if useBudget {
		watch = append(watch, budgetKey)
	}

	for attempt := 0; attempt < carpoolDeviceWriteMaxRetries; attempt++ {
		var out *service.CarpoolDeviceRecord
		err := c.rdb.Watch(ctx, func(tx *redis.Tx) error {
			raw, err := tx.HGet(ctx, recordedKey, field).Result()
			if err != nil && err != redis.Nil {
				return err
			}
			if err == nil {
				var record service.CarpoolDeviceRecord
				if err := json.Unmarshal([]byte(raw), &record); err != nil {
					return err
				}
				if adm.Class == service.MarketDeviceClassOwner && record.Class == "" {
					record.Class = service.MarketDeviceClassOwner
				}
				record.LastSeenAt = nowUnix
				applyCarpoolDeviceHints(&record, hints)
				payload, err := json.Marshal(&record)
				if err != nil {
					return err
				}
				_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
					pipe.HSet(ctx, recordedKey, field, payload)
					pipe.HDel(ctx, overflowKey, field)
					return nil
				})
				if err == nil {
					out = &record
				}
				return err
			}

			// New device (records are few, bounded by the device limit).
			count, err := tx.HLen(ctx, recordedKey).Result()
			if err != nil && err != redis.Nil {
				return err
			}
			allowed := count < int64(limit)
			var budget *carpoolAdmissionBudget
			if allowed && useBudget {
				budget, err = readCarpoolAdmissionBudget(ctx, tx, budgetKey, *policy)
				if err != nil {
					return err
				}
				if !budget.MarketClassified {
					// Admissions of this cycle made before classes were
					// counted: assume they were all buyer admissions.
					budget.BuyerUsed = budget.Used
					budget.MarketClassified = true
				}
				if budget.Generation != policy.Generation || budget.Used >= budget.Quota {
					allowed = false
				}
			}
			if !allowed {
				if err := recordCarpoolOverflowTx(ctx, tx, overflowKey, field, trimmed, hints, nowUnix); err != nil {
					return err
				}
				return fullErr
			}
			record := &service.CarpoolDeviceRecord{
				DeviceKey:        field,
				OriginalDeviceID: trimmed,
				CreatedAt:        nowUnix,
				LastSeenAt:       nowUnix,
				Class:            adm.Class,
			}
			applyCarpoolDeviceHints(record, hints)
			payload, err := json.Marshal(record)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				if budget != nil {
					budget.Used++
					if adm.Class == service.MarketDeviceClassBuyer {
						budget.BuyerUsed++
					}
					encoded, err := json.Marshal(budget)
					if err != nil {
						return err
					}
					pipe.Set(ctx, budgetKey, encoded, 0)
				}
				pipe.HSet(ctx, recordedKey, field, payload)
				pipe.HDel(ctx, overflowKey, field)
				return nil
			})
			if err == nil {
				out = record
			}
			return err
		}, watch...)
		if err == nil {
			return out, nil
		}
		if errors.Is(err, fullErr) {
			return nil, err
		}
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return nil, err
	}
	return nil, redis.TxFailedErr
}

// recordCarpoolOverflowTx mirrors the rejected-device bookkeeping of
// getOrCreateAdmissionDevice (bounded overflow hash, oldest dropped first).
func recordCarpoolOverflowTx(ctx context.Context, tx *redis.Tx, overflowKey, field, deviceID string, hints service.ClientHints, nowUnix int64) error {
	overflowRaw, overflowErr := tx.HGet(ctx, overflowKey, field).Result()
	if overflowErr != nil && overflowErr != redis.Nil {
		return overflowErr
	}
	var deleteFields []string
	if overflowErr == redis.Nil {
		count, err := tx.HLen(ctx, overflowKey).Result()
		if err != nil {
			return err
		}
		if count >= int64(carpoolOverflowMaxItems) {
			items, err := tx.HGetAll(ctx, overflowKey).Result()
			if err != nil {
				return err
			}
			deleteFields, err = oldestCarpoolOverflowFields(items, int(count)-carpoolOverflowMaxItems+1)
			if err != nil {
				return err
			}
		}
	}
	record := &service.CarpoolOverflowRecord{
		DeviceKey:        field,
		OriginalDeviceID: deviceID,
		FirstRejectedAt:  nowUnix,
		LastRejectedAt:   nowUnix,
		RejectCount:      1,
		LastUserAgent:    hints.UserAgent,
	}
	if overflowErr == nil {
		if err := json.Unmarshal([]byte(overflowRaw), record); err != nil {
			return err
		}
		if record.FirstRejectedAt == 0 {
			record.FirstRejectedAt = nowUnix
		}
		record.LastRejectedAt = nowUnix
		record.RejectCount++
		record.LastUserAgent = hints.UserAgent
	}
	if hints.Owner != nil {
		record.LastOwner = hints.Owner
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		if len(deleteFields) > 0 {
			pipe.HDel(ctx, overflowKey, deleteFields...)
		}
		pipe.HSet(ctx, overflowKey, field, payload)
		return nil
	})
	return err
}
