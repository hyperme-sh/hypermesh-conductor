package notes

import "sync"

// Cache is a bounded LRU. Keys are content hashes, prefix hashes, or an index
// generation mixed with those hashes, so an edit misses instead of reusing a
// stale neighborhood or summary.
type Cache struct {
	max     int
	mu      sync.Mutex
	items   map[string]*cacheItem
	head    *cacheItem
	tail    *cacheItem
	Lookups int
	Hits    int
}

type cacheItem struct {
	key, value string
	prev, next *cacheItem
}

// NewCache returns an LRU that keeps at most max entries.
func NewCache(max int) *Cache {
	if max < 1 {
		max = 1
	}
	return &Cache{max: max, items: map[string]*cacheItem{}}
}

// Get returns a cached value. The bool is false on a miss.
func (c *Cache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Lookups++
	item, ok := c.items[key]
	if !ok {
		return "", false
	}
	c.Hits++
	c.unlink(item)
	c.push(item)
	return item.value, true
}

// Put stores a value and evicts the least recently used entry when full.
func (c *Cache) Put(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if item, ok := c.items[key]; ok {
		item.value = value
		c.unlink(item)
		c.push(item)
		return
	}
	item := &cacheItem{key: key, value: value}
	c.items[key] = item
	c.push(item)
	if len(c.items) > c.max {
		evict := c.tail
		c.unlink(evict)
		delete(c.items, evict.key)
	}
}

func (c *Cache) push(item *cacheItem) {
	item.prev = nil
	item.next = c.head
	if c.head != nil {
		c.head.prev = item
	}
	c.head = item
	if c.tail == nil {
		c.tail = item
	}
}

func (c *Cache) unlink(item *cacheItem) {
	if item.prev != nil {
		item.prev.next = item.next
	} else {
		c.head = item.next
	}
	if item.next != nil {
		item.next.prev = item.prev
	} else {
		c.tail = item.prev
	}
	item.prev = nil
	item.next = nil
}

// Snapshot copies the counters.
func (c *Cache) Snapshot() (lookups, hits int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Lookups, c.Hits
}
