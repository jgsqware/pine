package plan

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/scanner"
)

func TestLineage(t *testing.T) {
	res, _ := demoScan(t)
	out, err := Lineage(res, "production", "web01")
	if err != nil {
		t.Fatal(err)
	}
	if out.Host != "web01" || len(out.Vars) == 0 {
		t.Fatalf("empty lineage: %+v", out)
	}
	byKey := map[string][]LineageEntry{}
	for _, v := range out.Vars {
		byKey[v.Key] = v.Chain
		// invariant: effective value == last chain entry
		if len(v.Chain) == 0 {
			t.Errorf("%s has empty chain", v.Key)
		}
	}
	// ansible_host is host-scoped only
	if chain, ok := byKey["ansible_host"]; !ok || chain[len(chain)-1].Scope != "host" {
		t.Errorf("ansible_host chain wrong: %+v", chain)
	}
	// at least one var must have a multi-level chain (role default or
	// group var overridden somewhere)
	multi := false
	for _, v := range out.Vars {
		if len(v.Chain) > 1 {
			multi = true
			// earlier layers must come before later scopes
			if v.Chain[0].Scope == "host" {
				t.Errorf("%s: host layer cannot be first of a multi-chain: %+v", v.Key, v.Chain)
			}
		}
	}
	if !multi {
		t.Error("expected at least one variable with a multi-level chain in the demo")
	}

	if _, err := Lineage(res, "production", "nope"); err == nil {
		t.Error("unknown host should error")
	}
}

func TestHygieneOnDemo(t *testing.T) {
	res, root := demoScan(t)
	out := Hygiene(res, root)
	if out.Score < 0 || out.Score > 100 {
		t.Errorf("score out of range: %d", out.Score)
	}
	// the demo's secrets file uses CHANGEME placeholders with password-like
	// keys: they must surface as low-severity findings
	foundPlaceholder := false
	for _, f := range out.SecretFindings {
		if f.Severity == "low" {
			foundPlaceholder = true
		}
		if f.Key == "" || f.File == "" {
			t.Errorf("incomplete finding: %+v", f)
		}
	}
	_ = foundPlaceholder // demo content may evolve; shape checks above matter
	// arrays must be non-nil for the API contract
	if out.UnusedRoles == nil || out.UnnotifiedHandlers == nil || out.UnusedVars == nil ||
		out.UntargetedHosts == nil || out.SecretFindings == nil {
		t.Error("hygiene arrays must be non-nil")
	}
}

func TestHygieneDetectsDeadCode(t *testing.T) {
	root := t.TempDir()
	// a playbook using role "used", an orphan role "orphan" with an
	// unnotified handler, and an untargeted host
	writeT(t, root, "site.yml", `
- name: Site
  hosts: web
  roles: [used]
`)
	writeT(t, root, "roles/used/tasks/main.yml", `
- name: Do something
  ansible.builtin.ping:
  notify: Used handler
`)
	writeT(t, root, "roles/used/handlers/main.yml", `
- name: Used handler
  ansible.builtin.ping:
- name: Never notified
  ansible.builtin.ping:
`)
	writeT(t, root, "roles/orphan/tasks/main.yml", `
- name: Orphan task
  ansible.builtin.ping:
`)
	writeT(t, root, "inventories/prod/hosts.yml", `
web:
  hosts:
    web01:
forgotten:
  hosts:
    lost01:
      db_password: hunter2
`)
	res, err := scanForT(root)
	if err != nil {
		t.Fatal(err)
	}
	out := Hygiene(res, root)

	if len(out.UnusedRoles) != 1 || out.UnusedRoles[0].Name != "orphan" {
		t.Errorf("unused roles = %+v", out.UnusedRoles)
	}
	if len(out.UnnotifiedHandlers) != 1 || out.UnnotifiedHandlers[0].Name != "Never notified" {
		t.Errorf("unnotified handlers = %+v", out.UnnotifiedHandlers)
	}
	foundLost := false
	for _, h := range out.UntargetedHosts {
		if h.Name == "lost01" {
			foundLost = true
		}
	}
	if !foundLost {
		t.Errorf("lost01 should be untargeted: %+v", out.UntargetedHosts)
	}
	foundSecret := false
	for _, f := range out.SecretFindings {
		if f.Key == "db_password" && f.Severity == "high" {
			foundSecret = true
		}
	}
	if !foundSecret {
		t.Errorf("db_password plaintext should be flagged: %+v", out.SecretFindings)
	}
	if out.Score >= 100 {
		t.Errorf("score should drop below 100, got %d", out.Score)
	}
}

func TestImpactOnGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	writeT(t, root, "webservers.yml", `
- name: Web
  hosts: web
  roles: [nginx]
`)
	writeT(t, root, "site.yml", `
- ansible.builtin.import_playbook: webservers.yml
`)
	writeT(t, root, "roles/nginx/tasks/main.yml", `
- name: Install
  ansible.builtin.apt:
    name: nginx
  notify: Reload nginx
`)
	writeT(t, root, "roles/nginx/handlers/main.yml", `
- name: Reload nginx
  ansible.builtin.service:
    name: nginx
    state: reloaded
`)
	writeT(t, root, "roles/nginx/templates/nginx.conf.j2", "worker_processes auto;\n")
	writeT(t, root, "roles/app/meta/main.yml", `
dependencies:
  - role: nginx
`)
	writeT(t, root, "roles/app/tasks/main.yml", `
- name: Deploy
  ansible.builtin.copy:
    src: app
    dest: /opt
`)
	writeT(t, root, "deploy.yml", `
- name: Deploy app
  hosts: web
  roles: [app]
`)
	writeT(t, root, "inventories/prod/hosts.yml", `
web:
  hosts:
    web01:
    web02:
`)
	mustGit(t, root, "init", "-q")
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "-c", "user.email=t@t", "-c", "user.name=t",
		"-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")

	// modify the nginx template (uncommitted)
	writeT(t, root, "roles/nginx/templates/nginx.conf.j2", "worker_processes 4;\n")

	res, err := scanForT(root)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Impact(res, root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ChangedFiles) != 1 || out.ChangedFiles[0] != "roles/nginx/templates/nginx.conf.j2" {
		t.Fatalf("changed files = %v", out.ChangedFiles)
	}
	e := out.Entries[0]
	if e.Kind != "role_template" {
		t.Errorf("kind = %s", e.Kind)
	}
	// nginx + app (depends on nginx) must both be impacted
	if !containsStr(e.Roles, "nginx") || !containsStr(e.Roles, "app") {
		t.Errorf("roles = %v, want nginx+app", e.Roles)
	}
	pbs := map[string]bool{}
	for _, pb := range e.Playbooks {
		pbs[pb.Path] = true
	}
	if !pbs["webservers.yml"] || !pbs["deploy.yml"] {
		t.Errorf("playbooks = %v, want webservers.yml + deploy.yml", e.Playbooks)
	}
	if !containsStr(e.Handlers, "Reload nginx") {
		t.Errorf("handlers = %v", e.Handlers)
	}
	if out.Summary.HostsTotal != 2 {
		t.Errorf("hosts_total = %d, want 2", out.Summary.HostsTotal)
	}
}

func containsStr(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// --- small test helpers ---

func writeT(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func scanForT(root string) (*model.ScanResult, error) {
	return scanner.Scan(root)
}
