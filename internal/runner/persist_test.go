package runner

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/store"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// openStore opens a store at dataDir/data and registers t.Cleanup to close it.
func openStore(t *testing.T, dataDir string) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(dataDir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// newPersistedManagerAt creates a Manager backed by a store at dataDir/data
// and adds a single local repo pointing at repoDir.  The store is registered
// for cleanup so the flock is released before the test ends.
func newPersistedManagerAt(t *testing.T, dataDir, repoDir string) (*Manager, string) {
	t.Helper()
	const id = "r_persist"
	st := openStore(t, dataDir)
	// Only add the repo if it doesn't exist yet (second call for the same dir).
	if _, err := st.GetRepo(id); err != nil {
		repo := model.Repo{ID: id, Name: "persist", Path: repoDir, Status: model.RepoReady}
		if err := st.AddRepo(repo); err != nil {
			t.Fatal(err)
		}
	}
	m := New(st)
	return m, id
}

// writeAnsibleRepo creates the minimal Ansible repo structure under dir.
func writeAnsibleRepo(t *testing.T, dir string) {
	t.Helper()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("site.yml", "- name: Site\n  hosts: web\n  tasks:\n    - name: Ping\n      ansible.builtin.ping:\n")
	write("inventories/prod/hosts.yml", "web:\n  hosts:\n    web01:\n      ansible_host: 10.0.0.1\n")
}

// ---------------------------------------------------------------------------
// TestScanSnapshotRoundTrip
// ---------------------------------------------------------------------------

// TestScanSnapshotRoundTrip verifies that a successful Scan serialises the
// result to disk and that a new Manager built on the same data directory
// pre-loads it into m.scans at boot (before any Scan call is made).
func TestScanSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	writeAnsibleRepo(t, repoDir)

	// --- Prime: scan via first manager ---
	m1, id := newPersistedManagerAt(t, dir, repoDir)
	res1, err := m1.Scan(id)
	if err != nil {
		t.Fatalf("first Scan: %v", err)
	}
	if len(res1.Playbooks) == 0 {
		t.Fatal("expected at least one playbook")
	}
	snapPath := m1.scanSnapshotPath(id)
	if _, err := os.Stat(snapPath); err != nil {
		t.Fatalf("snapshot file not found after scan: %v", err)
	}

	// Close first store so the flock is released.
	_ = m1.Store.Close()

	// --- Reload: new Manager on the same datadir ---
	// openStore re-acquires the flock.
	st2 := openStore(t, dir)
	m2 := New(st2)

	// bootWarmup's snapshot pre-load is synchronous; by the time New() returns
	// m2.scans[id] should already be populated.
	m2.mu.Lock()
	cached := m2.scans[id]
	m2.mu.Unlock()

	if cached == nil {
		t.Fatal("expected snapshot to be pre-loaded into m2.scans at boot")
	}
	if len(cached.Playbooks) != len(res1.Playbooks) {
		t.Errorf("round-trip mismatch: got %d playbooks, want %d",
			len(cached.Playbooks), len(res1.Playbooks))
	}
}

// ---------------------------------------------------------------------------
// TestScanSnapshotStaleMarkerIgnored
// ---------------------------------------------------------------------------

