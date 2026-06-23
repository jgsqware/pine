package tui

import (
	"strings"
	"testing"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
)

// fakeEngine is a minimal Engine for exercising the TUI's auto-refresh logic
// without a store or HTTP server.
type fakeEngine struct {
	repos     []model.Repo
	syncCalls int
}

func (f *fakeEngine) ListRepos() []model.Repo                       { return f.repos }
func (f *fakeEngine) ListJobs() []model.Job                         { return nil }
func (f *fakeEngine) Scan(string) (*model.ScanResult, error)        { return &model.ScanResult{}, nil }
func (f *fakeEngine) StartJob(model.Job) (model.Job, error)         { return model.Job{}, nil }
func (f *fakeEngine) Subscribe(string) (chan string, bool)          { return nil, false }
func (f *fakeEngine) JobLog(string) (string, error)                 { return "", nil }
func (f *fakeEngine) Plan(model.Repo, string) (*plan.Result, error) { return &plan.Result{}, nil }

func (f *fakeEngine) SyncRepo(id string) (model.Repo, error) {
	f.syncCalls++
	return model.Repo{ID: id, Status: model.RepoSyncing}, nil
}

func TestAutoRefreshNotifies(t *testing.T) {
	fe := &fakeEngine{repos: []model.Repo{{
		ID: "r1", Name: "infra", Status: model.RepoReady, LastSynced: "t1",
		Summary: model.RepoSummary{Playbooks: 15, Roles: 9, Hosts: 32},
	}}}
	a := newApp(fe)

	// First reload only records a baseline — no spurious notice.
	a.reload()
	if a.status != "" {
		t.Fatalf("baseline reload set status = %q, want empty", a.status)
	}

	// triggerSync(true) issues the sync and announces it (the load-time notice).
	a.triggerSync(true)
	if fe.syncCalls != 1 {
		t.Fatalf("triggerSync made %d sync calls, want 1", fe.syncCalls)
	}
	if !strings.Contains(a.status, "refreshing") {
		t.Fatalf("load notice = %q, want a refreshing message", a.status)
	}

	// The sync completes: LastSynced advances. Next reload must announce it with
	// the new summary counts.
	fe.repos[0].LastSynced = "t2"
	a.reload()
	if !strings.Contains(a.status, "refreshed") || !strings.Contains(a.status, "15 playbooks") {
		t.Fatalf("post-sync notice = %q, want a refreshed summary", a.status)
	}

	// A reload with no further change must not re-announce.
	a.status = ""
	a.reload()
	if a.status != "" {
		t.Fatalf("unchanged reload set status = %q, want empty", a.status)
	}
}
