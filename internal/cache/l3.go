package cache

import (
	"container/list"
	"sync"
)

// L3 is an in-memory LRU for computed geometry values. These are cheap to
// recompute but free to look up, and they keep repeated "and how far is X from
// Y again?" turns instant.
type L3 struct {
	mu       sync.Mutex
	max      int
	items    map[string]*list.Element
	order    *list.List // front = most recently used
	hits     int
	misses   int
}

type l3Entry struct {
	key   string
	value []byte
}

// NewL3 creates an LRU holding at most max entries.
func NewL3(max int) *L3 {
	if max <= 0 {
		max = 1000
	}
	return &L3{
		max:   max,
		items: make(map[string]*list.Element, max),
		order: list.New(),
	}
}

// Get returns a cached value and promotes it to most-recently-used.
func (l *L3) Get(key string) ([]byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.items[key]
	if !ok {
		l.misses++
		return nil, false
	}
	l.order.MoveToFront(el)
	l.hits++
	return el.Value.(*l3Entry).value, true
}

// Put inserts or refreshes a value, evicting the coldest entry when full.
func (l *L3) Put(key string, value []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.items[key]; ok {
		el.Value.(*l3Entry).value = value
		l.order.MoveToFront(el)
		return
	}
	el := l.order.PushFront(&l3Entry{key: key, value: value})
	l.items[key] = el
	for l.order.Len() > l.max {
		oldest := l.order.Back()
		if oldest == nil {
			break
		}
		l.order.Remove(oldest)
		delete(l.items, oldest.Value.(*l3Entry).key)
	}
}

// Delete removes one entry.
func (l *L3) Delete(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.items[key]; ok {
		l.order.Remove(el)
		delete(l.items, key)
	}
}

// Len reports the current entry count.
func (l *L3) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len()
}

// Stats reports hit/miss counters.
func (l *L3) Stats() (hits, misses int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hits, l.misses
}
