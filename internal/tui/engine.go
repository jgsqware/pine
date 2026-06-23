package tui

import (
	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
	"github.com/jgsqware/pine/internal/runner"
)

// Engine is the data and control surface the TUI drives. It is satisfied both
// by the in-process runner.Manager (pine tui, owns the data dir) and by an
// HTTP client that attaches to a running daemon (pine attach), so the same UI
// works locally or against a server without opening a second engine on the
// shared store.
type Engine interface {
	ListRepos() []model.Repo
	ListJobs() []model.Job
	Scan(repoID string) (*model.ScanResult, error)
	SyncRepo(repoID string) (model.Repo, error)
	StartJob(req model.Job) (model.Job, error)
	// Subscribe streams live log lines for a running job. live is false when the
	// job is not running (already finished or unknown); callers then fall back
	// to JobLog for the stored output.
	Subscribe(jobID string) (ch chan string, live bool)
	JobLog(jobID string) (string, error)
	Plan(repo model.Repo, playbook string) (*plan.Result, error)
}

// localEngine adapts an in-process runner.Manager to the Engine interface.
type localEngine struct{ mgr *runner.Manager }

// NewLocalEngine wraps an in-process manager so the TUI can drive it directly.
func NewLocalEngine(mgr *runner.Manager) Engine { return localEngine{mgr} }

func (e localEngine) ListRepos() []model.Repo                  { return e.mgr.Store.ListRepos() }
func (e localEngine) ListJobs() []model.Job                    { return e.mgr.Store.ListJobs() }
func (e localEngine) Scan(id string) (*model.ScanResult, error) { return e.mgr.Scan(id) }
func (e localEngine) SyncRepo(id string) (model.Repo, error)   { return e.mgr.SyncRepo(id) }
func (e localEngine) StartJob(req model.Job) (model.Job, error) { return e.mgr.StartJob(req) }
func (e localEngine) Subscribe(id string) (chan string, bool)  { return e.mgr.Subscribe(id) }

func (e localEngine) JobLog(id string) (string, error) {
	return readFile(e.mgr.Store.JobLogPath(id))
}

func (e localEngine) Plan(repo model.Repo, playbook string) (*plan.Result, error) {
	res, err := e.mgr.Scan(repo.ID)
	if err != nil {
		return nil, err
	}
	return plan.Compute(res, e.mgr.Store.RepoWorkdir(&repo), repo, plan.Request{
		RepoID: repo.ID, Playbook: playbook,
	})
}
