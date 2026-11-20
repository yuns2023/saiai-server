//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func redisZ(score float64, member string) redis.Z {
	return redis.Z{Score: score, Member: member}
}

func newMarketConcurrencyCache(t *testing.T) (*concurrencyCache, context.Context) {
	t.Helper()
	rdb := testRedis(t)
	cache, ok := NewConcurrencyCache(rdb, testSlotTTLMinutes, int(testSlotTTL.Seconds())).(*concurrencyCache)
	require.True(t, ok)
	return cache, context.Background()
}

// C=4, R=1: the owner keeps one slot whatever the arrival order; buyers stop
// at C-R even while the owner is idle.
func TestMarketBuyerSlotsReserveOwnerCapacity(t *testing.T) {
	const account, C, B = int64(41), 4, 3
	t.Run("owner first", func(t *testing.T) {
		cache, ctx := newMarketConcurrencyCache(t)
		ok, err := cache.AcquireAccountSlot(ctx, account, C, "owner-1")
		require.NoError(t, err)
		require.True(t, ok)
		for i := 0; i < B; i++ {
			ok, err := cache.AcquireAccountBuyerSlot(ctx, account, C, B, fmt.Sprintf("buyer-%d", i))
			require.NoError(t, err)
			require.True(t, ok, "buyer %d", i)
		}
		ok, err = cache.AcquireAccountBuyerSlot(ctx, account, C, B, "buyer-x")
		require.NoError(t, err)
		require.False(t, ok, "total is full")
		ok, err = cache.AcquireAccountSlot(ctx, account, C, "owner-2")
		require.NoError(t, err)
		require.False(t, ok, "owner only within the total")
	})
	t.Run("buyers first", func(t *testing.T) {
		cache, ctx := newMarketConcurrencyCache(t)
		for i := 0; i < B; i++ {
			ok, err := cache.AcquireAccountBuyerSlot(ctx, account, C, B, fmt.Sprintf("buyer-%d", i))
			require.NoError(t, err)
			require.True(t, ok)
		}
		// Owner idle: the 4th buyer is still refused (total 3 < 4).
		ok, err := cache.AcquireAccountBuyerSlot(ctx, account, C, B, "buyer-x")
		require.NoError(t, err)
		require.False(t, ok, "buyers stop at C-R while the owner is idle")
		ok, err = cache.AcquireAccountSlot(ctx, account, C, "owner-1")
		require.NoError(t, err)
		require.True(t, ok, "the reserved slot is the owner's")
		counts, err := cache.GetAccountBuyerConcurrencyBatch(ctx, []int64{account})
		require.NoError(t, err)
		require.Equal(t, B, counts[account])
		total, err := cache.GetAccountConcurrency(ctx, account)
		require.NoError(t, err)
		require.Equal(t, C, total, "one total set for every class")

		// Release frees both sets; a buyer fits again, the owner is unaffected.
		require.NoError(t, cache.ReleaseAccountBuyerSlot(ctx, account, "buyer-0"))
		counts, err = cache.GetAccountBuyerConcurrencyBatch(ctx, []int64{account})
		require.NoError(t, err)
		require.Equal(t, B-1, counts[account])
		ok, err = cache.AcquireAccountBuyerSlot(ctx, account, C, B, "buyer-y")
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("zero buyer cap is never unlimited", func(t *testing.T) {
		cache, ctx := newMarketConcurrencyCache(t)
		ok, err := cache.AcquireAccountBuyerSlot(ctx, account, C, 0, "buyer")
		require.NoError(t, err)
		require.False(t, ok)
		ok, err = cache.AcquireAccountBuyerSlot(ctx, account, 0, 0, "buyer")
		require.NoError(t, err)
		require.False(t, ok)
	})
}

func TestMarketBuyerSlotRetryRenewAndExpiry(t *testing.T) {
	cache, ctx := newMarketConcurrencyCache(t)
	const account = int64(42)
	ok, err := cache.AcquireAccountBuyerSlot(ctx, account, 2, 1, "buyer-1")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = cache.AcquireAccountBuyerSlot(ctx, account, 2, 1, "buyer-1")
	require.NoError(t, err)
	require.True(t, ok, "same request id = idempotent retry, not a second lease")
	counts, err := cache.GetAccountBuyerConcurrencyBatch(ctx, []int64{account})
	require.NoError(t, err)
	require.Equal(t, 1, counts[account])

	// A long stream: both members close to the TTL; renewal refreshes them,
	// so they survive past the original deadline.
	aging := float64(time.Now().Add(-testSlotTTL + time.Minute).Unix())
	require.NoError(t, cache.rdb.ZAddXX(ctx, accountSlotKey(account), redisZ(aging, "buyer-1")).Err())
	require.NoError(t, cache.rdb.ZAddXX(ctx, accountBuyerSlotKey(account), redisZ(aging, "buyer-1")).Err())
	present, err := cache.RenewAccountBuyerSlot(ctx, account, "buyer-1")
	require.NoError(t, err)
	require.True(t, present)
	ok, err = cache.AcquireAccountBuyerSlot(ctx, account, 2, 1, "buyer-2")
	require.NoError(t, err)
	require.False(t, ok, "the renewed lease still counts after the original TTL")

	// Without renewal both members expire together.
	old := float64(time.Now().Add(-testSlotTTL - time.Minute).Unix())
	require.NoError(t, cache.rdb.ZAddXX(ctx, accountSlotKey(account), redisZ(old, "buyer-1")).Err())
	require.NoError(t, cache.rdb.ZAddXX(ctx, accountBuyerSlotKey(account), redisZ(old, "buyer-1")).Err())
	ok, err = cache.AcquireAccountBuyerSlot(ctx, account, 2, 1, "buyer-2")
	require.NoError(t, err)
	require.True(t, ok, "expired members are trimmed from both sets")
	total, err := cache.GetAccountConcurrency(ctx, account)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	present, err = cache.RenewAccountBuyerSlot(ctx, account, "buyer-1")
	require.NoError(t, err)
	require.False(t, present, "renew never re-adds an expired lease")

	// Lease lost: an expired member taken over by someone else is not held;
	// a half lease is never refreshed by renewal.
	held, err := cache.AccountBuyerLeaseHeld(ctx, account, "buyer-2")
	require.NoError(t, err)
	require.True(t, held)
	held, err = cache.AccountBuyerLeaseHeld(ctx, account, "buyer-1")
	require.NoError(t, err)
	require.False(t, held, "the expired lease is gone, buyer-2 took the capacity")
	require.NoError(t, cache.rdb.ZRem(ctx, accountBuyerSlotKey(account), "buyer-2").Err())
	before, err := cache.rdb.ZScore(ctx, accountSlotKey(account), "buyer-2").Result()
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond)
	present, err = cache.RenewAccountBuyerSlot(ctx, account, "buyer-2")
	require.NoError(t, err)
	require.False(t, present)
	after, err := cache.rdb.ZScore(ctx, accountSlotKey(account), "buyer-2").Result()
	require.NoError(t, err)
	require.Equal(t, before, after, "the remaining member of a half lease is not refreshed")
	held, err = cache.AccountBuyerLeaseHeld(ctx, account, "buyer-2")
	require.NoError(t, err)
	require.False(t, held)

	// Cancel / release removes from both sets.
	require.NoError(t, cache.ReleaseAccountBuyerSlot(ctx, account, "buyer-2"))
	total, err = cache.GetAccountConcurrency(ctx, account)
	require.NoError(t, err)
	require.Zero(t, total)
	counts, err = cache.GetAccountBuyerConcurrencyBatch(ctx, []int64{account})
	require.NoError(t, err)
	require.Zero(t, counts[account])
}

func TestMarketBuyerSlotsStartupCleanup(t *testing.T) {
	cache, ctx := newMarketConcurrencyCache(t)
	const account = int64(43)
	ok, err := cache.AcquireAccountBuyerSlot(ctx, account, 3, 2, "rOld-1")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = cache.AcquireAccountBuyerSlot(ctx, account, 3, 2, "rNew-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, cache.CleanupStaleProcessSlots(ctx, "rNew"))
	counts, err := cache.GetAccountBuyerConcurrencyBatch(ctx, []int64{account})
	require.NoError(t, err)
	require.Equal(t, 1, counts[account], "other processes' buyer members are cleared like total members")
	total, err := cache.GetAccountConcurrency(ctx, account)
	require.NoError(t, err)
	require.Equal(t, 1, total)
}

func newMarketIdentityCache(t *testing.T) (*identityCache, context.Context) {
	t.Helper()
	cache, ok := NewIdentityCache(testRedis(t)).(*identityCache)
	require.True(t, ok)
	return cache, context.Background()
}

func marketDevice(t *testing.T, cache *identityCache, ctx context.Context, account int64, device string, limit int, policy *service.CarpoolAdmissionPolicy, adm service.MarketDeviceAdmission) error {
	t.Helper()
	_, err := cache.GetOrCreateCarpoolDeviceMarket(ctx, account, device, service.ClientHints{}, limit, time.Now().Unix(), policy, adm)
	return err
}

func TestMarketDeviceClasses(t *testing.T) {
	owner := service.MarketDeviceAdmission{Class: service.MarketDeviceClassOwner}
	buyer := service.MarketDeviceAdmission{Class: service.MarketDeviceClassBuyer, ReservedDevices: 2}
	full := service.ErrClaudeOAuthCarpoolDevicesFull

	t.Run("buyer limit is limit minus reserved; owner keeps the rest", func(t *testing.T) {
		cache, ctx := newMarketIdentityCache(t)
		const account, limit = int64(51), 5
		for i := 0; i < 3; i++ {
			require.NoError(t, marketDevice(t, cache, ctx, account, fmt.Sprintf("b%d", i), limit, nil, buyer))
		}
		require.ErrorIs(t, marketDevice(t, cache, ctx, account, "b3", limit, nil, buyer), full)
		require.NoError(t, marketDevice(t, cache, ctx, account, "o1", limit, nil, owner))
		require.NoError(t, marketDevice(t, cache, ctx, account, "o2", limit, nil, owner))
		require.ErrorIs(t, marketDevice(t, cache, ctx, account, "o3", limit, nil, owner), full, "total limit")
		// Known devices keep working.
		require.NoError(t, marketDevice(t, cache, ctx, account, "b1", limit, nil, buyer))
		require.NoError(t, marketDevice(t, cache, ctx, account, "o1", limit, nil, owner))
		// A buyer request on an owner device does not inherit the exemption.
		require.ErrorIs(t, marketDevice(t, cache, ctx, account, "o1", limit, nil, buyer), full)
		// An owner request on a buyer device is just a known device.
		require.NoError(t, marketDevice(t, cache, ctx, account, "b2", limit, nil, owner))
		// Manual delete frees the class count (derived from the records).
		require.NoError(t, cache.DeleteCarpoolDevice(ctx, account, carpoolDeviceField("b0")))
		require.NoError(t, marketDevice(t, cache, ctx, account, "b9", limit, nil, buyer))
	})

	t.Run("legacy records count as buyer devices; owner claims its own", func(t *testing.T) {
		cache, ctx := newMarketIdentityCache(t)
		const account, limit = int64(52), 5
		for i := 0; i < 4; i++ {
			_, err := cache.GetOrCreateCarpoolDevice(ctx, account, fmt.Sprintf("legacy%d", i), service.ClientHints{}, limit, time.Now().Unix())
			require.NoError(t, err)
		}
		// 4 legacy (buyer) devices > buyer limit 3: kept, but no new buyer device.
		require.ErrorIs(t, marketDevice(t, cache, ctx, account, "new-buyer", limit, nil, buyer), full)
		require.NoError(t, marketDevice(t, cache, ctx, account, "legacy0", limit, nil, buyer), "legacy devices are not evicted")
		// The owner uses legacy1: it becomes an owner device; buyers now 3.
		require.NoError(t, marketDevice(t, cache, ctx, account, "legacy1", limit, nil, owner))
		records, err := cache.ListCarpoolDevices(ctx, account)
		require.NoError(t, err)
		classes := map[string]string{}
		for _, r := range records {
			classes[r.OriginalDeviceID] = r.Class
		}
		require.Equal(t, service.MarketDeviceClassOwner, classes["legacy1"])
		require.Equal(t, "", classes["legacy0"])
		require.ErrorIs(t, marketDevice(t, cache, ctx, account, "new-buyer", limit, nil, buyer), full, "3 buyer devices = buyer limit")
		require.NoError(t, marketDevice(t, cache, ctx, account, "new-owner", limit, nil, owner))
	})

	t.Run("shrunk limit keeps devices and refuses new ones", func(t *testing.T) {
		cache, ctx := newMarketIdentityCache(t)
		const account = int64(53)
		for i := 0; i < 3; i++ {
			require.NoError(t, marketDevice(t, cache, ctx, account, fmt.Sprintf("b%d", i), 5, nil, buyer))
		}
		// Limit 5 -> 4: buyer limit 2 < 3 existing buyers.
		require.ErrorIs(t, marketDevice(t, cache, ctx, account, "b3", 4, nil, buyer), full)
		require.NoError(t, marketDevice(t, cache, ctx, account, "b0", 4, nil, buyer))
		require.NoError(t, marketDevice(t, cache, ctx, account, "o", 4, nil, owner))
		require.ErrorIs(t, marketDevice(t, cache, ctx, account, "o2", 4, nil, owner), full)
	})

	t.Run("unidentified device", func(t *testing.T) {
		cache, ctx := newMarketIdentityCache(t)
		require.ErrorIs(t, marketDevice(t, cache, ctx, 54, "  ", 5, nil, buyer), service.ErrMarketDeviceUnidentified)
	})
}

func TestMarketDeviceCycleBudget(t *testing.T) {
	owner := service.MarketDeviceAdmission{Class: service.MarketDeviceClassOwner}
	full := service.ErrClaudeOAuthCarpoolDevicesFull
	policy := func(q int) *service.CarpoolAdmissionPolicy {
		return &service.CarpoolAdmissionPolicy{Generation: "00000000000000000001", Admissions: q, InitialDue: "2026-11-19T00:00:00Z"}
	}

	t.Run("Q=0 is unlimited for both classes", func(t *testing.T) {
		cache, ctx := newMarketIdentityCache(t)
		buyer := service.MarketDeviceAdmission{Class: service.MarketDeviceClassBuyer, ReservedAdmissions: 1}
		for i := 0; i < 3; i++ {
			require.NoError(t, marketDevice(t, cache, ctx, 61, fmt.Sprintf("b%d", i), 10, policy(0), buyer))
		}
	})

	t.Run("Q-Rq=0 blocks new buyer devices this cycle, not the owner", func(t *testing.T) {
		cache, ctx := newMarketIdentityCache(t)
		buyer := service.MarketDeviceAdmission{Class: service.MarketDeviceClassBuyer, ReservedAdmissions: 2}
		require.ErrorIs(t, marketDevice(t, cache, ctx, 62, "b0", 10, policy(2), buyer), full)
		require.NoError(t, marketDevice(t, cache, ctx, 62, "o0", 10, policy(2), owner))
		require.NoError(t, marketDevice(t, cache, ctx, 62, "o1", 10, policy(2), owner))
		require.ErrorIs(t, marketDevice(t, cache, ctx, 62, "o2", 10, policy(2), owner), full, "total Q")
	})

	t.Run("a budget from before the market counts its admissions as buyer ones", func(t *testing.T) {
		cache, ctx := newMarketIdentityCache(t)
		buyer := service.MarketDeviceAdmission{Class: service.MarketDeviceClassBuyer, ReservedAdmissions: 2}
		p := policy(5)
		// Two unclassified admissions this cycle (old path).
		for _, d := range []string{"x0", "x1"} {
			_, err := cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, 64, d, service.ClientHints{}, 10, time.Now().Unix(), *p)
			require.NoError(t, err)
		}
		// Buyer quota 5-2 = 3, two already used: one more buyer device only.
		require.NoError(t, marketDevice(t, cache, ctx, 64, "b0", 10, p, buyer))
		require.ErrorIs(t, marketDevice(t, cache, ctx, 64, "b1", 10, p, buyer), full)
		require.NoError(t, marketDevice(t, cache, ctx, 64, "o0", 10, p, owner), "the owner keeps the reserved admissions")
	})

	t.Run("buyers within Q-Rq, owner within Q; rollover resets both counts", func(t *testing.T) {
		cache, ctx := newMarketIdentityCache(t)
		buyer := service.MarketDeviceAdmission{Class: service.MarketDeviceClassBuyer, ReservedAdmissions: 1}
		p := policy(3)
		require.NoError(t, marketDevice(t, cache, ctx, 63, "b0", 10, p, buyer))
		require.NoError(t, marketDevice(t, cache, ctx, 63, "b1", 10, p, buyer))
		require.ErrorIs(t, marketDevice(t, cache, ctx, 63, "b2", 10, p, buyer), full)
		require.NoError(t, marketDevice(t, cache, ctx, 63, "o0", 10, p, owner))
		require.ErrorIs(t, marketDevice(t, cache, ctx, 63, "o1", 10, p, owner), full)

		raw, err := cache.rdb.Get(ctx, carpoolAdmissionBudgetKey(63)).Result()
		require.NoError(t, err)
		var budget carpoolAdmissionBudget
		require.NoError(t, json.Unmarshal([]byte(raw), &budget))
		require.Equal(t, 3, budget.Used)
		require.Equal(t, 2, budget.BuyerUsed)

		_, err = cache.ResetCarpoolAdmissionCycle(ctx, 63, 10, *p, "2026-11-20T00:00:00Z")
		require.NoError(t, err)
		require.NoError(t, marketDevice(t, cache, ctx, 63, "b2", 10, p, buyer), "new cycle")
		raw, err = cache.rdb.Get(ctx, carpoolAdmissionBudgetKey(63)).Result()
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal([]byte(raw), &budget))
		require.Equal(t, 1, budget.Used)
		require.Equal(t, 1, budget.BuyerUsed)
	})
}

