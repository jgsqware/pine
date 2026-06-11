package scanner

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jgsqware/pine/internal/model"
	"gopkg.in/yaml.v3"
)

// task keys that are directives rather than module invocations
var taskKeywords = map[string]bool{
	"name": true, "when": true, "tags": true, "notify": true, "register": true,
	"become": true, "become_user": true, "become_method": true, "vars": true,
	"delegate_to": true, "delegate_facts": true, "run_once": true,
	"ignore_errors": true, "ignore_unreachable": true, "changed_when": true,
	"failed_when": true, "until": true, "retries": true, "delay": true,
	"args": true, "environment": true, "no_log": true, "loop": true,
	"loop_control": true, "throttle": true, "timeout": true, "any_errors_fatal": true,
	"check_mode": true, "diff": true, "listen": true, "module_defaults": true,
	"collections": true, "connection": true, "port": true, "remote_user": true,
	"vars_files": true, "vars_prompt": true, "static": true, "local_action": true,
	"action": true, "poll": true, "async": true, "become_flags": true,
}

// loop-style keys (with_items, with_dict, ...)
func hasLoop(m map[string]any) bool {
	if _, ok := m["loop"]; ok {
		return true
	}
	for k := range m {
		if strings.HasPrefix(k, "with_") {
			return true
		}
	}
	return false
}

// dirs whose YAML files are never playbooks (role internals, inventories,
// vars, plugin code, CI noise). Checked against every path component during
// the recursive walk.
var nonPlaybookDirs = map[string]bool{
	".git": true, ".github": true, ".gitlab": true, "roles": true,
	"collections": true, "inventories": true, "inventory": true,
	"environments": true, "group_vars": true, "host_vars": true,
	"vars": true, "defaults": true, "tasks": true, "handlers": true,
	"meta": true, "files": true, "templates": true, "filter_plugins": true,
	"library": true, "module_utils": true, "molecule": true,
	".venv": true, "venv": true, "node_modules": true,
}

const maxPlaybookDepth = 8

// scanPlaybooks discovers playbook YAML files. Explicit scanPaths (user
// configured: dirs, files or globs relative to root) take precedence and
// restrict discovery. Otherwise the whole repository is walked recursively,
// skipping role/inventory internals, keeping any YAML file shaped like a
// playbook, plus any extra playbook dirs contributed by a layout plugin.
func scanPlaybooks(root string, plugin *Plugin, scanPaths []string) []model.Playbook {
	var candidates []string
	if len(scanPaths) > 0 {
		candidates = expandScanPaths(root, scanPaths)
	} else {
		candidates = walkForPlaybooks(root)
		if plugin != nil {
			for _, sub := range plugin.PlaybookDirs {
				if isDir(filepath.Join(root, sub)) {
					candidates = append(candidates, walkForPlaybooks(filepath.Join(root, sub))...)
				}
			}
		}
	}

	seen := map[string]bool{}
	var out []model.Playbook
	for _, f := range candidates {
		if seen[f] {
			continue
		}
		seen[f] = true
		base := filepath.Base(f)
		if base == "requirements.yml" || base == "requirements.yaml" ||
			strings.HasPrefix(base, ".") || base == "galaxy.yml" {
			continue
		}
		pb, ok := parsePlaybook(root, f)
		if ok {
			out = append(out, pb)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// walkForPlaybooks recursively collects candidate YAML files under root.
func walkForPlaybooks(root string) []string {
	var out []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > maxPlaybookDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				if nonPlaybookDirs[name] || strings.HasPrefix(name, ".") {
					continue
				}
				walk(filepath.Join(dir, name), depth+1)
				continue
			}
			if strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml") {
				out = append(out, filepath.Join(dir, name))
			}
		}
	}
	walk(root, 0)
	sort.Strings(out)
	return out
}

