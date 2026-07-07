// persist.go — disk persistence for scan snapshots.
//
// Each successful scan is serialised to <datadir>/scan.<repoID>.json alongside
// an invalidation marker (HEAD SHA for git repos, root mtime for local paths).
// On boot, New() pre-loads every valid snapshot so the first /scan call is
// served instantly, then schedules a background warm-up goroutine per repo that
// runs a real scan through the singleflight path to refresh the cache.
package runner

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jgsqware/pine/internal/model"
)

// scanSnapshot is the on-disk representation of a persisted scan result.
type scanSnapshot struct {
	// Marker is the invalidation token captured at scan time:
	//   • git repos  → output of "git rev-parse HEAD" (40-char SHA)
	//   • local path → root directory mtime formatted as RFC3339Nano
	// A mismatch on reload means the working tree has changed; the snapshot
	// is discarded and a fresh scan is triggered.
	Marker string            `json:"marker"`
	Result *model.ScanResult `json:"result"`
}

// scanSnapshotPath returns the path of the snapshot file for repoID.
// Files live in the store data directory alongside state.json, jobs/, etc.
func (m *Manager) scanSnapshotPath(repoID string) string {
	return filepath.Join(m.Store.Dir(), "scan."+repoID+".json")
}

// repoMarker returns the invalidation marker for the repo's working directory.
//
// For git repos (workdir contains a .git directory) we ask git for the HEAD
// commit SHA.  This changes on every fetch/reset --hard so it is the minimal
// and most precise signal that the content has changed.  It is also cheap
// (single git rev-parse, no filesystem walk).
//
// For local path repos (no .git) we use the mtime of the workdir itself.
// Directory mtime is updated by the kernel whenever a direct child is
// created, renamed or deleted — good enough for the "is this stale?" check;
// deep changes inside sub-directories do not update the root mtime, but local
// repos are not sync'd by Pine, so changes happen via the user's own tooling.
func repoMarker(workdir string) string {
	// Try git HEAD first.
	if _, err := os.Stat(filepath.Join(workdir, ".git")); err == nil {
		cmd := exec.Command("git", "-C", workdir, "rev-parse", "HEAD")
		out, err := cmd.Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	// Fall back to root mtime.
	if fi, err := os.Stat(workdir); err == nil {
		return fi.ModTime().UTC().Format(time.RFC3339Nano)
	}
	return ""
}

// saveScanSnapshot serialises result alongside its invalidation marker to disk
// using an atomic temp+rename write (same contract as store.saveLocked).
// Errors are silently swallowed: a failed snapshot write degrades gracefully
// (cold-start next boot) rather than failing the scan.
func (m *Manager) saveScanSnapshot(repoID, marker string, result *model.ScanResult) {
	if marker == "" {
		return // nothing reliable to stamp
	}
	snap := scanSnapshot{Marker: marker, Result: result}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}
	path := m.scanSnapshotPath(repoID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// loadScanSnapshot reads the snapshot for repoID and validates it against the
// current marker of workdir.  Returns nil when the file is absent, unreadable,
// unparseable, or stale (marker mismatch).
func (m *Manager) loadScanSnapshot(repoID, workdir string) *model.ScanResult {
	data, err := os.ReadFile(m.scanSnapshotPath(repoID))
	if err != nil {
		return nil
	}
	var snap scanSnapshot
	if json.Unmarshal(data, &snap) != nil {
		return nil
	}
	if snap.Marker == "" || snap.Result == nil {
		return nil
	}
	current := repoMarker(workdir)
	if current == "" || current != snap.Marker {
		return nil // stale or unreadable — discard
	}
	return snap.Result
}

// bootWarmup pre-loads disk snapshots for all known repos into the in-memory
// scan cache, then launches one background goroutine per repo to refresh via a
// real scan.  The snapshot serves requests immediately (warm cache from the
// very first /scan); the background refresh updates the cache as soon as the
// scan completes, going through the singleflight path (no thundering herd).
//
// bootWarmup must be called once, at the end of New(), with the full list of
// repos available at startup.  It does not block the caller.
func (m *Manager) bootWarmup(repos []model.Repo) {
	for _, repo := range repos {
		workdir := m.Store.RepoWorkdir(&repo)
		snap := m.loadScanSnapshot(repo.ID, workdir)
		if snap != nil {
			m.mu.Lock()
			// Only pre-load if no other path already populated the cache
			// (defensive: bootWarmup runs before any concurrent serving).
			if m.scans[repo.ID] == nil {
				m.scans[repo.ID] = snap
			}
			m.mu.Unlock()
		}

		// Background refresh: run a real scan through Scan() (singleflight
		// path) so the cache is updated to the latest on-disk state.
		// We capture repo by value so the goroutine is independent.
		go func(r model.Repo) {
			_, _ = m.Scan(r.ID)
		}(repo)
	}
}

// deleteScanSnapshot removes the persisted snapshot for repoID.  Called by
// Forget so a deleted/forgotten repo does not leave stale data on disk.
func (m *Manager) deleteScanSnapshot(repoID string) {
	path := m.scanSnapshotPath(repoID)
	_ = os.Remove(path)
	_ = os.Remove(path + ".tmp") // clean up any leftover temp file
}

// repoMarkerForID computes the invalidation marker for the repo identified by
// repoID by looking it up in the store.  Returns "" on any error.
func (m *Manager) repoMarkerForID(id string) string {
	repo, err := m.Store.GetRepo(id)
	if err != nil {
		return ""
	}
	return repoMarker(m.Store.RepoWorkdir(&repo))
}
