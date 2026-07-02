// Package store persists Pine's state (repos, jobs) as plain JSON files
// under a data directory. No external database required.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/jgsqware/pine/internal/model"
)

// ErrNotFound is returned when an entity does not exist.
var ErrNotFound = errors.New("not found")

type state struct {
	Repos []model.Repo `json:"repos"`
}

// Store is a JSON-file backed state store, safe for concurrent use.
type Store struct {
	mu    sync.RWMutex
	dir   string
	state state
	lock  *os.File // held for the process lifetime to keep the flock
}

// Open loads (or initializes) the store at dir. It takes an exclusive
// inter-process lock so a second Pine writing the same directory fails fast
// instead of corrupting the JSON store.
func Open(dir string) (*Store, error) {
	// 0700: the store holds vault passwords and inventory data — keep it private
	// to the owner even on a shared host.
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "repos"), 0o700); err != nil {
		return nil, err
	}
	lock, err := lockDir(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, lock: lock}
	data, err := os.ReadFile(s.statePath())
	if err == nil {
		_ = json.Unmarshal(data, &s.state)
	}
	return s, nil
}

// Close releases the inter-process lock. Optional: the OS drops the flock when
// the process exits, but tests that open many stores in one process should call
// it to free the descriptor.
func (s *Store) Close() error {
	if s.lock != nil {
		err := s.lock.Close()
		s.lock = nil
		return err
	}
	return nil
}

// Dir returns the data directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) statePath() string { return filepath.Join(s.dir, "state.json") }

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.statePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil { // contains vault passwords
		return err
	}
	return os.Rename(tmp, s.statePath())
}

// NewID generates a short random identifier with the given prefix.
func NewID(prefix string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// --- repos ---

// ListRepos returns all repositories.
func (s *Store) ListRepos() []model.Repo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Repo, len(s.state.Repos))
	copy(out, s.state.Repos)
	return out
}

// GetRepo returns one repository by id.
func (s *Store) GetRepo(id string) (model.Repo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.state.Repos {
		if r.ID == id {
			return r, nil
		}
	}
	return model.Repo{}, ErrNotFound
}

// AddRepo stores a new repository.
func (s *Store) AddRepo(r model.Repo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Repos = append(s.state.Repos, r)
	return s.saveLocked()
}

// UpdateRepo replaces the repository with the same ID.
func (s *Store) UpdateRepo(r model.Repo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Repos {
		if s.state.Repos[i].ID == r.ID {
			s.state.Repos[i] = r
			return s.saveLocked()
		}
	}
	return ErrNotFound
}

// DeleteRepo removes the repository and its cloned working copy.
func (s *Store) DeleteRepo(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Repos {
		if s.state.Repos[i].ID == id {
			s.state.Repos = append(s.state.Repos[:i], s.state.Repos[i+1:]...)
			_ = os.RemoveAll(filepath.Join(s.dir, "repos", id))
			return s.saveLocked()
		}
	}
	return ErrNotFound
}

// RepoWorkdir returns the directory holding the repo's content: the local
// path for path-based repos, or the managed clone for git repos.
func (s *Store) RepoWorkdir(r *model.Repo) string {
	if r.Path != "" {
		return r.Path
	}
	return filepath.Join(s.dir, "repos", r.ID)
}

// --- jobs ---

func (s *Store) jobPath(id string) string {
	return filepath.Join(s.dir, "jobs", id+".json")
}

// JobLogPath returns the log file path for a job.
func (s *Store) JobLogPath(id string) string {
	return filepath.Join(s.dir, "jobs", id+".log")
}

// SaveJob writes the job metadata to disk.
func (s *Store) SaveJob(j model.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.jobPath(j.ID), data, 0o600)
}

// GetJob loads one job by id.
func (s *Store) GetJob(id string) (model.Job, error) {
	if strings.ContainsAny(id, "/\\.") {
		return model.Job{}, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(s.jobPath(id))
	if err != nil {
		return model.Job{}, ErrNotFound
	}
	var j model.Job
	if err := json.Unmarshal(data, &j); err != nil {
		return model.Job{}, fmt.Errorf("corrupt job %s: %w", id, err)
	}
	return j, nil
}

// ListJobs returns all jobs, newest first.
func (s *Store) ListJobs() []model.Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(filepath.Join(s.dir, "jobs"))
	if err != nil {
		return nil
	}
	var jobs []model.Job
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, "jobs", e.Name()))
		if err != nil {
			continue
		}
		var j model.Job
		if json.Unmarshal(data, &j) == nil {
			jobs = append(jobs, j)
		}
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].Created > jobs[k].Created })
	return jobs
}
