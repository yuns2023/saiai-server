package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const openAIUnpricedModelLocalMaxEntries = 1024

// unpricedModelCounterCache is deliberately separate from GatewayCache so
// existing cache implementations and tests remain source-compatible. The
// Redis gateway cache implements it to make the protective threshold shared
// across gateway replicas.
type unpricedModelCounterCache interface {
	GetUnpricedModelSuccessCount(ctx context.Context, model string) (int64, time.Duration, error)
	IncrementUnpricedModelSuccessCount(ctx context.Context, model string, window time.Duration) (int64, time.Duration, error)
}

type unpricedModelWindow struct {
	count     int64
	expiresAt time.Time
}

type openAIUnpricedModelGuard struct {
	maxSuccesses int64
	window       time.Duration
	cache        unpricedModelCounterCache

	mu    sync.Mutex
	local map[string]unpricedModelWindow
	now   func() time.Time
}

func newOpenAIUnpricedModelGuard(cfg *config.Config, cache GatewayCache) *openAIUnpricedModelGuard {
	guard := &openAIUnpricedModelGuard{
		local: make(map[string]unpricedModelWindow),
		now:   time.Now,
	}
	if cfg != nil {
		guard.maxSuccesses = int64(cfg.Gateway.OpenAIUnpricedModelMaxSuccesses)
		guard.window = time.Duration(cfg.Gateway.OpenAIUnpricedModelWindowSeconds) * time.Second
	}
	if guard.maxSuccesses > 0 && guard.window <= 0 {
		guard.window = time.Hour
	}
	if distributed, ok := cache.(unpricedModelCounterCache); ok {
		guard.cache = distributed
	}
	return guard
}

func (s *OpenAIGatewayService) getUnpricedModelGuard() *openAIUnpricedModelGuard {
	if s == nil {
		return nil
	}
	s.unpricedModelGuardOnce.Do(func() {
		if s.unpricedModelGuard == nil {
			s.unpricedModelGuard = newOpenAIUnpricedModelGuard(s.cfg, s.cache)
		}
	})
	return s.unpricedModelGuard
}

func (s *OpenAIGatewayService) isUnpricedModelCircuitOpen(ctx context.Context, model string) bool {
	guard := s.getUnpricedModelGuard()
	return guard != nil && guard.isOpen(ctx, model)
}

func (s *OpenAIGatewayService) recordUnpricedModelSuccess(ctx context.Context, model string) {
	guard := s.getUnpricedModelGuard()
	if guard != nil {
		guard.recordSuccess(ctx, model)
	}
}

func (g *openAIUnpricedModelGuard) enabled() bool {
	return g != nil && g.maxSuccesses > 0 && g.window > 0
}

func (g *openAIUnpricedModelGuard) isOpen(ctx context.Context, model string) bool {
	model = normalizeUnpricedModelName(model)
	if !g.enabled() || model == "" {
		return false
	}
	localKey := unpricedModelCounterKey(model)

	if g.localCount(localKey) >= g.maxSuccesses {
		return true
	}
	if g.cache == nil {
		return false
	}
	count, _, err := g.cache.GetUnpricedModelSuccessCount(ctx, model)
	if err != nil {
		logger.L().Warn("failed to read distributed unpriced-model protection counter; using local counter",
			zap.String("model", unpricedModelLogValue(model)),
			zap.Error(err),
		)
		return false
	}
	return count >= g.maxSuccesses
}

func (g *openAIUnpricedModelGuard) recordSuccess(ctx context.Context, model string) {
	model = normalizeUnpricedModelName(model)
	if !g.enabled() || model == "" {
		return
	}
	localKey := unpricedModelCounterKey(model)

	localCount := g.incrementLocal(localKey)
	distributedCount := int64(0)
	if g.cache != nil {
		count, _, err := g.cache.IncrementUnpricedModelSuccessCount(ctx, model, g.window)
		if err != nil {
			logger.L().Warn("failed to increment distributed unpriced-model protection counter; using local counter",
				zap.String("model", unpricedModelLogValue(model)),
				zap.Int64("local_count", localCount),
				zap.Error(err),
			)
		} else {
			distributedCount = count
		}
	}

	logger.L().Warn("openai model is missing pricing",
		zap.String("model", unpricedModelLogValue(model)),
		zap.Int64("local_success_count", localCount),
		zap.Int64("distributed_success_count", distributedCount),
		zap.Int64("max_successes", g.maxSuccesses),
		zap.Duration("window", g.window),
	)
}

func (g *openAIUnpricedModelGuard) localCount(model string) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	entry, ok := g.local[model]
	if !ok || !entry.expiresAt.After(now) {
		delete(g.local, model)
		return 0
	}
	return entry.count
}

func (g *openAIUnpricedModelGuard) incrementLocal(model string) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	entry, ok := g.local[model]
	if !ok || !entry.expiresAt.After(now) {
		if !ok && len(g.local) >= openAIUnpricedModelLocalMaxEntries {
			oldestKey := ""
			var oldestExpiry time.Time
			for key, candidate := range g.local {
				if !candidate.expiresAt.After(now) {
					delete(g.local, key)
					continue
				}
				if oldestKey == "" || candidate.expiresAt.Before(oldestExpiry) {
					oldestKey = key
					oldestExpiry = candidate.expiresAt
				}
			}
			if len(g.local) >= openAIUnpricedModelLocalMaxEntries && oldestKey != "" {
				delete(g.local, oldestKey)
			}
		}
		entry = unpricedModelWindow{expiresAt: now.Add(g.window)}
	}
	entry.count++
	g.local[model] = entry
	return entry.count
}

func normalizeUnpricedModelName(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func unpricedModelCounterKey(model string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(model)))
}

func unpricedModelLogValue(model string) string {
	const maxRunes = 256
	if len(model) <= maxRunes {
		return model
	}
	runeCount := 0
	for byteIndex := range model {
		if runeCount == maxRunes {
			return model[:byteIndex] + "…"
		}
		runeCount++
	}
	return model
}

func unpricedModelCircuitMessage(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "requested model"
	}
	model = unpricedModelLogValue(model)
	return fmt.Sprintf("Model %q is temporarily unavailable because pricing metadata has not been configured; contact your administrator.", model)
}
