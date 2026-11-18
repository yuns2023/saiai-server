package service

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// HostedApplicationExpiryWorker marks pending hosting applications older
// than 30 days expired and wipes their credentials. It runs whether or not
// the self-onboarding switch is on (pending rows hold upstream tokens).
// Expiry and approval compete through the same status = 'pending'
// conditional update; the expiry skips rows locked by an approval.
type HostedApplicationExpiryWorker struct {
	svc      *HostedSelfOnboardService
	interval time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewHostedApplicationExpiryWorker(svc *HostedSelfOnboardService, interval time.Duration) *HostedApplicationExpiryWorker {
	return &HostedApplicationExpiryWorker{svc: svc, interval: interval, stopCh: make(chan struct{})}
}

// ProvideHostedApplicationExpiryWorker creates and starts the hourly worker.
func ProvideHostedApplicationExpiryWorker(svc *HostedSelfOnboardService) *HostedApplicationExpiryWorker {
	w := NewHostedApplicationExpiryWorker(svc, time.Hour)
	w.Start()
	return w
}

func (w *HostedApplicationExpiryWorker) Start() {
	if w == nil || w.svc == nil || w.interval <= 0 {
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

func (w *HostedApplicationExpiryWorker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stopCh) })
	w.wg.Wait()
}

func (w *HostedApplicationExpiryWorker) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	n, err := w.svc.ExpireStale(ctx)
	log := logger.L().With(zap.String("component", "hosted.self_onboard"))
	if err != nil {
		log.Warn("hosted application expiry failed", zap.String("stage", "expire"), zap.Error(err))
		return
	}
	if n > 0 {
		log.Info("hosted applications expired", zap.Int64("count", n))
	}
}
