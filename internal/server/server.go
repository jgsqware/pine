// Package server exposes Pine's REST API, SSE job streams and the
// embedded web UI on a single HTTP listener.
package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
	"github.com/jgsqware/pine/internal/runner"
	"github.com/jgsqware/pine/internal/scanner"
	"github.com/jgsqware/pine/internal/store"
	"github.com/jgsqware/pine/web"
)

// Server wires the manager to HTTP.
type Server struct {
	Mgr *runner.Manager
}

// Version and BuildTime are stamped by main at build time (ldflags) and shown
// in the UI footer + /api/version, so you can tell which build is actually
// loaded (a stale value means the browser served cached assets).
var (
	Version   = "dev"
	BuildTime = ""
)

// buildLabel renders the footer/version string, e.g. "8baf98c · 2026-06-30 11:57 UTC".
func buildLabel() string {
	if BuildTime == "" {
		return Version
	}
	bt := BuildTime
	if t, err := time.Parse(time.RFC3339, BuildTime); err == nil {
		bt = t.UTC().Format("2006-01-02 15:04 MST")
	}
	return Version + " · " + bt
}

// New builds the HTTP handler.
func New(mgr *runner.Manager) http.Handler {
	s := &Server{Mgr: mgr}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/version", s.version)
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /api/repos", s.listRepos)
	mux.HandleFunc("POST /api/repos", s.addRepo)
	mux.HandleFunc("GET /api/repos/{id}", s.getRepo)
	mux.HandleFunc("PATCH /api/repos/{id}", s.updateRepo)
	mux.HandleFunc("DELETE /api/repos/{id}", s.deleteRepo)
	mux.HandleFunc("POST /api/repos/{id}/sync", s.syncRepo)
	mux.HandleFunc("GET /api/repos/{id}/scan", s.scanRepo)
	mux.HandleFunc("GET /api/repos/{id}/topology", s.topology)
	mux.HandleFunc("GET /api/repos/{id}/file", s.repoFile)

	mux.HandleFunc("POST /api/plans", s.computePlan)
	mux.HandleFunc("GET /api/fact-profiles", s.factProfiles)
	mux.HandleFunc("POST /api/repos/{id}/inventory-preview", s.inventoryPreview)
	mux.HandleFunc("GET /api/repos/{id}/lineage", s.lineage)
	mux.HandleFunc("GET /api/repos/{id}/hygiene", s.hygiene)
	mux.HandleFunc("GET /api/repos/{id}/impact", s.impact)
	mux.HandleFunc("GET /api/jobs/{id}/diff", s.jobDiff)
	mux.HandleFunc("GET /api/repos/{id}/facts", s.listFacts)
	mux.HandleFunc("POST /api/repos/{id}/facts/refresh", s.refreshFacts)
	mux.HandleFunc("GET /api/repos/{id}/drift", s.drift)
	mux.HandleFunc("POST /api/repos/{id}/drift/check", s.driftCheck)
	mux.HandleFunc("GET /api/repos/{id}/services", s.services)
	mux.HandleFunc("POST /api/repos/{id}/services/refresh", s.refreshServices)
	mux.HandleFunc("GET /api/repos/{id}/timelapse", s.timelapse)
	mux.HandleFunc("GET /api/repos/{id}/worktrees", s.worktrees)
	mux.HandleFunc("GET /api/repos/{id}/resolve", s.resolve)

	mux.HandleFunc("GET /api/schedules", s.listSchedules)
	mux.HandleFunc("POST /api/schedules", s.createSchedule)
	mux.HandleFunc("PATCH /api/schedules/{id}", s.updateSchedule)
	mux.HandleFunc("DELETE /api/schedules/{id}", s.deleteSchedule)
	mux.HandleFunc("POST /api/schedules/{id}/approve", s.approveSchedule)
	mux.HandleFunc("POST /api/schedules/{id}/run-now", s.runScheduleNow)

	mux.HandleFunc("GET /api/pipelines", s.listPipelines)
	mux.HandleFunc("POST /api/pipelines", s.createPipeline)
	mux.HandleFunc("DELETE /api/pipelines/{id}", s.deletePipeline)
	mux.HandleFunc("POST /api/pipelines/{id}/run", s.runPipeline)
	mux.HandleFunc("GET /api/pipeline-runs", s.listPipelineRuns)
	mux.HandleFunc("GET /api/pipeline-runs/{id}", s.getPipelineRun)
	mux.HandleFunc("POST /api/pipeline-runs/{id}/approve", s.approvePipelineRun)
	mux.HandleFunc("POST /api/pipeline-runs/{id}/cancel", s.cancelPipelineRun)

	mux.HandleFunc("GET /api/jobs", s.listJobs)
	mux.HandleFunc("POST /api/jobs", s.createJob)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("GET /api/jobs/{id}/log", s.jobLog)
	mux.HandleFunc("GET /api/jobs/{id}/events", s.jobEvents)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.cancelJob)

	mux.HandleFunc("/", s.static)
	return logRequests(mux)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func errCode(err error) int {
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

// --- stats ---

func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version":    Version,
		"build_time": BuildTime,
		"label":      buildLabel(),
	})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	repos := s.Mgr.Store.ListRepos()
	jobs := s.Mgr.Store.ListJobs()
	out := map[string]any{"repos": len(repos), "jobs": len(jobs)}
	var pb, roles, invs, hosts, groups, running int
	for _, repo := range repos {
		pb += repo.Summary.Playbooks
		roles += repo.Summary.Roles
		invs += repo.Summary.Inventories
		hosts += repo.Summary.Hosts
		groups += repo.Summary.Groups
	}
	recent := jobs
	if len(recent) > 8 {
		recent = recent[:8]
	}
	for _, j := range jobs {
		if j.Status == model.JobRunning || j.Status == model.JobPending {
			running++
		}
	}
	out["playbooks"], out["roles"], out["inventories"] = pb, roles, invs
	out["hosts"], out["groups"] = hosts, groups
	out["running_jobs"] = running
	if recent == nil {
		recent = []model.Job{}
	}
	out["recent_jobs"] = recent
	writeJSON(w, http.StatusOK, out)
}

