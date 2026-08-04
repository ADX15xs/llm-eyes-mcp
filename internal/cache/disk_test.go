package cache

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// disk_test exercises the on-disk tiers L0 (raw archive) and L1 (preprocessed,
// with TTL / idle / byte-budget eviction driven by Sweep).

func TestL0PutGetIsContentAddressed(t *testing.T) {
	dir := t.TempDir()
	l0, err := NewL0(dir)
	if err != nil {
		t.Fatalf("NewL0: %v", err)
	}
	const md5 = "aabbccddeeff00112233445566778899"
	data := []byte("original-bytes-do-not-touch")
	if err := l0.Put(md5, data); err != nil {
		t.Fatalf("L0.Put: %v", err)
	}
	if !l0.Has(md5) {
		t.Errorf("L0.Has should report the stored id")
	}
	got, ok := l0.Get(md5)
	if !ok || string(got) != string(data) {
		t.Fatalf("L0.Get = %q,%v want %q", got, ok, data)
	}
	if _, ok := l0.Get("missing"); ok {
		t.Errorf("L0.Get for unknown id should return ok=false")
	}
}

func TestL0PutIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	l0, _ := NewL0(dir)
	const md5 = "11223344556677889900aabbccddeeff"
	_ = l0.Put(md5, []byte("one"))
	if err := l0.Put(md5, []byte("changed")); err != nil {
		t.Fatalf("second L0.Put: %v", err)
	}
	got, _ := l0.Get(md5)
	if string(got) != "one" {
		t.Errorf("content-addressed L0 must not overwrite on second Put: got %q", got)
	}
}

func TestL0ShardsByPrefix(t *testing.T) {
	dir := t.TempDir()
	l0, _ := NewL0(dir)
	const md5 = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	_ = l0.Put(md5, []byte("x"))
	p := filepath.Join(dir, "raw", md5[:2], md5+".bin")
	if _, err := os.Stat(p); err != nil {
		t.Errorf("L0 should shard by first 2 bytes: %v", err)
	}
}

func TestL0SizeReflectsBytes(t *testing.T) {
	dir := t.TempDir()
	l0, _ := NewL0(dir)
	_ = l0.Put("aa", []byte("12345"))
	_ = l0.Put("bb", []byte("67890"))
	if sz := l0.Size(); sz != 10 {
		t.Errorf("L0.Size = %d, want 10", sz)
	}
}

func TestL1PutGetStripsHeader(t *testing.T) {
	dir := t.TempDir()
	l1, err := NewL1(dir, 1<<20, time.Hour)
	if err != nil {
		t.Fatalf("NewL1: %v", err)
	}
	payload := []byte("preprocessed-jpeg-bytes")
	if err := l1.Put("key-1", payload); err != nil {
		t.Fatalf("L1.Put: %v", err)
	}
	got, ok := l1.Get("key-1")
	if !ok {
		t.Fatalf("L1.Get returned ok=false")
	}
	if string(got) != string(payload) {
		t.Errorf("L1.Get returned payload with header: got %q want %q", got, payload)
	}
	if _, ok := l1.Get("absent"); ok {
		t.Errorf("L1.Get for absent key must be false")
	}
}

func TestL1GetTouchesMtime(t *testing.T) {
	dir := t.TempDir()
	l1, _ := NewL1(dir, 1<<20, time.Hour)
	_ = l1.Put("k", []byte("v"))
	fp := filepath.Join(dir, "proc", fileNameFor("k")+".bin")
	before := time.Now().Add(-time.Minute)
	_ = os.Chtimes(fp, before, before)
	_, _ = l1.Get("k")
	info, _ := os.Stat(fp)
	if !info.ModTime().After(before) {
		t.Errorf("L1.Get must update mtime (last-accessed) but it did not")
	}
}

func TestL1TTLExpiry(t *testing.T) {
	dir := t.TempDir()
	l1, _ := NewL1(dir, 1<<20, 10*time.Millisecond)
	_ = l1.Put("exp", []byte("v"))
	time.Sleep(20 * time.Millisecond)
	if _, ok := l1.Get("exp"); ok {
		t.Errorf("L1 entry past TTL must be treated as a miss")
	}
}

