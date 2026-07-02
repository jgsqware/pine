// Package runner manages repository synchronization, scan caching and
// playbook job execution (real ansible-playbook or simulated runs).
package runner

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/scanner"
	"github.com/jgsqware/pine/internal/store"
)

// Manager owns repo syncing, scan results and running jobs.
type Manager struct {
	Store *store.Store

	mu    sync.Mutex
	scans map[string]*model.ScanResult // repoID -> cached scan
	runs  map[string]*run              // jobID -> live run
}

// New creates a Manager on top of the store.
func New(st *store.Store) *Manager {
	return &Manager{
		Store: st,
		scans: map[string]*model.ScanResult{},
		runs:  map[string]*run{},
	}
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
func (m *Manager) rescan(repo *model.Repo) error {
	res, err := scanner.Scan(m.Store.RepoWorkdir(repo), repo.ScanPaths...)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.scans[repo.ID] = res
	m.mu.Unlock()
	repo.Summary = scanner.Summarize(res)
	return nil
}

// Scan returns the (possibly cached) scan result for a repo.
func (m *Manager) Scan(id string) (*model.ScanResult, error) {
	m.mu.Lock()
	if res, ok := m.scans[id]; ok {
		m.mu.Unlock()
		return res, nil
	}
	m.mu.Unlock()

	repo, err := m.Store.GetRepo(id)
	if err != nil {
		return nil, err
	}
	res, err := scanner.Scan(m.Store.RepoWorkdir(&repo), repo.ScanPaths...)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.scans[id] = res
	m.mu.Unlock()
	return res, nil
}

// Forget drops cached state for a deleted repo.
func (m *Manager) Forget(id string) {
	m.mu.Lock()
	delete(m.scans, id)
	m.mu.Unlock()
}
