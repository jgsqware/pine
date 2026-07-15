package claudecode

import (
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := map[string]string{
		"2.1.210 (Claude Code)\n": "2.1.210",
		"1.0.0":                   "1.0.0",
		"":                        "",
		"   ":                     "",
	}
	for in, want := range cases {
		if got := parseVersion(in); got != want {
			t.Errorf("parseVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestArgsDryRunReadOnly(t *testing.T) {
	args := Args("/repo", false, true)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--allowedTools Read") {
		t.Errorf("dry-run must allow only Read: %v", args)
	}
	if strings.Contains(joined, "Edit") || strings.Contains(joined, "Write") || strings.Contains(joined, "acceptEdits") {
		t.Errorf("dry-run must not enable writes: %v", args)
	}
	if !strings.Contains(joined, "--add-dir /repo") {
		t.Errorf("workdir not passed via --add-dir: %v", args)
	}
	if !strings.Contains(joined, "stream-json") {
		t.Errorf("stream mode should request stream-json: %v", args)
	}
}

func TestArgsWriteEnablesEdits(t *testing.T) {
	args := Args("/repo", true, false)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "acceptEdits") {
		t.Errorf("write mode must set acceptEdits: %v", args)
	}
	if !strings.Contains(joined, "Read,Edit,Write") {
		t.Errorf("write mode must allow edits: %v", args)
	}
	if !strings.Contains(joined, "--output-format json") {
		t.Errorf("non-stream mode should use json: %v", args)
	}
}

func TestPromptDiffers(t *testing.T) {
	if Prompt(false) == Prompt(true) {
		t.Fatal("dry-run and write prompts must differ")
	}
	if !strings.Contains(Prompt(false), "DO NOT modify") {
		t.Error("dry-run prompt must forbid writes")
	}
	if !strings.Contains(Prompt(true), SidecarName) {
		t.Error("write prompt must reference the sidecar file")
	}
}
