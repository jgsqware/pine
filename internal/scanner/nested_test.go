package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile creates a file with parents.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const nestedPlaybook = `---
- name: Deploy %s
  hosts: web
  tasks:
    - name: Ping
      ansible.builtin.ping:
`

// Layout reported by a user: playbooks live in
// playbooks/<env>/<application>/<playbook>.yml (or .yaml).
func TestScanNestedPlaybooks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "playbooks/prod/shop/deploy.yml"), "---\n- name: Deploy shop\n  hosts: web\n  tasks:\n    - name: Ping\n      ansible.builtin.ping:\n")
	writeFile(t, filepath.Join(root, "playbooks/prod/billing/deploy.yaml"), "---\n- name: Deploy billing\n  hosts: web\n  tasks:\n    - name: Ping\n      ansible.builtin.ping:\n")
	writeFile(t, filepath.Join(root, "playbooks/staging/shop/deploy.yaml"), "---\n- name: Deploy shop staging\n  hosts: web\n  tasks:\n    - name: Ping\n      ansible.builtin.ping:\n")
	// non-playbook YAML noise that must NOT be picked up
	writeFile(t, filepath.Join(root, "roles/shop/tasks/main.yml"), "---\n- name: A role task\n  ansible.builtin.ping:\n")
	writeFile(t, filepath.Join(root, "group_vars/all.yml"), "---\nfoo: bar\n")
	writeFile(t, filepath.Join(root, ".github/workflows/ci.yml"), "on: push\njobs: {}\n")

	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(res.Playbooks); got != 3 {
		paths := make([]string, 0, got)
		for _, p := range res.Playbooks {
			paths = append(paths, p.Path)
		}
		t.Fatalf("playbooks = %d (%v), want 3", got, paths)
	}
	wantPaths := map[string]bool{
		"playbooks/prod/shop/deploy.yml":     false,
		"playbooks/prod/billing/deploy.yaml": false,
		"playbooks/staging/shop/deploy.yaml": false,
	}
	for _, p := range res.Playbooks {
		if _, ok := wantPaths[p.Path]; !ok {
			t.Errorf("unexpected playbook %s", p.Path)
		}
		wantPaths[p.Path] = true
	}
	for p, found := range wantPaths {
		if !found {
			t.Errorf("missing playbook %s", p)
		}
	}
}

func TestScanPathsRestriction(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "playbooks/prod/shop/deploy.yml"), "---\n- hosts: web\n  tasks:\n    - name: Ping\n      ansible.builtin.ping:\n")
	writeFile(t, filepath.Join(root, "legacy/old.yml"), "---\n- hosts: all\n  tasks:\n    - name: Ping\n      ansible.builtin.ping:\n")

	// explicit dir restricts discovery
	res, err := Scan(root, "playbooks/prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Playbooks) != 1 || res.Playbooks[0].Path != "playbooks/prod/shop/deploy.yml" {
		t.Fatalf("scan_paths dir: got %+v", res.Playbooks)
	}

	// glob pattern works too
	res, err = Scan(root, "legacy/*.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Playbooks) != 1 || res.Playbooks[0].Path != "legacy/old.yml" {
		t.Fatalf("scan_paths glob: got %+v", res.Playbooks)
	}
}
