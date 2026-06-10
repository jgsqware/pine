// Package server exposes Pine's REST API, SSE job streams and the
// embedded web UI on a single HTTP listener.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/runner"
	"github.com/jgsqware/pine/internal/scanner"
	"github.com/jgsqware/pine/internal/store"
	"github.com/jgsqware/pine/web"
)

// Server wires the manager to HTTP.
type Server struct {
	Mgr *runner.Manager
}

// New builds the HTTP handler.
func New(mgr *runner.Manager) http.Handler {
	s := &Server{Mgr: mgr}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /api/repos", s.listRepos)
	mux.HandleFunc("POST /api/repos", s.addRepo)
	mux.HandleFunc("GET /api/repos/{id}", s.getRepo)
	mux.HandleFunc("DELETE /api/repos/{id}", s.deleteRepo)
	mux.HandleFunc("POST /api/repos/{id}/sync", s.syncRepo)
	mux.HandleFunc("GET /api/repos/{id}/scan", s.scanRepo)
	mux.HandleFunc("GET /api/repos/{id}/topology", s.topology)
	mux.HandleFunc("GET /api/repos/{id}/file", s.repoFile)

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
		Name   string `json:"name"`
		URL    string `json:"url"`
		Path   string `json:"path"`
		Branch string `json:"branch"`
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
		ID:     store.NewID("r"),
		Name:   req.Name,
		URL:    req.URL,
		Path:   req.Path,
		Branch: req.Branch,
		Status: model.RepoNew,
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

// --- jobs ---

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	jobs := s.Mgr.Store.ListJobs()
	if jobs == nil {
		jobs = []model.Job{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var req model.Job
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.RepoID == "" || req.Playbook == "" {
		writeErr(w, http.StatusBadRequest, errors.New("repo_id and playbook are required"))
		return
	}
	job, err := s.Mgr.StartJob(req)
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
	_, _ = w.Write(data)
}
