// Package claudecode detects the Claude Code CLI (`claude`) on the host and
// builds the argv/environment to run it headlessly. Pine uses it to generate
// missing descriptions for an Ansible repo's playbooks and roles: when the CLI
// is present the Guide surfaces a "Generate descriptions" action that launches
// a `claude -p` session in the repo's working copy.
//
// Detection mirrors internal/ansible: a Pine daemon (systemd/cron) runs under a
// minimal PATH that misses the per-user install dir (`~/.local/bin`), so a bare
// exec.LookPath would report "not installed" for a `claude` the user can run
// from their shell. We augment PATH with the same common locations before
// resolving.
package claudecode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Capability is the detected state of the Claude Code CLI, surfaced to the API
// so the web UI can show or hide the "Generate descriptions" action.
type Capability struct {
	Available bool   `json:"available"`         // the `claude` binary was found
	Version   string `json:"version,omitempty"` // e.g. "2.1.210"
	Path      string `json:"path,omitempty"`    // resolved absolute path
}

// toolDirs lists directories where `claude` commonly lives but a minimal
// (systemd/cron) PATH omits. Order is preference; only existing dirs are used.
func toolDirs() []string {
	home, _ := os.UserHomeDir()
	var dirs []string
	add := func(parts ...string) {
		if parts[0] != "" {
			dirs = append(dirs, filepath.Join(parts...))
		}
	}
	if extra := os.Getenv("PINE_TOOL_PATH"); extra != "" {
		dirs = append(dirs, filepath.SplitList(extra)...)
	}
	add(home, ".local", "bin")
	add(home, ".claude", "local")
	add("/opt/homebrew/bin")
	add("/usr/local/bin")
	add(home, "bin")
	return dirs
}

// searchPath returns the current PATH with any existing tool dirs not already
// present appended.
func searchPath() string {
	cur := os.Getenv("PATH")
	seen := map[string]bool{}
	for _, p := range filepath.SplitList(cur) {
		if p != "" {
			seen[p] = true
		}
	}
	var extra []string
	for _, d := range toolDirs() {
		if d == "" || seen[d] {
			continue
		}
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			extra = append(extra, d)
			seen[d] = true
		}
	}
	if len(extra) == 0 {
		return cur
	}
	if cur == "" {
		return strings.Join(extra, string(os.PathListSeparator))
	}
	return cur + string(os.PathListSeparator) + strings.Join(extra, string(os.PathListSeparator))
}

// LookPath resolves the `claude` executable against the augmented PATH.
func LookPath() (string, bool) {
	for _, dir := range filepath.SplitList(searchPath()) {
		if dir == "" {
			continue
		}
		cand := filepath.Join(dir, "claude")
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return cand, true
		}
	}
	return "", false
}

// Bin returns the resolved path for `claude`, or "claude" as a fallback so the
// OS can still try to run it.
func Bin() string {
	if p, ok := LookPath(); ok {
		return p
	}
	return "claude"
}

// Available reports whether the `claude` binary can be found.
func Available() bool {
	_, ok := LookPath()
	return ok
}

// Env returns os.Environ with PATH augmented so `claude` (and anything it in
// turn shells out to) is found at runtime. The user's existing Claude Code auth
// (a prior login, ANTHROPIC_API_KEY, CLAUDE_CODE_OAUTH_TOKEN, …) is inherited.
func Env() []string {
	p := searchPath()
	env := os.Environ()
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + p
			return env
		}
	}
	return append(env, "PATH="+p)
}

// Detect resolves the CLI and, when present, reads its version. The version
// probe is bounded so a wedged binary can't hang a request.
func Detect() Capability {
	path, ok := LookPath()
	if !ok {
		return Capability{Available: false}
	}
	cap := Capability{Available: true, Path: path}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = Env()
	if out, err := cmd.Output(); err == nil {
		cap.Version = parseVersion(string(out))
	}
	return cap
}

// parseVersion extracts the leading semver token from `claude --version`
// output, which looks like "2.1.210 (Claude Code)".
func parseVersion(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	return strings.Fields(out)[0]
}