// --- repos ---

func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	repos := s.Mgr.Store.ListRepos()
	if repos == nil {
		repos = []model.Repo{}
	}
	writeJSON(w, http.StatusOK, repos)
}

func (s *Server) addRepo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string   `json:"name"`
		URL       string   `json:"url"`
		Path      string   `json:"path"`
		Branch    string   `json:"branch"`
		ScanPaths []string `json:"scan_paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.URL == "" && req.Path == "" {
		writeErr(w, http.StatusBadRequest, errors.New("either url or path is required"))
		return
	}
	if req.Path != "" {
		abs, err := filepath.Abs(req.Path)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("path is not a directory: %s", abs))
			return
		}
		req.Path = abs
	}
	if req.Name == "" {
		src := req.URL
		if src == "" {
			src = req.Path
		}
		req.Name = strings.TrimSuffix(filepath.Base(src), ".git")
	}
	repo := model.Repo{
		ID:        store.NewID("r"),
		Name:      req.Name,
		URL:       req.URL,
		Path:      req.Path,
		Branch:    req.Branch,
		ScanPaths: cleanScanPaths(req.ScanPaths),
		Status:    model.RepoNew,
	}
	if err := s.Mgr.Store.AddRepo(repo); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	repo, err := s.Mgr.SyncRepo(repo.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, repo)
}

// cleanScanPaths drops empty entries and rejects path escapes.
func cleanScanPaths(in []string) []string {
	var out []string
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || strings.Contains(p, "..") || filepath.IsAbs(p) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// updateRepo changes repo settings (name, branch, scan_paths) and triggers
// a re-sync so the new settings take effect immediately.
func (s *Server) updateRepo(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Mgr.Store.GetRepo(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	var req struct {
		Name      *string   `json:"name"`
		Branch    *string   `json:"branch"`
		ScanPaths *[]string `json:"scan_paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Name != nil && *req.Name != "" {
		repo.Name = *req.Name
	}
	if req.Branch != nil {
		repo.Branch = *req.Branch
	}
	if req.ScanPaths != nil {
		repo.ScanPaths = cleanScanPaths(*req.ScanPaths)
	}
	if err := s.Mgr.Store.UpdateRepo(repo); err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	s.Mgr.Forget(repo.ID)
	repo, err = s.Mgr.SyncRepo(repo.ID)
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, repo)
}

func (s *Server) getRepo(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Mgr.Store.GetRepo(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, repo)
}

func (s *Server) deleteRepo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Mgr.Store.DeleteRepo(id); err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	s.Mgr.Forget(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) syncRepo(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Mgr.SyncRepo(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, repo)
}

