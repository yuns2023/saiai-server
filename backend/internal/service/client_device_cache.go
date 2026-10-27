package service

import (
	"sync"
	"time"
)

// clientDeviceCache is a size-capped TTL cache for device lookups. Forged
// device ids cannot grow it without bound: when full, expired entries are
// swept and, if still full, arbitrary entries are evicted (map order).
//
// It also carries the revocation generation: a lookup that started before a
// local revoke must not repopulate the cache with the stale row, so
// "compare generation + set" and "bump + delete" share one mutex.
type clientDeviceCache struct {
	mu          sync.Mutex
	entries     map[string]clientDeviceCacheEntry
	maxEntries  int
	revocations uint64
	now         func() time.Time
}

type clientDeviceCacheEntry struct {
	value   cachedClientDevice
	expires time.Time
}

func newClientDeviceCache(maxEntries int) *clientDeviceCache {
	return &clientDeviceCache{entries: make(map[string]clientDeviceCacheEntry), maxEntries: maxEntries, now: time.Now}
}

func (c *clientDeviceCache) get(deviceID string) (cachedClientDevice, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[deviceID]
	if !ok {
		return cachedClientDevice{}, false
	}
	if !c.now().Before(entry.expires) {
		delete(c.entries, deviceID)
		return cachedClientDevice{}, false
	}
	return entry.value, true
}

// generation returns the revocation generation to pass to setIfCurrent.
func (c *clientDeviceCache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revocations
}

// setIfCurrent stores the value unless a revoke happened since generation.
func (c *clientDeviceCache) setIfCurrent(deviceID string, value cachedClientDevice, ttl time.Duration, generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revocations != generation {
		return
	}
	if _, exists := c.entries[deviceID]; !exists && len(c.entries) >= c.maxEntries {
		c.evictLocked()
	}
	c.entries[deviceID] = clientDeviceCacheEntry{value: value, expires: c.now().Add(ttl)}
}

func (c *clientDeviceCache) evictLocked() {
	now := c.now()
	for id, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, id)
		}
	}
	// Still full: drop ~1/8 of the entries (at least one) in map order.
	if len(c.entries) >= c.maxEntries {
		drop := c.maxEntries/8 + 1
		for id := range c.entries {
			delete(c.entries, id)
			drop--
			if drop == 0 {
				break
			}
		}
	}
}

// delete removes an entry without bumping the revocation generation.
func (c *clientDeviceCache) delete(deviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, deviceID)
}

// revoke bumps the generation and removes the entry atomically.
func (c *clientDeviceCache) revoke(deviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revocations++
	delete(c.entries, deviceID)
}
