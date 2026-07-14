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

// dirs that never contain inventory sources (role internals, code, deps).
var nonInventoryDirs = map[string]bool{
	"roles": true, "tasks": true, "handlers": true, "templates": true,
	"files": true, "defaults": true, "meta": true, "vars": true,
	"library": true, "filter_plugins": true, "module_utils": true,
	"molecule": true, "collections": true, "node_modules": true,
	"venv": true, ".venv": true,
}

// dir names that conventionally hold inventories.
var inventoryDirNames = map[string]bool{
	"inventory": true, "inventories": true, "environments": true, "envs": true,
}

// generic file stems that don't make good inventory names.
var genericInvStems = map[string]bool{"hosts": true, "inventory": true, "00-hosts": true}

const maxInventoryDepth = 6

// scanInventories discovers inventory sources anywhere in the repository.
// A directory is considered an inventory location when it is named
// inventory/inventories/environments, sits directly inside one, carries
// group_vars/ or host_vars/, or is the repo root. Within such a directory,
// every hosts*/inventory* file, every .ini file and every YAML file shaped
// like an inventory becomes an inventory source (so both
// inventories/<env>/hosts.ini and inventories/production.yml layouts work),
// with sibling group_vars/host_vars merged in.
func scanInventories(root string) []model.Inventory {
	seenFiles := map[string]bool{}
	usedNames := map[string]bool{}
	var out []model.Inventory

	addFromDir := func(dir string) {
		files := inventoryFilesIn(dir)
		cfgs, pluginFiles := pluginConfigsIn(dir)
		for _, f := range pluginFiles {
			seenFiles[f] = true
		}
		// drop plugin configs from the host-source list (a constructed
		// file can be named hosts-constructed.yml and match both)
		files = exclude(files, pluginFiles)

		// A plugin config marks this directory as a single merged
		// inventory (`-i dir/` semantics): combine every source file,
		// then emulate the constructed plugin on the result.
		if len(cfgs) > 0 {
			var fresh []string
			for _, f := range files {
				if !seenFiles[f] {
					seenFiles[f] = true
					fresh = append(fresh, f)
				}
			}
			if len(fresh) == 0 {
				return
			}
			name := mergedInventoryName(root, dir, usedNames)
			if inv, ok := parseInventorySources(root, fresh, name, dir); ok {
				for _, cfg := range cfgs {
					inv.ConstructedRules = append(inv.ConstructedRules, *cfg)
					applyConstructed(&inv, cfg)
				}
				usedNames[name] = true
				out = append(out, inv)
			}
			return
		}

		for _, f := range files {
			if seenFiles[f] {
				continue
			}
			seenFiles[f] = true
			name := inventoryName(root, dir, f, usedNames)
			if inv, ok := parseInventoryFile(root, f, name, dir); ok {
				// several sources share this dir: -i must target the file,
				// not the dir, or ansible would load all of them at once
				if len(files) > 1 {
					if rel, err := filepath.Rel(root, f); err == nil {
						inv.Path = rel
					}
				}
				usedNames[name] = true
				out = append(out, inv)
			}
		}
	}

	var walk func(dir string, depth int, parentIsInvDir bool)
	walk = func(dir string, depth int, parentIsInvDir bool) {
		if depth > maxInventoryDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		hasVars := false
		var subdirs []string
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			n := e.Name()
			if n == "group_vars" || n == "host_vars" {
				hasVars = true
				continue
			}
			if nonInventoryDirs[n] || strings.HasPrefix(n, ".") {
				continue
			}
			subdirs = append(subdirs, filepath.Join(dir, n))
		}
		isInvDir := inventoryDirNames[filepath.Base(dir)]
		if dir == root || isInvDir || parentIsInvDir || hasVars {
			addFromDir(dir)
		}
		for _, sd := range subdirs {
			walk(sd, depth+1, isInvDir)
		}
	}
	walk(root, 0, false)

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// inventoryFilesIn lists the inventory source files directly inside dir:
// hosts*/inventory* files with any of no/.ini/.yml/.yaml extension, any
// .ini file, or YAML files whose content is shaped like an inventory.
func inventoryFilesIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		stem := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		full := filepath.Join(dir, name)
		switch {
		case ext != "" && ext != ".ini" && ext != ".yml" && ext != ".yaml":
			continue
		case stem == "requirements" || stem == "galaxy" || stem == "site" || stem == "ansible":
			continue
		case strings.Contains(stem, "hosts") || strings.Contains(stem, "inventor"):
			out = append(out, full)
		case ext == ".ini":
			out = append(out, full)
		case ext == ".yml" || ext == ".yaml" || ext == "":
			// extensionless files (e.g. a root `production` inventory) are
			// admitted only when their content is shaped like a YAML inventory
			if looksLikeYAMLInventory(full) {
				out = append(out, full)
			}
		}
	}
	sort.Strings(out)
	return out
}

