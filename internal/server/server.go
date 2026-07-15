// Package server exposes Pine's REST API, SSE job streams and the
// embedded web UI on a single HTTP listener.
package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
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
	Mgr   *runner.Manager
	Label string // operator instance label (--label / PINE_LABEL), personalises the PWA
}

// Config tunes the HTTP surface. The zero value is safe for loopback use.
type Config struct {
	// Token, when non-empty, is required on every /api/ request. It may be
	// presented as a Bearer Authorization header, an X-Pine-Token header, a
	// pine_token cookie, or a ?token= query parameter (the latter so the
	// browser EventSource, which cannot set headers, can authenticate the SSE
	// stream). An empty Token disables authentication (loopback-only default).
	Token string

	// Label is an optional operator-chosen instance name (e.g. "iba",
	// "gaming1"). When set it is stamped into the SPA <title>, the sidebar
	// brand and the PWA manifest so multiple Pine instances installed as PWAs
	// on one machine show as "Pine · iba" / "Pine · gaming1" instead of all
	// reading "Pine" in the Dock / app switcher.
	Label string
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

// stampLabel substitutes the instance-label placeholders in the SPA shell and
// the PWA manifest. An empty label leaves everything reading plain "Pine".
// The label is operator-controlled but still context-escaped (HTML for the
// shell, JSON for the manifest) so an odd character can't corrupt the markup.
//
//	__PINE_LABEL_SUFFIX__ → " · iba"  (title / manifest name)
//	__PINE_LABEL_APP__    → " iba"    (apple app title / manifest short_name)
//	__PINE_LABEL_VER__    → "iba"     (sidebar sub-brand, defaults to "automation")
func stampLabel(data []byte, path, label string) []byte {
	label = strings.TrimSpace(label)
	suffix, app, ver := "", "", "automation"
	if label != "" {
		esc := label
		if strings.HasSuffix(path, ".webmanifest") {
			if b, err := json.Marshal(label); err == nil {
				esc = string(b[1 : len(b)-1]) // inner of the JSON string, no quotes
			}
		} else {
			esc = html.EscapeString(label)
		}
		suffix, app, ver = " · "+esc, " "+esc, esc
	}
	s := string(data)
	s = strings.ReplaceAll(s, "__PINE_LABEL_SUFFIX__", suffix)
	s = strings.ReplaceAll(s, "__PINE_LABEL_APP__", app)
	s = strings.ReplaceAll(s, "__PINE_LABEL_VER__", ver)
	return []byte(s)
}

// New builds the HTTP handler.
func New(mgr *runner.Manager, cfg Config) http.Handler {
	s := &Server{Mgr: mgr, Label: strings.TrimSpace(cfg.Label)}
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
	mux.HandleFunc("GET /api/repos/{id}/playbook", s.playbookDetail)
	mux.HandleFunc("GET /api/repos/{id}/role", s.roleDetail)
	mux.HandleFunc("GET /api/repos/{id}/topology", s.topology)
	mux.HandleFunc("GET /api/repos/{id}/overview", s.overview)
	mux.HandleFunc("POST /api/repos/{id}/describe", s.describe)
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
	mux.HandleFunc("GET /api/probes", s.listProbes)
	mux.HandleFunc("POST /api/repos/{id}/probes", s.runProbe)
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
	return logRequests(gzipMiddleware(secure(cfg, mux)))
}

// gzipMiddleware compresses responses when the client accepts gzip. It skips
// the SSE stream (compression buffers and would break live job logs) and only
// encodes 2xx bodies. JSON payloads like /scan (several MB on big repos) shrink
// ~5-10x on the wire.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") ||
			strings.HasSuffix(r.URL.Path, "/events") {
			next.ServeHTTP(w, r)
			return
		}
		gzw := &gzipResponseWriter{ResponseWriter: w}
		defer gzw.finish()
		next.ServeHTTP(gzw, r)
	})
}

// gzipResponseWriter lazily gzips: it only wraps the body once a 2xx status is
// known (so 204/304 and already-encoded responses pass through untouched).
type gzipResponseWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	started bool
}

