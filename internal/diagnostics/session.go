package diagnostics

import (
	"container/list"
	"sort"
	"sync"
	"time"
)

const (
	// defaultSessionCapacity bounds how many conversations are tracked. The
	// session key includes the first message digest, so a busy gateway sees one
	// entry per conversation, not per user.
	defaultSessionCapacity = 1000
	// defaultSessionTTL bounds how long a conversation is remembered. A provider
	// prompt cache lives five minutes, so anything older than this cannot
	// produce a meaningful comparison anyway.
	defaultSessionTTL = 30 * time.Minute
)

// sessionState is the previous request of one conversation plus the counters
// accumulated for it.
//
// The counters live here rather than in a parallel map on the Collector so both
// share one bound: when the LRU evicts a conversation, its counters leave with
// it. A separate map would have kept every session key ever seen resident for
// the lifetime of the process.
type sessionState struct {
	fp       fingerprints
	msgCount int
	requests int64
	busted   int64
	seen     time.Time
}

type sessionEntry struct {
	key string
	st  sessionState
}

// sessionStore is a small LRU with a TTL, safe for concurrent use.
//
// Both bounds matter. Without the capacity an attacker could grow the map by
// sending requests with a fresh first message each time; without the TTL every
// conversation ever seen would stay resident for the process lifetime.
type sessionStore struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	order    *list.List
	items    map[string]*list.Element
	now      func() time.Time
}

func newSessionStore(capacity int, ttl time.Duration) *sessionStore {
	if capacity <= 0 {
		capacity = defaultSessionCapacity
	}
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	return &sessionStore{
		capacity: capacity,
		ttl:      ttl,
		order:    list.New(),
		items:    make(map[string]*list.Element, capacity),
		now:      time.Now,
	}
}

// get returns a copy of the stored state, or false when the session is unknown
// or expired. A copy is returned so the caller never holds a pointer into a
// structure another request may replace.
func (s *sessionStore) get(key string) (sessionState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	el, ok := s.items[key]
	if !ok {
		return sessionState{}, false
	}
	entry := el.Value.(*sessionEntry)
	if s.now().Sub(entry.st.seen) > s.ttl {
		s.order.Remove(el)
		delete(s.items, key)
		return sessionState{}, false
	}
	s.order.MoveToFront(el)
	return entry.st, true
}

// put stores state for key, accumulating the counters rather than replacing
// them. Everything else — the fingerprints and the message count — is replaced,
// because that is the observation the next request of the session will compare
// against.
func (s *sessionStore) put(key string, st sessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st.seen = s.now()
	if el, ok := s.items[key]; ok {
		prev := el.Value.(*sessionEntry).st
		st.requests += prev.requests
		st.busted += prev.busted
		el.Value.(*sessionEntry).st = st
		s.order.MoveToFront(el)
		return
	}

	el := s.order.PushFront(&sessionEntry{key: key, st: st})
	s.items[key] = el

	for s.order.Len() > s.capacity {
		oldest := s.order.Back()
		if oldest == nil {
			break
		}
		s.order.Remove(oldest)
		delete(s.items, oldest.Value.(*sessionEntry).key)
	}
}

// countBump adds one request, and one busted result when busted, to a session.
// It is called while the store lock is held by the caller, so it takes no lock
// of its own.
func (s *sessionStore) countBump(key string, busted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	el, ok := s.items[key]
	if !ok {
		return
	}
	st := el.Value.(*sessionEntry)
	st.st.requests++
	if busted {
		st.st.busted++
	}
}

// topStats returns the conversations with the most busted results, truncated to
// limit, most-busted first.
func (s *sessionStore) topStats(limit int) []SessionSeries {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]SessionSeries, 0, len(s.items))
	for _, el := range s.items {
		st := el.Value.(*sessionEntry).st
		out = append(out, SessionSeries{
			Key:      el.Value.(*sessionEntry).key[:min(len(el.Value.(*sessionEntry).key), sessionKeyPrefix)],
			Requests: st.requests,
			Busted:   st.busted,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Busted != out[j].Busted {
			return out[i].Busted > out[j].Busted
		}
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// len reports the number of tracked sessions. Used by tests and /stats/cache.
func (s *sessionStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.order.Len()
}
