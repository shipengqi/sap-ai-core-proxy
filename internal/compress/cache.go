package compress

import (
	"container/list"
	"crypto/sha256"
	"sync"
)

// Consistent compression re-runs the same transform over the same history on
// every turn, so a 200-message conversation would otherwise pay for a hundred
// regex scans per request just to arrive at the bytes it already computed. This
// file is the memo that makes that affordable.
//
// What is cached is the *decision*, not just the output: "this text is a log
// and shrank to these bytes" and "this text is not a log, leave it alone" are
// both worth remembering, because LooksLikeLog is the expensive part.
//
// The cache cannot change the output. compressTextUncached is a pure function
// of the input text, so a hit returns exactly what a miss would have computed;
// the key is the sha256 of the input, so two different inputs cannot share an
// entry short of a 128-bit collision. That is what lets the cache sit under the
// determinism guarantee the package promises.

const (
	// defaultCacheCapacity bounds the process-wide memo. A long conversation
	// rewrites every tool_result in it on every turn, so the working set is
	// "distinct tool outputs still in the context" — a few hundred for a heavy
	// session. 4096 leaves generous headroom while keeping the worst case at a
	// few megabytes of retained strings.
	defaultCacheCapacity = 4096

	// cacheKeyBytes is how much of the sha256 digest the key keeps. 16 bytes is
	// 128 bits: a collision needs 2^64 distinct texts by the birthday bound,
	// which no conversation reaches.
	cacheKeyBytes = 16
)

// cacheKey identifies one input text.
type cacheKey [cacheKeyBytes]byte

// cacheValue is the remembered outcome of compressing one text. An empty
// result means the text was left alone and reason says why.
type cacheValue struct {
	result string
	reason string
}

// cacheEntry is what the LRU list stores, so eviction knows the map key.
type cacheEntry struct {
	key   cacheKey
	value cacheValue
}

// textCache is a fixed-capacity LRU: concurrent-safe, bounded, and free of any
// behaviour that could reorder or alter an output.
type textCache struct {
	mu    sync.Mutex
	cap   int
	order *list.List // front is the most recently used entry
	items map[cacheKey]*list.Element
}

// newTextCache returns an empty cache holding at most capacity entries. A
// non-positive capacity falls back to defaultCacheCapacity.
func newTextCache(capacity int) *textCache {
	if capacity <= 0 {
		capacity = defaultCacheCapacity
	}
	return &textCache{
		cap:   capacity,
		order: list.New(),
		items: make(map[cacheKey]*list.Element, capacity),
	}
}

// cacheKeyOf hashes one input text into a cache key.
func cacheKeyOf(text string) cacheKey {
	sum := sha256.Sum256([]byte(text))
	var key cacheKey
	copy(key[:], sum[:cacheKeyBytes])
	return key
}

// get returns the remembered outcome and marks the entry as recently used.
func (c *textCache) get(key cacheKey) (cacheValue, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return cacheValue{}, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*cacheEntry).value, true
}

// put remembers one outcome, evicting the least recently used entry when the
// cache is full.
func (c *textCache) put(key cacheKey, value cacheValue) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		el.Value.(*cacheEntry).value = value
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&cacheEntry{key: key, value: value})
	for len(c.items) > c.cap {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*cacheEntry).key)
	}
}

// reset empties the cache. It exists so a test can prove that the cache is an
// accelerator and never a source of different bytes.
func (c *textCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.order.Init()
	c.items = make(map[cacheKey]*list.Element, c.cap)
}

// length returns how many entries the cache holds.
func (c *textCache) length() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// capacity returns the configured upper bound.
func (c *textCache) capacity() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cap
}

// textCacheRef is the process-wide memo every request shares. It is created at
// package init and never replaced, so a cache hit and a cache miss are
// indistinguishable in the bytes that reach the wire.
var textCacheRef = newTextCache(defaultCacheCapacity)

// compressTextCached is compressText behind the process-wide LRU.
func compressTextCached(text string) (string, string) {
	key := cacheKeyOf(text)
	if v, ok := textCacheRef.get(key); ok {
		return v.result, v.reason
	}
	result, reason := compressTextUncached(text)
	textCacheRef.put(key, cacheValue{result: result, reason: reason})
	return result, reason
}

// resetTextCache clears the process-wide memo. Only tests call it; it is the
// hook that proves determinism does not depend on the cache being warm.
func resetTextCache() {
	textCacheRef.reset()
}
