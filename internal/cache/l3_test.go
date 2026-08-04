package cache

import (
	"testing"
)

// l3_test exercises the in-memory LRU: promotion on Get, eviction at capacity,
// and hit/miss accounting.

func TestL3PutGet(t *testing.T) {
	l3 := NewL3(4)
	if _, ok := l3.Get("missing"); ok {
		t.Errorf("L3.Get on empty cache must be false")
	}
	l3.Put("a", []byte("1"))
	got, ok := l3.Get("a")
	if !ok || string(got) != "1" {
		t.Fatalf("L3.Get = %q,%v want 1", got, ok)
	}
}

func TestL3PromotesOnGet(t *testing.T) {
	l3 := NewL3(2)
	l3.Put("a", []byte("1"))
	l3.Put("b", []byte("2"))
	// Access a so it becomes MRU; b is now coldest.
	_, _ = l3.Get("a")
	// Overfill: inserting c must evict b (coldest), not a.
	l3.Put("c", []byte("3"))
	if _, ok := l3.Get("a"); !ok {
		t.Errorf("a (recently accessed) must survive eviction")
	}
	if _, ok := l3.Get("b"); ok {
		t.Errorf("b (coldest) must be evicted")
	}
	if _, ok := l3.Get("c"); !ok {
		t.Errorf("c must be present")
	}
}

func TestL3EvictsAtCapacity(t *testing.T) {
	l3 := NewL3(3)
	for _, k := range []string{"a", "b", "c", "d"} {
		l3.Put(k, []byte(k))
	}
	if l3.Len() != 3 {
		t.Errorf("L3 must cap at capacity: len=%d want 3", l3.Len())
	}
	if _, ok := l3.Get("a"); ok {
		t.Errorf("a (oldest, never re-accessed) must be evicted")
	}
}

func TestL3PutRefreshesValue(t *testing.T) {
	l3 := NewL3(2)
	l3.Put("a", []byte("old"))
	l3.Put("a", []byte("new"))
	got, _ := l3.Get("a")
	if string(got) != "new" {
		t.Errorf("L3.Put must refresh value: got %q", got)
	}
}

func TestL3Delete(t *testing.T) {
	l3 := NewL3(2)
	l3.Put("a", []byte("1"))
	l3.Delete("a")
	if _, ok := l3.Get("a"); ok {
		t.Errorf("L3.Delete must remove the entry")
	}
	l3.Delete("missing") // no-op
	if l3.Len() != 0 {
		t.Errorf("len should be 0 after delete")
	}
}

func TestL3Stats(t *testing.T) {
	l3 := NewL3(2)
	l3.Put("a", []byte("1"))
	_, _ = l3.Get("a")  // hit
	_, _ = l3.Get("a")  // hit
	_, _ = l3.Get("zz") // miss
	hits, misses := l3.Stats()
	if hits != 2 {
		t.Errorf("hits = %d, want 2", hits)
	}
	if misses != 1 {
		t.Errorf("misses = %d, want 1", misses)
	}
}

func TestL3NegativeMaxDefaults(t *testing.T) {
	l3 := NewL3(-5)
	if l3.Len() != 0 {
		t.Errorf("empty L3 Len should be 0")
	}
	l3.Put("a", []byte("1"))
	if _, ok := l3.Get("a"); !ok {
		t.Errorf("L3 with defaulted capacity must still store")
	}
}
