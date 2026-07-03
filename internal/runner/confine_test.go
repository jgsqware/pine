package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/store"
)

func TestConfineToWorkdir(t *testing.T) {
	root := t.TempDir()
	// a legitimate playbook inside the workdir
	if err := os.WriteFile(filepath.Join(root, "site.yml"), []byte("- hosts: all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(root, "inventories", "prod"), 0o755)
	if err := os.WriteFile(filepath.Join(root, "inventories", "prod", "hosts.yml"), []byte("all:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a symlink inside the workdir that escapes to an outside file
	outside := filepath.Join(t.TempDir(), "evil.yml")
	_ = os.WriteFile(outside, []byte("- hosts: all\n"), 0o644)
	link := filepath.Join(root, "link.yml")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	cases := []struct {
		name    string
		rel     string
		wantErr bool
	}{
		{"legit playbook", "site.yml", false},
		{"legit nested inventory", "inventories/prod/hosts.yml", false},
		{"traversal", "../evil.yml", true},
		{"deep traversal", "../../etc/passwd", true},
		{"absolute outside", filepath.Join(t.TempDir(), "evil.yml"), true},
		{"absolute root file", "/etc/passwd", true},
		{"option-looking arg", "-e@/tmp/x", true},
		{"empty", "", true},
		{"escaping symlink", "link.yml", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := confineToWorkdir(root, tc.rel)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got path %q", tc.rel, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.rel, err)
			}
			if got == "" {
				t.Fatalf("expected a cleaned path for %q", tc.rel)
			}
		})
	}
}

// ansibleManager builds a manager whose repo workdir holds a valid playbook.
func ansibleManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	_ = os.MkdirAll(repoDir, 0o755)
	if err := os.WriteFile(filepath.Join(repoDir, "site.yml"), []byte("- hosts: all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(st)
	repo := model.Repo{ID: "r_ans", Name: "ans", Path: repoDir, Status: model.RepoReady}
	if err := st.AddRepo(repo); err != nil {
		t.Fatal(err)
	}
	return m, repoDir
}

// collectRun returns a run that buffers published lines on a channel, plus a
// drain() that (after r.close()) returns everything published as one string.
// Draining after close is safe: a closed channel still yields buffered values.
func collectRun() (*run, func() string) {
	f, _ := os.CreateTemp("", "pine-runtest-*")
	r := &run{subs: map[chan string]bool{}, file: f}
	ch := make(chan string, 1024)
	r.subs[ch] = true
	drain := func() string {
		var buf strings.Builder
		for line := range ch {
			buf.WriteString(line + "\n")
		}
		return buf.String()
	}
	return r, drain
}

func TestRunAnsibleRejectsTraversalPlaybook(t *testing.T) {
	m, _ := ansibleManager(t)
	r, drain := collectRun()
	job := &model.Job{RepoID: "r_ans", Playbook: "../evil.yml"}
	failed := m.runAnsible(context.Background(), job, r)
	r.close()
	out := drain()
	if !failed {
		t.Fatal("expected runAnsible to fail for a traversal playbook")
	}
	if !strings.Contains(out, "invalid playbook") {
		t.Fatalf("expected an invalid-playbook error in the log, got:\n%s", out)
	}
}

func TestRunAnsibleRejectsAbsolutePlaybook(t *testing.T) {
	m, _ := ansibleManager(t)
	outside := filepath.Join(t.TempDir(), "evil.yml")
	_ = os.WriteFile(outside, []byte("- hosts: all\n"), 0o644)
	r, drain := collectRun()
	job := &model.Job{RepoID: "r_ans", Playbook: outside}
	failed := m.runAnsible(context.Background(), job, r)
	r.close()
	out := drain()
	if !failed {
		t.Fatal("expected runAnsible to fail for an absolute out-of-workdir playbook")
	}
	if !strings.Contains(out, "invalid playbook") {
		t.Fatalf("expected an invalid-playbook error in the log, got:\n%s", out)
	}
}

func TestRunAnsibleRejectsOptionLookingPlaybook(t *testing.T) {
	m, _ := ansibleManager(t)
	r, drain := collectRun()
	job := &model.Job{RepoID: "r_ans", Playbook: "-e@/tmp/x"}
	failed := m.runAnsible(context.Background(), job, r)
	r.close()
	out := drain()
	if !failed {
		t.Fatal("expected runAnsible to fail for an option-looking playbook")
	}
	if !strings.Contains(out, "invalid playbook") {
		t.Fatalf("expected an invalid-playbook error in the log, got:\n%s", out)
	}
}

func TestRunAnsibleRejectsBadInventory(t *testing.T) {
	m, _ := ansibleManager(t)
	r, drain := collectRun()
	job := &model.Job{RepoID: "r_ans", Playbook: "site.yml", Inventory: "../../etc/hosts"}
	failed := m.runAnsible(context.Background(), job, r)
	r.close()
	out := drain()
	if !failed {
		t.Fatal("expected runAnsible to fail for a traversal inventory")
	}
	if !strings.Contains(out, "invalid inventory") {
		t.Fatalf("expected an invalid-inventory error in the log, got:\n%s", out)
	}
}

// TestRunAnsibleLegitReachesExec verifies a legitimate confined playbook passes
// validation and the argv is built with "--" before the positional playbook.
// ansible-playbook is very likely absent in the test env, so we assert on the
// emitted "$ ansible-playbook ..." command line rather than a successful run.
func TestRunAnsibleLegitPassesConfinement(t *testing.T) {
	m, _ := ansibleManager(t)
	r, drain := collectRun()
	job := &model.Job{RepoID: "r_ans", Playbook: "site.yml"}
	_ = m.runAnsible(context.Background(), job, r)
	r.close()
	out := drain()
	if strings.Contains(out, "invalid playbook") || strings.Contains(out, "invalid inventory") {
		t.Fatalf("legitimate playbook was rejected:\n%s", out)
	}
	if !strings.Contains(out, "-- site.yml") {
		t.Fatalf("expected argv to end with '-- site.yml', got:\n%s", out)
	}
}
