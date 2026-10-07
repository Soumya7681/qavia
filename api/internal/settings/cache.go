package settings

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// DefaultCacheTTL bounds how long a stale value can survive if an invalidation is
// ever missed.
//
// Invalidation is the primary mechanism and the TTL is the backstop. A short TTL
// alone would be wrong: a settings change has to take effect on the next job, not
// within thirty seconds of one.
const DefaultCacheTTL = 30 * time.Second

// cache holds resolved values in process.
//
// Keyed by setting key plus target, because the same key resolves differently for
// two users or two projects. Invalidation is by setting key, which drops every
// target's entry for that key: coarse, and correct, since the alternative is
// tracking which targets ever read which key.
type cache struct {
	ttl time.Duration

	mu      sync.RWMutex
	entries map[cacheKey]cacheEntry
}

type cacheKey struct {
	key       string
	userID    uuid.UUID
	projectID uuid.UUID
}

type cacheEntry struct {
	value     Value
	expiresAt time.Time
}

func newCache(ttl time.Duration) *cache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &cache{ttl: ttl, entries: make(map[cacheKey]cacheEntry)}
}

func makeCacheKey(key string, target Target) cacheKey {
	out := cacheKey{key: key}
	if target.UserID != nil {
		out.userID = *target.UserID
	}
	if target.ProjectID != nil {
		out.projectID = *target.ProjectID
	}
	return out
}

func (c *cache) get(key string, target Target) (Value, bool) {
	c.mu.RLock()
	entry, found := c.entries[makeCacheKey(key, target)]
	c.mu.RUnlock()

	if !found || time.Now().After(entry.expiresAt) {
		return Value{}, false
	}
	return entry.value, true
}

func (c *cache) put(key string, target Target, value Value) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[makeCacheKey(key, target)] = cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
}

// forget drops every cached entry for one setting key, across all targets.
func (c *cache) forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for cached := range c.entries {
		if cached.key == key {
			delete(c.entries, cached)
		}
	}
}

// clear empties the cache. Used when an invalidation message cannot be parsed, where
// dropping everything is safer than guessing which key changed.
func (c *cache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[cacheKey]cacheEntry)
}

// size is used by tests to assert the cache is actually doing something.
func (c *cache) size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.entries)
}
