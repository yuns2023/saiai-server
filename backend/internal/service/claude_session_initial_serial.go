package service

import (
	"context"
	"time"
)

const ClaudeSessionInitialSerialRequests = 3

type SessionInitialSerialDecision int

const (
	SessionInitialSerialWait SessionInitialSerialDecision = iota
	SessionInitialSerialAcquired
	SessionInitialSerialBypass
	SessionInitialSerialStale
)

// SessionInitialSerialResult identifies both the account generation and the
// request holding its short startup lease. It is independent of sticky success.
type SessionInitialSerialResult struct {
	Decision   SessionInitialSerialDecision
	Generation string
	Ordinal    int
	AccountID  int64
}

// SessionInitialSerialCache is optional so old cache implementations and local
// mocks retain their existing forwarding behavior.
type SessionInitialSerialCache interface {
	TryAcquireSessionInitialSerial(ctx context.Context, groupID int64, session string, accountID int64, generation, owner string, lease time.Duration) (*SessionInitialSerialResult, error)
	ReleaseSessionInitialSerial(ctx context.Context, groupID int64, session string, accountID int64, generation, owner string, forwarded bool) error
}

func (s *GatewayService) TryAcquireSessionInitialSerial(ctx context.Context, groupID int64, session string, accountID int64, generation, owner string, lease time.Duration) (*SessionInitialSerialResult, error) {
	cache, ok := s.cache.(SessionInitialSerialCache)
	if !ok || session == "" || accountID <= 0 {
		return &SessionInitialSerialResult{Decision: SessionInitialSerialBypass}, nil
	}
	return cache.TryAcquireSessionInitialSerial(ctx, groupID, session, accountID, generation, owner, lease)
}

func (s *GatewayService) ReleaseSessionInitialSerial(ctx context.Context, groupID int64, session string, accountID int64, generation, owner string, forwarded bool) error {
	if cache, ok := s.cache.(SessionInitialSerialCache); ok {
		return cache.ReleaseSessionInitialSerial(ctx, groupID, session, accountID, generation, owner, forwarded)
	}
	return nil
}