func TestMarketDeviceBudgetAfterMaintenanceThenPlainAdmissions(t *testing.T) {
	cache, ctx := newMarketIdentityCache(t)
	full := service.ErrClaudeOAuthCarpoolDevicesFull
	p := service.CarpoolAdmissionPolicy{Generation: "00000000000000000002", Admissions: 5, InitialDue: "2026-11-19T00:00:00Z"}
	// Cycle rollover (not the first cycle), then two plain admissions, then
	// the account is listed in the same cycle.
	_, err := cache.ResetCarpoolAdmissionCycle(ctx, 65, 10, p, "2026-11-20T00:00:00Z")
	require.NoError(t, err)
	for _, d := range []string{"x0", "x1"} {
		_, err := cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, 65, d, service.ClientHints{}, 10, time.Now().Unix(), p)
		require.NoError(t, err)
	}
	buyer := service.MarketDeviceAdmission{Class: service.MarketDeviceClassBuyer, ReservedAdmissions: 2}
	require.NoError(t, marketDevice(t, cache, ctx, 65, "b0", 10, &p, buyer))
	require.ErrorIs(t, marketDevice(t, cache, ctx, 65, "b1", 10, &p, buyer), full, "plain admissions count as buyer ones: 2 + 1 = Q - Rq")
}

