package scanner

import (
	"path/filepath"
	"testing"

	"github.com/jgsqware/pine/internal/model"
)

// import_tasks is static, so Pine inlines the referenced file's tasks into the
// playbook tree; dynamic include_tasks and Jinja paths are left as a reference.
func TestImportTasksInlined(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "site.yml"), `---
- name: Site
  hosts: all
  tasks:
    - name: Pull in setup
      ansible.builtin.import_tasks: tasks/setup.yml
    - name: Dynamic include
      ansible.builtin.include_tasks: tasks/setup.yml
    - name: Computed import
      ansible.builtin.import_tasks: "tasks/{{ env }}.yml"
`)
	writeFile(t, filepath.Join(root, "tasks/setup.yml"), `---
- name: Install nginx
  ansible.builtin.apt:
    name: nginx
- name: Start nginx
  ansible.builtin.service:
    name: nginx
    state: started
`)

	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	tasks := playbookTasks(t, res, "site.yml")
	if len(tasks) < 3 {
		t.Fatalf("expected >=3 tasks, got %d", len(tasks))
	}

	// import_tasks → inlined
	if got := tasks[0].Imported; len(got) != 2 {
		t.Fatalf("import_tasks: expected 2 inlined tasks, got %d", len(got))
	} else if got[0].Name != "Install nginx" || got[1].Name != "Start nginx" {
		t.Errorf("inlined task names = %q, %q", got[0].Name, got[1].Name)
	}
	// include_tasks (dynamic) → not inlined, but still a clickable reference
	if len(tasks[1].Imported) != 0 {
		t.Errorf("include_tasks should not be inlined, got %d", len(tasks[1].Imported))
	}
	if tasks[1].IncludePath != "tasks/setup.yml" {
		t.Errorf("include_tasks include_path = %q", tasks[1].IncludePath)
	}
	// Jinja path → not resolvable statically
	if len(tasks[2].Imported) != 0 {
		t.Errorf("import_tasks with a Jinja path should not be inlined, got %d", len(tasks[2].Imported))
	}
}

// TestImportTasksNested follows an import_tasks chain (a → b) and inlines both,
// and stops a cycle (c imports itself) without recursing forever.
func TestImportTasksNested(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "site.yml"), `---
- hosts: all
  tasks:
    - import_tasks: tasks/a.yml
    - import_tasks: tasks/loop.yml
`)
	writeFile(t, filepath.Join(root, "tasks/a.yml"), `---
- name: a-task
  ansible.builtin.debug: msg=a
- import_tasks: b.yml
`)
	writeFile(t, filepath.Join(root, "tasks/b.yml"), `---
- name: b-task
  ansible.builtin.debug: msg=b
`)
	writeFile(t, filepath.Join(root, "tasks/loop.yml"), `---
- import_tasks: loop.yml
`)

	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	tasks := playbookTasks(t, res, "site.yml")

	a := tasks[0].Imported
	if len(a) != 2 {
		t.Fatalf("a.yml: expected 2 inlined tasks, got %d", len(a))
	}
	if len(a[1].Imported) != 1 || a[1].Imported[0].Name != "b-task" {
		t.Errorf("nested import b.yml not inlined: %+v", a[1].Imported)
	}
	// loop.yml imports itself: the first level inlines, the cycle is then cut.
	if len(tasks[1].Imported) != 1 {
		t.Fatalf("loop.yml: expected 1 inlined task, got %d", len(tasks[1].Imported))
	}
	if len(tasks[1].Imported[0].Imported) != 0 {
		t.Errorf("cycle should be cut, got %d", len(tasks[1].Imported[0].Imported))
	}
}

func playbookTasks(t *testing.T, res *model.ScanResult, path string) []model.Task {
	t.Helper()
	for _, pb := range res.Playbooks {
		if pb.Path == path && len(pb.Plays) > 0 {
			return pb.Plays[0].Tasks
		}
	}
	t.Fatalf("playbook %s not found", path)
	return nil
}
