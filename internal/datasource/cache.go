package datasource

import (
	"sync"
	"time"
)

// 缓存 TTL：快照 3s（盯盘要新鲜）、K 线 60s（日线不会分钟级变化）、资金流 30s。
const (
	ttlQuote = 3 * time.Second
	ttlBars  = 60 * time.Second
	ttlFlow  = 30 * time.Second
)

// cache 是带 TTL 的进程内缓存。命中要求 key 相同且未过期；
// 只缓存在内存里：这些 TTL 都在分钟以内，落盘没有价值，反而会读到过期数据。
type cache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	now     func() time.Time
}

type cacheEntry struct {
	value   any
	expires time.Time
}

func newCache(now func() time.Time) *cache {
	if now == nil {
		now = time.Now
	}
	return &cache{entries: map[string]cacheEntry{}, now: now}
}

// get 命中返回值副本引用；过期条目顺手删除，避免无上限增长。
func (c *cache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if !entry.expires.IsZero() && c.now().After(entry.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return entry.value, true
}

func (c *cache) put(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := cacheEntry{value: value}
	if ttl > 0 {
		entry.expires = c.now().Add(ttl)
	}
	c.entries[key] = entry
}

func (c *cache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]cacheEntry{}
}

func (c *cache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
