package scanner

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/jgsqware/pine/internal/model"
	"gopkg.in/yaml.v3"
)

// roleDirNames are directory names whose children are roles.
var roleDirNames = map[string]bool{"roles": true}

// scanRoles parses every role under roles/, plus any extra role parent dirs
// contributed by a layout plugin (e.g. generic_roles/).
func scanRoles(root string, plugin *Plugin, cache *ScanCache) []model.Role {
	dirs := []string{"roles"}
	if plugin != nil {
		dirs = append(dirs, plugin.RoleDirs...)
	}
	// nested roles dirs anywhere in the tree (ansible-for-devops style:
	// one roles/ per chapter or project)
	dirs = append(dirs, findRoleDirs(root)...)

	// Deduplicate sequentially (the shared `seen` map is not touched during
	// the parallel parse below), building the flat worklist of role dirs.
	var roleDirs []string
	seen := map[string]bool{}
	for _, rd := range dirs {
		rolesDir := filepath.Join(root, rd)
		entries, err := os.ReadDir(rolesDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || seen[e.Name()] {
				continue
			}
			roleDir := filepath.Join(rolesDir, e.Name())
			if !looksLikeRole(roleDir) {
				continue
			}
			seen[e.Name()] = true
			roleDirs = append(roleDirs, roleDir)
		}
	}

	// Parse each role concurrently (bounded to GOMAXPROCS). Results are
	// written by index into a pre-sized slice, so there is no shared mutable
	// state and the final sort keeps ordering deterministic.
	out := make([]model.Role, len(roleDirs))
	sem := make(chan struct{}, maxParseWorkers())
	var wg sync.WaitGroup
	for i, dir := range roleDirs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, dir string) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = parseRoleCached(root, dir, cache)
		}(i, dir)
	}
	wg.Wait()

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// maxParseWorkers bounds the per-item parse fan-out to the available CPUs.
func maxParseWorkers() int {
	n := runtime.GOMAXPROCS(0)
	if n < 1 {
		n = 1
	}
	return n
}

// looksLikeRole requires at least one conventional role subdirectory, so
// arbitrary folders inside a roles/ dir are not misparsed.
func looksLikeRole(dir string) bool {
	for _, sub := range []string{"tasks", "defaults", "handlers", "meta", "templates", "vars", "files"} {
		if isDir(filepath.Join(dir, sub)) {
			return true
		}
	}
	return false
}

const maxRoleDirDepth = 5

// findRoleDirs returns repo-relative paths of nested "roles" directories
// (the repo root one is handled by the caller). It does not descend into
// roles themselves or vendored/code directories.
func findRoleDirs(root string) []string {
	var out []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > maxRoleDirDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || nonPlaybookDirs[name] && !roleDirNames[name] {
				continue
			}
			full := filepath.Join(dir, name)
			if roleDirNames[name] {
				if rel, err := filepath.Rel(root, full); err == nil && rel != "roles" {
					out = append(out, rel)
				}
				continue // don't look for roles dirs inside roles
			}
			walk(full, depth+1)
		}
	}
	walk(root, 0)
	sort.Strings(out)
	return out
}

// parseRoleCached returns the cached parse of a role directory when no file in
// its tree changed (aggregate mtime/size), otherwise parses and records it. A
// nil cache parses directly.
func parseRoleCached(root, dir string, cache *ScanCache) model.Role {
	if cache == nil {
		return parseRole(root, dir)
	}
	s := roleSig(dir)
	if v, hit := cache.lookup(dir, s); hit {
		return v.(model.Role)
	}
	r := parseRole(root, dir)
	cache.store(dir, s, r)
	return r
}

func parseRole(root, dir string) model.Role {
	rel, _ := filepath.Rel(root, dir)
	r := model.Role{
		Name: filepath.Base(dir),
		Path: rel,
	}

	// tasks: main.yml plus any included task files in tasks/
	for _, f := range yamlFiles(filepath.Join(dir, "tasks")) {
		tasks := parseTaskFile(f)
		if filepath.Base(f) == "main.yml" || filepath.Base(f) == "main.yaml" {
			r.Tasks = append(tasks, r.Tasks...)
		} else {
			r.Tasks = append(r.Tasks, tasks...)
		}
	}
	r.TasksCount = countTasks(r.Tasks)

	for _, f := range yamlFiles(filepath.Join(dir, "handlers")) {
		r.Handlers = append(r.Handlers, parseTaskFile(f)...)
	}

	r.Defaults = parseVarsFile(filepath.Join(dir, "defaults", "main.yml"))
	if r.Defaults == nil {
		r.Defaults = parseVarsFile(filepath.Join(dir, "defaults", "main.yaml"))
	}
	r.Vars = parseVarsFile(filepath.Join(dir, "vars", "main.yml"))
	if r.Vars == nil {
		r.Vars = parseVarsFile(filepath.Join(dir, "vars", "main.yaml"))
	}

	if isDir(filepath.Join(dir, "templates")) {
		r.Templates = listRel(filepath.Join(dir, "templates"))
	}
	if isDir(filepath.Join(dir, "files")) {
		r.Files = listRel(filepath.Join(dir, "files"))
	}

	parseRoleMeta(filepath.Join(dir, "meta", "main.yml"), &r)
	return r
}

func parseTaskFile(file string) []model.Task {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var doc []any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	return parseTaskList(doc)
}

func parseVarsFile(file string) map[string]any {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	return doc
}

func parseRoleMeta(file string, r *model.Role) {
	doc := parseVarsFile(file)
	if doc == nil {
		doc = parseVarsFile(file[:len(file)-4] + ".yaml")
		if doc == nil {
			return
		}
	}
	if gi, ok := doc["galaxy_info"].(map[string]any); ok {
		r.Description = toStr(gi["description"])
	}
	if deps, ok := doc["dependencies"].([]any); ok {
		for _, d := range deps {
			switch t := d.(type) {
			case string:
				r.Dependencies = append(r.Dependencies, t)
			case map[string]any:
				if name, ok := t["role"].(string); ok {
					r.Dependencies = append(r.Dependencies, name)
				} else if name, ok := t["name"].(string); ok {
					r.Dependencies = append(r.Dependencies, name)
				}
			}
		}
	}
}

// LoadVarsFile reads a YAML mapping file (vars_files, group_vars...);
// nil when missing or invalid.
func LoadVarsFile(path string) map[string]any { return parseVarsFile(path) }
