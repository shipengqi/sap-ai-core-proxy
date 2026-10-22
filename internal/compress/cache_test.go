package compress

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestTextCache_CapacityIsEnforced proves the bound: pushing far more entries
// than the capacity leaves the cache holding exactly the capacity, and the
// survivors are the most recently used ones.
func TestTextCache_CapacityIsEnforced(t *testing.T) {
	const capacity = 8
	c := newTextCache(capacity)

	if got := c.capacity(); got != capacity {
		t.Fatalf("capacity = %d, want %d", got, capacity)
	}

	const total = 50
	for i := 0; i < total; i++ {
		key := cacheKeyOf(fmt.Sprintf("text-%02d", i))
		c.put(key, cacheValue{result: fmt.Sprintf("out-%02d", i)})
	}

	if got := c.length(); got != capacity {
		t.Fatalf("length = %d, want %d", got, capacity)
	}

	// The last `capacity` insertions must still be there.
	for i := total - capacity; i < total; i++ {
		key := cacheKeyOf(fmt.Sprintf("text-%02d", i))
		v, ok := c.get(key)
		if !ok {
			t.Fatalf("entry %d was evicted although it is among the newest", i)
		}
		if want := fmt.Sprintf("out-%02d", i); v.result != want {
			t.Errorf("entry %d = %q, want %q", i, v.result, want)
		}
	}
	// The oldest must be gone.
	if _, ok := c.get(cacheKeyOf("text-00")); ok {
		t.Errorf("the oldest entry survived eviction")
	}
}

// TestTextCache_LRUOrderIsUsedByEviction: reading an old entry makes it young
// again, so the entry evicted next is the one that has gone untouched longest.
func TestTextCache_LRUOrderIsUsedByEviction(t *testing.T) {
	c := newTextCache(3)
	keys := []cacheKey{cacheKeyOf("a"), cacheKeyOf("b"), cacheKeyOf("c")}
	for i, k := range keys {
		c.put(k, cacheValue{result: string(rune('A' + i))})
	}

	// Touch "a" so "b" becomes the least recently used.
	if _, ok := c.get(keys[0]); !ok {
		t.Fatal("a is missing before eviction")
	}
	c.put(cacheKeyOf("d"), cacheValue{result: "D"})

	if _, ok := c.get(keys[1]); ok {
		t.Errorf("b survived although it was the least recently used")
	}
	for _, k := range []cacheKey{keys[0], keys[2], cacheKeyOf("d")} {
		if _, ok := c.get(k); !ok {
			t.Errorf("an entry that should have survived was evicted")
		}
	}
}

// TestTextCache_DefaultCapacity covers the fallback for a non-positive bound.
func TestTextCache_DefaultCapacity(t *testing.T) {
	for _, capacity := range []int{0, -1} {
		if got := newTextCache(capacity).capacity(); got != defaultCacheCapacity {
			t.Errorf("capacity for %d = %d, want %d", capacity, got, defaultCacheCapacity)
		}
	}
}

// TestTextCache_ResetEmpties covers the test hook itself.
func TestTextCache_ResetEmpties(t *testing.T) {
	c := newTextCache(4)
	c.put(cacheKeyOf("x"), cacheValue{result: "y"})
	c.reset()
	if got := c.length(); got != 0 {
		t.Fatalf("length after reset = %d, want 0", got)
	}
	if _, ok := c.get(cacheKeyOf("x")); ok {
		t.Errorf("an entry survived reset")
	}
	// The cache stays usable after a reset.
	c.put(cacheKeyOf("x"), cacheValue{result: "z"})
	if v, ok := c.get(cacheKeyOf("x")); !ok || v.result != "z" {
		t.Errorf("cache unusable after reset: %+v, %v", v, ok)
	}
}

