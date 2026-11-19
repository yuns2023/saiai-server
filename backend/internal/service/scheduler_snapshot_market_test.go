//go:build unit

package service

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// bucketRecordingCache records the rebuilt buckets and keeps account copies.
type bucketRecordingCache struct {
	SchedulerCache
	mu       sync.Mutex
	accounts map[int64]*Account
	rebuilt  []SchedulerBucket
	// failOnce makes the next SetSnapshot of that bucket fail (nothing
	// published).
	failOnce *SchedulerBucket
	// locks mimics the Redis rebuild lock (held until its TTL; expire()).
	locks map[string]bool
}

func (c *bucketRecordingCache) GetAccount(_ context.Context, id int64) (*Account, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a, ok := c.accounts[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, nil
}

func (c *bucketRecordingCache) SetAccount(_ context.Context, a *Account) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := *a
	c.accounts[a.ID] = &cp
	return nil
}

func (c *bucketRecordingCache) DeleteAccount(_ context.Context, id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.accounts, id)
	return nil
}

func (c *bucketRecordingCache) TryLockBucket(_ context.Context, b SchedulerBucket, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.locks == nil {
		c.locks = map[string]bool{}
	}
	if c.locks[b.String()] {
		return false, nil
	}
	c.locks[b.String()] = true
	return true, nil
}

// expire drops every lock (their TTL ran out).
func (c *bucketRecordingCache) expire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.locks = nil
}

func (c *bucketRecordingCache) SetSnapshot(_ context.Context, b SchedulerBucket, _ []Account) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failOnce != nil && *c.failOnce == b {
		c.failOnce = nil
		return errors.New("redis down")
	}
	c.rebuilt = append(c.rebuilt, b)
	return nil
}

// rebuiltGroups returns the sorted distinct group ids rebuilt for platform.
func (c *bucketRecordingCache) rebuiltGroups(platform string) []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []int64
	for _, b := range c.rebuilt {
		if b.Platform == platform && !slices.Contains(out, b.GroupID) {
			out = append(out, b.GroupID)
		}
	}
	slices.Sort(out)
	return out
}

type snapshotEventAccountRepo struct {
	AccountRepository
	accounts map[int64]*Account
}

func (r *snapshotEventAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if a, ok := r.accounts[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, ErrAccountNotFound
}

func (r *snapshotEventAccountRepo) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	var out []*Account
	for _, id := range ids {
		if a, ok := r.accounts[id]; ok {
			cp := *a
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *snapshotEventAccountRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	return nil, nil
}

func (r *snapshotEventAccountRepo) ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]Account, error) {
	return nil, nil
}

func (r *snapshotEventAccountRepo) ListSchedulableUngroupedByPlatform(context.Context, string) ([]Account, error) {
	return nil, nil
}

func (r *snapshotEventAccountRepo) ListSchedulableUngroupedByPlatforms(context.Context, []string) ([]Account, error) {
	return nil, nil
}

func (r *snapshotEventAccountRepo) ListSchedulableByPlatform(context.Context, string) ([]Account, error) {
	return nil, nil
}

func (r *snapshotEventAccountRepo) ListSchedulableByPlatforms(context.Context, []string) ([]Account, error) {
	return nil, nil
}

