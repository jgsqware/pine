// Package runner manages repository synchronization, scan caching and
// playbook job execution (real ansible-playbook or simulated runs).
package runner

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/scanner"
	"github.com/jgsqware/pine/internal/store"
)

// inFlightScan is an in-progress scan entry.  The first goroutine that reaches
// a cache miss starts the actual scanner and records itself here; subsequent
// goroutines for the same repoID wait on done.  When the scan finishes (or
// fails) the leader closes done, making all waiters unblock simultaneously.
type inFlightScan struct {
	done chan struct{}
	res  *model.ScanResult
	err  error
}

// Manager owns repo syncing, scan results and running jobs.
type Manager struct {
	Store *store.Store

	mu       sync.Mutex
	scans    map[string]*model.ScanResult  // repoID -> cached scan result
	caches   map[string]*scanner.ScanCache // repoID -> incremental parse cache
	inflight map[string]*inFlightScan      // repoID -> in-progress scan (nil when idle)
	runs     map[string]*run               // jobID -> live run
	sem      chan struct{}                 // bounds concurrent job execution
}

// New creates a Manager on top of the store.
//
// Boot warm-up: valid disk snapshots are pre-loaded into the in-memory scan
// cache so the very first /scan call after restart is served from memory
// without blocking.  A background goroutine per repo then triggers a real scan
// (through the singleflight path) to refresh the cache to the latest on-disk
// state.  See bootWarmup in persist.go for the full contract.
func New(st *store.Store) *Manager {
	m := &Manager{
		Store:    st,
		scans:    map[string]*model.ScanResult{},
		caches:   map[string]*scanner.ScanCache{},
		inflight: map[string]*inFlightScan{},
		runs:     map[string]*run{},
		sem:      make(chan struct{}, maxConcurrentJobs()),
	}
	m.bootWarmup(st.ListRepos())
	return m
}

// scanCacheFor returns the per-repo incremental parse cache, creating it on
// first use. The cache lives for the Manager's lifetime so consecutive syncs
// of the same repo reuse unchanged parse results.
func (m *Manager) scanCacheFor(id string) *scanner.ScanCache {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.caches[id]
	if c == nil {
		c = scanner.NewScanCache()
		m.caches[id] = c
	}
	return c
}

