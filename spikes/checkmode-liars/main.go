// Spike: how often does --check lie on real-world Ansible repos?
//
// Throwaway measurement tool for the state-machine counter-analysis
// (docs/design/state-machine-counter-analysis.md, pillar 2). It walks one
// or more repos, extracts every task, and classifies each task's behaviour
// under `ansible-playbook --check`:
//
//	honest     module supports check mode and predicts changes
//	blind      module is skipped under check (command/shell/...) — drift
//	           on these tasks is INVISIBLE to the drift heatmap
//	forced     check_mode: false — the task RUNS FOR REAL during a check
//	overridden changed_when present — verdict is author-defined (may fix a
//	           blind task, may lie on an honest one; counted separately)
//	unknown    module not in the knowledge base — honestly unclassified
//
// Static analysis only: no ansible install and no target hosts in this
// environment, so this measures the *structural* reliability ceiling of
// check mode, not observed lies.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// checkSupport is a deliberately conservative knowledge base: module ->
// true (supports check honestly) / false (skipped under check). Anything
// absent is reported unknown, never guessed.
var checkSupport = map[string]bool{
	// honest: predict changes under --check
	"file": true, "copy": true, "template": true, "lineinfile": true,
	"blockinfile": true, "replace": true, "assemble": true, "ini_file": true,
	"apt": true, "apt_key": true, "apt_repository": true, "deb822_repository": true,
	"yum": true, "dnf": true, "package": true, "pip": true, "gem": true,
	"npm": true, "user": true, "group": true, "authorized_key": true,
	"known_hosts": true, "service": true, "systemd": true, "systemd_service": true,
	"cron": true, "hostname": true, "sysctl": true, "mount": true,
	"timezone": true, "locale_gen": true, "alternatives": true,
	"ufw": true, "firewalld": true, "seboolean": true, "selinux": true,
	"git": true, "get_url": true, "unarchive": true, "sysvinit": true,
	"capabilities": true, "modprobe": true, "kernel_blacklist": true,
	"docker_container": true, "docker_network": true, "docker_image": true,
	"docker_volume": true, "mysql_db": true, "mysql_user": true,
	"postgresql_db": true, "postgresql_user": true, "postgresql_privs": true,
	"debconf": true, "dpkg_divert": true, "k8s": true, "apache2_module": true,
	"htpasswd": true, "getent": true, "import_playbook": true,
	// read-only / control-flow: execute under check, cannot lie about drift
	"stat": true, "debug": true, "assert": true, "fail": true,
	"set_fact": true, "setup": true, "slurp": true, "find": true,
	"wait_for": true, "wait_for_connection": true, "meta": true,
	"include_tasks": true, "import_tasks": true, "include_role": true,
	"import_role": true, "include_vars": true, "add_host": true,
	"group_by": true, "gather_facts": true, "ping": true, "pause": true,
	// blind: skipped entirely under --check
	"command": false, "shell": false, "raw": false, "script": false,
	"expect": false, "reboot": false, "uri": false, "get_certificate": false,
	"synchronize": false, "fetch": false, "telegram": false, "mail": false,
}

var taskKeywords = map[string]bool{
	"name": true, "when": true, "register": true, "tags": true,
	"become": true, "become_user": true, "become_method": true,
	"become_flags": true, "vars": true, "loop": true, "until": true,
	"retries": true, "delay": true, "delegate_to": true, "delegate_facts": true,
	"run_once": true, "changed_when": true, "failed_when": true,
	"check_mode": true, "ignore_errors": true, "ignore_unreachable": true,
	"no_log": true, "environment": true, "notify": true, "listen": true,
	"args": true, "any_errors_fatal": true, "connection": true,
	"remote_user": true, "loop_control": true, "throttle": true,
	"module_defaults": true, "collections": true, "diff": true,
	"timeout": true, "async": true, "poll": true, "static": true,
	"action": false, // action IS the module carrier, handled explicitly
}

type counts struct {
	Tasks      int            `json:"tasks"`
	Honest     int            `json:"honest"`
	Blind      int            `json:"blind"`
	Forced     int            `json:"forced"`
	Overridden int            `json:"overridden"`
	Unknown    int            `json:"unknown"`
	ParseFails int            `json:"parse_fails"`
	Files      int            `json:"files"`
	BlindTop   map[string]int `json:"blind_modules"`
	UnknownTop map[string]int `json:"unknown_modules"`
}

func normalize(mod string) string {
	if strings.HasPrefix(mod, "with_") {
		return "" // loop directive, not a module
	}
	// strip any collection prefix: ns.coll.module -> module
	if i := strings.LastIndex(mod, "."); i >= 0 {
		return mod[i+1:]
	}
	return mod
}

