package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Shared by the Gateway and reset control service through RateLimitService.
// Account-local locks keep a slow account from blocking unrelated accounts.
type codexUsageWriteCoordinator struct {
	accounts sync.Map
}

var errCodexUsageSuperseded = errors.New("Codex usage observation predates reset")

type codexUsageWriteState struct {
	lock    chan struct{}
	resetAt atomic.Pointer[time.Time] // also read without blocking incoming observations
}

func (s *RateLimitService) lockCodexUsageWrite(ctx context.Context, id int64) (*codexUsageWriteState, func(), error) {
	if s == nil {
		return nil, func() {}, nil
	}
	value, _ := s.codexUsageWrites.accounts.LoadOrStore(id, &codexUsageWriteState{lock: make(chan struct{}, 1)})
	state := value.(*codexUsageWriteState)
	select {
	case state.lock <- struct{}{}:
		return state, func() { <-state.lock }, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func (s *RateLimitService) codexUsageWrite(ctx context.Context, id int64, observedAt time.Time, write func() error) error {
	state, release, err := s.lockCodexUsageWrite(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	if state != nil {
		if boundary := state.resetAt.Load(); boundary != nil && !observedAt.After(*boundary) {
			return errCodexUsageSuperseded
		}
	}
	return write()
}

// A reset must also invalidate pre-reset throttle decisions, otherwise an
// identical but genuinely new exhausted observation could be suppressed.
func (s *RateLimitService) resetCodexUsageThrottle(id int64, throttle *accountWriteThrottle) {
	if s == nil || throttle == nil {
		return
	}
	value, ok := s.codexUsageWrites.accounts.Load(id)
	if !ok {
		return
	}
	boundary := value.(*codexUsageWriteState).resetAt.Load()
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