// pluginConfigsIn finds inventory plugin config files in dir. Every plugin
// file is returned in files (so it is never parsed as a hosts source);
// constructed-plugin configs are also parsed for emulation.
func pluginConfigsIn(dir string) (cfgs []*model.ConstructedRule, files []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") ||
			(!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}
		full := filepath.Join(dir, name)
		if isPlugin, cfg := parsePluginFile(full); isPlugin {
			files = append(files, full)
			if cfg != nil {
				cfgs = append(cfgs, cfg)
			}
		}
	}
	sort.Strings(files)
	return cfgs, files
}

func exclude(list, drop []string) []string {
	var out []string
	for _, e := range list {
		if !contains(drop, e) {
			out = append(out, e)
		}
	}
	return out
}

// mergedInventoryName names a directory-merged inventory after its dir,
// falling back to "default" for generic dir names and to the repo-relative
// path on collision.
func mergedInventoryName(root, dir string, used map[string]bool) string {
	name := filepath.Base(dir)
	if dir == root || inventoryDirNames[name] {
		name = "default"
	}
	if used[name] {
		if rel, err := filepath.Rel(root, dir); err == nil && rel != "." {
			name = rel
		}
	}
	return name
}

// inventoryName derives a human name for an inventory source: the env dir
// name when meaningful (inventories/production/hosts.ini -> production), the
// file stem when the dir is generic (inventories/staging.yml -> staging),
// and "default" when both are generic (inventories/hosts.yml, ./hosts).
// Collisions get the repo-relative path instead.
func inventoryName(root, dir, file string, used map[string]bool) string {
	dirBase := filepath.Base(dir)
	stem := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	name := dirBase
	if dir == root || inventoryDirNames[dirBase] {
		name = stem
		if genericInvStems[strings.ToLower(stem)] {
			name = "default"
		}
	}
	if used[name] {
		if rel, err := filepath.Rel(root, file); err == nil {
			name = strings.TrimSuffix(rel, filepath.Ext(rel))
		}
	}
	return name
}

func parseInventoryFile(root, file, name, varsDir string) (model.Inventory, bool) {
	return parseInventorySources(root, []string{file}, name, varsDir)
}

// parseInventorySources merges one or more inventory files (lexicographic
// order, like ansible does for `-i dir/`) into a single inventory, then
// layers varsDir's group_vars/host_vars on top.
func parseInventorySources(root string, files []string, name, varsDir string) (model.Inventory, bool) {
	b := newInvBuilder()
	format := ""
	sort.Strings(files)
	for _, file := range files {
		f := detectInventoryFormat(file)
		if f == "yaml" {
			if !parseYAMLInventory(file, b) {
				continue
			}
		} else {
			if !parseINIInventory(file, b) {
				continue
			}
		}
		if format == "" {
			format = f
		} else if format != f {
			format = "mixed"
		}
	}
	if format == "" {
		return model.Inventory{}, false
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
	if relPath == "." && len(files) == 1 {
		relPath, _ = filepath.Rel(root, files[0])
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

// detectInventoryFormat decides whether an inventory file is YAML or INI.
// Ansible accepts both regardless of name (a YAML inventory is commonly just
// "hosts" with no extension), so when the extension isn't decisive we sniff
// the content.
func detectInventoryFormat(file string) string {
	switch {
	case strings.HasSuffix(file, ".yml"), strings.HasSuffix(file, ".yaml"):
		return "yaml"
	case strings.HasSuffix(file, ".ini"):
		return "ini"
	}
	if looksLikeYAMLInventory(file) {
		return "yaml"
	}
	return "ini"
}

// maxInventorySniffBytes caps the size of a file we're willing to read and
// YAML-parse just to guess whether it's an inventory. Real inventories, even
// with thousands of hosts, stay far below this; the cap keeps the sniff from
// slurping large extensionless blobs (binaries, LFS artifacts) that now reach
// it via the extensionless branch of inventoryFilesIn.
const maxInventorySniffBytes = 4 << 20 // 4 MiB

// looksLikeYAMLInventory reports whether file parses as a YAML mapping that
// carries inventory structure (a group with hosts/children/vars, or the "all"
// root). INI inventories fail to unmarshal into a map, so this stays false.
func looksLikeYAMLInventory(file string) bool {
	if info, err := os.Stat(file); err != nil || info.Size() > maxInventorySniffBytes {
		return false
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc) == 0 {
		return false
	}
	if _, ok := doc["all"].(map[string]any); ok {
		return true
	}
	for _, v := range doc {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := m["hosts"]; ok {
			return true
		}
		if _, ok := m["children"]; ok {
			return true
		}
		if _, ok := m["vars"]; ok {
			return true
		}
	}
	return false
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
