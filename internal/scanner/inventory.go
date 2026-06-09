package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jgsqware/pine/internal/model"
	"gopkg.in/yaml.v3"
)

// invBuilder accumulates groups/hosts while parsing an inventory source.
type invBuilder struct {
	groups    map[string]*model.Group
	hostVars  map[string]map[string]any
	hostOrder []string
}

func newInvBuilder() *invBuilder {
	return &invBuilder{groups: map[string]*model.Group{}, hostVars: map[string]map[string]any{}}
}

func (b *invBuilder) group(name string) *model.Group {
	g, ok := b.groups[name]
	if !ok {
		g = &model.Group{Name: name}
		b.groups[name] = g
	}
	return g
}

func (b *invBuilder) addHost(group, host string, vars map[string]any) {
	g := b.group(group)
	if !contains(g.Hosts, host) {
		g.Hosts = append(g.Hosts, host)
	}
	if _, ok := b.hostVars[host]; !ok {
		b.hostVars[host] = map[string]any{}
		b.hostOrder = append(b.hostOrder, host)
	}
	for k, v := range vars {
		b.hostVars[host][k] = v
	}
}

func contains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

// scanInventories finds inventory sources: inventories/<env>/ dirs, an
// inventory/ dir, or top-level hosts/hosts.ini/hosts.yml files.
func scanInventories(root string) []model.Inventory {
	var out []model.Inventory

	for _, base := range []string{"inventories", "environments"} {
		dir := filepath.Join(root, base)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			invDir := filepath.Join(dir, e.Name())
			if inv, ok := parseInventoryDir(root, invDir, e.Name()); ok {
				out = append(out, inv)
			}
		}
	}

	if inv, ok := parseInventoryDir(root, filepath.Join(root, "inventory"), "inventory"); ok {
		out = append(out, inv)
	}

	if len(out) == 0 {
		for _, f := range []string{"hosts", "hosts.ini", "hosts.yml", "hosts.yaml", "inventory.ini", "inventory.yml"} {
			p := filepath.Join(root, f)
			if isFile(p) {
				if inv, ok := parseInventoryFile(root, p, "default", root); ok {
					out = append(out, inv)
					break
				}
			}
		}
	}
	return out
}

// parseInventoryDir parses an inventory directory containing a hosts file
// plus optional group_vars/ and host_vars/.
func parseInventoryDir(root, dir, name string) (model.Inventory, bool) {
	if !isDir(dir) {
		return model.Inventory{}, false
	}
	for _, f := range []string{"hosts", "hosts.ini", "hosts.yml", "hosts.yaml", "inventory.ini", "inventory.yml", "00-hosts.ini"} {
		p := filepath.Join(dir, f)
		if isFile(p) {
			return parseInventoryFile(root, p, name, dir)
		}
	}
	return model.Inventory{}, false
}

