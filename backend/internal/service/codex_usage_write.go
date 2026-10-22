package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Shared by the Gateway and reset control service through RateLimitService.
// Account-local locks keep a slow account from blocking unrelated accounts.
//
// A state without a reset boundary carries only its lock, so it is removed
// once nobody holds or waits for it. A state with a boundary is retained: it
// must keep rejecting stale observations, some of which (cached account
// snapshots) can be arbitrarily old. Boundaries exist only for accounts whose
// limits were reset in this process, which keeps the retained set small.
type codexUsageWriteCoordinator struct {
	mu       sync.Mutex
	accounts map[int64]*codexUsageWriteState
	pending  sync.WaitGroup // background workers, so tests can await completion

	// Background writes are coalesced per account: one worker per account and
	// one pending write per kind, holding the latest observation.
	queueMu sync.Mutex
	queues  map[int64]*codexUsageWriteQueue
}

type codexUsageWriteQueue struct {
	pending map[string]codexUsageQueuedWrite
	latest  map[string]time.Time // newest accepted observation per kind, pending or in flight
}

type codexUsageQueuedWrite struct {
	observedAt time.Time
	write      func(context.Context) error
	onError    func(error)
}

// Kinds of coalesced background writes. A newer observation of one kind
// replaces an older pending one, because it writes the same fields.
const (
	codexUsageWrite429         = "openai_429"
	codexUsageWrite429Limit    = "openai_429_rate_limit"
	codexUsageWriteSnapLimit   = "snapshot_rate_limit"
	codexUsageWriteExtraLimit  = "stored_snapshot_rate_limit"
	codexUsageWriteProbe       = "usage_probe"
	codexUsageWriteAccountTest = "account_test"
	codexUsageWriteTestLimit   = "account_test_rate_limit"
)

var errCodexUsageSuperseded = errors.New("codex usage observation predates reset")

type codexUsageWriteState struct {
	lock    chan struct{}
	refs    int                       // holders and waiters; guarded by coordinator.mu
	resetAt atomic.Pointer[time.Time] // also read without blocking incoming observations
}

const (
	// codexUsageLockWait outlasts a reset reconciliation's 25s operation
	// budget, so a newer observation waits for the reset instead of being
	// dropped.
	codexUsageLockWait = 30 * time.Second
	// codexUsageWriteBudget is the database budget of one fenced write; waiting
	// for the lock does not consume it.
	codexUsageWriteBudget = 5 * time.Second
)

func (c *codexUsageWriteCoordinator) acquireRef(id int64) *codexUsageWriteState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accounts == nil {
		c.accounts = make(map[int64]*codexUsageWriteState)
	}
	state := c.accounts[id]
	if state == nil {
		state = &codexUsageWriteState{lock: make(chan struct{}, 1)}
		c.accounts[id] = state
	}
	state.refs++
	return state
}

func (c *codexUsageWriteCoordinator) releaseRef(id int64, state *codexUsageWriteState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state.refs--
	if state.refs == 0 && state.resetAt.Load() == nil && c.accounts[id] == state {
		delete(c.accounts, id)
	}
}

func (c *codexUsageWriteCoordinator) boundary(id int64) *time.Time {
	c.mu.Lock()
	state := c.accounts[id]
	c.mu.Unlock()
	if state == nil {
		return nil
	}
	return state.resetAt.Load()
}

func (s *RateLimitService) lockCodexUsageWrite(ctx context.Context, id int64) (*codexUsageWriteState, func(), error) {
	if s == nil {
		return nil, func() {}, nil
	}
	state := s.codexUsageWrites.acquireRef(id)
	select {
	case state.lock <- struct{}{}:
		return state, s.codexUsageUnlock(id, state), nil
	case <-ctx.Done():
		s.codexUsageWrites.releaseRef(id, state)
		return nil, nil, ctx.Err()
	}
}

type codexObservedAtKey struct{}

// withCodexObservedAt records when an upstream response's headers arrived, so
// error handling that runs after reading the body uses the receipt time.
func withCodexObservedAt(ctx context.Context, observedAt time.Time) context.Context {
	return context.WithValue(ctx, codexObservedAtKey{}, observedAt)
}

