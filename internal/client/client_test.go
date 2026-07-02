package client

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/runner"
	"github.com/jgsqware/pine/internal/server"
	"github.com/jgsqware/pine/internal/store"
	"github.com/jgsqware/pine/internal/tui"
)

// the HTTP client must satisfy the surface the TUI drives.
var _ tui.Engine = (*Client)(nil)

// TestClientRoundTrip drives a real Pine server over HTTP through the client,
// covering the read paths `pine attach` relies on.
func TestClientRoundTrip(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr := runner.New(st)
	abs, _ := filepath.Abs("../../examples/demo-infra")
	repo := model.Repo{ID: store.NewID("r"), Name: "demo-infra", Path: abs, Status: model.RepoNew}
	if err := st.AddRepo(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SyncRepo(repo.ID); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(server.New(mgr, server.Config{}))
	defer srv.Close()
	c := New(srv.URL)

	if err := c.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	repos := c.ListRepos()
	if len(repos) != 1 || repos[0].ID != repo.ID {
		t.Fatalf("ListRepos = %+v", repos)
	}

	res, err := c.Scan(repo.ID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Playbooks) == 0 {
		t.Fatal("scan returned no playbooks")
	}

	// Plan the first playbook end-to-end through the API.
	out, err := c.Plan(repo, res.Playbooks[0].Path)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if out.Playbook == "" {
		t.Fatal("plan result missing playbook")
	}

	// A non-existent repo should surface the server's error.
	if _, err := c.Scan("r_nope"); err == nil {
		t.Fatal("expected error scanning unknown repo")
	}
}