// An account event rebuilds the market tiers of the account (now and per
// the previous cached copy), not only its admin groups: a listed account
// that turns schedulable again (or changes) must not wait for a full
// rebuild to (re)appear in its tier bucket.
func TestSchedulerAccountEventRebuildsMarketTierBuckets(t *testing.T) {
	const (
		admin = int64(3)
		tier  = int64(7)
		old   = int64(8)
	)
	listed := func(id int64, adminGroups, market []int64) *Account {
		return &Account{ID: id, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true,
			GroupIDs: adminGroups, MarketGroupIDs: market}
	}
	standard := &config.Config{RunMode: config.RunModeStandard}
	run := func(t *testing.T, cfg *config.Config, repoAccounts map[int64]*Account, cached map[int64]*Account, event SchedulerOutboxEvent) []int64 {
		t.Helper()
		if cached == nil {
			cached = map[int64]*Account{}
		}
		cache := &bucketRecordingCache{accounts: cached}
		svc := NewSchedulerSnapshotService(cache, nil, &snapshotEventAccountRepo{accounts: repoAccounts}, nil, cfg)
		require.NoError(t, svc.handleOutboxEvent(context.Background(), event))
		return cache.rebuiltGroups(PlatformAnthropic)
	}
	accountID := int64(11)
	changed := func(payload map[string]any) SchedulerOutboxEvent {
		return SchedulerOutboxEvent{EventType: SchedulerOutboxEventAccountChanged, AccountID: &accountID, Payload: payload}
	}

	t.Run("admin and market groups", func(t *testing.T) {
		got := run(t, standard, map[int64]*Account{accountID: listed(accountID, []int64{admin}, []int64{tier})}, nil, changed(nil))
		require.Equal(t, []int64{admin, tier}, got)
	})
	t.Run("market only keeps the ungrouped bucket", func(t *testing.T) {
		got := run(t, standard, map[int64]*Account{accountID: listed(accountID, nil, []int64{tier})}, nil, changed(nil))
		require.Equal(t, []int64{0, tier}, got)
	})
	t.Run("tier of the previous cached copy", func(t *testing.T) {
		got := run(t, standard, map[int64]*Account{accountID: listed(accountID, []int64{admin}, []int64{tier})},
			map[int64]*Account{accountID: listed(accountID, []int64{admin}, []int64{old})}, changed(nil))
		require.Equal(t, []int64{admin, tier, old}, got)
	})
	t.Run("explicit admin group payload still adds the tiers", func(t *testing.T) {
		got := run(t, standard, map[int64]*Account{accountID: listed(accountID, []int64{admin}, []int64{tier})}, nil,
			changed(map[string]any{"group_ids": []any{float64(admin)}}))
		require.Equal(t, []int64{admin, tier}, got)
	})
	t.Run("account groups changed event", func(t *testing.T) {
		got := run(t, standard, map[int64]*Account{accountID: listed(accountID, []int64{admin}, []int64{tier})}, nil,
			SchedulerOutboxEvent{EventType: SchedulerOutboxEventAccountGroupsChanged, AccountID: &accountID})
		require.Equal(t, []int64{admin, tier}, got)
	})
	t.Run("deleted account rebuilds the previous copy's buckets", func(t *testing.T) {
		got := run(t, standard, map[int64]*Account{},
			map[int64]*Account{accountID: listed(accountID, []int64{admin}, []int64{tier})}, changed(nil))
		require.Equal(t, []int64{0, admin, tier}, got)
	})
	t.Run("deleted account without a cached copy", func(t *testing.T) {
		got := run(t, standard, map[int64]*Account{}, nil, changed(nil))
		require.Equal(t, []int64{0}, got)
	})
	t.Run("bulk event", func(t *testing.T) {
		got := run(t, standard, map[int64]*Account{
			1: listed(1, []int64{admin}, []int64{tier}),
			2: listed(2, nil, nil),
		}, map[int64]*Account{3: listed(3, nil, []int64{old})},
			SchedulerOutboxEvent{EventType: SchedulerOutboxEventAccountBulkChanged,
				Payload: map[string]any{"account_ids": []any{float64(1), float64(2), float64(3)}}})
		require.Equal(t, []int64{0, admin, tier, old}, got)
	})
	t.Run("simple mode keeps the global bucket", func(t *testing.T) {
		got := run(t, &config.Config{RunMode: config.RunModeSimple},
			map[int64]*Account{accountID: listed(accountID, []int64{admin}, []int64{tier})}, nil, changed(nil))
		require.Equal(t, []int64{0}, got)
	})
}

// A second account event on a bucket still under the rebuild throttle (or
// with a rebuild in flight) is not dropped: the bucket is rebuilt once the
// lock is free (e.g. an account disabled and re-enabled within 30 s).
func TestSchedulerThrottledBucketGetsTrailingRebuild(t *testing.T) {
	ctx := context.Background()
	accountID := int64(11)
	acc := &Account{ID: accountID, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, GroupIDs: []int64{3}, MarketGroupIDs: []int64{7}}
	cache := &bucketRecordingCache{accounts: map[int64]*Account{}}
	svc := NewSchedulerSnapshotService(cache, nil, &snapshotEventAccountRepo{accounts: map[int64]*Account{accountID: acc}}, nil,
		&config.Config{RunMode: config.RunModeStandard})
	event := SchedulerOutboxEvent{EventType: SchedulerOutboxEventAccountChanged, AccountID: &accountID}
	tier := SchedulerBucket{GroupID: 7, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	count := func() int {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		n := 0
		for _, b := range cache.rebuilt {
			if b == tier {
				n++
			}
		}
		return n
	}

	require.NoError(t, svc.handleOutboxEvent(ctx, event))
	require.Equal(t, 1, count())
	require.NoError(t, svc.handleOutboxEvent(ctx, event), "a throttled event does not stall the outbox")
	require.Equal(t, 1, count())

	svc.rebuildPendingBuckets() // still locked: stays pending
	require.Equal(t, 1, count())
	// The trailing rebuild fails to publish: the bucket stays pending and
	// is retried without any new outbox event.
	cache.expire()
	cache.mu.Lock()
	cache.failOnce = &tier
	cache.mu.Unlock()
	svc.rebuildPendingBuckets()
	require.Equal(t, 1, count())
	cache.expire()
	svc.rebuildPendingBuckets()
	require.Equal(t, 2, count(), "trailing rebuild once the lock is free")
	cache.expire()
	svc.rebuildPendingBuckets()
	require.Equal(t, 2, count(), "nothing pending after the trailing rebuild")
}
