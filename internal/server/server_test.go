package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
	return New(mgr), repo.ID
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
