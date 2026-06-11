package runner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/store"
)

// newTestManager opens a manager over a temp store with a tiny local repo.
func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	write := func(rel, content string) {
		p := filepath.Join(repoDir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("site.yml", "- name: Site\n  hosts: web\n  tasks:\n    - name: Ping\n      ansible.builtin.ping:\n")
	write("inventories/prod/hosts.yml", "web:\n  hosts:\n    web01:\n      ansible_host: 10.0.0.1\n")

	st, err := store.Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(st)
	repo := model.Repo{ID: "r_test", Name: "test", Path: repoDir, Status: model.RepoReady}
	if err := st.AddRepo(repo); err != nil {
		t.Fatal(err)
	}
	return m, repoDir
}

func waitJob(t *testing.T, m *Manager, id string) model.Job {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		j, err := m.Store.GetJob(id)
		if err == nil && j.Terminal() {
			return j
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return model.Job{}
}

func TestGatherFactsSimulated(t *testing.T) {
	m, _ := newTestManager(t)
	job, err := m.GatherFacts("r_test", "")
	if err != nil {
		t.Fatal(err)
	}
	final := waitJob(t, m, job.ID)
	if final.Status != model.JobSuccess {
		t.Fatalf("status = %s", final.Status)
	}
	facts := m.Store.HostFacts("r_test", "web01")
	if facts == nil || facts["os_family"] != "Debian" {
		t.Errorf("web01 facts = %v", facts)
	}
	metas := m.Store.ListFacts("r_test")
	if len(metas) != 1 || metas["web01"].Keys == 0 {
		t.Errorf("metas = %v", metas)
	}
}

func TestDriftFromCheckJobs(t *testing.T) {
	m, _ := newTestManager(t)
	jobs, err := m.DriftCheck("r_test", nil, "")
	if err != nil || len(jobs) != 1 {
		t.Fatalf("drift check: %v %v", jobs, err)
	}
	waitJob(t, m, jobs[0].ID)

	report, err := m.Drift("r_test")
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.CheckedPlaybooks != 1 || len(report.Playbooks) != 1 {
		t.Fatalf("report = %+v", report.Summary)
	}
	if report.Playbooks[0].Playbook != "site.yml" {
		t.Errorf("playbook = %s", report.Playbooks[0].Playbook)
	}
	if len(report.Hosts) == 0 {
		t.Error("expected hosts in the report")
	}
}

func TestScheduleGateBlocksOnPlanChange(t *testing.T) {
	m, repoDir := newTestManager(t)
	sc, err := m.CreateSchedule(model.Schedule{
		RepoID: "r_test", Playbook: "site.yml", Interval: "1h", Gate: true, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sc.ApprovedFingerprint == "" {
		t.Fatal("creation should approve the current plan")
	}

	// force due now, unchanged plan -> must launch a job
	sc.NextRunAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	_ = m.Store.SaveSchedule(sc)
	m.tickSchedules(time.Now())
	got, _ := m.getSchedule(sc.ID)
	if got.LastRunID == "" || got.Status != "ok" {
		t.Fatalf("expected a run, got %+v", got)
	}
	waitJob(t, m, got.LastRunID)

	// change the playbook -> plan fingerprint changes -> next tick blocks
	extra := "- name: Site\n  hosts: web\n  tasks:\n    - name: Ping\n      ansible.builtin.ping:\n    - name: New task\n      ansible.builtin.command: echo hi\n"
	if err := os.WriteFile(filepath.Join(repoDir, "site.yml"), []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Forget("r_test") // drop the scan cache like a sync would

	got.NextRunAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	_ = m.Store.SaveSchedule(got)
	m.tickSchedules(time.Now())
	got2, _ := m.getSchedule(sc.ID)
	if got2.Status != "blocked" {
		t.Fatalf("expected blocked, got %+v", got2)
	}
	if got2.LastRunID != got.LastRunID {
		t.Error("blocked schedule must not have launched a new job")
	}

	// approve -> unblocks and next tick runs
	approved, err := m.ApproveSchedule(sc.ID)
	if err != nil || approved.Status != "ok" {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	approved.NextRunAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	_ = m.Store.SaveSchedule(approved)
	m.tickSchedules(time.Now())
	got3, _ := m.getSchedule(sc.ID)
	if got3.Status != "ok" || got3.LastRunID == got.LastRunID {
		t.Fatalf("expected a new run after approval, got %+v", got3)
	}
}

func TestPipelineRunWithApprovalGate(t *testing.T) {
	m, _ := newTestManager(t)
	pipe, err := m.CreatePipeline(model.Pipeline{
		Name: "deploy", RepoID: "r_test",
		Steps: []model.PipelineStep{
			{Name: "canary", Playbook: "site.yml", Limit: "web01"},
			{Name: "fleet", Playbook: "site.yml", RequireApproval: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := m.RunPipeline(pipe.ID)
	if err != nil {
		t.Fatal(err)
	}

	// wait for the approval pause
	deadline := time.Now().Add(30 * time.Second)
	for {
		cur, _ := m.Store.GetPipelineRun(run.ID)
		if cur.Status == model.PipeWaiting {
			if cur.Steps[0].Status != "success" || cur.Steps[1].Status != model.PipeWaiting {
				t.Fatalf("unexpected step states: %+v", cur.Steps)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never reached waiting_approval: %+v", cur)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if _, err := m.ApprovePipelineRun(run.ID); err != nil {
		t.Fatal(err)
	}
	for {
		cur, _ := m.Store.GetPipelineRun(run.ID)
		if cur.Status == model.PipeSuccess {
			if cur.Steps[1].Status != "success" || cur.Steps[1].JobID == "" {
				t.Fatalf("step 2 = %+v", cur.Steps[1])
			}
			break
		}
		if cur.Status == model.PipeFailed || time.Now().After(deadline) {
			t.Fatalf("pipeline did not succeed: %+v", cur)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