func codexObservedAt(ctx context.Context) time.Time {
	if ctx != nil {
		if observedAt, ok := ctx.Value(codexObservedAtKey{}).(time.Time); ok && !observedAt.IsZero() {
			return observedAt
		}
	}
	return time.Now()
}

// tryLockCodexUsageWrite never waits; ok is false while another write or a
// reset reconciliation holds the account.
func (s *RateLimitService) tryLockCodexUsageWrite(id int64) (state *codexUsageWriteState, release func(), ok bool) {
	if s == nil {
		return nil, func() {}, true
	}
	state = s.codexUsageWrites.acquireRef(id)
	select {
	case state.lock <- struct{}{}:
		return state, s.codexUsageUnlock(id, state), true
	default:
		s.codexUsageWrites.releaseRef(id, state)
		return nil, nil, false
	}
}

func (s *RateLimitService) codexUsageUnlock(id int64, state *codexUsageWriteState) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			<-state.lock
			s.codexUsageWrites.releaseRef(id, state)
		})
	}
}

func codexUsageSuperseded(state *codexUsageWriteState, observedAt time.Time) bool {
	if state == nil {
		return false
	}
	boundary := state.resetAt.Load()
	return boundary != nil && !observedAt.After(*boundary)
}

// codexUsageObservationSuperseded is a lock-free check for callers that apply
// an observation in memory before persisting it through the fence.
func (s *RateLimitService) codexUsageObservationSuperseded(id int64, observedAt time.Time) bool {
	if s == nil {
		return false
	}
	boundary := s.codexUsageWrites.boundary(id)
	return boundary != nil && !observedAt.After(*boundary)
}

