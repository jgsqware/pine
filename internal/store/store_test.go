package store

import (
	"testing"

	"github.com/jgsqware/pine/internal/model"
)

func TestJobIndexAndPagination(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// created oldest → newest; ListJobs must return newest first
	jobs := []model.Job{
		{ID: "j1", Playbook: "a.yml", Status: model.JobSuccess, Created: "2026-07-01T10:00:00Z"},
		{ID: "j2", Playbook: "b.yml", Status: model.JobRunning, Created: "2026-07-02T10:00:00Z"},
		{ID: "j3", Playbook: "c.yml", Status: model.JobPending, Created: "2026-07-03T10:00:00Z"},
	}
	for _, j := range jobs {
		if err := st.SaveJob(j); err != nil {
			t.Fatal(err)
		}
	}

	all := st.ListJobs()
	if len(all) != 3 || all[0].ID != "j3" || all[2].ID != "j1" {
		t.Fatalf("ListJobs order wrong: %+v", all)
	}

	// pagination window
	page, total := st.ListJobsPage(1, 1)
	if total != 3 || len(page) != 1 || page[0].ID != "j2" {
		t.Errorf("ListJobsPage(1,1) = %+v total=%d", page, total)
	}
	if empty, _ := st.ListJobsPage(5, 10); len(empty) != 0 {
		t.Errorf("offset past end should be empty, got %+v", empty)
	}

	// counts without disk I/O
	running, tot := st.JobCounts()
	if running != 2 || tot != 3 {
		t.Errorf("JobCounts = (%d,%d), want (2,3)", running, tot)
	}

	// GetJob from the index; update reflects immediately
	if _, err := st.GetJob("j2"); err != nil {
		t.Errorf("GetJob(j2): %v", err)
	}
	if _, err := st.GetJob("nope"); err != ErrNotFound {
		t.Errorf("GetJob(nope) err = %v, want ErrNotFound", err)
	}
	updated := jobs[1]
	updated.Status = model.JobSuccess
	if err := st.SaveJob(updated); err != nil {
		t.Fatal(err)
	}
	if running, _ := st.JobCounts(); running != 1 {
		t.Errorf("after finishing j2, running = %d, want 1", running)
	}

	// reopen: the index is rebuilt from disk
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if reloaded := st2.ListJobs(); len(reloaded) != 3 {
		t.Errorf("after reopen ListJobs = %d jobs, want 3", len(reloaded))
	}
}
