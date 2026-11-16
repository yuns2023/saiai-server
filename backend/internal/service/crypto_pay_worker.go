package service

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

const cryptoPayWorkerAdvisoryLockID int64 = 731_902_608_127

// CryptoPayWorker runs chain scans and the credit backlog sweep. A PostgreSQL
// advisory lock ensures only one application instance works at a time. It
// never starts when the deployment has no configured network, and it only
// scans when scanning_enabled is on and a scanner is injected.
type CryptoPayWorker struct {
	db       *sql.DB
	svc      *CryptoPayService
	interval time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewCryptoPayWorker(db *sql.DB, svc *CryptoPayService, interval time.Duration) *CryptoPayWorker {
	return &CryptoPayWorker{db: db, svc: svc, interval: interval, stopCh: make(chan struct{})}
}

func (w *CryptoPayWorker) Start() {
	if w == nil || w.db == nil || w.svc == nil || !w.svc.Enabled() || w.interval <= 0 {
		return
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		w.runOnce()
		for {
			select {
			case <-ticker.C:
				w.runOnce()
			case <-w.stopCh:
				return
			}
		}
	}()
}

func (w *CryptoPayWorker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stopCh) })
	w.wg.Wait()
}

func (w *CryptoPayWorker) runOnce() {
	timeout := 4 * w.interval
	if timeout < time.Minute {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	go func() {
		select {
		case <-w.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	conn, err := w.db.Conn(ctx)
	if err != nil {
		cryptoPayLogf("worker: get database connection failed: %v", err)
		return
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			cryptoPayLogf("worker: close database connection failed: %v", closeErr)
		}
	}()
	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", cryptoPayWorkerAdvisoryLockID).Scan(&locked); err != nil {
		cryptoPayLogf("worker: acquire advisory lock failed: %v", err)
		return
	}
	if !locked {
		return
	}
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer unlockCancel()
		var unlocked bool
		if unlockErr := conn.QueryRowContext(unlockCtx, "SELECT pg_advisory_unlock($1)", cryptoPayWorkerAdvisoryLockID).Scan(&unlocked); unlockErr != nil {
			cryptoPayLogf("worker: release advisory lock failed: %v", unlockErr)
		}
	}()
	w.svc.RunWorkerPass(ctx)
}

// RunWorkerPass is one worker iteration (exported for tests and manual runs).
func (s *CryptoPayService) RunWorkerPass(ctx context.Context) {
	st, err := s.GetSettings(ctx)
	if err != nil {
		cryptoPayLogf("worker: settings unavailable: %v", err)
		return
	}
	if st.ScanningEnabled && (s.evmScanner() != nil || s.solanaScanner() != nil) {
		if err := s.ScanOnce(ctx); err != nil {
			cryptoPayLogf("worker: scan errors: %v", err)
		}
	}
	if credited, err := s.CreditFinalizedBacklog(ctx); err != nil {
		cryptoPayLogf("worker: credit backlog failed: %v", err)
	} else if credited > 0 {
		cryptoPayLogf("worker: credited %d finalized deposits from backlog", credited)
	}
}
