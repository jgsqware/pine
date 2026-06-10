package scanner

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/jgsqware/pine/internal/model"
	"gopkg.in/yaml.v3"
)

// scanRoles parses every role under roles/, plus any extra role parent dirs
// contributed by a layout plugin (e.g. generic_roles/).
func scanRoles(root string, plugin *Plugin) []model.Role {
	dirs := []string{"roles"}
	if plugin != nil {
		dirs = append(dirs, plugin.RoleDirs...)
	}
	var out []model.Role
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
			seen[e.Name()] = true
			out = append(out, parseRole(root, filepath.Join(rolesDir, e.Name())))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