func TestMarketDeviceBudgetUnlistRelistSameCycle(t *testing.T) {
	cache, ctx := newMarketIdentityCache(t)
	full := service.ErrClaudeOAuthCarpoolDevicesFull
	p := service.CarpoolAdmissionPolicy{Generation: "00000000000000000003", Admissions: 5, InitialDue: "2026-11-19T00:00:00Z"}
	buyer := service.MarketDeviceAdmission{Class: service.MarketDeviceClassBuyer, ReservedAdmissions: 2}
	require.NoError(t, marketDevice(t, cache, ctx, 66, "b0", 10, &p, buyer)) // listed: classified
	// Unlisted: two plain admissions in the same cycle.
	for _, d := range []string{"x0", "x1"} {
		_, err := cache.GetOrCreateCarpoolDeviceWithPolicy(ctx, 66, d, service.ClientHints{}, 10, time.Now().Unix(), p)
		require.NoError(t, err)
	}
	// Relisted: 1 + 2 = Q - Rq, no further buyer device this cycle.
	require.ErrorIs(t, marketDevice(t, cache, ctx, 66, "b1", 10, &p, buyer), full)
	require.NoError(t, marketDevice(t, cache, ctx, 66, "o0", 10, &p, service.MarketDeviceAdmission{Class: service.MarketDeviceClassOwner}))
}

