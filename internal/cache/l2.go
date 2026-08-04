package cache

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	// Pure-Go SQLite driver: no CGO, so cross-compilation stays trivial and the
	// binary remains a single self-contained file.
	_ "modernc.org/sqlite"
)

// L2 stores VLM responses (coordinate JSON, descriptions, OCR text) keyed by
// image MD5 + tool + model version. This is the tier that actually saves money:
// a hit means zero API calls.
type L2 struct {
	db       *sql.DB
	ttl      time.Duration
	maxBytes int64
}

const l2Schema = `
CREATE TABLE IF NOT EXISTS vlm_cache (
    key         TEXT PRIMARY KEY,
    value       BLOB NOT NULL,
    size        INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,
    accessed_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vlm_accessed ON vlm_cache(accessed_at);
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

// NewL2 opens (creating if needed) the SQLite-backed semantic cache.
func NewL2(root string, ttl time.Duration, maxBytes int64) (*L2, error) {
	path := filepath.Join(root, "l2.db")
	// WAL keeps readers from blocking the writer; busy_timeout avoids spurious
	// "database is locked" errors when tool calls run concurrently. Some
	// filesystems (network mounts, certain sandboxed volumes) refuse to create
	// the -wal/-shm sidecar files, so we fall back to the default DELETE journal
	// mode, which needs no extra files and still works for our read-heavy load.
	dsns := []string{
		fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
			filepath.ToSlash(path)),
		fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
			filepath.ToSlash(path)),
	}
	var lastErr error
	for _, dsn := range dsns {
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, fmt.Errorf("open L2 database %s: %w", path, err)
		}
		if _, err := db.Exec(l2Schema); err != nil {
			db.Close()
			lastErr = err
			continue // try the next journal mode
		}
		return &L2{db: db, ttl: ttl, maxBytes: maxBytes}, nil
	}
	return nil, fmt.Errorf("init L2 schema: %w", lastErr)
}

// Close releases the database handle.
func (l *L2) Close() error {
	if l == nil || l.db == nil {
		return nil
	}
	return l.db.Close()
}

// Get returns a cached VLM response, or false if absent or expired.
func (l *L2) Get(key string) ([]byte, bool) {
	if l == nil || l.db == nil {
		return nil, false
	}
	var value []byte
	var createdAt int64
	err := l.db.QueryRow(`SELECT value, created_at FROM vlm_cache WHERE key = ?`, key).
		Scan(&value, &createdAt)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false
		}
		return nil, false
	}
	if l.ttl > 0 && time.Since(time.Unix(0, createdAt)) > l.ttl {
		_, _ = l.db.Exec(`DELETE FROM vlm_cache WHERE key = ?`, key)
		return nil, false
	}
	_, _ = l.db.Exec(`UPDATE vlm_cache SET accessed_at = ? WHERE key = ?`, time.Now().UnixNano(), key)
	return value, true
}

// Put stores or replaces a VLM response.
func (l *L2) Put(key string, value []byte) error {
	if l == nil || l.db == nil {
		return nil
	}
	now := time.Now().UnixNano()
	_, err := l.db.Exec(
		`INSERT INTO vlm_cache(key, value, size, created_at, accessed_at) VALUES(?,?,?,?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, size=excluded.size,
		     created_at=excluded.created_at, accessed_at=excluded.accessed_at`,
		key, value, len(value), now, now)
	if err != nil {
		return fmt.Errorf("write L2 entry: %w", err)
	}
	return nil
}

// Delete removes one entry. Used by the "re-analyse" force-refresh path.
func (l *L2) Delete(key string) {
	if l == nil || l.db == nil {
		return
	}
	_, _ = l.db.Exec(`DELETE FROM vlm_cache WHERE key = ?`, key)
}

// Stats reports entry count and total bytes.
func (l *L2) Stats() (entries int, bytes int64) {
	if l == nil || l.db == nil {
		return 0, 0
	}
	_ = l.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size),0) FROM vlm_cache`).Scan(&entries, &bytes)
	return
}

// Sweep deletes expired rows then evicts least-recently-used rows until the
// cache is under its byte budget.
func (l *L2) Sweep() (removed int64, err error) {
	if l == nil || l.db == nil {
		return 0, nil
	}
	if l.ttl > 0 {
		cutoff := time.Now().Add(-l.ttl).UnixNano()
		res, err := l.db.Exec(`DELETE FROM vlm_cache WHERE created_at < ?`, cutoff)
		if err != nil {
			return 0, fmt.Errorf("sweep expired L2 entries: %w", err)
		}
		n, _ := res.RowsAffected()
		removed += n
	}
	if l.maxBytes > 0 {
		_, total := l.Stats()
		if total > l.maxBytes {
			// Drop the coldest 20% of rows; repeated calls converge quickly and
			// this avoids a row-by-row delete loop.
			res, err := l.db.Exec(`DELETE FROM vlm_cache WHERE key IN (
				SELECT key FROM vlm_cache ORDER BY accessed_at ASC
				LIMIT MAX(1, (SELECT COUNT(*) FROM vlm_cache) / 5))`)
			if err != nil {
				return removed, fmt.Errorf("evict L2 entries: %w", err)
			}
			n, _ := res.RowsAffected()
			removed += n
		}
	}
	return removed, nil
}

// SyncCredentials purges the whole cache when the credential fingerprint
// changes. Answers produced under a different API key or endpoint must not be
// reused - they may come from a different model entirely.
//
// It reports whether a purge happened.
func (l *L2) SyncCredentials(fingerprint string) (purged bool, err error) {
	if l == nil || l.db == nil {
		return false, nil
	}
	var stored string
	err = l.db.QueryRow(`SELECT value FROM meta WHERE key = 'credential_fingerprint'`).Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		stored = ""
	case err != nil:
		return false, fmt.Errorf("read credential fingerprint: %w", err)
	}

	if stored != "" && stored != fingerprint {
		if _, err := l.db.Exec(`DELETE FROM vlm_cache`); err != nil {
			return false, fmt.Errorf("purge L2 after credential change: %w", err)
		}
		purged = true
	}
	if stored != fingerprint {
		if _, err := l.db.Exec(
			`INSERT INTO meta(key, value) VALUES('credential_fingerprint', ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fingerprint); err != nil {
			return purged, fmt.Errorf("store credential fingerprint: %w", err)
		}
	}
	return purged, nil
}