// expandScanPaths resolves user-provided scan paths: a directory (walked
// recursively), a single file, or a glob pattern - all relative to root.
func expandScanPaths(root string, paths []string) []string {
	var out []string
	for _, p := range paths {
		p = strings.TrimSpace(strings.Trim(p, "/"))
		if p == "" {
			continue
		}
		abs := filepath.Join(root, p)
		switch {
		case isDir(abs):
			out = append(out, walkForPlaybooks(abs)...)
		case isFile(abs):
			out = append(out, abs)
		default:
			if matches, err := filepath.Glob(abs); err == nil {
				for _, m := range matches {
					if isDir(m) {
						out = append(out, walkForPlaybooks(m)...)
					} else if strings.HasSuffix(m, ".yml") || strings.HasSuffix(m, ".yaml") {
						out = append(out, m)
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func parsePlaybook(root, file string) (model.Playbook, bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return model.Playbook{}, false
	}
	var doc []map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc) == 0 {
		return model.Playbook{}, false
	}
	// A playbook is a list whose entries have hosts or import_playbook.
	valid := false
	for _, entry := range doc {
		if _, ok := entry["hosts"]; ok {
			valid = true
		}
		if importTarget(entry) != "" {
			valid = true
		}
	}
	if !valid {
		return model.Playbook{}, false
	}

	rel, _ := filepath.Rel(root, file)
	pb := model.Playbook{Path: rel, Name: strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))}
	for _, entry := range doc {
		if imp := importTarget(entry); imp != "" {
			name := toStr(entry["name"])
			if name == "" {
				name = "Import playbook"
			}
			pb.Plays = append(pb.Plays, model.Play{
				Name:   name,
				Hosts:  imp,
				Import: imp,
			})
			continue
		}
		pb.Plays = append(pb.Plays, parsePlay(entry))
	}
	if len(pb.Plays) > 0 && pb.Plays[0].Name != "" && pb.Plays[0].Import == "" {
		pb.Name = pb.Plays[0].Name
	}
	return pb, true
}

// importTarget returns the imported playbook path for import_playbook
// entries (short or fully-qualified form), or "".
func importTarget(entry map[string]any) string {
	for _, k := range []string{"import_playbook", "ansible.builtin.import_playbook"} {
		if v, ok := entry[k].(string); ok {
			return v
		}
	}
	return ""
}

func parsePlay(m map[string]any) model.Play {
	p := model.Play{
		Name:      toStr(m["name"]),
		Hosts:     toStr(m["hosts"]),
		Serial:    toStr(m["serial"]),
		Strategy:  toStr(m["strategy"]),
		Tags:      toStrSlice(m["tags"]),
		VarsFiles: toStrSlice(m["vars_files"]),
	}
	if b, ok := m["become"].(bool); ok {
		p.Become = b
	}
	if vars, ok := m["vars"].(map[string]any); ok {
		p.Vars = vars
	}
	if vp, ok := m["vars_prompt"].([]any); ok {
		for _, e := range vp {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			pv := model.PromptVar{
				Name:    toStr(em["name"]),
				Prompt:  toStr(em["prompt"]),
				Default: toStr(em["default"]),
			}
			if b, ok := em["private"].(bool); ok {
				pv.Private = b
			}
			p.VarsPrompt = append(p.VarsPrompt, pv)
		}
	}
	if roles, ok := m["roles"].([]any); ok {
		for _, r := range roles {
			switch t := r.(type) {
			case string:
				p.Roles = append(p.Roles, t)
			case map[string]any:
				if name, ok := t["role"].(string); ok {
					p.Roles = append(p.Roles, name)
				} else if name, ok := t["name"].(string); ok {
					p.Roles = append(p.Roles, name)
				}
			}
		}
	}
	p.PreTasks = parseTaskList(m["pre_tasks"])
	p.Tasks = parseTaskList(m["tasks"])
	p.PostTasks = parseTaskList(m["post_tasks"])
	p.Handlers = parseTaskList(m["handlers"])
	return p
}

// ParseTaskList exposes task parsing for role task files.
func parseTaskList(v any) []model.Task {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []model.Task
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, parseTask(m))
	}
	return out
}

func parseTask(m map[string]any) model.Task {
	t := model.Task{
		Name:   toStr(m["name"]),
		Tags:   toStrSlice(m["tags"]),
		When:   toStr(m["when"]),
		Loop:   hasLoop(m),
		Notify: toStrSlice(m["notify"]),
		Listen: toStr(m["listen"]),
	}
	if t.Loop {
		loopVal, ok := m["loop"]
		if !ok {
			for k, v := range m {
				if strings.HasPrefix(k, "with_") {
					loopVal = v
					break
				}
			}
		}
		switch lv := loopVal.(type) {
		case string:
			t.LoopExpr = lv
		case []any:
			t.LoopItems = len(lv)
		}
	}
	if blk, ok := m["block"]; ok {
		t.Module = "block"
		t.Block = parseTaskList(blk)
		t.Rescue = parseTaskList(m["rescue"])
		t.Always = parseTaskList(m["always"])
		return t
	}
	for k := range m {
		if taskKeywords[k] || strings.HasPrefix(k, "with_") {
			continue
		}
		t.Module = k
		t.Args = summarizeArgs(m[k])
		t.IncludePath = includePath(k, m[k])
		break
	}
	if t.Module == "" {
		t.Module = "meta"
	}
	if t.Name == "" {
		t.Name = t.Module
	}
	return t
}

// includePath returns the file referenced by an include_/import_ task module
// (string short form or the `file:` key of the dict form), or "" for modules
// that don't reference a file. Role-name includes (include_role/import_role)
// are intentionally excluded — they point at a role, not a file.
func includePath(module string, v any) string {
	m := strings.TrimPrefix(module, "ansible.builtin.")
	m = strings.TrimPrefix(m, "ansible.legacy.")
	switch m {
	case "include_vars", "include_tasks", "import_tasks", "include":
		switch t := v.(type) {
		case string:
			return strings.TrimSpace(t)
		case map[string]any:
			if f, ok := t["file"].(string); ok {
				return strings.TrimSpace(f)
			}
		}
	}
	return ""
}

// maxArgLen caps the rendered module-argument summary so a large debug msg or
// set_fact block doesn't blow up a task node.
const maxArgLen = 240

// summarizeArgs renders a module's argument value into a single concise line,
// e.g. include_vars: "foo.yml" -> foo.yml, debug: {msg: x} -> msg: x.
func summarizeArgs(v any) string {
	var s string
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		s = t
	case bool, int, int64, float64:
		s = toStr(t)
	case []any:
		s = toStr(t)
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			val := toStr(t[k])
			if val == "" {
				val = "…"
			}
			parts = append(parts, k+": "+val)
		}
		s = strings.Join(parts, ", ")
	default:
		return ""
	}
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) > maxArgLen {
		s = s[:maxArgLen] + "…"
	}
	return s
}

// countTasks counts tasks recursively, including block contents.
func countTasks(tasks []model.Task) int {
	n := 0
	for _, t := range tasks {
		if t.Module == "block" {
			n += countTasks(t.Block) + countTasks(t.Rescue) + countTasks(t.Always)
		} else {
			n++
		}
	}
	return n
}
