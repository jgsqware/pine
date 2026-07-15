package ansible

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unique tool names so the tests never collide with a real ansible on the host.
const (
	fakeTool    = "pine-fake-ansible"
	fakeNonExec = "pine-fake-ansible-noexec"
)

// TestResolvesFromToolDir simulates a mise/asdf-style install dir that a minimal
// PATH would miss: PINE_TOOL_PATH points at it and the tool must be found there.
func TestResolvesFromToolDir(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, fakeTool)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PINE_TOOL_PATH", dir)

	got, ok := LookPath(fakeTool)
	if !ok || got != bin {
		t.Fatalf("LookPath = %q, %v; want %q, true", got, ok, bin)
	}
	if !Available(fakeTool) {
		t.Error("Available = false, want true")
	}
	if Bin(fakeTool) != bin {
		t.Errorf("Bin = %q, want %q", Bin(fakeTool), bin)
	}

	// Env must carry the dir on PATH so the tool (and what it shells out to) resolves.
	var path string
	for _, kv := range Env() {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
		}
	}
	found := false
	for _, p := range filepath.SplitList(path) {
		if p == dir {
			found = true
		}
	}
	if !found {
		t.Errorf("Env PATH %q does not include %q", path, dir)
	}
}

// TestResolveNestedAnsibleCfg reproduces the real-world layout that motivated
// Resolve: a monorepo where the Pine-registered root has no ansible.cfg of
// its own, but a nested sub-project (tc-agent-ansible/) does — and that
// sub-project's ansible.cfg is what declares roles_path. Running from
// repoRoot would leave it invisible to ansible; Resolve must point cmd.Dir at
// the sub-project instead and rebase both paths onto it.
func TestResolveNestedAnsibleCfg(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "tc-agent-ansible")
	mustMkdirAll(t, filepath.Join(sub, "playbooks", "components"))
	mustWriteFile(t, filepath.Join(sub, "ansible.cfg"), "[defaults]\nroles_path = ./roles\n")

	ctx := Resolve(root,
		"tc-agent-ansible/playbooks/components/grafana-stack-setup-only.yml",
		"tc-agent-ansible/inventories/production")

	if ctx.Dir != sub {
		t.Errorf("Dir = %q, want %q", ctx.Dir, sub)
	}
	if ctx.Playbook != filepath.Join("playbooks", "components", "grafana-stack-setup-only.yml") {
		t.Errorf("Playbook = %q", ctx.Playbook)
	}
	if ctx.Inventory != filepath.Join("inventories", "production") {
		t.Errorf("Inventory = %q", ctx.Inventory)
	}
}

// TestResolveNoAnsibleCfgFallsBackToRepoRoot covers every repo that has no
// ansible.cfg at all, or only one at its own root — today's behavior, and it
// must not change: cmd.Dir stays repoRoot and paths stay exactly as scanned.
func TestResolveNoAnsibleCfgFallsBackToRepoRoot(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "playbooks"))

	ctx := Resolve(root, "playbooks/site.yml", "inventories/prod")

	if ctx.Dir != root {
		t.Errorf("Dir = %q, want repoRoot %q", ctx.Dir, root)
	}
	if ctx.Playbook != "playbooks/site.yml" {
		t.Errorf("Playbook = %q, want unchanged", ctx.Playbook)
	}
	if ctx.Inventory != "inventories/prod" {
		t.Errorf("Inventory = %q, want unchanged", ctx.Inventory)
	}
}

// TestResolveAnsibleCfgAtRepoRoot: an ansible.cfg at repoRoot itself (the
// common single-project layout) must still resolve to repoRoot, not error or
// skip past it.
func TestResolveAnsibleCfgAtRepoRoot(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "playbooks"))
	mustWriteFile(t, filepath.Join(root, "ansible.cfg"), "[defaults]\n")

	ctx := Resolve(root, "playbooks/site.yml", "")
	if ctx.Dir != root {
		t.Errorf("Dir = %q, want repoRoot %q", ctx.Dir, root)
	}
	if ctx.Playbook != "playbooks/site.yml" {
		t.Errorf("Playbook = %q", ctx.Playbook)
	}
	if ctx.Inventory != "" {
		t.Errorf("Inventory = %q, want empty", ctx.Inventory)
	}
}

// TestResolveNoPlaybookAnchorsOnInventory covers probe/facts/services runs,
// which only ever carry an inventory path (no playbook).
func TestResolveNoPlaybookAnchorsOnInventory(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "proj")
	mustMkdirAll(t, sub)
	mustWriteFile(t, filepath.Join(sub, "ansible.cfg"), "[defaults]\n")

	ctx := Resolve(root, "", "proj/inventories/prod")
	if ctx.Dir != sub {
		t.Errorf("Dir = %q, want %q", ctx.Dir, sub)
	}
	if ctx.Playbook != "" {
		t.Errorf("Playbook = %q, want empty", ctx.Playbook)
	}
	if ctx.Inventory != filepath.Join("inventories", "prod") {
		t.Errorf("Inventory = %q", ctx.Inventory)
	}
}

// TestResolveDeeplyNestedFindsClosestCfg: with ansible.cfg at more than one
// depth, the nearest one (closest to the playbook) must win, not repoRoot's.
func TestResolveDeeplyNestedFindsClosestCfg(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "ansible.cfg"), "[defaults]\n# outer, decoy\n")
	inner := filepath.Join(root, "a", "b")
	mustMkdirAll(t, inner)
	mustWriteFile(t, filepath.Join(inner, "ansible.cfg"), "[defaults]\n# inner, wins\n")

	ctx := Resolve(root, "a/b/site.yml", "")
	if ctx.Dir != inner {
		t.Errorf("Dir = %q, want the closer %q, not repoRoot", ctx.Dir, inner)
	}
	if ctx.Playbook != "site.yml" {
		t.Errorf("Playbook = %q, want \"site.yml\"", ctx.Playbook)
	}
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNonExecutableAndMissing(t *testing.T) {
	dir := t.TempDir()
	// a non-executable file must not count as a resolvable tool
	if err := os.WriteFile(filepath.Join(dir, fakeNonExec), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PINE_TOOL_PATH", dir)
	if Available(fakeNonExec) {
		t.Error("non-executable file reported as available")
	}
	if Available("pine-nonexistent-tool-xyz") {
		t.Error("missing tool reported as available")
	}
	// Bin falls back to the bare name so the OS can still try.
	if Bin("pine-nonexistent-tool-xyz") != "pine-nonexistent-tool-xyz" {
		t.Error("Bin should fall back to the bare name")
	}
}
