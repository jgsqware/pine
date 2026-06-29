package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/store"
)

// servicesManager builds a manager with an inventory that declares `services:`
// on its hosts (host-level and group-level), plus one repo registered ready.
func servicesManager(t *testing.T) *Manager {
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
	write("site.yml", "- name: Site\n  hosts: all\n  tasks:\n    - ansible.builtin.ping:\n")
	// app1 declares services inline; db1 inherits from its group's group_vars.
	write("inventories/prod/hosts.yml",
		"all:\n  children:\n    app:\n      hosts:\n        app1:\n          services: [teamcity-agent, docker]\n    db:\n      hosts:\n        db1: {}\n")
	write("inventories/prod/group_vars/db.yml", "services: [postgresql]\n")

	st, err := store.Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(st)
	repo := model.Repo{ID: "r_svc", Name: "svc", Path: repoDir, Status: model.RepoReady}
	if err := st.AddRepo(repo); err != nil {
		t.Fatal(err)
	}
	return m
}

// runServicesSimulated drives the simulated harvest directly (ansible-playbook
// may be installed in the test env, which would otherwise pick the real path).
func runServicesSimulated(t *testing.T, m *Manager) {
	t.Helper()
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	r := &run{subs: map[chan string]bool{}, file: devnull}
	job := &model.Job{RepoID: "r_svc", Playbook: ServicesJobName, Simulated: true}
	if failed := m.runServices(context.Background(), job, r); failed {
		t.Fatal("simulated service harvest reported failure")
	}
}

func TestServiceStatusUnknownBeforeCheck(t *testing.T) {
	m := servicesManager(t)
	rep, err := m.ServiceStatus("r_svc", "")
	if err != nil {
		t.Fatal(err)
	}
	// Services are known from the declared vars even before any harvest…
	want := []string{"docker", "postgresql", "teamcity-agent"}
	if got := rep.Services; len(got) != 3 || got[0] != want[0] || got[2] != want[2] {
		t.Fatalf("services = %v, want %v", got, want)
	}
	if len(rep.Hosts) != 2 {
		t.Fatalf("hosts = %v, want app1+db1", rep.Hosts)
	}
	// …but every cell is unknown (nothing gathered yet), nothing counted running.
	if rep.Summary.Running != 0 || rep.Summary.Down != 0 {
		t.Fatalf("pre-check summary running=%d down=%d, want 0/0", rep.Summary.Running, rep.Summary.Down)
	}
	if c := rep.Cells["teamcity-agent"]["app1"]; c.State != model.ServiceUnknown {
		t.Fatalf("teamcity-agent@app1 = %q, want unknown", c.State)
	}
}

func TestServiceStatusAfterSimulatedCheck(t *testing.T) {
	m := servicesManager(t)
	runServicesSimulated(t, m)

	rep, err := m.ServiceStatus("r_svc", "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Watched != 3 || rep.Summary.Hosts != 2 {
		t.Fatalf("summary watched=%d hosts=%d, want 3/2", rep.Summary.Watched, rep.Summary.Hosts)
	}
	// db1 inherits postgresql from group_vars — it must resolve to a real state.
	c := rep.Cells["postgresql"]["db1"]
	if c.State != model.ServiceRunning && c.State != model.ServiceStopped {
		t.Fatalf("postgresql@db1 = %q, want running|stopped (group var not resolved?)", c.State)
	}
	if c.Unit != "postgresql.service" {
		t.Fatalf("postgresql unit = %q, want postgresql.service", c.Unit)
	}
	// Every declared cell must now be running or stopped (none unknown).
	for _, svc := range rep.Services {
		for _, h := range rep.Hosts {
			cell, ok := rep.Cells[svc][h]
			if !ok {
				continue // host doesn't declare this service (n/a)
			}
			if cell.State == model.ServiceUnknown {
				t.Errorf("%s@%s still unknown after harvest", svc, h)
			}
		}
	}
	if rep.Summary.Running+rep.Summary.Down == 0 {
		t.Fatal("no running/down cells recorded after harvest")
	}
}

func TestServiceHelpers(t *testing.T) {
	if got := canonService("TeamCity-Agent.service"); got != "teamcity-agent" {
		t.Errorf("canonService = %q", got)
	}
	if got := canonUnit("docker"); got != "docker.service" {
		t.Errorf("canonUnit = %q", got)
	}
	if got := canonUnit("pi-hole.service"); got != "pi-hole.service" {
		t.Errorf("canonUnit kept = %q", got)
	}
	for in, want := range map[string]string{
		"running": model.ServiceRunning, "stopped": model.ServiceStopped,
		"dead": model.ServiceStopped, "inactive": model.ServiceStopped, "weird": model.ServiceUnknown,
	} {
		if got := normState(in); got != want {
			t.Errorf("normState(%q) = %q, want %q", in, got, want)
		}
	}
	if normStatus("enabled-runtime") != "enabled" || normStatus("") != "unknown" {
		t.Errorf("normStatus mapping wrong")
	}
}
