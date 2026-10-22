package handler

import "sync"

// openAIWSTurnSlots tracks the user/account concurrency slots of one
// Responses WebSocket session. v2 passthrough may pipeline turns: BeforeTurn
// runs on the client reader goroutine and AfterTurn on the upstream reader,
// so the state is guarded and the slots are released only when the last
// outstanding turn has finished (including turns finished by the post-
// disconnect usage drain). Later turns of an idle session re-acquire them.
//
// Slots live in Redis for gateway.concurrency_slot_ttl_minutes from
// acquisition and are not renewed while held (the cache has no renewal
// primitive that a holder can call). A turn, including its drain bounded by
// gateway.openai_ws.read_timeout_seconds, that outlives that TTL loses its
// reservation early; that can over-admit concurrency but never affects
// billing.
//
// Turns are tracked by number, so a repeated BeforeTurn for the same turn
// (an account-failover replay) is not counted twice.
type openAIWSTurnSlots struct {
	mu       sync.Mutex
	user     func()
	account  func()
	inFlight map[int]struct{}
}

func (s *openAIWSTurnSlots) setUser(release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.user = release
}

func (s *openAIWSTurnSlots) setAccount(release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.account = release
}

func (s *openAIWSTurnSlots) releaseLocked() {
	if s.account != nil {
		s.account()
		s.account = nil
	}
	if s.user != nil {
		s.user()
		s.user = nil
	}
}

// release frees both slots immediately (session end, account failover).
func (s *openAIWSTurnSlots) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseLocked()
}

// beginTurn registers a turn, reusing the held slots or acquiring new ones.
func (s *openAIWSTurnSlots) beginTurn(turn int, acquire func() (user func(), account func(), err error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.user != nil && s.account != nil {
		s.markLocked(turn)
		return nil
	}
	// 防御式清理：避免异常路径下旧槽位覆盖导致泄漏。
	s.releaseLocked()
	user, account, err := acquire()
	if err != nil {
		return err
	}
	s.user, s.account = user, account
	s.markLocked(turn)
	return nil
}

func (s *openAIWSTurnSlots) markLocked(turn int) {
	if s.inFlight == nil {
		s.inFlight = make(map[int]struct{})
	}
	s.inFlight[turn] = struct{}{}
}

// endTurn finishes a turn and releases the slots when none is outstanding.
// Reports for unknown turns (for example a final error report) only release
// when nothing is outstanding.
func (s *openAIWSTurnSlots) endTurn(turn int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inFlight, turn)
	if len(s.inFlight) == 0 {
		s.releaseLocked()
	}
}
