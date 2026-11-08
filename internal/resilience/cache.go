package resilience

import (
	"container/list"
	"sync"
	"time"

	"magpie-search-adapter/internal/magpie"
)

type cacheEntry struct {
	key     string
	results []magpie.Result
	expires time.Time
}

// ttlCache is a small LRU with a time-to-live per entry.
type ttlCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	items map[string]*list.Element
	order *list.List
}

func newTTLCache(ttl time.Duration, max int) *ttlCache {
	if max < 1 {
		max = 1
	}
	return &ttlCache{
		ttl:   ttl,
		max:   max,
		items: make(map[string]*list.Element, max),
		order: list.New(),
	}
}

func (c *ttlCache) get(key string) ([]magpie.Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		return nil, false
	}
	entry := elem.Value.(*cacheEntry)
	if time.Now().After(entry.expires) {
		c.order.Remove(elem)
		delete(c.items, key)
		return nil, false
	}
	c.order.MoveToFront(elem)
	return copyResults(entry.results), true
}

func (c *ttlCache) set(key string, results []magpie.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*cacheEntry)
		entry.results = copyResults(results)
		entry.expires = time.Now().Add(c.ttl)
		c.order.MoveToFront(elem)
		return
	}
	entry := &cacheEntry{key: key, results: copyResults(results), expires: time.Now().Add(c.ttl)}
	c.items[key] = c.order.PushFront(entry)
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*cacheEntry).key)
	}
}

func (c *ttlCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

func copyResults(results []magpie.Result) []magpie.Result {
	return append([]magpie.Result(nil), results...)
}