// moduleOf returns the module key of a task map, or "" if none found.
func moduleOf(task map[string]any) string {
	if a, ok := task["action"].(string); ok {
		return normalize(strings.Fields(a)[0])
	}
	for k := range task {
		if taskKeywords[k] || strings.HasPrefix(k, "with_") {
			continue
		}
		if k == "block" || k == "rescue" || k == "always" {
			continue
		}
		return normalize(k)
	}
	return ""
}

func classify(task map[string]any, c *counts) {
	// blocks recurse; block-level check_mode/changed_when apply to children,
	// approximated by tagging children that don't set their own.
	for _, sect := range []string{"block", "rescue", "always"} {
		if list, ok := task[sect].([]any); ok {
			for _, it := range list {
				if m, ok := it.(map[string]any); ok {
					for _, inh := range []string{"check_mode", "changed_when"} {
						if v, has := task[inh]; has {
							if _, own := m[inh]; !own {
								m[inh] = v
							}
						}
					}
					classify(m, c)
				}
			}
			return
		}
	}
	mod := moduleOf(task)
	if mod == "" {
		return
	}
	c.Tasks++
	if cm, ok := task["check_mode"]; ok {
		if b, isB := cm.(bool); isB && !b {
			c.Forced++
			return
		}
	}
	if _, ok := task["changed_when"]; ok {
		c.Overridden++
		return
	}
	support, known := checkSupport[mod]
	switch {
	case !known:
		c.Unknown++
		c.UnknownTop[mod]++
	case support:
		c.Honest++
	default:
		c.Blind++
		c.BlindTop[mod]++
	}
}

func harvest(node any, c *counts) {
	list, ok := node.([]any)
	if !ok {
		return
	}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if _, isPlay := m["hosts"]; isPlay {
			for _, sect := range []string{"tasks", "pre_tasks", "post_tasks", "handlers"} {
				if l, ok := m[sect].([]any); ok {
					for _, t := range l {
						if tm, ok := t.(map[string]any); ok {
							classify(tm, c)
						}
					}
				}
			}
			continue
		}
		classify(m, c)
	}
}

func scanRepo(root string) counts {
	c := counts{BlindTop: map[string]int{}, UnknownTop: map[string]int{}}
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				base := info.Name()
				if base == ".git" || base == "molecule" || base == ".github" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".yml" && ext != ".yaml" {
			return nil
		}
		// only files that plausibly hold tasks: playbooks at any level,
		// and anything under tasks/ or handlers/
		dir := filepath.Dir(path)
		underTasks := strings.Contains(dir, "/tasks") || strings.Contains(dir, "/handlers")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var doc any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			c.ParseFails++
			return nil
		}
		if _, isList := doc.([]any); !isList {
			return nil
		}
		if !underTasks {
			// accept top-level lists of plays only
			l := doc.([]any)
			isPlays := false
			for _, it := range l {
				if m, ok := it.(map[string]any); ok {
					if _, h := m["hosts"]; h {
						isPlays = true
					}
				}
			}
			if !isPlays {
				return nil
			}
		}
		c.Files++
		harvest(doc, &c)
		return nil
	})
	return c
}

func top(m map[string]int, n int) []string {
	type kv struct {
		k string
		v int
	}
	var s []kv
	for k, v := range m {
		s = append(s, kv{k, v})
	}
	sort.Slice(s, func(i, j int) bool { return s[i].v > s[j].v })
	var out []string
	for i, e := range s {
		if i >= n {
			break
		}
		out = append(out, fmt.Sprintf("%s(%d)", e.k, e.v))
	}
	return out
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: checkmode-liars REPO_DIR...")
		os.Exit(2)
	}
	results := map[string]counts{}
	for _, root := range os.Args[1:] {
		results[filepath.Base(root)] = scanRepo(root)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(results)
	fmt.Println()
	for name, c := range results {
		reliable := c.Honest
		pct := func(n int) float64 {
			if c.Tasks == 0 {
				return 0
			}
			return 100 * float64(n) / float64(c.Tasks)
		}
		fmt.Printf("%-22s tasks=%-5d honest=%.0f%% blind=%.0f%% forced=%.0f%% overridden=%.0f%% unknown=%.0f%% (files=%d, parse_fails=%d)\n",
			name, c.Tasks, pct(reliable), pct(c.Blind), pct(c.Forced), pct(c.Overridden), pct(c.Unknown), c.Files, c.ParseFails)
		fmt.Printf("  blind top:   %s\n", strings.Join(top(c.BlindTop, 8), " "))
		fmt.Printf("  unknown top: %s\n", strings.Join(top(c.UnknownTop, 8), " "))
	}
}
