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
