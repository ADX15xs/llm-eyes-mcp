package cache

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)


// L0 is the original-bytes archive. It is deliberately transformation-free:
// the intent is not known at load time, and any early lossy step would be
// irreversible. It also backs image_id lookups on later turns.
type L0 struct {
	dir string
}

// NewL0 opens (creating if needed) the raw archive under root.
func NewL0(root string) (*L0, error) {
	dir := filepath.Join(root, "raw")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create L0 dir %s: %w", dir, err)
	}
	return &L0{dir: dir}, nil
}

func (l *L0) path(md5 string) string {
	if len(md5) < 2 {
		return filepath.Join(l.dir, md5+".bin")
	}
	// Shard by the first byte so a busy archive does not create one giant dir.
	return filepath.Join(l.dir, md5[:2], md5+".bin")
}

// Get returns archived bytes for an image id.
func (l *L0) Get(md5 string) ([]byte, bool) {
	data, err := os.ReadFile(l.path(md5))
	if err != nil {
		return nil, false
	}
	return data, true
}

// Has reports whether the image is already archived.
func (l *L0) Has(md5 string) bool {
	_, err := os.Stat(l.path(md5))
	return err == nil
}

// Put archives bytes. Writing is atomic (temp file + rename) so a crash cannot
// leave a truncated file that would later be served as a valid image.
func (l *L0) Put(md5 string, data []byte) error {
	p := l.path(md5)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("create L0 shard: %w", err)
	}
	if _, err := os.Stat(p); err == nil {
		return nil // content-addressed: identical bytes, nothing to do
	}
	return writeFileAtomic(p, data)
}

// Size reports the total bytes held by the archive.
func (l *L0) Size() int64 { return dirSize(l.dir) }

// l1Header is an 8-byte big-endian unix-nano creation timestamp prefixed to
// every L1 payload.
//
// Why not use the filesystem's atime? Reading it portably requires per-OS
// syscall structs (Windows has no os.FileInfo.Sys() shape in common with
// Unix). Storing creation time in the payload and using mtime as "last
// accessed" gives both timestamps with zero platform-specific code.
const l1HeaderSize = 8

// L1 caches preprocessed images with a TTL and an LRU byte budget.
type L1 struct {
	dir      string
	maxBytes int64
	ttl      time.Duration
}

// NewL1 opens the processed-image cache.
func NewL1(root string, maxBytes int64, ttl time.Duration) (*L1, error) {
	dir := filepath.Join(root, "proc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create L1 dir %s: %w", dir, err)
	}
	return &L1{dir: dir, maxBytes: maxBytes, ttl: ttl}, nil
}

func (l *L1) path(key string) string { return filepath.Join(l.dir, fileNameFor(key)+".bin") }

// Get returns a cached rendition, touching mtime so the LRU sweep keeps hot
// entries alive.
func (l *L1) Get(key string) ([]byte, bool) {
	p := l.path(key)
	blob, err := os.ReadFile(p)
	if err != nil || len(blob) < l1HeaderSize {
		return nil, false
	}
	created := time.Unix(0, int64(binary.BigEndian.Uint64(blob[:l1HeaderSize])))
	if l.ttl > 0 && time.Since(created) > l.ttl {
		_ = os.Remove(p)
		return nil, false
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now) // mtime == last access
	return blob[l1HeaderSize:], true
}

// Put stores a rendition.
func (l *L1) Put(key string, data []byte) error {
	blob := make([]byte, l1HeaderSize+len(data))
	binary.BigEndian.PutUint64(blob[:l1HeaderSize], uint64(time.Now().UnixNano()))
	copy(blob[l1HeaderSize:], data)
	return writeFileAtomic(l.path(key), blob)
}

// Size reports total bytes held.
func (l *L1) Size() int64 { return dirSize(l.dir) }

// Sweep drops expired entries, entries idle beyond maxIdle, and then evicts
// least-recently-accessed files until the cache fits its byte budget.
func (l *L1) Sweep(maxIdle time.Duration) (removed int, freed int64) {
	type entry struct {
		path     string
		size     int64
		accessed time.Time
	}
	var entries []entry
	var total int64

	_ = filepath.Walk(l.dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		accessed := info.ModTime() // maintained by Get
		if l.ttl > 0 && time.Since(l1CreatedAt(p, info)) > l.ttl {
			if os.Remove(p) == nil {
				removed++
				freed += info.Size()
			}
			return nil
		}
		if maxIdle > 0 && time.Since(accessed) > maxIdle {
			if os.Remove(p) == nil {
				removed++
				freed += info.Size()
			}
			return nil
		}
		entries = append(entries, entry{p, info.Size(), accessed})
		total += info.Size()
		return nil
	})

	if l.maxBytes <= 0 || total <= l.maxBytes {
		return removed, freed
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].accessed.Before(entries[j].accessed) })
	for _, e := range entries {
		if total <= l.maxBytes {
			break
		}
		if os.Remove(e.path) == nil {
			total -= e.size
			removed++
			freed += e.size
		}
	}
	return removed, freed
}

// l1CreatedAt reads the embedded creation timestamp, falling back to mtime for
// files written by an older layout.
func l1CreatedAt(path string, info os.FileInfo) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return info.ModTime()
	}
	defer f.Close()
	var hdr [l1HeaderSize]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return info.ModTime()
	}
	return time.Unix(0, int64(binary.BigEndian.Uint64(hdr[:])))
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create dir for %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp for %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename temp to %s: %w", path, err)
	}
	return nil
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}
