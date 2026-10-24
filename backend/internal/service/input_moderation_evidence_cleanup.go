package service

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"
)

const (
	moderationEvidenceCleanupLockID   int64 = 731_902_608_113
	moderationEvidenceCleanupBatch          = 500
	moderationEvidenceCleanupMaxBatch       = 200
	moderationEvidenceAccessRetention       = 730 * 24 * time.Hour
)

// ModerationEvidenceCleanupService deletes expired, unheld evidence and old
// access rows every hour. It runs whether or not ops features are enabled,
// and a PostgreSQL advisory lock keeps passes on a single instance.
type ModerationEvidenceCleanupService struct {
	db       *sql.DB
	repo     InputModerationEvidenceRepository
	interval time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewModerationEvidenceCleanupService(db *sql.DB, repo InputModerationEvidenceRepository, interval time.Duration) *ModerationEvidenceCleanupService {
	return &ModerationEvidenceCleanupService{db: db, repo: repo, interval: interval, stopCh: make(chan struct{})}
}

func (s *ModerationEvidenceCleanupService) Start() {
	if s == nil || s.db == nil || s.repo == nil || s.interval <= 0 {
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

func (s *ModerationEvidenceCleanupService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
}

func (s *ModerationEvidenceCleanupService) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// The lock and every cleanup statement share one connection, so a pass
	// never waits for a second pooled connection (max_open_conns may be 1).
	conn, err := s.db.Conn(ctx)
	if err != nil {
		slog.Warn("input_moderation_evidence_cleanup_conn_failed", "error", err)
		return
	}
	defer func() { _ = conn.Close() }()
	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", moderationEvidenceCleanupLockID).Scan(&locked); err != nil || !locked {
		return
	}
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer unlockCancel()
		_, _ = conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", moderationEvidenceCleanupLockID)
	}()
	evidence, err := s.purge(ctx, func() (int64, error) {
		return s.repo.PurgeExpiredInputModerationEvidence(ctx, conn, moderationEvidenceCleanupBatch)
	})
	if err != nil {
		slog.Warn("input_moderation_evidence_cleanup_failed", "error", err)
	}
	before := time.Now().UTC().Add(-moderationEvidenceAccessRetention)
	access, err := s.purge(ctx, func() (int64, error) {
		return s.repo.PurgeInputModerationEvidenceAccess(ctx, conn, before, moderationEvidenceCleanupBatch)
	})
	if err != nil {
		slog.Warn("input_moderation_evidence_access_cleanup_failed", "error", err)
	}
	if evidence > 0 || access > 0 {
		slog.Info("input_moderation_evidence_cleanup", "evidence_deleted", evidence, "access_deleted", access)
	}
}

func (s *ModerationEvidenceCleanupService) purge(ctx context.Context, batch func() (int64, error)) (int64, error) {
	var total int64
	for i := 0; i < moderationEvidenceCleanupMaxBatch; i++ {
		deleted, err := batch()
		total += deleted
		if err != nil || deleted < moderationEvidenceCleanupBatch || ctx.Err() != nil {
			return total, err
		}
	}
	return total, nil
}