// codexUsageWrite runs write while holding the account's fence. write must not
// call back into the fence: the lock is not reentrant.
func (s *RateLimitService) codexUsageWrite(ctx context.Context, id int64, observedAt time.Time, write func() error) error {
	state, release, err := s.lockCodexUsageWrite(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	if codexUsageSuperseded(state, observedAt) {
		return errCodexUsageSuperseded
	}
	return write()
}

// writeCodexUsageDetached is for background workers. It waits up to
// codexUsageLockWait for the fence and then gives write its own budget.
// observedAt is when the observation was received, not when it is written.
func (s *RateLimitService) writeCodexUsageDetached(accountID int64, observedAt time.Time, write func(context.Context) error) error {
	waitCtx, cancelWait := context.WithTimeout(context.Background(), codexUsageLockWait)
	defer cancelWait()
	return s.codexUsageWrite(waitCtx, accountID, observedAt, func() error {
		writeCtx, cancel := context.WithTimeout(context.Background(), codexUsageWriteBudget)
		defer cancel()
		return write(writeCtx)
	})
}

// writeCodexUsageInBackground queues writeCodexUsageDetached. Each account has
// at most one background worker and one pending write per kind; an older
// observation than the pending one, or one predating a reset, is dropped.
// A superseded observation is not an error.
func (s *RateLimitService) writeCodexUsageInBackground(accountID int64, kind string, observedAt time.Time, write func(context.Context) error, onError func(error)) {
	if s == nil {
		go func() {
			if err := s.writeCodexUsageDetached(accountID, observedAt, write); err != nil && !errors.Is(err, errCodexUsageSuperseded) && onError != nil {
				onError(err)
			}
		}()
		return
	}
	if s.codexUsageObservationSuperseded(accountID, observedAt) {
		return
	}
	c := &s.codexUsageWrites
	c.queueMu.Lock()
	defer c.queueMu.Unlock()
	s.enqueueCodexUsageWriteLocked(accountID, kind, observedAt, write, onError)
}

// enqueueCodexUsageWriteLocked requires codexUsageWrites.queueMu.
func (s *RateLimitService) enqueueCodexUsageWriteLocked(accountID int64, kind string, observedAt time.Time, write func(context.Context) error, onError func(error)) {
	c := &s.codexUsageWrites
	if c.queues == nil {
		c.queues = make(map[int64]*codexUsageWriteQueue)
	}
	queue := c.queues[accountID]
	start := queue == nil
	if start {
		queue = &codexUsageWriteQueue{pending: make(map[string]codexUsageQueuedWrite), latest: make(map[string]time.Time)}
		c.queues[accountID] = queue
	}
	// Writes of one kind stay in observation order, so an older observation
	// never overwrites a newer one that is pending or being written.
	if latest, ok := queue.latest[kind]; ok && !observedAt.After(latest) {
		return
	}
	queue.latest[kind] = observedAt
	queue.pending[kind] = codexUsageQueuedWrite{observedAt: observedAt, write: write, onError: onError}
	if start {
		c.pending.Add(1)
		go s.runCodexUsageWriteQueue(accountID, queue)
	}
}

// runCodexUsageWriteQueue writes pending observations oldest first and exits
// when the account's queue is empty.
func (s *RateLimitService) runCodexUsageWriteQueue(accountID int64, queue *codexUsageWriteQueue) {
	c := &s.codexUsageWrites
	defer c.pending.Done()
	for {
		c.queueMu.Lock()
		kind := ""
		for k, item := range queue.pending {
			if kind == "" || item.observedAt.Before(queue.pending[kind].observedAt) {
				kind = k
			}
		}
		if kind == "" {
			delete(c.queues, accountID)
			c.queueMu.Unlock()
			return
		}
		item := queue.pending[kind]
		delete(queue.pending, kind)
		c.queueMu.Unlock()
		if err := s.writeCodexUsageDetached(accountID, item.observedAt, item.write); err != nil && !errors.Is(err, errCodexUsageSuperseded) && item.onError != nil {
			item.onError(err)
		}
	}
}

// writeCodexUsageFromRequest persists an observation from a request path
// without waiting: a reset reconciliation holds the fence across its upstream
// usage fetch. A free fence is written immediately with the write budget;
// otherwise the write is queued in the background and queued is true.
func (s *RateLimitService) writeCodexUsageFromRequest(ctx context.Context, accountID int64, kind string, observedAt time.Time, write func(context.Context) error) (queued bool, err error) {
	// While background writes are queued, keep later observations behind them
	// so an older queued observation never lands after a newer one.
	if s == nil {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexUsageWriteBudget)
		defer cancel()
		return false, write(writeCtx)
	}
	if s.codexUsageObservationSuperseded(accountID, observedAt) {
		return false, errCodexUsageSuperseded
	}
	// Checking for a queue and taking the fence happen under queueMu, so a
	// request never writes ahead of an older observation already queued.
	c := &s.codexUsageWrites
	c.queueMu.Lock()
	var (
		state   *codexUsageWriteState
		release func()
		ok      bool
	)
	if c.queues[accountID] == nil {
		state, release, ok = s.tryLockCodexUsageWrite(accountID)
	}
	if !ok {
		s.enqueueCodexUsageWriteLocked(accountID, kind, observedAt, write, func(err error) {
			slog.Warn("codex_usage_queued_write_failed", "account_id", accountID, "error", err)
		})
		c.queueMu.Unlock()
		return true, nil
	}
	c.queueMu.Unlock()
	defer release()
	if codexUsageSuperseded(state, observedAt) {
		return false, errCodexUsageSuperseded
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexUsageWriteBudget)
	defer cancel()
	return false, write(writeCtx)
}

// A reset must also invalidate pre-reset throttle decisions, otherwise an
// identical but genuinely new exhausted observation could be suppressed.
func (s *RateLimitService) resetCodexUsageThrottle(id int64, throttle *accountWriteThrottle) {
	if s == nil || throttle == nil {
		return
	}
	boundary := s.codexUsageWrites.boundary(id)
	if boundary == nil {
		return
	}
	throttle.mu.Lock()
	defer throttle.mu.Unlock()
	if last, ok := throttle.lastByID[id]; ok && !last.After(*boundary) {
		delete(throttle.lastByID, id)
		delete(throttle.lastCodexFiveHourByID, id)
	}
}