func (s *Server) scanRepo(w http.ResponseWriter, r *http.Request) {
	res, err := s.Mgr.Scan(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	out := *res
	if out.Playbooks == nil {
		out.Playbooks = []model.Playbook{}
	}
	if out.Roles == nil {
		out.Roles = []model.Role{}
	}
	if out.Inventories == nil {
		out.Inventories = []model.Inventory{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) topology(w http.ResponseWriter, r *http.Request) {
	res, err := s.Mgr.Scan(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	want := r.URL.Query().Get("inventory")
	var inv *model.Inventory
	for i := range res.Inventories {
		if want == "" || res.Inventories[i].Name == want || res.Inventories[i].Path == want {
			inv = &res.Inventories[i]
			break
		}
	}
	if inv == nil {
		writeErr(w, http.StatusNotFound, errors.New("inventory not found"))
		return
	}
	writeJSON(w, http.StatusOK, scanner.BuildTopology(inv))
}

// maxFilePreview caps how much of a source file the raw-file endpoint serves.
const maxFilePreview = 2 << 20 // 2 MiB

// repoFile serves the raw bytes of a file inside a repository's working
// directory, so the UI can preview the real YAML behind a parsed view. The
// requested path is confined to the repo workdir to prevent traversal.
func (s *Server) repoFile(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Mgr.Store.GetRepo(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	root := s.Mgr.Store.RepoWorkdir(&repo)
	full := filepath.Join(root, filepath.FromSlash(rel))
	// confine to the repo workdir
	rootAbs, err1 := filepath.Abs(root)
	fullAbs, err2 := filepath.Abs(full)
	if err1 != nil || err2 != nil || (fullAbs != rootAbs && !strings.HasPrefix(fullAbs, rootAbs+string(os.PathSeparator))) {
		writeErr(w, http.StatusBadRequest, errors.New("invalid path"))
		return
	}
	info, err := os.Stat(fullAbs)
	if err != nil || info.IsDir() {
		writeErr(w, http.StatusNotFound, errors.New("file not found"))
		return
	}
	data, err := os.ReadFile(fullAbs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if len(data) > maxFilePreview {
		data = data[:maxFilePreview]
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// --- plans ---

func (s *Server) factProfiles(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]string, 0, len(plan.Profiles))
	for _, p := range plan.Profiles {
		out = append(out, map[string]string{"id": p.ID, "label": p.Label})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) computePlan(w http.ResponseWriter, r *http.Request) {
	var req plan.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.RepoID == "" || req.Playbook == "" {
		writeErr(w, http.StatusBadRequest, errors.New("repo_id and playbook are required"))
		return
	}
	repo, err := s.Mgr.Store.GetRepo(req.RepoID)
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	res, err := s.Mgr.Scan(req.RepoID)
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	if req.Mode == "exact" {
		out, err := plan.ComputeExact(s.Mgr.Store.RepoWorkdir(&repo), repo, req)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	req.TaskDurations = s.taskDurationHistory(repo.ID, req.Playbook)
	req.HostFacts = s.Mgr.HostFactsFor(repo.ID)
	out, err := plan.Compute(res, s.Mgr.Store.RepoWorkdir(&repo), repo, req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) inventoryPreview(w http.ResponseWriter, r *http.Request) {
	var req plan.PreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.Mgr.Scan(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	out, err := plan.PreviewInventory(res, req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- jobs ---

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	jobs := s.Mgr.Store.ListJobs()
	if jobs == nil {
		jobs = []model.Job{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	// vault_password is decoded alongside the job but kept out of model.Job so
	// it is never persisted; it's forwarded to the runner transiently.
	var body struct {
		model.Job
		VaultPassword string         `json:"vault_password"`
		Vars          map[string]any `json:"vars"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req := body.Job
	if req.RepoID == "" || req.Playbook == "" {
		writeErr(w, http.StatusBadRequest, errors.New("repo_id and playbook are required"))
		return
	}
	job, err := s.Mgr.StartJob(req, runner.RunOpts{VaultPassword: body.VaultPassword, ExtraVars: body.Vars})
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Mgr.Store.GetJob(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) jobLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Mgr.Store.GetJob(id); err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.ServeFile(w, r, s.Mgr.Store.JobLogPath(id))
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Mgr.Cancel(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// jobEvents streams log lines and status updates over SSE.
func (s *Server) jobEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := s.Mgr.Store.GetJob(id)
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sendLine := func(line string) {
		for _, l := range strings.Split(line, "\n") {
			fmt.Fprintf(w, "event: line\ndata: %s\n\n", l)
		}
	}
	sendStatus := func(j model.Job) {
		data, _ := json.Marshal(j)
		fmt.Fprintf(w, "event: status\ndata: %s\n\n", data)
	}

	// subscribe BEFORE replaying the log to avoid missing lines
	ch, live := s.Mgr.Subscribe(id)

	if data, err := os.ReadFile(s.Mgr.Store.JobLogPath(id)); err == nil && len(data) > 0 {
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			sendLine(line)
		}
	}
	sendStatus(job)
	fl.Flush()

	if !live {
		return
	}
	defer s.Mgr.Unsubscribe(id, ch)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case line, more := <-ch:
			if !more {
				if j, err := s.Mgr.Store.GetJob(id); err == nil {
					sendStatus(j)
				}
				fl.Flush()
				return
			}
			sendLine(line)
			fl.Flush()
		case <-ticker.C:
			if j, err := s.Mgr.Store.GetJob(id); err == nil {
				sendStatus(j)
				fl.Flush()
			}
		}
	}
}

// --- static UI ---

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if path == "embed.go" {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(web.FS, path)
	if err != nil {
		// SPA fallback
		data, err = fs.ReadFile(web.FS, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		path = "index.html"
	}
	// Stamp the deployed build into index.html so it's visible in the footer
	// even when app.js is cached (the build label changes every build).
	if path == "index.html" {
		data = []byte(strings.Replace(string(data), "__PINE_BUILD__", buildLabel(), 1))
	}
	switch {
	case strings.HasSuffix(path, ".html"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case strings.HasSuffix(path, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(path, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case strings.HasSuffix(path, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
	}
	// Assets are embedded at build time: tag them by content and require
	// revalidation. After an upgrade the bytes (and ETag) change, so browsers
	// fetch the new file on the next load instead of serving a stale cache;
	// when unchanged they get a cheap 304.
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(data)
}

// --- insights: lineage, hygiene, impact, job diff ---

func (s *Server) lineage(w http.ResponseWriter, r *http.Request) {
	res, err := s.Mgr.Scan(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	out, err := plan.Lineage(res, r.URL.Query().Get("inventory"), r.URL.Query().Get("host"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// resolve answers "what do this playbook's {{ vars }} resolve to?" — host
// agnostically by default, or against ?inventory=&host= when given. Secrets are
// redacted before the values ever leave Pine.
func (s *Server) resolve(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Mgr.Store.GetRepo(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	res, err := s.Mgr.Scan(repo.ID)
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	out, err := plan.Resolve(res, s.Mgr.Store.RepoWorkdir(&repo),
		r.URL.Query().Get("playbook"), r.URL.Query().Get("inventory"), r.URL.Query().Get("host"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	out.Redact()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) hygiene(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Mgr.Store.GetRepo(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	res, err := s.Mgr.Scan(repo.ID)
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, plan.Hygiene(res, s.Mgr.Store.RepoWorkdir(&repo)))
}

func (s *Server) impact(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Mgr.Store.GetRepo(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	res, err := s.Mgr.Scan(repo.ID)
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	out, err := plan.Impact(res, s.Mgr.Store.RepoWorkdir(&repo),
		r.URL.Query().Get("base"), r.URL.Query().Get("head"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) jobDiff(w http.ResponseWriter, r *http.Request) {
	other := r.URL.Query().Get("with")
	if other == "" {
		writeErr(w, http.StatusBadRequest, errors.New("query parameter 'with' is required"))
		return
	}
	out, err := s.Mgr.DiffJobs(r.PathValue("id"), other)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
		} else {
			writeErr(w, http.StatusBadRequest, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// taskDurationHistory averages per-task durations over the most recent
// terminal jobs of the same repo+playbook, for plan time estimates.
func (s *Server) taskDurationHistory(repoID, playbook string) map[string]int64 {
	jobs := s.Mgr.Store.ListJobs()
	sum := map[string]int64{}
	count := map[string]int64{}
	used := 0
	for _, j := range jobs {
		if used >= 5 {
			break
		}
		if j.RepoID != repoID || j.Playbook != playbook || !j.Terminal() || len(j.TaskDurations) == 0 {
			continue
		}
		used++
		for _, td := range j.TaskDurations {
			sum[td.Task] += td.MS
			count[td.Task]++
		}
	}
	if used == 0 {
		return nil
	}
	out := make(map[string]int64, len(sum))
	for k := range sum {
		out[k] = sum[k] / count[k]
	}
	return out
}

// --- facts, drift, timelapse ---

func (s *Server) listFacts(w http.ResponseWriter, r *http.Request) {
	metas := s.Mgr.Store.ListFacts(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"count": len(metas), "hosts": metas})
}

func (s *Server) refreshFacts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Inventory string `json:"inventory"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	job, err := s.Mgr.GatherFacts(r.PathValue("id"), req.Inventory)
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) drift(w http.ResponseWriter, r *http.Request) {
	out, err := s.Mgr.Drift(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) driftCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Playbooks []string `json:"playbooks"`
		Inventory string   `json:"inventory"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	jobs, err := s.Mgr.DriftCheck(r.PathValue("id"), req.Playbooks, req.Inventory)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, jobs)
}

func (s *Server) services(w http.ResponseWriter, r *http.Request) {
	out, err := s.Mgr.ServiceStatus(r.PathValue("id"), r.URL.Query().Get("inventory"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) refreshServices(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Inventory string `json:"inventory"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	job, err := s.Mgr.CheckServices(r.PathValue("id"), req.Inventory)
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) timelapse(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Mgr.Store.GetRepo(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	limit := 30
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		limit = n
	}
	out, err := plan.Timelapse(s.Mgr.Store.RepoWorkdir(&repo), r.URL.Query().Get("inventory"), limit)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// worktrees lists the git worktrees attached to a repo's working copy.
func (s *Server) worktrees(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Mgr.Store.GetRepo(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	out, err := plan.Worktrees(s.Mgr.Store.RepoWorkdir(&repo))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- schedules ---

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	items := s.Mgr.Store.ListSchedules()
	if items == nil {
		items = []model.Schedule{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) {
	var sc model.Schedule
	if err := json.NewDecoder(r.Body).Decode(&sc); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	out, err := s.Mgr.CreateSchedule(sc)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateSchedule(w http.ResponseWriter, r *http.Request) {
	cur := model.Schedule{}
	found := false
	for _, sc := range s.Mgr.Store.ListSchedules() {
		if sc.ID == r.PathValue("id") {
			cur, found = sc, true
		}
	}
	if !found {
		writeErr(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	apply := func(key string, dst *string) {
		if v, ok := req[key].(string); ok {
			*dst = v
		}
	}
	apply("playbook", &cur.Playbook)
	apply("inventory", &cur.Inventory)
	apply("limit", &cur.Limit)
	apply("tags", &cur.Tags)
	apply("interval", &cur.Interval)
	if v, ok := req["check"].(bool); ok {
		cur.Check = v
	}
	if v, ok := req["gate"].(bool); ok {
		cur.Gate = v
	}
	if v, ok := req["enabled"].(bool); ok {
		cur.Enabled = v
		if v {
			cur.BlockedReason = ""
		}
	}
	if _, err := time.ParseDuration(cur.Interval); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid interval %q", cur.Interval))
		return
	}
	cur.Status = "ok"
	if !cur.Enabled {
		cur.Status = "disabled"
	} else if cur.BlockedReason != "" {
		cur.Status = "blocked"
	}
	if err := s.Mgr.Store.SaveSchedule(cur); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cur)
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	if err := s.Mgr.Store.DeleteSchedule(r.PathValue("id")); err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) approveSchedule(w http.ResponseWriter, r *http.Request) {
	out, err := s.Mgr.ApproveSchedule(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) runScheduleNow(w http.ResponseWriter, r *http.Request) {
	job, err := s.Mgr.RunScheduleNow(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

// --- pipelines ---

func (s *Server) listPipelines(w http.ResponseWriter, r *http.Request) {
	items := s.Mgr.Store.ListPipelines()
	if items == nil {
		items = []model.Pipeline{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createPipeline(w http.ResponseWriter, r *http.Request) {
	var p model.Pipeline
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	out, err := s.Mgr.CreatePipeline(p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) deletePipeline(w http.ResponseWriter, r *http.Request) {
	if err := s.Mgr.Store.DeletePipeline(r.PathValue("id")); err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runPipeline(w http.ResponseWriter, r *http.Request) {
	run, err := s.Mgr.RunPipeline(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) listPipelineRuns(w http.ResponseWriter, r *http.Request) {
	runs := s.Mgr.Store.ListPipelineRuns(r.URL.Query().Get("pipeline"))
	if runs == nil {
		runs = []model.PipelineRun{}
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) getPipelineRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.Mgr.Store.GetPipelineRun(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) approvePipelineRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.Mgr.ApprovePipelineRun(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) cancelPipelineRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.Mgr.CancelPipelineRun(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}
