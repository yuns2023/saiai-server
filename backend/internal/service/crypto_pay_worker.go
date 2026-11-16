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
	// The parent context only ends on Stop; each phase has its own budget
	// (RunWorkerPass) so a slow chain cannot block settlement.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-w.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	lockCtx, lockCancel := context.WithTimeout(ctx, 10*time.Second)
	defer lockCancel()
	conn, err := w.db.Conn(lockCtx)
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
	if err := conn.QueryRowContext(lockCtx, "SELECT pg_try_advisory_lock($1)", cryptoPayWorkerAdvisoryLockID).Scan(&locked); err != nil {
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

// RunWorkerPass is one worker iteration with the regular phase budget
// (2 x scan interval); the worker and tests use the same code path.
func (s *CryptoPayService) RunWorkerPass(ctx context.Context) {
	s.runWorkerPass(ctx, s.phaseBudget)
}

// runWorkerPass settles the finalized backlog first, then scans; every phase
// gets its own budget (0 = parent ctx only).
func (s *CryptoPayService) runWorkerPass(ctx context.Context, budget time.Duration) {
	phase := func() (context.Context, context.CancelFunc) {
		if budget <= 0 {
			return context.WithCancel(ctx)
		}
		return context.WithTimeout(ctx, budget)
	}
	backlogCtx, cancel := phase()
	if credited, err := s.CreditFinalizedBacklog(backlogCtx); err != nil {
		cryptoPayLogf("worker: credit backlog failed: %v", err)
	} else if credited > 0 {
		cryptoPayLogf("worker: credited %d finalized deposits from backlog", credited)
	}
	cancel()
	settingsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	st, err := s.GetSettings(settingsCtx)
	cancel()
	if err != nil {
		cryptoPayLogf("worker: settings unavailable: %v", err)
		return
	}
	if st.ScanningEnabled && (s.evmScanner() != nil || s.solanaScanner() != nil) {
		if err := s.scanNetworks(ctx, budget); err != nil {
			cryptoPayLogf("worker: scan errors: %v", err)
		}
	}
}
