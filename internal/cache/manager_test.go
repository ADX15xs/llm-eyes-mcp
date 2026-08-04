package cache

import (
	"testing"
	"time"
)

// manager_test verifies the four-tier wiring: Open, Startup maintenance, and
// the ArchiveLookup closure that lets later turns resolve a bare image_id.

func TestManagerOpenInitialisesAllTiers(t *testing.T) {
	m, err := Open(Settings{Root: t.TempDir(), L3MaxEntries: 10})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer m.Close()
	if m.L0 == nil || m.L1 == nil || m.L2 == nil || m.L3 == nil {
		t.Fatalf("Open must initialise every tier: %+v", m)
	}
}

func TestManagerStartupCredentialCheck(t *testing.T) {
	m, err := Open(Settings{Root: t.TempDir(), L3MaxEntries: 10, CredentialHash: "fp-1"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer m.Close()
	// First Startup stores the baseline fingerprint without purging (there is
	// nothing to purge yet).
	report0, errs := m.Startup("fp-1", time.Hour)
	if len(errs) != 0 {
		t.Fatalf("first Startup errors: %v", errs)
	}
	if report0.CredentialPurged {
		t.Errorf("first Startup must not purge (no previous fingerprint)")
	}
	_ = m.L2.Put("vlm:k", []byte("stale answer"))
	// A different fingerprint on the next Startup must purge the cache.
	report, errs := m.Startup("fp-2", time.Hour)
	if len(errs) != 0 {
		t.Errorf("Startup should not surface non-fatal errors: %v", errs)
	}
	if !report.CredentialPurged {
		t.Errorf("Startup must purge L2 when the credential fingerprint changes")
	}
	if _, ok := m.L2.Get("vlm:k"); ok {
		t.Errorf("purged L2 must not serve the stale entry")
	}
}

func TestManagerStartupNoCredentialPurgeWhenUnchanged(t *testing.T) {
	m, _ := Open(Settings{Root: t.TempDir(), L3MaxEntries: 10, CredentialHash: "fp-1"})
	defer m.Close()
	_ = m.L2.Put("vlm:k", []byte("good answer"))
	report, _ := m.Startup("fp-1", time.Hour)
	if report.CredentialPurged {
		t.Errorf("Startup must not purge when the fingerprint is unchanged")
	}
	if _, ok := m.L2.Get("vlm:k"); !ok {
		t.Errorf("entry must survive an unchanged fingerprint")
	}
}

func TestManagerStartupSweepsL1(t *testing.T) {
	m, _ := Open(Settings{Root: t.TempDir(), L1MaxBytes: 1 << 20, L1TTL: 10 * time.Millisecond, L3MaxEntries: 10})
	defer m.Close()
	_ = m.L1.Put("old", []byte("v"))
	time.Sleep(20 * time.Millisecond)
	report, errs := m.Startup("fp-1", time.Hour)
	if len(errs) != 0 {
		t.Errorf("Startup errors: %v", errs)
	}
	if report.L1Removed != 1 {
		t.Errorf("Startup must sweep the expired L1 entry: removed=%d", report.L1Removed)
	}
}

func TestManagerArchiveLookupResolvesImageID(t *testing.T) {
	m, _ := Open(Settings{Root: t.TempDir(), L3MaxEntries: 10})
	defer m.Close()
	const id = "00112233445566778899aabbccddeeff"
	raw := []byte("original-png-bytes")
	if err := m.L0.Put(id, raw); err != nil {
		t.Fatalf("L0.Put: %v", err)
	}
	lookup := m.ArchiveLookup()
	got, ok := lookup(id)
	if !ok || string(got) != string(raw) {
		t.Fatalf("ArchiveLookup returned %q,%v want %q", got, ok, raw)
	}
	if _, ok := lookup("nope"); ok {
		t.Errorf("ArchiveLookup must miss on an unknown id")
	}
}

func TestManagerCloseIdempotent(t *testing.T) {
	m, _ := Open(Settings{Root: t.TempDir(), L3MaxEntries: 10})
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Errorf("double Close must not error: %v", err)
	}
}
