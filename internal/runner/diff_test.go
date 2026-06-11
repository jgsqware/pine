package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/store"
)

const logA = `PLAY [Web] *********
TASK [nginx : Install nginx] *********
ok: [web01]
ok: [web02]
TASK [nginx : Deploy config] *********
ok: [web01]
ok: [web02]
TASK [Old task] *********
ok: [web01]
PLAY RECAP *********
`

const logB = `PLAY [Web] *********
TASK [nginx : Install nginx] *********
ok: [web01]
changed: [web02]
TASK [nginx : Deploy config] *********
ok: [web01]
fatal: [web02]: FAILED! => {"msg": "boom"}
TASK [Brand new task] *********
ok: [web01]
PLAY RECAP *********
web01 : ok=3 changed=0 unreachable=0 failed=0 skipped=0
`

func TestDiffJobs(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := New(st)

	mk := func(id, log string) model.Job {
		j := model.Job{ID: id, RepoID: "r_1", Playbook: "web.yml", Status: model.JobSuccess,
			Created: id}
		if err := st.SaveJob(j); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(st.JobLogPath(id), []byte(log), 0o644); err != nil {
			t.Fatal(err)
		}
		return j
	}
	mk("j_old", logA)
	mk("j_new", logB)

	out, err := m.DiffJobs("j_new", "j_old")
	if err != nil {
		t.Fatal(err)
	}
	if out.A.ID != "j_old" || out.B.ID != "j_new" {
		t.Errorf("a/b = %s/%s", out.A.ID, out.B.ID)
	}
	byName := map[string]TaskDiff{}
	for _, td := range out.Tasks {
		byName[td.Name] = td
	}

	install := byName["nginx : Install nginx"]
	if len(install.Changes) != 1 || install.Changes[0].Host != "web02" ||
		install.Changes[0].A != "ok" || install.Changes[0].B != "changed" {
		t.Errorf("install diff = %+v", install)
	}
	deploy := byName["nginx : Deploy config"]
	if len(deploy.Changes) != 1 || deploy.Changes[0].B != "failed" {
		t.Errorf("deploy diff = %+v", deploy)
	}
	if byName["Brand new task"].OnlyIn != "b" {
		t.Errorf("new task: %+v", byName["Brand new task"])
	}
	if byName["Old task"].OnlyIn != "a" {
		t.Errorf("removed task: %+v", byName["Old task"])
	}
	// ok->changed counts regressed, ok->failed counts regressed
	if out.Summary.Regressed != 2 || out.Summary.NewTasks != 1 || out.Summary.RemovedTasks != 1 {
		t.Errorf("summary = %+v", out.Summary)
	}

	// different playbooks must be rejected
	j3 := model.Job{ID: "j_other", RepoID: "r_1", Playbook: "db.yml", Status: model.JobSuccess, Created: "x"}
	_ = st.SaveJob(j3)
	_ = os.WriteFile(filepath.Join(dir, "jobs", "j_other.log"), []byte(logA), 0o644)
	if _, err := m.DiffJobs("j_new", "j_other"); err == nil {
		t.Error("different playbooks should error")
	}
}