func TestL1SweepDropsExpired(t *testing.T) {
	// TTL (120ms) sits comfortably between the fresh entry's age (just the test
	// machinery overhead, ~tens of ms) and the expired entry's age (sleep + overhead).
	// A too-tight TTL would let the still-valid entry age out during the test itself.
	dir := t.TempDir()
	l1, _ := NewL1(dir, 1<<20, 120*time.Millisecond)
	_ = l1.Put("old", []byte("v"))
	time.Sleep(300 * time.Millisecond)
	_ = l1.Put("fresh", []byte("v"))
	removed, freed := l1.Sweep(time.Hour)
	if removed != 1 {
		t.Errorf("Sweep should drop the expired entry: removed=%d", removed)
	}
	if freed <= 0 {
		t.Errorf("Sweep should report freed bytes: %d", freed)
	}
	if _, ok := l1.Get("fresh"); !ok {
		t.Errorf("fresh entry must survive the sweep")
	}
}

func TestL1SweepDropsIdle(t *testing.T) {
	dir := t.TempDir()
	l1, _ := NewL1(dir, 1<<20, time.Hour)
	_ = l1.Put("idle", []byte("v"))
	fp := filepath.Join(dir, "proc", fileNameFor("idle")+".bin")
	idleSince := time.Now().Add(-time.Hour)
	_ = os.Chtimes(fp, idleSince, idleSince)
	_ = l1.Put("hot", []byte("v"))
	removed, _ := l1.Sweep(30 * time.Minute)
	if removed != 1 {
		t.Errorf("Sweep should drop the idle entry: removed=%d", removed)
	}
	if _, ok := l1.Get("hot"); !ok {
		t.Errorf("hot entry must survive the sweep")
	}
}

func setMtime(t *testing.T, dir, key string, mod time.Time) {
	t.Helper()
	p := filepath.Join(dir, "proc", fileNameFor(key)+".bin")
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatalf("Chtimes %s: %v", p, err)
	}
}

func TestL1SweepEnforcesByteBudgetLRU(t *testing.T) {
	// Four 1KiB entries under a 2KiB budget; the two least-recently-used must go.
	// We pin explicit mtimes so the test does not depend on the filesystem's
	// ModTime resolution - an LRU keyed on mtime is only as precise as the OS.
	dir := t.TempDir()
	// Each L1 file carries an 8-byte creation header, so a 1024-byte payload is
	// a 1032-byte file on disk. Budget is set between two (2064) and three
	// (3096) entry sizes so exactly the two coldest are evicted.
	l1, _ := NewL1(dir, 2300, time.Hour)
	chunks := bytes.Repeat([]byte("x"), 1024)
	for _, k := range []string{"a", "b", "c", "d"} {
		_ = l1.Put(k, chunks)
	}
	now := time.Now()
	// c and d are made clearly coldest (10/9 minutes ago).
	setMtime(t, dir, "c", now.Add(-10*time.Minute))
	setMtime(t, dir, "d", now.Add(-9*time.Minute))
	// a and b are touched via Get, pushing their mtime to ~now (hottest).
	_, _ = l1.Get("b")
	_, _ = l1.Get("a")
	removed, _ := l1.Sweep(time.Hour)
	if removed != 2 {
		t.Errorf("byte-budget sweep should evict exactly 2 coldest: removed=%d (want 2)", removed)
	}
	if _, ok := l1.Get("a"); !ok {
		t.Errorf("a (recently used) must survive")
	}
	if _, ok := l1.Get("b"); !ok {
		t.Errorf("b (recently used) must survive")
	}
	if _, ok := l1.Get("c"); ok {
		t.Errorf("c (coldest) must be evicted")
	}
	if _, ok := l1.Get("d"); ok {
		t.Errorf("d (coldest) must be evicted")
	}
}

func TestL1SweepNoEvictionUnderBudget(t *testing.T) {
	dir := t.TempDir()
	l1, _ := NewL1(dir, 1<<20, time.Hour)
	_ = l1.Put("a", []byte("small"))
	removed, _ := l1.Sweep(time.Hour)
	if removed != 0 {
		t.Errorf("under-budget sweep must remove nothing: removed=%d", removed)
	}
}
