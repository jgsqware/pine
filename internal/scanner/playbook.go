package scanner

import (
	"os"
	"path/filepath"
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

// scanPlaybooks finds playbook YAML files at the repo root and in playbooks/.
func scanPlaybooks(root string) []model.Playbook {
	var candidates []string
	candidates = append(candidates, yamlFiles(root)...)
	for _, sub := range []string{"playbooks", "plays"} {
		if isDir(filepath.Join(root, sub)) {
			candidates = append(candidates, yamlFiles(filepath.Join(root, sub))...)
		}
	}
	var out []model.Playbook
	for _, f := range candidates {
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
