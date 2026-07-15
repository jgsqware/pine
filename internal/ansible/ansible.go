// Package ansible resolves the ansible CLIs and builds the environment to run
// them in. When Pine runs as a systemd service (or any non-login process), the
// PATH is minimal and misses version-manager shim directories — so an ansible
// installed via mise/asdf/pipx is invisible to a bare exec.LookPath and Pine
// wrongly falls back to simulation. This package augments PATH with the common
// shim/bin locations so those installs are found for both detection and exec.
package ansible

import (
	"os"
	"path/filepath"
	"strings"
)

// toolDirs lists directories where ansible tools commonly live but a minimal
// (systemd/cron) PATH omits. Order is preference; only existing dirs are used.
func toolDirs() []string {
	home, _ := os.UserHomeDir()
	var dirs []string
	add := func(parts ...string) {
		if parts[0] != "" {
			dirs = append(dirs, filepath.Join(parts...))
		}
	}
	// An explicit override wins (colon-separated list of dirs).
	if extra := os.Getenv("PINE_TOOL_PATH"); extra != "" {
		dirs = append(dirs, filepath.SplitList(extra)...)
	}
	// mise (honour its env overrides, then the XDG/default locations).
	if d := os.Getenv("MISE_DATA_DIR"); d != "" {
		add(d, "shims")
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		add(x, "mise", "shims")
	}
	add(home, ".local", "share", "mise", "shims")
	// asdf.
	if d := os.Getenv("ASDF_DATA_DIR"); d != "" {
		add(d, "shims")
	}
	add(home, ".asdf", "shims")
	// pipx / user-local / homebrew / ~/bin.
	add(home, ".local", "bin")
	add("/opt/homebrew/bin")
	add("/usr/local/bin")
	add(home, "bin")
	return dirs
}

// searchPath returns the current PATH with any existing tool dirs that are not
// already present appended.
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

// LookPath resolves an executable (e.g. "ansible-playbook") against the
// augmented PATH, returning its absolute path.
func LookPath(name string) (string, bool) {
	for _, dir := range filepath.SplitList(searchPath()) {
		if dir == "" {
			continue
		}
		cand := filepath.Join(dir, name)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return cand, true
		}
	}
	return "", false
}

// Available reports whether an ansible executable can be found.
func Available(name string) bool {
	_, ok := LookPath(name)
	return ok
}

// Bin returns the resolved path for name, or name itself as a fallback so the
// OS can still try to run it.
func Bin(name string) string {
	if p, ok := LookPath(name); ok {
		return p
	}
	return name
}

// Env returns os.Environ with PATH augmented, so a resolved tool (and the
// interpreter/plugins it in turn shells out to) is found at runtime.
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

// ExecContext is where an ansible/ansible-playbook invocation should run
// (cmd.Dir) and its playbook/inventory arguments rebased to be relative to
// that directory instead of the repo root.
type ExecContext struct {
	Dir       string // cmd.Dir
	Playbook  string // argv-ready; "" if playbookRel was ""
	Inventory string // argv-ready; "" if inventoryRel was ""
}

// Resolve finds the directory an ansible/ansible-playbook invocation should
// actually run from, and rebases playbookRel/inventoryRel (paths relative to
// repoRoot, as scanned and stored on a Job) to be relative to it.
//
// ansible only ever auto-loads ansible.cfg from its current working
// directory — never from the directory the playbook file happens to live in.
// A Pine repo can contain more than one independent ansible project (a
// monorepo with several ansible.cfg files at different depths); running
// everything from repoRoot means any nested project's own ansible.cfg
// (roles_path, inventory=, collections_path, ...) is silently invisible,
// and ansible falls back to its hardcoded defaults instead — most visibly
// "role not found" for roles that are right there, one directory over.
//
// The fix: walk up from the playbook's own directory (or the inventory's, if
// there is no playbook) looking for the nearest ansible.cfg, stopping at
// repoRoot, and run from there instead. Falls back to repoRoot when no
// ansible.cfg is found anywhere in between, which reproduces today's
// behavior exactly for every repo that only has one ansible.cfg at its root
// (or none at all).
func Resolve(repoRoot, playbookRel, inventoryRel string) ExecContext {
	anchor := playbookRel
	if anchor == "" {
		anchor = inventoryRel
	}
	dir := projectRoot(repoRoot, anchor)
	return ExecContext{
		Dir:       dir,
		Playbook:  rebase(dir, repoRoot, playbookRel),
		Inventory: rebase(dir, repoRoot, inventoryRel),
	}
}

// projectRoot returns the nearest ancestor of repoRoot/repoRelPath (down to
// repoRoot itself) that contains an ansible.cfg, or repoRoot if none does.
func projectRoot(repoRoot, repoRelPath string) string {
	repoRoot = filepath.Clean(repoRoot)
	if repoRelPath == "" {
		return repoRoot
	}
	dir := filepath.Clean(filepath.Join(repoRoot, filepath.Dir(repoRelPath)))
	for dir == repoRoot || strings.HasPrefix(dir, repoRoot+string(filepath.Separator)) {
		if fi, err := os.Stat(filepath.Join(dir, "ansible.cfg")); err == nil && !fi.IsDir() {
			return dir
		}
		if dir == repoRoot {
			break
		}
		dir = filepath.Dir(dir)
	}
	return repoRoot
}

// rebase turns a path relative to repoRoot into one relative to dir, falling
// back to an absolute path on the rare error (different volumes on Windows —
// never happens on the single-filesystem Linux/macOS hosts Pine targets, but
// an absolute path is always correct regardless of cwd, so it's a safe belt).
func rebase(dir, repoRoot, repoRelPath string) string {
	if repoRelPath == "" {
		return ""
	}
	abs := filepath.Join(repoRoot, repoRelPath)
	if rel, err := filepath.Rel(dir, abs); err == nil {
		return rel
	}
	return abs
}