// TestTextCache_SkipDecisionsAreCached: the decision "not worth compressing" is
// remembered too, because LooksLikeLog is the expensive half of the transform.
func TestTextCache_SkipDecisionsAreCached(t *testing.T) {
	resetTextCache()
	prose := strings.Repeat("the parser reads a line and returns its upper case form. ", 40)

	first, why := compressTextCached(prose)
	if first != "" || why != skipNotLog {
		t.Fatalf("compressTextCached = (%q, %q), want (\"\", %q)", first, why, skipNotLog)
	}
	key := cacheKeyOf(prose)
	v, ok := textCacheRef.get(key)
	if !ok {
		t.Fatalf("the skip decision was not cached")
	}
	if v.result != "" || v.reason != skipNotLog {
		t.Errorf("cached value = %+v, want an empty result with reason %q", v, skipNotLog)
	}
}

// TestCompressRequest_EvictionDoesNotChangeOutput is the "bounded memory must
// not change the answer" test: compress a log, flood the cache until the entry
// is certainly gone, compress the same log again and compare the bytes.
func TestCompressRequest_EvictionDoesNotChangeOutput(t *testing.T) {
	in := mustRawMap(t, map[string]any{
		"messages": []any{map[string]any{"role": "user",
			"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t", "content": fixturePytest}}}},
	})

	resetTextCache()
	before, beforeRep := CompressRequest(in)

	// defaultCacheCapacity+1 distinct inputs push the entry out for certain.
	for i := 0; i < defaultCacheCapacity+1; i++ {
		text := fmt.Sprintf("ERROR handler: distinct input %05d with a long enough body to hash\n", i)
		_, _ = compressTextCached(text)
	}
	if got := textCacheRef.length(); got != defaultCacheCapacity {
		t.Fatalf("cache length = %d, want %d", got, defaultCacheCapacity)
	}
	if _, ok := textCacheRef.get(cacheKeyOf(fixturePytest)); ok {
		t.Fatalf("the fixture entry was not evicted, so the test proves nothing")
	}

	after, afterRep := CompressRequest(in)
	if string(before["messages"]) != string(after["messages"]) {
		t.Fatalf("eviction changed the output:\nbefore: %s\nafter:  %s", before["messages"], after["messages"])
	}
	if beforeRep != afterRep {
		t.Fatalf("eviction changed the report: %+v vs %+v", beforeRep, afterRep)
	}
}

// TestTextCache_Concurrent runs the cache the way the proxy does — many
// requests at once, on overlapping texts — and must stay clean under -race.
func TestTextCache_Concurrent(t *testing.T) {
	resetTextCache()

	texts := []string{fixturePytest, fixtureNPM, fixtureCargo, fixtureGoTest, fixtureJest, bigInfoLog()}
	want := make([]string, len(texts))
	for i, text := range texts {
		out, why := compressTextUncached(text)
		if out == "" {
			t.Fatalf("fixture %d did not compress: %q", i, why)
		}
		want[i] = out
	}

	const workers = 16
	var wg sync.WaitGroup
	errs := make([]string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for round := 0; round < 20; round++ {
				for i, text := range texts {
					out, why := compressTextCached(text)
					if out != want[i] {
						errs[worker] = fmt.Sprintf("worker %d: fixture %d = %q (why %q), want %q",
							worker, i, out, why, want[i])
						return
					}
				}
				// Mix in misses so eviction runs concurrently with reads.
				_, _ = compressTextCached(fmt.Sprintf("worker %d round %d noise", worker, round))
			}
		}(w)
	}
	wg.Wait()

	for _, msg := range errs {
		if msg != "" {
			t.Error(msg)
		}
	}
	if got := textCacheRef.length(); got > defaultCacheCapacity {
		t.Errorf("cache length = %d, want <= %d", got, defaultCacheCapacity)
	}
}

// TestTextCache_ConcurrentReset exercises reset against concurrent readers: the
// hook must not introduce a race in a package that promises one.
func TestTextCache_ConcurrentReset(t *testing.T) {
	resetTextCache()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for round := 0; round < 10; round++ {
				_, _ = compressTextCached(fixturePytest)
				if worker == 0 && round%3 == 0 {
					resetTextCache()
				}
			}
		}(w)
	}
	wg.Wait()

	out, why := compressTextCached(fixturePytest)
	if out == "" {
		t.Fatalf("compressTextCached after concurrent resets = (%q, %q)", out, why)
	}
}
