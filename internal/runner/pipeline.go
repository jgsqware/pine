package runner

import (
	"fmt"
	"time"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/store"
)

// CreatePipeline validates and stores a pipeline definition.
func (m *Manager) CreatePipeline(p model.Pipeline) (model.Pipeline, error) {
	repo, err := m.Store.GetRepo(p.RepoID)
	if err != nil {
		return p, fmt.Errorf("unknown repo: %w", err)
	}
	if p.Name == "" || len(p.Steps) == 0 {
		return p, fmt.Errorf("name and at least one step are required")
	}
	for i, st := range p.Steps {
		if st.Playbook == "" {
			return p, fmt.Errorf("step %d: playbook is required", i+1)
		}
		if st.Name == "" {
			p.Steps[i].Name = st.Playbook
		}
	}
	p.ID = store.NewID("pl")
	p.RepoName = repo.Name
	return p, m.Store.SavePipeline(p)
}

// RunPipeline starts a new run of a pipeline.
func (m *Manager) RunPipeline(id string) (model.PipelineRun, error) {
	var pipe *model.Pipeline
	for _, p := range m.Store.ListPipelines() {
		if p.ID == id {
			cp := p
			pipe = &cp
		}
	}
	if pipe == nil {
		return model.PipelineRun{}, store.ErrNotFound
	}
	run := model.PipelineRun{
		ID:           store.NewID("pr"),
		PipelineID:   pipe.ID,
		PipelineName: pipe.Name,
		Status:       model.PipeRunning,
		Created:      time.Now().UTC().Format(time.RFC3339),
	}
	for _, st := range pipe.Steps {
		run.Steps = append(run.Steps, model.PipelineRunStep{Name: st.Name, Status: "pending"})
	}
	if err := m.Store.SavePipelineRun(run); err != nil {
		return run, err
	}
	go m.advancePipeline(run.ID, *pipe, 0)
	return run, nil
}

// ApprovePipelineRun resumes a run paused on an approval gate.
func (m *Manager) ApprovePipelineRun(id string) (model.PipelineRun, error) {
	run, err := m.Store.GetPipelineRun(id)
	if err != nil {
		return run, err
	}
	if run.Status != model.PipeWaiting {
		return run, fmt.Errorf("run is not waiting for approval")
	}
	idx := -1
	for i, st := range run.Steps {
		if st.Status == model.PipeWaiting {
			idx = i
			break
		}
	}
	if idx < 0 {
		return run, fmt.Errorf("no step waiting for approval")
	}
	var pipe *model.Pipeline
	for _, p := range m.Store.ListPipelines() {
		if p.ID == run.PipelineID {
			cp := p
			pipe = &cp
		}
	}
	if pipe == nil || len(pipe.Steps) != len(run.Steps) {
		return run, fmt.Errorf("pipeline definition changed; cannot resume")
	}
	run.Status = model.PipeRunning
	run.Steps[idx].Status = "pending"
	if err := m.Store.SavePipelineRun(run); err != nil {
		return run, err
	}
	go m.advancePipeline(run.ID, *pipe, idxApproved(idx))
	return run, nil
}

// idxApproved encodes "start at idx with the approval gate already passed".
func idxApproved(idx int) int { return -(idx + 1) }

// CancelPipelineRun stops a run (and the in-flight job, when any).
func (m *Manager) CancelPipelineRun(id string) (model.PipelineRun, error) {
	run, err := m.Store.GetPipelineRun(id)
	if err != nil {
		return run, err
	}
	if run.Status != model.PipeRunning && run.Status != model.PipeWaiting {
		return run, nil
	}
	run.Status = model.PipeCanceled
	run.Finished = time.Now().UTC().Format(time.RFC3339)
	for i := range run.Steps {
		switch run.Steps[i].Status {
		case "running":
			run.Steps[i].Status = "canceled"
			if run.Steps[i].JobID != "" {
				_, _ = m.Cancel(run.Steps[i].JobID)
			}
		case "pending", model.PipeWaiting:
			run.Steps[i].Status = "skipped"
		}
	}
	return run, m.Store.SavePipelineRun(run)
}

// advancePipeline executes steps from index `start` (negative encodes an
// already-approved gate at -(idx+1)). It persists state at every
// transition so the UI can poll.
func (m *Manager) advancePipeline(runID string, pipe model.Pipeline, start int) {
	approved := false
	if start < 0 {
		start = -start - 1
		approved = true
	}
	for i := start; i < len(pipe.Steps); i++ {
		step := pipe.Steps[i]
		run, err := m.Store.GetPipelineRun(runID)
		if err != nil || run.Status == model.PipeCanceled {
			return
		}

		if step.RequireApproval && !approved {
			run.Status = model.PipeWaiting
			run.Steps[i].Status = model.PipeWaiting
			_ = m.Store.SavePipelineRun(run)
			return // resumed by ApprovePipelineRun
		}
		approved = false

		job, err := m.StartJob(model.Job{
			RepoID: pipe.RepoID, Playbook: step.Playbook, Inventory: step.Inventory,
			Limit: step.Limit, Tags: step.Tags, Check: step.Check,
		})
		run.Steps[i].Status = "running"
		run.Steps[i].JobID = job.ID
		if err != nil {
			run.Steps[i].Status = "failed"
			m.failPipeline(&run, i, step.ContinueOnFailure)
			return
		}
		_ = m.Store.SavePipelineRun(run)

		final := m.waitForJob(job.ID)
		run, err = m.Store.GetPipelineRun(runID)
		if err != nil || run.Status == model.PipeCanceled {
			return
		}
		switch final {
		case model.JobSuccess:
			run.Steps[i].Status = "success"
			_ = m.Store.SavePipelineRun(run)
		default:
			run.Steps[i].Status = "failed"
			if step.ContinueOnFailure {
				_ = m.Store.SavePipelineRun(run)
				continue
			}
			m.failPipeline(&run, i, false)
			return
		}
	}
	run, err := m.Store.GetPipelineRun(runID)
	if err != nil || run.Status == model.PipeCanceled {
		return
	}
	run.Status = model.PipeSuccess
	run.Finished = time.Now().UTC().Format(time.RFC3339)
	_ = m.Store.SavePipelineRun(run)
}

func (m *Manager) failPipeline(run *model.PipelineRun, failedIdx int, _ bool) {
	run.Status = model.PipeFailed
	run.Finished = time.Now().UTC().Format(time.RFC3339)
	for i := failedIdx + 1; i < len(run.Steps); i++ {
		if run.Steps[i].Status == "pending" {
			run.Steps[i].Status = "skipped"
		}
	}
	_ = m.Store.SavePipelineRun(*run)
}

// waitForJob polls until the job reaches a terminal state.
func (m *Manager) waitForJob(id string) string {
	for {
		time.Sleep(time.Second)
		j, err := m.Store.GetJob(id)
		if err != nil {
			return model.JobFailed
		}
		if j.Terminal() {
			return j.Status
		}
	}
}