// maxConcurrentJobs caps how many jobs run ansible at once (a bulk of scheduled
// runs firing together would otherwise spawn unbounded processes). Override with
// PINE_MAX_JOBS; defaults to 4.
func maxConcurrentJobs() int {
	if v := os.Getenv("PINE_MAX_JOBS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 4
}

// ReconcileInterruptedJobs marks jobs left running/pending by a previous
// process as failed: their in-memory run is gone, so they are not executing.
// Call once at startup, before serving. Returns how many were fixed.
func (m *Manager) ReconcileInterruptedJobs() int {
	n := 0
	now := time.Now().UTC().Format(time.RFC3339)
	for _, j := range m.Store.ListJobs() {
		if j.Status != model.JobRunning && j.Status != model.JobPending {
			continue
		}
		j.Status = model.JobFailed
		if j.Finished == "" {
			j.Finished = now
		}
		_ = m.Store.SaveJob(j)
		if f, err := os.OpenFile(m.Store.JobLogPath(j.ID), os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.WriteString("\n[pine] interrupted: the server restarted while this job was in flight — marked failed.\n")
			_ = f.Close()
		}
		n++
	}
	return n
}

// SyncRepo clones/pulls (git repos) then rescans, asynchronously.
// The repo status transitions syncing -> ready|error.
func (m *Manager) SyncRepo(id string) (model.Repo, error) {
	repo, err := m.Store.GetRepo(id)
	if err != nil {
		return repo, err
	}
	repo.Status = model.RepoSyncing
	repo.Error = ""
	if err := m.Store.UpdateRepo(repo); err != nil {
		return repo, err
	}
	go m.doSync(repo)
	return repo, nil
}

func (m *Manager) doSync(repo model.Repo) {
	err := m.fetch(&repo)
	if err == nil {
		err = m.rescan(&repo)
	}
	if err != nil {
		repo.Status = model.RepoError
		repo.Error = err.Error()
	} else {
		repo.Status = model.RepoReady
		repo.Error = ""
		repo.LastSynced = time.Now().UTC().Format(time.RFC3339)
	}
	_ = m.Store.UpdateRepo(repo)
}

// fetch makes sure the working copy is up to date (git repos only).
func (m *Manager) fetch(repo *model.Repo) error {
	if repo.URL == "" {
		if _, err := os.Stat(repo.Path); err != nil {
			return fmt.Errorf("local path not accessible: %w", err)
		}
		return nil
	}
	dir := m.Store.RepoWorkdir(repo)
	if _, err := os.Stat(dir + "/.git"); err != nil {
		args := []string{"clone", "--depth", "1"}
		if repo.Branch != "" {
			args = append(args, "--branch", repo.Branch)
		}
		args = append(args, repo.URL, dir)
		return runGit("", args...)
	}
	if err := runGit(dir, "fetch", "--depth", "1", "origin"); err != nil {
		return err
	}
	ref := "origin/HEAD"
	if repo.Branch != "" {
		ref = "origin/" + repo.Branch
	}
	return runGit(dir, "reset", "--hard", ref)
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	// Constrain git's transports (blocks ext::/fd:: shell execution) and never
	// block on a credential prompt for a non-interactive clone.
	cmd.Env = append(os.Environ(),
		"GIT_ALLOW_PROTOCOL="+gitAllowProtocol,
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := string(out)
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return fmt.Errorf("git %s: %s", args[0], msg)
	}
	return nil
}

// rescan refreshes the cached scan and the repo summary counters.
// On success the result is persisted to disk (atomic temp+rename) so that the
// next boot can serve it instantly while the background warm-up refresh runs.
func (m *Manager) rescan(repo *model.Repo) error {
	workdir := m.Store.RepoWorkdir(repo)
	res, err := scanner.ScanWithCache(workdir, m.scanCacheFor(repo.ID), repo.ScanPaths...)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.scans[repo.ID] = res
	m.mu.Unlock()
	repo.Summary = scanner.Summarize(res)
	// Persist snapshot for warm-up on next boot.  Compute marker after the
	// scan so it reflects the state the scan actually observed (HEAD may have
	// been updated by fetch→reset --hard just before us).
	m.saveScanSnapshot(repo.ID, repoMarker(workdir), res)
	return nil
}

// Scan returns the (possibly cached) scan result for a repo.
//
// The returned *ScanResult is a shallow structural copy of the cached value:
// the three top-level slices (Playbooks, Roles, Inventories) are
// re-allocated so that callers can safely append to them without corrupting
// the cache. The slice elements themselves (Playbook, Role, Inventory structs)
// are shared with the cache — callers must not mutate their fields or
// sub-maps. All current callers are read-only; this contract is documented
// here as the canonical reference for the immutable-cache chain (Audit §2 A6).
//
// Concurrent cache-miss deduplication (Audit §5-P3):
// When N goroutines call Scan for the same repo and no cached result exists,
// only the first goroutine performs the actual scan; the rest wait on a shared
// channel and share the single result (or error).  Every caller gets its own
// shallow copy of the result regardless of whether it was the leader or a
// waiter.  This eliminates the thundering-herd on HTTP fan-out.
//
// Forget semantics during an in-flight scan:
// If Forget is called while a scan is in progress, the in-flight entry is
// removed from the map immediately.  The scan that is already running continues
// to completion; its result is stored in the cache and broadcast to all current
// waiters (they already hold a reference to the entry).  A subsequent Scan call
// that arrives after Forget will start a new in-flight scan independently.
// This guarantees that no waiter is stranded and that the cache is eventually
// consistent after a Forget+Scan sequence.
func (m *Manager) Scan(id string) (*model.ScanResult, error) {
	// Fast path: cache hit — no scan needed.
	m.mu.Lock()
	if res, ok := m.scans[id]; ok {
		m.mu.Unlock()
		return shallowCopyScanResult(res), nil
	}

	// Slow path: cache miss.
	// Check for an in-flight scan started by another goroutine for the same id.
	if entry, ok := m.inflight[id]; ok {
		// Another goroutine is already scanning; wait for it.
		m.mu.Unlock()
		<-entry.done
		if entry.err != nil {
			return nil, entry.err
		}
		return shallowCopyScanResult(entry.res), nil
	}

	// We are the first goroutine to miss for this id.  Register an in-flight
	// entry so that concurrent callers join our result instead of racing us.
	entry := &inFlightScan{done: make(chan struct{})}
	m.inflight[id] = entry
	m.mu.Unlock()

	// Perform the scan outside of the mutex.
	// Use a named return + defer so that panics also close done and clean up.
	func() {
		defer func() {
			// Always close done (unblocks all waiters) and remove the in-flight
			// entry.  We only remove our own entry; if Forget was called while we
			// were scanning, the slot may already be absent or replaced — that is
			// intentional (see doc comment above).
			m.mu.Lock()
			if m.inflight[id] == entry {
				delete(m.inflight, id)
			}
			if entry.res != nil {
				m.scans[id] = entry.res
			}
			m.mu.Unlock()
			close(entry.done)
		}()

		repo, err := m.Store.GetRepo(id)
		if err != nil {
			entry.err = err
			return
		}
		workdir := m.Store.RepoWorkdir(&repo)
		res, err := scanner.ScanWithCache(workdir, m.scanCacheFor(id), repo.ScanPaths...)
		if err != nil {
			entry.err = err
			return
		}
		entry.res = res
		// Persist snapshot for boot warm-up on next restart.
		m.saveScanSnapshot(id, repoMarker(workdir), res)
	}()

	if entry.err != nil {
		return nil, entry.err
	}
	return shallowCopyScanResult(entry.res), nil
}

// shallowCopyScanResult returns a new *ScanResult whose top-level slices are
// freshly allocated copies of src's slices. The slice elements (Playbook,
// Role, Inventory) are shared by value — callers must treat them as read-only.
// This protects the cache from append-mutations on the returned slices while
// avoiding a full deep copy of potentially large task trees.
func shallowCopyScanResult(src *model.ScanResult) *model.ScanResult {
	out := &model.ScanResult{
		Playbooks:   append([]model.Playbook(nil), src.Playbooks...),
		Roles:       append([]model.Role(nil), src.Roles...),
		Inventories: append([]model.Inventory(nil), src.Inventories...),
	}
	return out
}

// Forget drops cached state for a deleted or re-synced repo.
//
// If a scan is currently in flight for id, the in-flight entry is removed from
// the map so that new callers arriving after Forget will start a fresh scan.
// The in-flight scan itself continues running; its result will be broadcast to
// any goroutines that are already waiting on the entry's channel, and will be
// stored in the scan cache once it completes (the deferred cleanup inside Scan
// only removes the entry if it is still ours, so a replaced entry is left
// alone).  See the Scan doc comment for the full Forget-during-scan semantics.
//
// The persisted disk snapshot is also removed so a subsequent boot does not
// serve stale content for this repo.
func (m *Manager) Forget(id string) {
	m.mu.Lock()
	delete(m.scans, id)
	delete(m.caches, id)
	delete(m.inflight, id)
	m.mu.Unlock()
	m.deleteScanSnapshot(id)
}
