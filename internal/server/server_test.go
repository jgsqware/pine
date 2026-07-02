package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/runner"
	"github.com/jgsqware/pine/internal/store"
)

// newTestServer wires a server backed by a temp store with the bundled
// demo-infra repo registered as a local-path repository.
func newTestServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	mgr := runner.New(st)

	_, file, _, _ := runtime.Caller(0)
	demo := filepath.Join(filepath.Dir(file), "..", "..", "examples", "demo-infra")
	repo := model.Repo{ID: store.NewID("r"), Name: "demo-infra", Path: demo, Status: model.RepoNew}
	if err := st.AddRepo(repo); err != nil {
		t.Fatalf("add repo: %v", err)
	}
	if _, err := mgr.SyncRepo(repo.ID); err != nil {
		t.Fatalf("sync repo: %v", err)
	}
	// SyncRepo scans in a background goroutine that writes into the temp data
	// dir; wait for it to settle so t.TempDir() cleanup doesn't race the writer.
	for i := 0; i < 200; i++ {
		if r, _ := st.GetRepo(repo.ID); r.Status == model.RepoReady || r.Status == model.RepoError {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return New(mgr, Config{}), repo.ID
}

func TestRepoFile(t *testing.T) {
	h, repoID := newTestServer(t)

	get := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/repos/"+repoID+"/file"+query, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	t.Run("serves a playbook file", func(t *testing.T) {
		rec := get("?path=site.yml")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("content-type = %q, want text/plain", ct)
		}
		if !strings.Contains(rec.Body.String(), "import_playbook") {
			t.Errorf("site.yml body does not look like the real file:\n%s", rec.Body.String())
		}
	})

	t.Run("serves a nested role file", func(t *testing.T) {
		rec := get("?path=roles/docker/tasks/main.yml")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("rejects path traversal", func(t *testing.T) {
		if rec := get("?path=../../../../etc/passwd"); rec.Code != http.StatusBadRequest {
			t.Errorf("traversal status = %d, want 400", rec.Code)
		}
	})

	t.Run("404 on a directory", func(t *testing.T) {
		if rec := get("?path=roles"); rec.Code != http.StatusNotFound {
			t.Errorf("dir status = %d, want 404", rec.Code)
		}
	})

	t.Run("400 without a path", func(t *testing.T) {
		if rec := get(""); rec.Code != http.StatusBadRequest {
			t.Errorf("missing-path status = %d, want 400", rec.Code)
		}
	})
}

// TestLineageRedaction guards the regression the audit found: the /lineage
// endpoint must mask inventory secrets (vault blobs, password-like scalars)
// exactly like /resolve does. The demo's db group carries password variables.
func TestLineageRedaction(t *testing.T) {
	h, repoID := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/repos/"+repoID+"/lineage?inventory=production&host=db-primary", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("lineage status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// No raw secret material may appear in the JSON.
	for _, leak := range []string{"$ANSIBLE_VAULT", "CHANGEME"} {
		if strings.Contains(body, leak) {
			t.Errorf("lineage response leaks %q:\n%s", leak, body)
		}
	}
	// And the redaction must actually have fired on this dataset, otherwise the
	// test would pass even if the handler forgot to call Redact().
	if !strings.Contains(body, "***REDACTED***") {
		t.Errorf("expected at least one redacted secret in db-primary lineage; body:\n%s", body)
	}
}

func TestServicesEndpoint(t *testing.T) {
	h, repoID := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/repos/"+repoID+"/services", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET services status = %d, want 200", rec.Code)
	}
	// The demo homelab declares services (teamcity-agent, docker, …); the report
	// must auto-pick that inventory and list them even before any check.
	body := rec.Body.String()
	for _, want := range []string{`"inventory":"homelab"`, "teamcity-agent", "docker", `"services"`, `"cells"`} {
		if !strings.Contains(body, want) {
			t.Errorf("services report missing %q in:\n%s", want, body)
		}
	}
}
