package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Upstream model catalogs back /v1/models for clients that are not served the
// native Codex manifest. A catalog is fetched once per group, kept fresh for
// modelsCatalogTTL and, when a refresh fails, the last good copy keeps being
// served for up to modelsCatalogStaleTTL. Group blacklists are applied by the
// caller on every request, so a blacklist change never waits for a refresh.
const (
	modelsCatalogTTL          = 10 * time.Minute
	modelsCatalogStaleTTL     = 24 * time.Hour
	modelsCatalogRetryBackoff = time.Minute
	modelsCatalogFetchTimeout = 20 * time.Second
	modelsCatalogMaxEntries   = 256
	modelsCatalogMaxModels    = 2000
)

// ErrModelsCatalogUnavailable means no upstream catalog could be fetched and
// no previous copy is cached; callers fall back to the built-in model list.
var ErrModelsCatalogUnavailable = errors.New("models catalog unavailable")

// ModelsCatalogEntry is one upstream model object kept verbatim so unknown
// upstream fields reach the client unchanged.
type ModelsCatalogEntry struct {
	ID  string
	Raw json.RawMessage
}

type modelsCatalogCacheEntry struct {
	models     []ModelsCatalogEntry
	fetchedAt  time.Time
	failedAt   time.Time
	staleUntil time.Time
}

type modelsCatalogCache struct {
	mu      sync.Mutex
	entries map[string]*modelsCatalogCacheEntry
	refresh singleflight.Group
}

// get returns the cached catalog for key, refreshing it through fetch when it
// is missing or older than modelsCatalogTTL. The refresh runs detached from
// the caller's context so a client hanging up does not abort it for others.
func (c *modelsCatalogCache) get(ctx context.Context, key string, fetch func(context.Context) ([]ModelsCatalogEntry, error)) ([]ModelsCatalogEntry, error) {
	now := time.Now()
	models, needsRefresh := c.lookup(key, now)
	if !needsRefresh {
		if models == nil {
			return nil, ErrModelsCatalogUnavailable
		}
		return models, nil
	}
	resultCh := c.refresh.DoChan(key, func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(context.Background(), modelsCatalogFetchTimeout)
		defer cancel()
		fresh, err := fetch(fetchCtx)
		if err != nil {
			c.markFailed(key, time.Now())
			return nil, err
		}
		c.store(key, fresh, time.Now())
		return fresh, nil
	})
	select {
	case <-ctx.Done():
		if models != nil {
			return models, nil
		}
		return nil, ctx.Err()
	case result := <-resultCh:
		if result.Err == nil {
			return result.Val.([]ModelsCatalogEntry), nil
		}
		if models != nil {
			return models, nil
		}
		return nil, result.Err
	}
}

// lookup reports the cached models (nil when absent or expired) and whether a
// refresh should be attempted now.
func (c *modelsCatalogCache) lookup(key string, now time.Time) ([]ModelsCatalogEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, true
	}
	if entry.models != nil && !now.Before(entry.staleUntil) {
		entry.models = nil
	}
	if !entry.failedAt.IsZero() && now.Sub(entry.failedAt) < modelsCatalogRetryBackoff {
		return entry.models, false
	}
	if entry.models != nil && now.Sub(entry.fetchedAt) < modelsCatalogTTL {
		return entry.models, false
	}
	return entry.models, true
}

func (c *modelsCatalogCache) store(key string, models []ModelsCatalogEntry, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureRoomLocked(key)
	c.entries[key] = &modelsCatalogCacheEntry{
		models:     models,
		fetchedAt:  now,
		staleUntil: now.Add(modelsCatalogStaleTTL),
	}
}

func (c *modelsCatalogCache) markFailed(key string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok {
		entry.failedAt = now
		return
	}
	c.ensureRoomLocked(key)
	c.entries[key] = &modelsCatalogCacheEntry{failedAt: now}
}

func (c *modelsCatalogCache) ensureRoomLocked(key string) {
	if c.entries == nil {
		c.entries = make(map[string]*modelsCatalogCacheEntry)
	}
	if _, exists := c.entries[key]; exists || len(c.entries) < modelsCatalogMaxEntries {
		return
	}
	oldestKey := ""
	var oldest time.Time
	for candidate, entry := range c.entries {
		touched := entry.fetchedAt
		if entry.failedAt.After(touched) {
			touched = entry.failedAt
		}
		if oldestKey == "" || touched.Before(oldest) {
			oldestKey, oldest = candidate, touched
		}
	}
	delete(c.entries, oldestKey)
}

// parseModelsCatalogData validates an upstream `{"data":[{"id":...}]}` list
// and returns its entries; duplicate IDs keep the first occurrence.
func parseModelsCatalogData(body []byte) ([]ModelsCatalogEntry, *modelsCatalogPage, error) {
	var page struct {
		Data    []json.RawMessage `json:"data"`
		HasMore bool              `json:"has_more"`
		LastID  string            `json:"last_id"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, nil, err
	}
	if page.Data == nil {
		return nil, nil, errors.New("models list has no data array")
	}
	models := make([]ModelsCatalogEntry, 0, len(page.Data))
	for _, raw := range page.Data {
		var item struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &item); err != nil || item.ID == "" {
			return nil, nil, errors.New("models list entry has no string id")
		}
		models = append(models, ModelsCatalogEntry{ID: item.ID, Raw: raw})
	}
	return models, &modelsCatalogPage{hasMore: page.HasMore, lastID: page.LastID}, nil
}

type modelsCatalogPage struct {
	hasMore bool
	lastID  string
}

func appendUniqueModels(dst []ModelsCatalogEntry, seen map[string]struct{}, src []ModelsCatalogEntry) []ModelsCatalogEntry {
	for _, model := range src {
		if _, dup := seen[model.ID]; dup {
			continue
		}
		seen[model.ID] = struct{}{}
		dst = append(dst, model)
	}
	return dst
}
