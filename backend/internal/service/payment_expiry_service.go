package service

import (
	"context"
	"database/sql"
	"log"
	"sync"
	"time"
)

const paymentExpiryAdvisoryLockID int64 = 731_902_608_085

// PaymentExpiryService reconciles and expires timed-out orders. A PostgreSQL
// advisory lock ensures only one application instance performs each pass.
type PaymentExpiryService struct {
	db             *sql.DB
	paymentService *PaymentService
	interval       time.Duration
	stopCh         chan struct{}
	stopOnce       sync.Once
	wg             sync.WaitGroup
}

func NewPaymentExpiryService(db *sql.DB, paymentService *PaymentService, interval time.Duration) *PaymentExpiryService {
	return &PaymentExpiryService{
		db: db, paymentService: paymentService, interval: interval, stopCh: make(chan struct{}),
	}
}

func (s *PaymentExpiryService) Start() {
	if s == nil || s.db == nil || s.paymentService == nil || s.interval <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		s.runOnce()
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.stopCh:
				return
			}
		}
	}()
}

func (s *PaymentExpiryService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
}

func (s *PaymentExpiryService) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		log.Printf("[PaymentExpiry] get database connection failed: %s", safePaymentFailureReason(err))
		return
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			log.Printf("[PaymentExpiry] close database connection failed: %s", safePaymentFailureReason(closeErr))
		}
	}()
	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", paymentExpiryAdvisoryLockID).Scan(&locked); err != nil {
		log.Printf("[PaymentExpiry] acquire advisory lock failed: %s", safePaymentFailureReason(err))
		return
	}
	if !locked {
		return
	}
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer unlockCancel()
		var unlocked bool
		if unlockErr := conn.QueryRowContext(unlockCtx, "SELECT pg_advisory_unlock($1)", paymentExpiryAdvisoryLockID).Scan(&unlocked); unlockErr != nil {
			log.Printf("[PaymentExpiry] release advisory lock failed: %s", safePaymentFailureReason(unlockErr))
		}
	}()
	recovered, err := s.paymentService.RecoverIncompleteFulfillments(ctx)
	if err != nil {
		log.Printf("[PaymentExpiry] recover incomplete fulfillment failed: %s", safePaymentFailureReason(err))
	} else if recovered > 0 {
		log.Printf("[PaymentExpiry] recovered %d incomplete payment fulfillments", recovered)
	}
	refunds, err := s.paymentService.RecoverIncompleteRefunds(ctx)
	if err != nil {
		log.Printf("[PaymentExpiry] recover incomplete refunds failed: %s", safePaymentFailureReason(err))
	} else if refunds > 0 {
		log.Printf("[PaymentExpiry] reconciled %d incomplete refunds", refunds)
	}
	expired, err := s.paymentService.ExpireTimedOutOrders(ctx)
	if err != nil {
		log.Printf("[PaymentExpiry] reconcile timed-out orders failed: %s", safePaymentFailureReason(err))
		return
	}
	if expired > 0 {
		log.Printf("[PaymentExpiry] expired %d payment orders", expired)
	}
}