func (w *gzipResponseWriter) WriteHeader(code int) {
	if w.started {
		return
	}
	w.started = true
	if code >= 200 && code < 300 && code != http.StatusNoContent &&
		w.Header().Get("Content-Encoding") == "" {
		w.Header().Del("Content-Length") // gzipped body has a different size
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		w.gz = gzip.NewWriter(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.started {
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *gzipResponseWriter) Flush() {
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *gzipResponseWriter) finish() {
	if w.gz != nil {
		_ = w.gz.Close()
	}
}

// secure gates every /api/ request with a lightweight CSRF check and, when a
// token is configured, token authentication. Static assets (the SPA shell) are
// served unauthenticated so the browser can load the login prompt; all data
// flows through /api/ and is protected.
func secure(cfg Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			// CSRF: a browser attaches Origin to state-changing requests. If it
			// is present and does not match the host we are served on, refuse —
			// this blocks a malicious page from POSTing to a local Pine. Non-
			// browser clients (curl, CI) send no Origin and are unaffected.
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				if o := r.Header.Get("Origin"); o != "" && !sameOrigin(o, r.Host) {
					writeErr(w, http.StatusForbidden, errors.New("cross-origin request blocked"))
					return
				}
			}
			if cfg.Token != "" && !tokenOK(r, cfg.Token) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="pine"`)
				writeErr(w, http.StatusUnauthorized, errors.New("authentication required"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// tokenOK reports whether the request carries the configured token, accepted as
// a Bearer header, an X-Pine-Token header, a pine_token cookie, or a ?token=
// query parameter (for EventSource). The comparison is constant-time.
func tokenOK(r *http.Request, want string) bool {
	var got string
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got = strings.TrimPrefix(h, "Bearer ")
	} else if h := r.Header.Get("X-Pine-Token"); h != "" {
		got = h
	} else if c, err := r.Cookie("pine_token"); err == nil {
		got = c.Value
	} else {
		got = r.URL.Query().Get("token")
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// sameOrigin reports whether the Origin header's host:port matches the request
// Host we are being served on.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
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
		"instance":   s.Label,
	})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	repos := s.Mgr.Store.ListRepos()
	running, totalJobs := s.Mgr.Store.JobCounts()
	recent, _ := s.Mgr.Store.ListJobsPage(8, 0) // just the newest few for the dashboard
	out := map[string]any{"repos": len(repos), "jobs": totalJobs}
	var pb, roles, invs, hosts, groups int
	for _, repo := range repos {
		pb += repo.Summary.Playbooks
		roles += repo.Summary.Roles
		invs += repo.Summary.Inventories
		hosts += repo.Summary.Hosts
		groups += repo.Summary.Groups
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

// redactRepo strips the stored vault password from a repo before it leaves the
// API, leaving only a boolean marker that one is set.
func redactRepo(r model.Repo) model.Repo {
	if r.VaultPassword != "" {
		r.HasVaultPassword = true
		r.VaultPassword = ""
	}
	return r
}

func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	repos := s.Mgr.Store.ListRepos()
	if repos == nil {
		repos = []model.Repo{}
	}
	for i := range repos {
		repos[i] = redactRepo(repos[i])
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
	if req.URL != "" {
		if err := runner.ValidateGitURL(req.URL); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
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
	writeJSON(w, http.StatusCreated, redactRepo(repo))
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
		Name               *string   `json:"name"`
		Branch             *string   `json:"branch"`
		ScanPaths          *[]string `json:"scan_paths"`
		VaultPassword      *string   `json:"vault_password"`
		ClearVaultPassword bool      `json:"clear_vault_password"`
		HostKeyChecking    *string   `json:"host_key_checking"`
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
	// vault password: set only when a non-empty value is provided; clear on request
	if req.ClearVaultPassword {
		repo.VaultPassword = ""
	} else if req.VaultPassword != nil && *req.VaultPassword != "" {
		repo.VaultPassword = *req.VaultPassword
	}
	if req.HostKeyChecking != nil {
		switch *req.HostKeyChecking {
		case "", "accept-new", "disabled":
			repo.HostKeyChecking = *req.HostKeyChecking
		default:
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid host_key_checking: %s", *req.HostKeyChecking))
			return
		}
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
	writeJSON(w, http.StatusOK, redactRepo(repo))
}

func (s *Server) getRepo(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Mgr.Store.GetRepo(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, redactRepo(repo))
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
	writeJSON(w, http.StatusOK, redactRepo(repo))
}

// slimPlay is a Play with task arrays stripped; metadata needed for the
// playbook list and run-modal prompts (hosts, play-level tags, vars_prompt) is
// preserved. Per-task detail is available on demand via GET /api/repos/{id}/playbook.
type slimPlay struct {
	Name          string            `json:"name"`
	Hosts         string            `json:"hosts"`
	Become        bool              `json:"become,omitempty"`
	Serial        string            `json:"serial,omitempty"`
	Strategy      string            `json:"strategy,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	Vars          map[string]any    `json:"vars,omitempty"`
	VarsFiles     []string          `json:"vars_files,omitempty"`
	VarsPrompt    []model.PromptVar `json:"vars_prompt,omitempty"`
	Roles         []string          `json:"roles,omitempty"`
	Import        string            `json:"import,omitempty"`
	TasksCount    int               `json:"tasks_count,omitempty"`
	PreTasksCount int               `json:"pre_tasks_count,omitempty"`
	HandlersCount int               `json:"handlers_count,omitempty"`
}

// slimPlaybook is Playbook with slimmed plays (no task trees).
type slimPlaybook struct {
	Path        string     `json:"path"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Plays       []slimPlay `json:"plays"`
}

// slimRole is Role without tasks, handlers, defaults and vars maps. Counts
// and lightweight metadata are preserved so the role-list cards stay accurate.
type slimRole struct {
	Name          string   `json:"name"`
	Path          string   `json:"path"`
	Description   string   `json:"description,omitempty"`
	Dependencies  []string `json:"dependencies,omitempty"`
	TasksCount    int      `json:"tasks_count"`
	HandlersCount int      `json:"handlers_count,omitempty"`
	Templates     []string `json:"templates,omitempty"`
	Files         []string `json:"files,omitempty"`
}

// slimScanResult is ScanResult without deep task trees (see GET /scan?slim=1).
// Inventories are carried unchanged — they contain no task data.
type slimScanResult struct {
	Playbooks   []slimPlaybook    `json:"playbooks"`
	Roles       []slimRole        `json:"roles"`
	Inventories []model.Inventory `json:"inventories"`
}

// toSlimScan projects a full ScanResult down to a slimScanResult, omitting
// task arrays (Tasks, PreTasks, PostTasks, Handlers inside plays; Tasks,
// Handlers, Defaults, Vars inside roles). Derived counts replace the arrays.
func toSlimScan(res *model.ScanResult) slimScanResult {
	pbs := make([]slimPlaybook, 0, len(res.Playbooks))
	for _, pb := range res.Playbooks {
		slim := slimPlaybook{Path: pb.Path, Name: pb.Name, Description: pb.Description, Plays: make([]slimPlay, 0, len(pb.Plays))}
		for _, p := range pb.Plays {
			slim.Plays = append(slim.Plays, slimPlay{
				Name:          p.Name,
				Hosts:         p.Hosts,
				Become:        p.Become,
				Serial:        p.Serial,
				Strategy:      p.Strategy,
				Tags:          p.Tags,
				Vars:          p.Vars,
				VarsFiles:     p.VarsFiles,
				VarsPrompt:    p.VarsPrompt,
				Roles:         p.Roles,
				Import:        p.Import,
				TasksCount:    len(p.Tasks),
				PreTasksCount: len(p.PreTasks),
				HandlersCount: len(p.Handlers),
			})
		}
		pbs = append(pbs, slim)
	}
	roles := make([]slimRole, 0, len(res.Roles))
	for _, r := range res.Roles {
		roles = append(roles, slimRole{
			Name:          r.Name,
			Path:          r.Path,
			Description:   r.Description,
			Dependencies:  r.Dependencies,
			TasksCount:    r.TasksCount,
			HandlersCount: len(r.Handlers),
			Templates:     r.Templates,
			Files:         r.Files,
		})
	}
	invs := res.Inventories
	if invs == nil {
		invs = []model.Inventory{}
	}
	return slimScanResult{Playbooks: pbs, Roles: roles, Inventories: invs}
}

func (s *Server) scanRepo(w http.ResponseWriter, r *http.Request) {
	res, err := s.Mgr.Scan(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	// ?slim=1: strip task trees, return metadata + counters only.
	// The full result (backward-compatible default) remains at the same URL
	// without the parameter so existing integrations are unaffected.
	if r.URL.Query().Get("slim") == "1" {
		writeJSON(w, http.StatusOK, toSlimScan(res))
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

// playbookDetail returns the full scan detail (plays + task trees) of a single
// playbook, identified by its repo-relative path (?path=). The path is confined
// via the same idiom as /file: reject ".." traversals and absolute paths, then
// verify the path exists within the scan result (which was itself confined to
// the repo workdir at scan time).
func (s *Server) playbookDetail(w http.ResponseWriter, r *http.Request) {
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
	// Confinement: reject traversal components and absolute paths.
	// filepath.FromSlash normalises cross-platform; then we re-check the
	// cleaned segments to catch any residual ".." or rooted paths.
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if filepath.IsAbs(rel) || strings.Contains(rel, "..") || strings.HasPrefix(cleaned, "..") {
		writeErr(w, http.StatusBadRequest, errors.New("invalid path"))
		return
	}
	_ = repo // verified above; scan is keyed by ID
	res, err := s.Mgr.Scan(repo.ID)
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	for i := range res.Playbooks {
		if res.Playbooks[i].Path == rel {
			pb := res.Playbooks[i]
			if pb.Plays == nil {
				pb.Plays = []model.Play{}
			}
			writeJSON(w, http.StatusOK, pb)
			return
		}
	}
	writeErr(w, http.StatusNotFound, fmt.Errorf("playbook %q not found", rel))
}

// roleDetail returns the full scan detail (tasks, handlers, defaults, vars) of
// a single role, identified by its name (?name=).
func (s *Server) roleDetail(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Mgr.Store.GetRepo(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	_ = repo
	res, err := s.Mgr.Scan(repo.ID)
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	for i := range res.Roles {
		if res.Roles[i].Name == name {
			writeJSON(w, http.StatusOK, res.Roles[i])
			return
		}
	}
	writeErr(w, http.StatusNotFound, fmt.Errorf("role %q not found", name))
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

// maxLogReplay caps how much of a job log the SSE endpoint replays into memory
// when a client connects. Without a bound, os.ReadFile slurps the entire log
// into RAM per connected client, and a verbose job (ansible with -vvv over many
// hosts routinely emits hundreds of MiB) turns each /events subscriber into a
// memory-exhaustion DoS. We replay only the tail of the log; the full,
// unbounded log is still available at GET /api/jobs/{id}/log, which streams
// straight from disk via http.ServeFile and never buffers the whole file.
const maxLogReplay = 256 << 10 // 256 KiB of recent scrollback

// tailLogFile returns at most maxBytes read from the end of the file at path.
// When the file exceeds maxBytes it reports truncated=true and drops the
// leading partial line so replay always starts on a clean line boundary. It
// bounds the allocation to maxBytes regardless of the file's real size, so a
// single pathologically long line cannot blow the cap either.
func tailLogFile(path string, maxBytes int64) (data []byte, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if fi.Size() <= maxBytes {
		data, err = io.ReadAll(io.LimitReader(f, maxBytes))
		return data, false, err
	}
	if _, err = f.Seek(fi.Size()-maxBytes, io.SeekStart); err != nil {
		return nil, false, err
	}
	if data, err = io.ReadAll(io.LimitReader(f, maxBytes)); err != nil {
		return nil, false, err
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		data = data[i+1:]
	}
	return data, true, nil
}

// withinRoot reports whether the absolute path p is root itself or lives under
// it, used to confine file access to a repo's working directory.
func withinRoot(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(os.PathSeparator))
}

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
	if err1 != nil || err2 != nil || !withinRoot(rootAbs, fullAbs) {
		writeErr(w, http.StatusBadRequest, errors.New("invalid path"))
		return
	}
	// Resolve symlinks and re-check: a symlink inside the workdir must not point
	// outside it (guards against a repo shipping a link to /etc/passwd).
	realRoot, err1 := filepath.EvalSymlinks(rootAbs)
	realFull, err2 := filepath.EvalSymlinks(fullAbs)
	if err1 != nil || err2 != nil || !withinRoot(realRoot, realFull) {
		writeErr(w, http.StatusNotFound, errors.New("file not found"))
		return
	}
	fullAbs = realFull
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
	// fall back to the repo's stored vault password when none was supplied
	if req.VaultPassword == "" {
		req.VaultPassword = repo.VaultPassword
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
	limit := atoiDefault(r.URL.Query().Get("limit"), 0) // 0 = all (back-compat)
	offset := atoiDefault(r.URL.Query().Get("offset"), 0)
	jobs, total := s.Mgr.Store.ListJobsPage(limit, offset)
	if jobs == nil {
		jobs = []model.Job{}
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	writeJSON(w, http.StatusOK, jobs)
}

// atoiDefault parses s as an int, falling back to def on empty/invalid input.
func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
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
	// Persist only non-secret extra vars on the job (for Re-run prefill); the
	// full set, including any secrets, is still passed to ansible transiently.
	req.Vars = storableVars(body.Vars)
	job, err := s.Mgr.StartJob(req, runner.RunOpts{VaultPassword: body.VaultPassword, ExtraVars: body.Vars})
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

// storableVars drops secret-looking keys so they are never persisted on a job
// (they must be re-entered on a Re-run); returns nil when nothing remains.
func storableVars(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := map[string]any{}
	for k, v := range in {
		if plan.IsSecretKey(k) {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

	// Replay only the tail of the log (see maxLogReplay) so a huge/verbose job
	// cannot exhaust memory per subscriber. Live lines beyond the tail keep
	// arriving on ch below.
	if data, truncated, err := tailLogFile(s.Mgr.Store.JobLogPath(id), maxLogReplay); err == nil && len(data) > 0 {
		if truncated {
			// Surface the drop to the operator using the existing "line" event —
			// this does not change the SSE event format, just prepends one
			// clearly-marked synthetic line so the truncation is visible in the
			// log viewer (an invisible SSE ": comment" would leave the user
			// unaware that older output is missing).
			sendLine(fmt.Sprintf("[pine] earlier output truncated — showing last %d KiB; full log at /api/jobs/%s/log", maxLogReplay>>10, id))
		}
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
	// Personalise the shell + manifest with the operator's instance label so
	// multiple Pine PWAs on one machine are distinguishable.
	if path == "index.html" || strings.HasSuffix(path, ".webmanifest") {
		data = stampLabel(data, path, s.Label)
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
	case strings.HasSuffix(path, ".png"):
		w.Header().Set("Content-Type", "image/png")
	case strings.HasSuffix(path, ".webmanifest"):
		w.Header().Set("Content-Type", "application/manifest+json")
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
	out.Redact() // never leak inventory secrets/vault blobs in the JSON
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

// --- probes ---

func (s *Server) listProbes(w http.ResponseWriter, r *http.Request) {
	probes := runner.Probes()
	writeJSON(w, http.StatusOK, map[string]any{"count": len(probes), "probes": probes})
}

// runProbe launches a read-only probe. The body names a probe by catalog ID;
// there is deliberately no way to pass a command string.
func (s *Server) runProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Probe     string `json:"probe"`
		Inventory string `json:"inventory"`
		Limit     string `json:"limit"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	job, err := s.Mgr.RunProbe(r.PathValue("id"), req.Probe, req.Inventory, req.Limit)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
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
