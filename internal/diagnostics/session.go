package diagnostics

import (
	"container/list"
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

// sessionState is the previous request of one conversation.
type sessionState struct {
	fp       fingerprints
	msgCount int
	msgHash  [][32]byte
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

// put stores state for key, evicting the least recently used entry when full.
func (s *sessionStore) put(key string, st sessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st.seen = s.now()
	if el, ok := s.items[key]; ok {
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

// len reports the number of tracked sessions. Used by tests and /stats/cache.
func (s *sessionStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.order.Len()
}
