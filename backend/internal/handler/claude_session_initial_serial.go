package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var errSessionInitialSerialStale = errors.New("session initial serial account changed")

type sessionInitialSerialStaleError struct {
	accountID int64
}

func (e *sessionInitialSerialStaleError) Error() string { return errSessionInitialSerialStale.Error() }
func (e *sessionInitialSerialStaleError) Unwrap() error { return errSessionInitialSerialStale }

type sessionInitialSerialClientWriteError struct{ err error }

func (e *sessionInitialSerialClientWriteError) Error() string { return e.err.Error() }
func (e *sessionInitialSerialClientWriteError) Unwrap() error { return e.err }

type sessionInitialSerialPermit struct {
	forwarded atomic.Bool
	release   func()
	stop      atomic.Pointer[func() bool]
	once      sync.Once
}

func (p *sessionInitialSerialPermit) MarkForwarded() {
	if p != nil {
		p.forwarded.Store(true)
	}
}

func (p *sessionInitialSerialPermit) Release() {
	if p != nil {
		p.once.Do(func() {
			if stop := p.stop.Load(); stop != nil {
				(*stop)()
			}
			p.release()
		})
	}
}

// acquireSessionInitialSerial leaves the user slot in place to bound waiting
// requests, but releases any selected account slot as soon as it has to wait.
// A fourth request waits for the third response start before bypassing the gate.
func acquireSessionInitialSerial(
	c *gin.Context, svc service.SessionInitialSerialCache, groupID int64, session string, accountID int64,
	deadline time.Time, isStream bool, streamStarted *bool, pingInterval time.Duration,
	onWait func(), reqLog *zap.Logger,
) (*sessionInitialSerialPermit, error) {
	ctx, cancel := context.WithDeadline(c.Request.Context(), deadline)
	defer cancel()
	owner := uuid.NewString()
	generation := ""
	leaseTTL := 2 * time.Minute
	if remaining := time.Until(deadline) + time.Minute; remaining > leaseTTL {
		leaseTTL = remaining
	}
	if pingInterval <= 0 {
		pingInterval = defaultPingInterval
	}
	var pingCh <-chan time.Time
	var pingTicker *time.Ticker
	defer func() {
		if pingTicker != nil {
			pingTicker.Stop()
		}
	}()
	waiting := false
	backoff := initialBackoff
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := svc.TryAcquireSessionInitialSerial(ctx, groupID, session, accountID, generation, owner, leaseTTL)
		if err != nil {
			return nil, err
		}
		generation = result.Generation
		switch result.Decision {
		case service.SessionInitialSerialBypass:
			return nil, nil
		case service.SessionInitialSerialStale:
			return nil, &sessionInitialSerialStaleError{accountID: result.AccountID}
		case service.SessionInitialSerialAcquired:
			permit := &sessionInitialSerialPermit{}
			permit.release = func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cleanupCancel()
				if err := svc.ReleaseSessionInitialSerial(cleanupCtx, groupID, session, accountID, generation, owner, permit.forwarded.Load()); err != nil {
					reqLog.Warn("gateway.session_initial_serial_release_failed", zap.Int64("account_id", accountID), zap.Error(err))
				}
			}
			// Register only after the release closure is initialized. The stop
			// callback is atomic because an already-canceled request can invoke
			// Release before AfterFunc returns.
			stop := context.AfterFunc(c.Request.Context(), permit.Release)
			permit.stop.Store(&stop)
			reqLog.Debug("gateway.session_initial_serial_acquired", zap.Int64("account_id", accountID), zap.Int("ordinal", result.Ordinal))
			return permit, nil
		case service.SessionInitialSerialWait:
			if !waiting {
				waiting = true
				onWait()
				if isStream {
					pingTicker = time.NewTicker(pingInterval)
					pingCh = pingTicker.C
				}
			}
		default:
			return nil, fmt.Errorf("invalid session initial serial decision: %d", result.Decision)
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
			backoff = nextBackoff(backoff)
		case <-pingCh:
			timer.Stop()
			if !*streamStarted {
				c.Header("Content-Type", "text/event-stream")
				c.Header("Cache-Control", "no-cache")
				c.Header("Connection", "keep-alive")
				c.Header("X-Accel-Buffering", "no")
				*streamStarted = true
			}
			if _, err := fmt.Fprint(c.Writer, string(SSEPingFormatClaude)); err != nil {
				return nil, &sessionInitialSerialClientWriteError{err: err}
			}
			if flusher, ok := c.Writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}
}

func (h *GatewayHandler) useSessionInitialSerial(isClaudeCode bool, session string, account *service.Account, parsed *service.ParsedRequest) bool {
	if h.cfg == nil || !h.cfg.Gateway.ClaudeSessionInitialSerial.Enabled || !isClaudeCode || session == "" ||
		account == nil || !account.IsAnthropicOAuthOrSetupToken() || parsed == nil {
		return false
	}
	identity := service.ParseMetadataUserID(parsed.MetadataUserID)
	return identity != nil && identity.SessionID != ""
}

func (h *GatewayHandler) releaseSelectedSlotForSessionWait(selection *service.AccountSelectionResult) {
	if selection.Acquired && selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
		selection.Acquired = false
		selection.ReleaseFunc = nil
	}
	if selection.WaitPlan == nil {
		timeout := h.cfg.Gateway.Scheduling.FallbackWaitTimeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		maxWaiting := h.cfg.Gateway.Scheduling.FallbackMaxWaiting
		if maxWaiting <= 0 {
			maxWaiting = 100
		}
		selection.WaitPlan = &service.AccountWaitPlan{
			AccountID: selection.Account.ID, MaxConcurrency: selection.Account.Concurrency,
			Timeout: timeout, MaxWaiting: maxWaiting,
		}
	}
}
