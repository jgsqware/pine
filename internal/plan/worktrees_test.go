package plan

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseWorktrees(t *testing.T) {
	// Hand-written porcelain covering main, linked branch, detached, locked.
	porcelain := `worktree /repos/main
HEAD 1111111111111111111111111111111111111111
branch refs/heads/main

worktree /repos/feature
HEAD 2222222222222222222222222222222222222222
branch refs/heads/feature/login

worktree /repos/detached
HEAD 3333333333333333333333333333333333333333
detached

worktree /repos/pinned
HEAD 4444444444444444444444444444444444444444
branch refs/heads/release
locked needs the disk image
prunable gitdir file points to non-existent location
`
	got := parseWorktrees(porcelain)
	if len(got) != 4 {
		t.Fatalf("expected 4 worktrees, got %d: %+v", len(got), got)
	}

	if got[0].Name != "main" || got[0].Branch != "main" {
		t.Errorf("main worktree wrong: %+v", got[0])
	}
	if got[1].Branch != "feature/login" {
		t.Errorf("branch ref not shortened: %q", got[1].Branch)
	}
	if !got[2].Detached || got[2].Branch != "" {
		t.Errorf("detached worktree wrong: %+v", got[2])
	}
	if !got[3].Locked || got[3].LockReason != "needs the disk image" {
		t.Errorf("lock not parsed: %+v", got[3])
	}
	if !got[3].Prunable || got[3].PrunableReason != "gitdir file points to non-existent location" {
		t.Errorf("prunable not parsed: %+v", got[3])
	}
}

func TestWorktreesNonGit(t *testing.T) {
	out, err := Worktrees(t.TempDir())
	if err != nil {
		t.Fatalf("non-git dir should not error: %v", err)
	}
	if out.IsGit {
		t.Errorf("plain dir reported as git: %+v", out)
	}
	if len(out.Worktrees) != 0 {
		t.Errorf("plain dir should have no worktrees: %+v", out.Worktrees)
	}
}

func TestWorktreesOnGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	writeT(t, root, "README.md", "hello\n")
	mustGit(t, root, "init", "-q", "-b", "main")
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "-c", "user.email=t@t", "-c", "user.name=t",
		"-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")

	// add a linked worktree on a new branch, in a sibling dir
	linked := filepath.Join(filepath.Dir(root), "wt-"+filepath.Base(root))
	mustGit(t, root, "worktree", "add", "-q", "-b", "feature", linked)
	t.Cleanup(func() { _ = exec.Command("git", "-C", root, "worktree", "remove", "--force", linked).Run() })

	out, err := Worktrees(root)
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	if !out.IsGit {
		t.Fatal("git repo not detected as git")
	}
	if len(out.Worktrees) != 2 {
		t.Fatalf("expected main + linked worktree, got %d: %+v", len(out.Worktrees), out.Worktrees)
	}
	if !out.Worktrees[0].Main {
		t.Errorf("first worktree should be flagged main: %+v", out.Worktrees[0])
	}
	if out.Worktrees[1].Main {
		t.Errorf("linked worktree must not be main: %+v", out.Worktrees[1])
	}
	branches := map[string]bool{out.Worktrees[0].Branch: true, out.Worktrees[1].Branch: true}
	if !branches["main"] || !branches["feature"] {
		t.Errorf("expected main+feature branches, got %+v", branches)
	}
	for _, w := range out.Worktrees {
		if w.Head == "" {
			t.Errorf("worktree missing HEAD: %+v", w)
		}
	}
}
