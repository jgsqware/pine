package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jgsqware/pine/internal/model"
)

// --- harvested facts (per repo, per host) ---

// FactsMeta describes one host's stored facts.
type FactsMeta struct {
	GatheredAt string `json:"gathered_at"`
	Keys       int    `json:"keys"`
}

type factsFile struct {
	GatheredAt string         `json:"gathered_at"`
	Facts      map[string]any `json:"facts"`
}

func (s *Store) factsDir(repoID string) string {
	return filepath.Join(s.dir, "facts", repoID)
}

func safeName(n string) bool {
	return n != "" && !strings.ContainsAny(n, "/\\")
}

// SaveHostFacts stores gathered facts for one host.
func (s *Store) SaveHostFacts(repoID, host string, facts map[string]any) error {
	if !safeName(repoID) || !safeName(host) {
		return ErrNotFound
	}
	if err := os.MkdirAll(s.factsDir(repoID), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(factsFile{
		GatheredAt: time.Now().UTC().Format(time.RFC3339),
		Facts:      facts,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.factsDir(repoID), host+".json"), data, 0o600)
}

// HostFacts loads stored facts for one host (nil when absent).
func (s *Store) HostFacts(repoID, host string) map[string]any {
	if !safeName(repoID) || !safeName(host) {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(s.factsDir(repoID), host+".json"))
	if err != nil {
		return nil
	}
	var f factsFile
	if json.Unmarshal(data, &f) != nil {
		return nil
	}
	return f.Facts
}

// ListFacts summarizes stored facts per host for a repo.
func (s *Store) ListFacts(repoID string) map[string]FactsMeta {
	out := map[string]FactsMeta{}
	entries, err := os.ReadDir(s.factsDir(repoID))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.factsDir(repoID), e.Name()))
		if err != nil {
			continue
		}
		var f factsFile
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		out[strings.TrimSuffix(e.Name(), ".json")] = FactsMeta{
			GatheredAt: f.GatheredAt, Keys: len(f.Facts),
		}
	}
	return out
}

// --- harvested service status (per repo, per host) ---

type servicesFile struct {
	GatheredAt string               `json:"gathered_at"`
	Services   []model.ServiceState `json:"services"`
}

func (s *Store) servicesDir(repoID string) string {
	return filepath.Join(s.dir, "services", repoID)
}

// SaveHostServices stores harvested service status for one host.
func (s *Store) SaveHostServices(repoID, host string, svcs []model.ServiceState) error {
	if !safeName(repoID) || !safeName(host) {
		return ErrNotFound
	}
	if err := os.MkdirAll(s.servicesDir(repoID), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(servicesFile{
		GatheredAt: time.Now().UTC().Format(time.RFC3339),
		Services:   svcs,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.servicesDir(repoID), host+".json"), data, 0o600)
}

// HostServices loads stored service status for one host (nil when absent),
// along with the gather timestamp.
func (s *Store) HostServices(repoID, host string) ([]model.ServiceState, string) {
	if !safeName(repoID) || !safeName(host) {
		return nil, ""
	}
	data, err := os.ReadFile(filepath.Join(s.servicesDir(repoID), host+".json"))
	if err != nil {
		return nil, ""
	}
	var f servicesFile
	if json.Unmarshal(data, &f) != nil {
		return nil, ""
	}
	return f.Services, f.GatheredAt
}

// ListServices summarizes stored service status per host for a repo (reusing
// FactsMeta: Keys is the number of services recorded).
func (s *Store) ListServices(repoID string) map[string]FactsMeta {
	out := map[string]FactsMeta{}
	entries, err := os.ReadDir(s.servicesDir(repoID))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.servicesDir(repoID), e.Name()))
		if err != nil {
			continue
		}
		var f servicesFile
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		out[strings.TrimSuffix(e.Name(), ".json")] = FactsMeta{
			GatheredAt: f.GatheredAt, Keys: len(f.Services),
		}
	}
	return out
}

// --- generic JSON collections (schedules, pipelines, pipeline runs) ---

func loadJSON[T any](path string) []T {
	var out []T
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func saveJSON[T any](path string, items []T) error {
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ListSchedules returns all schedules.
func (s *Store) ListSchedules() []model.Schedule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return loadJSON[model.Schedule](filepath.Join(s.dir, "schedules.json"))
}

// SaveSchedule inserts or replaces a schedule.
func (s *Store) SaveSchedule(sc model.Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := filepath.Join(s.dir, "schedules.json")
	items := loadJSON[model.Schedule](p)
	found := false
	for i := range items {
		if items[i].ID == sc.ID {
			items[i] = sc
			found = true
		}
	}
	if !found {
		items = append(items, sc)
	}
	return saveJSON(p, items)
}

// DeleteSchedule removes a schedule.
func (s *Store) DeleteSchedule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := filepath.Join(s.dir, "schedules.json")
	items := loadJSON[model.Schedule](p)
	for i := range items {
		if items[i].ID == id {
			return saveJSON(p, append(items[:i], items[i+1:]...))
		}
	}
	return ErrNotFound
}

// ListPipelines returns all pipelines.
func (s *Store) ListPipelines() []model.Pipeline {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return loadJSON[model.Pipeline](filepath.Join(s.dir, "pipelines.json"))
}

// SavePipeline inserts or replaces a pipeline.
func (s *Store) SavePipeline(p model.Pipeline) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, "pipelines.json")
	items := loadJSON[model.Pipeline](path)
	found := false
	for i := range items {
		if items[i].ID == p.ID {
			items[i] = p
			found = true
		}
	}
	if !found {
		items = append(items, p)
	}
	return saveJSON(path, items)
}

// DeletePipeline removes a pipeline (runs are kept as history).
func (s *Store) DeletePipeline(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, "pipelines.json")
	items := loadJSON[model.Pipeline](path)
	for i := range items {
		if items[i].ID == id {
			return saveJSON(path, append(items[:i], items[i+1:]...))
		}
	}
	return ErrNotFound
}

// ListPipelineRuns returns runs, newest first, optionally filtered.
func (s *Store) ListPipelineRuns(pipelineID string) []model.PipelineRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := loadJSON[model.PipelineRun](filepath.Join(s.dir, "pipelineruns.json"))
	var out []model.PipelineRun
	for _, r := range all {
		if pipelineID == "" || r.PipelineID == pipelineID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out
}

// GetPipelineRun loads one run.
func (s *Store) GetPipelineRun(id string) (model.PipelineRun, error) {
	for _, r := range s.ListPipelineRuns("") {
		if r.ID == id {
			return r, nil
		}
	}
	return model.PipelineRun{}, ErrNotFound
}

// SavePipelineRun inserts or replaces a run.
func (s *Store) SavePipelineRun(r model.PipelineRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, "pipelineruns.json")
	items := loadJSON[model.PipelineRun](path)
	found := false
	for i := range items {
		if items[i].ID == r.ID {
			items[i] = r
			found = true
		}
	}
	if !found {
		items = append(items, r)
	}
	return saveJSON(path, items)
}