// TestScanSnapshotStaleMarkerIgnored verifies that when the repo's working
// tree changes (root mtime advances), a new Manager discards the stale
// snapshot instead of pre-loading it.
func TestScanSnapshotStaleMarkerIgnored(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	writeAnsibleRepo(t, repoDir)

	// Write snapshot with current marker.
	m1, id := newPersistedManagerAt(t, dir, repoDir)
	if _, err := m1.Scan(id); err != nil {
		t.Fatalf("first Scan: %v", err)
	}
	_ = m1.Store.Close() // release flock

	// Mutate the repo dir so its mtime changes.
	time.Sleep(10 * time.Millisecond) // ensure clock advances
	if err := os.WriteFile(filepath.Join(repoDir, "new_task.yml"), []byte("# new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// New Manager: marker mismatch → snapshot must be discarded.
	st2 := openStore(t, dir)
	m2 := New(st2)

	m2.mu.Lock()
	cached := m2.scans[id]
	m2.mu.Unlock()

	if cached != nil {
		t.Error("stale snapshot should not have been pre-loaded into m2.scans")
	}
}

// ---------------------------------------------------------------------------
// TestScanSnapshotBootServesBeforeRescan
// ---------------------------------------------------------------------------

// TestScanSnapshotBootServesBeforeRescan verifies the key perf contract:
// after a reboot with a valid snapshot, the first Scan() call returns from the
// fast path (cache hit) without invoking the real scanner.
func TestScanSnapshotBootServesBeforeRescan(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	writeAnsibleRepo(t, repoDir)

	// Prime
	m1, id := newPersistedManagerAt(t, dir, repoDir)
	if _, err := m1.Scan(id); err != nil {
		t.Fatalf("first Scan: %v", err)
	}
	_ = m1.Store.Close()

	// Reboot
	st2 := openStore(t, dir)
	m2 := New(st2)

	m2.mu.Lock()
	preloaded := m2.scans[id]
	m2.mu.Unlock()
	if preloaded == nil {
		t.Fatal("snapshot was not pre-loaded; cannot verify serve-from-snapshot behaviour")
	}

	// Grab the parse counter from the ScanCache BEFORE the Scan call.
	// scanCacheFor() creates a new empty cache if absent; for a snapshot pre-load
	// no ScanCache is built yet, so parses == 0.
	cacheBeforeScan := m2.scanCacheFor(id)
	parsesBeforeScan := cacheBeforeScan.Parses()

	res, err := m2.Scan(id)
	if err != nil {
		t.Fatalf("Scan on rebooted manager: %v", err)
	}
	if len(res.Playbooks) == 0 {
		t.Fatal("expected at least one playbook")
	}

	// The fast path returned the snapshot; the scanner must not have been invoked.
	if got := cacheBeforeScan.Parses(); got != parsesBeforeScan {
		t.Errorf("scanner was invoked during first Scan after boot (parses before=%d after=%d); want cache hit",
			parsesBeforeScan, got)
	}
}

// ---------------------------------------------------------------------------
// TestScanSnapshotWarmupTriggered
// ---------------------------------------------------------------------------

// TestScanSnapshotWarmupTriggered verifies that bootWarmup launches a
// background goroutine that eventually makes a valid scan result available.
func TestScanSnapshotWarmupTriggered(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	writeAnsibleRepo(t, repoDir)

	// Prime
	m1, id := newPersistedManagerAt(t, dir, repoDir)
	if _, err := m1.Scan(id); err != nil {
		t.Fatalf("first Scan: %v", err)
	}
	_ = m1.Store.Close()

	// Reboot
	st2 := openStore(t, dir)
	m2 := New(st2)

	// Poll until Scan returns a non-empty result (covers both the pre-loaded
	// snapshot and any subsequent background refresh).
	deadline := time.Now().Add(5 * time.Second)
	var result *model.ScanResult
	for time.Now().Before(deadline) {
		r, err := m2.Scan(id)
		if err == nil && r != nil && len(r.Playbooks) > 0 {
			result = r
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if result == nil {
		t.Fatal("warm-up did not produce a valid scan result within timeout")
	}
}

// ---------------------------------------------------------------------------
// TestScanSnapshotForgetDeletesFile
// ---------------------------------------------------------------------------

// TestScanSnapshotForgetDeletesFile verifies that Forget removes the persisted
// snapshot file so a subsequent boot does not serve stale data.
func TestScanSnapshotForgetDeletesFile(t *testing.T) {
	// newTestManager from ops_test.go uses id "r_test" and a temp dir.
	m, _ := newTestManager(t)
	const id = "r_test"

	if _, err := m.Scan(id); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	snapPath := m.scanSnapshotPath(id)
	if _, err := os.Stat(snapPath); err != nil {
		t.Fatalf("snapshot file must exist before Forget: %v", err)
	}

	m.Forget(id)

	if _, err := os.Stat(snapPath); !os.IsNotExist(err) {
		t.Errorf("snapshot file must be deleted after Forget; stat returned: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestScanSnapshotRescanPersists
// ---------------------------------------------------------------------------

// TestScanSnapshotRescanPersists verifies that rescan (called by SyncRepo)
// also writes the snapshot to disk.
func TestScanSnapshotRescanPersists(t *testing.T) {
	m, _ := newTestManager(t)
	const id = "r_test"

	_, err := m.SyncRepo(id)
	if err != nil {
		t.Fatalf("SyncRepo: %v", err)
	}
	// Wait for doSync to complete.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		repo, _ := m.Store.GetRepo(id)
		if repo.Status == model.RepoReady || repo.Status == model.RepoError {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	snapPath := m.scanSnapshotPath(id)
	if _, err := os.Stat(snapPath); err != nil {
		t.Errorf("snapshot file must exist after SyncRepo+rescan: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestScanSnapshotRaceNoDataRace
// ---------------------------------------------------------------------------

// TestScanSnapshotRaceNoDataRace runs the race detector over concurrent
// Scan+Forget operations that also involve snapshot read/write.
func TestScanSnapshotRaceNoDataRace(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	writeAnsibleRepo(t, repoDir)

	m, id := newPersistedManagerAt(t, dir, repoDir)

	var counter atomic.Int64
	const goroutines = 12
	gate := make(chan struct{})
	done := make(chan struct{}, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			<-gate
			if idx%4 == 0 {
				m.Forget(id)
			}
			if _, err := m.Scan(id); err == nil {
				counter.Add(1)
			}
		}(i)
	}
	close(gate)
	for i := 0; i < goroutines; i++ {
		<-done
	}
	if counter.Load() == 0 {
		t.Error("expected at least one successful Scan")
	}
}
