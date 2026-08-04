package cache

import (
	"fmt"
	"time"
)

// Manager wires the four tiers together and owns their lifecycle.
type Manager struct {
	Root string
	L0   *L0
	L1   *L1
	L2   *L2
	L3   *L3
}

// Settings configures the tiers.
type Settings struct {
	Root           string
	L1MaxBytes     int64
	L1TTL          time.Duration
	L2MaxBytes     int64
	L2TTL          time.Duration
	L3MaxEntries   int
	SweepIdle      time.Duration
	CredentialHash string
}

// Open initialises all four tiers.
func Open(s Settings) (*Manager, error) {
	l0, err := NewL0(s.Root)
	if err != nil {
		return nil, err
	}
	l1, err := NewL1(s.Root, s.L1MaxBytes, s.L1TTL)
	if err != nil {
		return nil, err
	}
	l2, err := NewL2(s.Root, s.L2TTL, s.L2MaxBytes)
	if err != nil {
		return nil, err
	}
	return &Manager{Root: s.Root, L0: l0, L1: l1, L2: l2, L3: NewL3(s.L3MaxEntries)}, nil
}

// Close releases resources.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	return m.L2.Close()
}

// SweepReport summarises a startup maintenance pass.
type SweepReport struct {
	L1Removed        int
	L1Freed          int64
	L2Removed        int64
	CredentialPurged bool
}

func (r SweepReport) String() string {
	return fmt.Sprintf("L1 removed=%d freed=%.1fMB, L2 removed=%d, credential purge=%t",
		r.L1Removed, float64(r.L1Freed)/(1<<20), r.L2Removed, r.CredentialPurged)
}

// Startup runs the maintenance pass: credential check, then eviction of stale
// and over-budget entries. Failures here are non-fatal - a cache problem must
// never prevent the server from serving requests.
func (m *Manager) Startup(credentialHash string, sweepIdle time.Duration) (SweepReport, []error) {
	var report SweepReport
	var errs []error

	if credentialHash != "" {
		purged, err := m.L2.SyncCredentials(credentialHash)
		if err != nil {
			errs = append(errs, err)
		}
		report.CredentialPurged = purged
	}
	report.L1Removed, report.L1Freed = m.L1.Sweep(sweepIdle)
	n, err := m.L2.Sweep()
	if err != nil {
		errs = append(errs, err)
	}
	report.L2Removed = n
	return report, errs
}

// ArchiveLookup returns a closure suitable for imageio.Loader.Archive, letting
// agents pass a bare image_id instead of re-uploading megabytes of base64.
func (m *Manager) ArchiveLookup() func(string) ([]byte, bool) {
	return func(id string) ([]byte, bool) { return m.L0.Get(id) }
}
