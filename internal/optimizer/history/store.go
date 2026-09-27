package history

import (
	"container/list"
	"sync"
	"time"
)

// Store keeps summaries by prefix hash. MemoryStore is enough for one
// instance; a Redis implementation makes the proxy horizontally scalable.
type Store interface {
	Get(key string) (string, bool)
	Put(key, val string)
}

// MemoryStore is an LRU cache with TTL.
type MemoryStore struct {
	mu    sync.Mutex
	max   int
	ttl   time.Duration
	ll    *list.List
	items map[string]*list.Element
	now   func() time.Time
}

type entry struct {
	key, val string
	exp      time.Time
}

func NewMemoryStore(max int, ttl time.Duration) *MemoryStore {
	return &MemoryStore{max: max, ttl: ttl, ll: list.New(), items: map[string]*list.Element{}, now: time.Now}
}

func (s *MemoryStore) Get(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.items[key]
	if !ok {
		return "", false
	}
	e := el.Value.(*entry)
	if s.now().After(e.exp) {
		s.ll.Remove(el)
		delete(s.items, key)
		return "", false
	}
	s.ll.MoveToFront(el)
	return e.val, true
}

func (s *MemoryStore) Put(key, val string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp := s.now().Add(s.ttl)
	if el, ok := s.items[key]; ok {
		e := el.Value.(*entry)
		e.val, e.exp = val, exp
		s.ll.MoveToFront(el)
		return
	}
	s.items[key] = s.ll.PushFront(&entry{key: key, val: val, exp: exp})
	for s.ll.Len() > s.max {
		old := s.ll.Back()
		s.ll.Remove(old)
		delete(s.items, old.Value.(*entry).key)
	}
}
