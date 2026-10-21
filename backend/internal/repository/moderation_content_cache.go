package repository

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"math"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Process-local, bounded cache. Neither prompts nor credentials are retained in
// entries. A random HMAC key prevents offline guessing of short input hashes.
// Restarting or moving to another replica is a cache miss, never an exemption.
type moderationContentCache struct {
	mu       sync.Mutex
	salt     [32]byte
	entries  map[[32]byte]moderationCachedScores
	flights  map[[32]byte]chan struct{}
	now      func() time.Time
	ttl      time.Duration
	capacity int
}
type moderationCachedScores struct {
	result        *service.InputModerationClassification
	expires, used time.Time
}

func newModerationContentCache() *moderationContentCache {
	c := &moderationContentCache{entries: make(map[[32]byte]moderationCachedScores), flights: make(map[[32]byte]chan struct{}), now: time.Now, ttl: 24 * time.Hour, capacity: 4096}
	if _, err := rand.Read(c.salt[:]); err != nil {
		panic("moderation cache entropy unavailable")
	}
	return c
}
func copyModerationScores(r *service.InputModerationClassification) *service.InputModerationClassification {
	copy := *r
	copy.Scores = make(map[string]float64, len(r.Scores))
	for k, v := range r.Scores {
		copy.Scores[k] = v
	}
	copy.Categories = append([]string(nil), r.Categories...)
	return &copy
}
func validCachedScores(r *service.InputModerationClassification, rules map[string]string) bool {
	if r == nil || r.ModelVersion != jevModerationModel || len(r.Scores) != len(rules) {
		return false
	}
	for key := range rules {
		score, ok := r.Scores[key]
		if !ok || math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
			return false
		}
	}
	return true
}
func (c *moderationContentCache) classify(ctx context.Context, settings config.GatewayInputModerationConfig, text string, policy *service.InputModerationPolicy, call func() (*service.InputModerationClassification, error)) (*service.InputModerationClassification, error) {
	scope, ok := service.InputModerationCacheScope(ctx)
	if !ok || settings.Engine != "jev" || settings.APIKey == "" {
		if err := service.ReserveInputModerationCall(ctx); err != nil {
			return nil, err
		}
		return call()
	}
	rules, err := service.JevRulesForPolicy(policy)
	if err != nil {
		return nil, err
	}
	version := service.JevLegacyRulesVersion
	if policy != nil {
		version = policy.RulesVersion
	}
	// Thresholds/penalties are deliberately absent: only raw scores are reused.
	payload, _ := json.Marshal(struct {
		Scope                                      service.ModerationCacheScope
		Endpoint, Credential, Model, Version, Text string
		Rules                                      map[string]string
	}{scope, settings.Endpoint, settings.APIKey, jevModerationModel, version, text, rules})
	mac := hmac.New(sha256.New, c.salt[:])
	_, _ = mac.Write(payload)
	var key [32]byte
	copy(key[:], mac.Sum(nil))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		now := c.now()
		if entry, found := c.entries[key]; found && now.Before(entry.expires) {
			entry.used = now
			c.entries[key] = entry
			result := copyModerationScores(entry.result)
			c.mu.Unlock()
			service.ObserveInputModerationCacheHit(ctx)
			return result, nil
		}
		delete(c.entries, key)
		if done, found := c.flights[key]; found {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				continue
			}
		}
		done := make(chan struct{})
		c.flights[key] = done
		c.mu.Unlock()
		var result *service.InputModerationClassification
		callErr := service.ReserveInputModerationCall(ctx)
		if callErr == nil {
			result, callErr = call()
		}
		c.mu.Lock()
		if callErr == nil && validCachedScores(result, rules) {
			now = c.now()
			for k, entry := range c.entries {
				if !now.Before(entry.expires) {
					delete(c.entries, k)
				}
			}
			if len(c.entries) >= c.capacity {
				var oldest [32]byte
				var oldestTime time.Time
				for k, entry := range c.entries {
					if oldestTime.IsZero() || entry.used.Before(oldestTime) {
						oldest = k
						oldestTime = entry.used
					}
				}
				delete(c.entries, oldest)
			}
			c.entries[key] = moderationCachedScores{copyModerationScores(result), now.Add(c.ttl), now}
		}
		delete(c.flights, key)
		close(done)
		c.mu.Unlock()
		return result, callErr
	}
}