func parseInventoryFile(root, file, name, varsDir string) (model.Inventory, bool) {
	b := newInvBuilder()
	format := "ini"
	if strings.HasSuffix(file, ".yml") || strings.HasSuffix(file, ".yaml") {
		format = "yaml"
		if !parseYAMLInventory(file, b) {
			return model.Inventory{}, false
		}
	} else {
		if !parseINIInventory(file, b) {
			return model.Inventory{}, false
		}
	}

	mergeVarsDir(filepath.Join(varsDir, "group_vars"), func(group string, vars map[string]any) {
		g := b.group(group)
		if g.Vars == nil {
			g.Vars = map[string]any{}
		}
		for k, v := range vars {
			g.Vars[k] = v
		}
	})
	mergeVarsDir(filepath.Join(varsDir, "host_vars"), func(host string, vars map[string]any) {
		if _, ok := b.hostVars[host]; !ok {
			return
		}
		for k, v := range vars {
			b.hostVars[host][k] = v
		}
	})

	relPath, _ := filepath.Rel(root, varsDir)
	if relPath == "." {
		relPath, _ = filepath.Rel(root, file)
	}
	inv := model.Inventory{Name: name, Path: relPath, Format: format}

	// finalize groups (stable order)
	var names []string
	for n, g := range b.groups {
		// keep "all" only when it carries vars; keep "ungrouped" only when used
		if n == "all" && len(g.Vars) == 0 {
			continue
		}
		if n == "ungrouped" && len(g.Hosts) == 0 {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		inv.Groups = append(inv.Groups, *b.groups[n])
	}

	// resolve transitive host -> groups membership
	memberships := resolveMemberships(b)
	for _, h := range b.hostOrder {
		groups := memberships[h]
		sort.Strings(groups)
		vars := b.hostVars[h]
		if len(vars) == 0 {
			vars = nil
		}
		inv.Hosts = append(inv.Hosts, model.Host{Name: h, Groups: groups, Vars: vars})
	}
	sort.Slice(inv.Hosts, func(i, j int) bool { return inv.Hosts[i].Name < inv.Hosts[j].Name })
	return inv, len(inv.Hosts) > 0 || len(inv.Groups) > 0
}

// resolveMemberships returns, for each host, all groups it belongs to
// (directly or through parent groups).
func resolveMemberships(b *invBuilder) map[string][]string {
	// parentsOf[g] = groups that list g as a child
	parentsOf := map[string][]string{}
	for name, g := range b.groups {
		for _, c := range g.Children {
			parentsOf[c] = append(parentsOf[c], name)
		}
	}
	out := map[string][]string{}
	for name, g := range b.groups {
		if name == "all" {
			continue
		}
		// collect this group and all its ancestors
		anc := map[string]bool{}
		var walk func(string)
		walk = func(n string) {
			if anc[n] || n == "all" {
				return
			}
			anc[n] = true
			for _, p := range parentsOf[n] {
				walk(p)
			}
		}
		walk(name)
		for _, h := range g.Hosts {
			for a := range anc {
				if !contains(out[h], a) {
					out[h] = append(out[h], a)
				}
			}
		}
	}
	return out
}

// --- INI format ---

var rangeRe = regexp.MustCompile(`\[(\d+):(\d+)\]`)

// expandRange expands web[01:03] into web01 web02 web03.
func expandRange(name string) []string {
	m := rangeRe.FindStringSubmatchIndex(name)
	if m == nil {
		return []string{name}
	}
	lo := name[m[2]:m[3]]
	hi := name[m[4]:m[5]]
	start, err1 := strconv.Atoi(lo)
	end, err2 := strconv.Atoi(hi)
	if err1 != nil || err2 != nil || end < start {
		return []string{name}
	}
	width := len(lo)
	var out []string
	for i := start; i <= end; i++ {
		n := name[:m[0]] + fmt.Sprintf("%0*d", width, i) + name[m[1]:]
		out = append(out, expandRange(n)...)
	}
	return out
}

func parseINIInventory(file string, b *invBuilder) bool {
	data, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	section := "ungrouped"
	kind := "hosts" // hosts | children | vars
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			kind = "hosts"
			if s, ok := strings.CutSuffix(section, ":children"); ok {
				section, kind = s, "children"
			} else if s, ok := strings.CutSuffix(section, ":vars"); ok {
				section, kind = s, "vars"
			}
			b.group(section)
			continue
		}
		switch kind {
		case "children":
			g := b.group(section)
			if !contains(g.Children, line) {
				g.Children = append(g.Children, line)
			}
			b.group(line)
		case "vars":
			k, v, ok := strings.Cut(line, "=")
			if ok {
				g := b.group(section)
				if g.Vars == nil {
					g.Vars = map[string]any{}
				}
				g.Vars[strings.TrimSpace(k)] = parseScalar(strings.TrimSpace(v))
			}
		default:
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			vars := map[string]any{}
			for _, f := range fields[1:] {
				if k, v, ok := strings.Cut(f, "="); ok {
					vars[k] = parseScalar(v)
				}
			}
			for _, h := range expandRange(fields[0]) {
				b.addHost(section, h, vars)
			}
		}
	}
	return true
}

// parseScalar interprets an INI value as bool/int/string.
func parseScalar(s string) any {
	if i, err := strconv.Atoi(s); err == nil {
		return i
	}
	switch strings.ToLower(s) {
	case "true", "yes":
		return true
	case "false", "no":
		return false
	}
	return strings.Trim(s, `"'`)
}

// --- YAML format ---

func parseYAMLInventory(file string, b *invBuilder) bool {
	data, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false
	}
	if len(doc) == 0 {
		return false
	}
	for name, v := range doc {
		walkYAMLGroup(name, v, b)
	}
	return true
}

func walkYAMLGroup(name string, v any, b *invBuilder) {
	g := b.group(name)
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if hosts, ok := m["hosts"].(map[string]any); ok {
		for h, hv := range hosts {
			vars, _ := hv.(map[string]any)
			if vars == nil {
				vars = map[string]any{}
			}
			for _, hn := range expandRange(h) {
				b.addHost(name, hn, vars)
			}
		}
	}
	if vars, ok := m["vars"].(map[string]any); ok {
		if g.Vars == nil {
			g.Vars = map[string]any{}
		}
		for k, val := range vars {
			g.Vars[k] = val
		}
	}
	if children, ok := m["children"].(map[string]any); ok {
		for c, cv := range children {
			if !contains(g.Children, c) {
				g.Children = append(g.Children, c)
			}
			walkYAMLGroup(c, cv, b)
		}
	}
}

// mergeVarsDir reads group_vars/ or host_vars/: either <name>.yml files or
// <name>/ directories containing YAML fragments.
func mergeVarsDir(dir string, apply func(name string, vars map[string]any)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			merged := map[string]any{}
			for _, f := range yamlFiles(filepath.Join(dir, name)) {
				for k, v := range parseVarsFile(f) {
					merged[k] = v
				}
			}
			if len(merged) > 0 {
				apply(name, merged)
			}
			continue
		}
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".yml"), ".yaml")
		vars := parseVarsFile(filepath.Join(dir, name))
		if len(vars) > 0 {
			apply(base, vars)
		}
	}
}
