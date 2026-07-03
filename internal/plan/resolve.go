package plan

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/scanner"
)

// HostInfo is one host in the "resolve as" picker. Varies marks hosts whose
// resolution would differ from the host-agnostic constants (host/group vars);
// Targeted marks hosts the playbook actually runs on (its plays' hosts: pattern).
type HostInfo struct {
	Name     string `json:"name"`
	Varies   bool   `json:"varies"`
	Targeted bool   `json:"targeted"`
}

// InvHosts lists an inventory's hosts, for the playbook "resolve as" picker.
type InvHosts struct {
	Name  string     `json:"name"`
	Hosts []HostInfo `json:"hosts"`
}

// PlayVars carries the effective variables of one play (index-aligned with the
// playbook's plays) plus the precedence chain of each, so the UI can both
// interpolate {{ vars }} inline and explain where a value comes from.
type PlayVars struct {
	Vars    map[string]any            `json:"vars"`
	Lineage map[string][]LineageEntry `json:"lineage"`
}

// ResolveResult answers "what do this playbook's variables resolve to?" either
// host-agnostically ("from my machine": role defaults, every inventory's
// group_vars/all, vars_files and play vars — the constants) or against one
// inventory host's full precedence chain.
type ResolveResult struct {
	Mode        string     `json:"mode"` // "machine" | "host"
	Inventory   string     `json:"inventory,omitempty"`
	Host        string     `json:"host,omitempty"`
	Inventories []InvHosts `json:"inventories"`
	Plays       []PlayVars `json:"plays"`
	// Known lists every variable name defined *somewhere* Pine can see across
	// the whole repo (role defaults & vars, every inventory's group_vars and
	// host_vars, this playbook's play vars & vars_files). It lets the UI tell
	// "defined elsewhere, just not in this scope" apart from "defined nowhere".
	Known []string `json:"known"`
	// VaultVars names the variables whose value is ansible-vault encrypted, so
	// the UI can prompt for a vault password to decrypt them in a plan.
	VaultVars []string `json:"vault_vars,omitempty"`
}

// Resolve computes per-play effective variables for one playbook. With no host
// it resolves host-agnostically; with inventory+host it resolves against that
// host. Values that read as secrets are redacted by the caller via Redact.
func Resolve(res *model.ScanResult, root, playbookPath, inventory, host string) (*ResolveResult, error) {
	var pb *model.Playbook
	for i := range res.Playbooks {
		if res.Playbooks[i].Path == playbookPath {
			pb = &res.Playbooks[i]
		}
	}
	if pb == nil {
		return nil, fmt.Errorf("playbook not found: %s", playbookPath)
	}

	var hostPatterns []string
	for pi := range pb.Plays {
		if pb.Plays[pi].Import == "" && pb.Plays[pi].Hosts != "" {
			hostPatterns = append(hostPatterns, pb.Plays[pi].Hosts)
		}
	}
	out := &ResolveResult{Mode: "machine", Inventories: invHostsList(res, relevantVarNames(res, root, pb), hostPatterns), Plays: []PlayVars{}}

	// Optional host scope.
	var inv *model.Inventory
	var theHost *model.Host
	var depth map[string]int
	if host != "" {
		if inv = pickInventory(res, inventory); inv != nil {
			out.Inventory = inv.Name
			for i := range inv.Hosts {
				if inv.Hosts[i].Name == host {
					theHost = &inv.Hosts[i]
				}
			}
			if theHost != nil {
				out.Mode, out.Host = "host", host
				depth = newVarResolver(inv, nil, nil, nil).depth
			}
		}
	}

	roleByName := map[string]*model.Role{}
	for i := range res.Roles {
		roleByName[res.Roles[i].Name] = &res.Roles[i]
	}

	for pi := range pb.Plays {
		play := &pb.Plays[pi]
		pv := PlayVars{Vars: map[string]any{}, Lineage: map[string][]LineageEntry{}}
		if play.Import != "" { // import_playbook stage carries no vars of its own
			out.Plays = append(out.Plays, pv)
			continue
		}
		// add layers in increasing precedence so the last chain entry wins.
		add := func(scope, name string, vars map[string]any) {
			for _, k := range sortedMapKeys(vars) {
				pv.Vars[k] = vars[k]
				pv.Lineage[k] = append(pv.Lineage[k], LineageEntry{Scope: scope, Name: name, Value: vars[k]})
			}
		}

		// roles in scope: those listed under `roles:` plus any pulled in via
		// include_role / import_role tasks.
		roleNames := append([]string{}, play.Roles...)
		collectRoleRefs(&roleNames, play.PreTasks)
		collectRoleRefs(&roleNames, play.Tasks)
		collectRoleRefs(&roleNames, play.PostTasks)
		roleNames = uniqueStrings(roleNames)

		// role defaults are the lowest precedence layer
		for _, rn := range roleNames {
			if r := roleByName[rn]; r != nil {
				add("role_default", rn, r.Defaults)
			}
		}

		if theHost != nil {
			gv := groupVarsOf(inv)
			add("group", "all", gv["all"])
			groups := append([]string{}, theHost.Groups...)
			sort.SliceStable(groups, func(i, j int) bool { return depth[groups[i]] < depth[groups[j]] })
			for _, gn := range groups {
				if gn != "all" {
					add("group", gn, gv[gn])
				}
			}
			add("host", theHost.Name, theHost.Vars)
		} else {
			// machine scope: fold in every inventory's group_vars/all (the
			// constants a local run would inherit), labelling provenance by
			// inventory when there is more than one.
			for i := range res.Inventories {
				iv := &res.Inventories[i]
				name := "all"
				if len(res.Inventories) > 1 && iv.Name != "" {
					name = iv.Name + ":all"
				}
				add("group", name, groupVarsOf(iv)["all"])
			}
		}

		// Ansible play-level precedence (low → high): play vars (12) < vars_prompt
		// (13) < vars_files (14). Add them in that order so the last write wins.
		add("play_vars", playLabel(play, pi), play.Vars)

		// vars_prompt: the variable IS defined (asked interactively). Use its
		// default — resolved against what we have so far — so a prompted var is
		// never mistaken for undefined.
		for _, pr := range play.VarsPrompt {
			if pr.Name == "" {
				continue
			}
			var val any = "(prompted)"
			if pr.Default != "" {
				if s, _, _ := scanner.Interpolate(pr.Default, pv.Vars); s != "" {
					val = s
				}
			}
			pv.Vars[pr.Name] = val
			pv.Lineage[pr.Name] = append(pv.Lineage[pr.Name], LineageEntry{Scope: "vars_prompt", Name: pr.Name, Value: val})
		}

		// vars_files outrank play vars and vars_prompt in Ansible's precedence,
		// so load them last — last write wins.
		for _, vf := range play.VarsFiles {
			if vars := loadPlayVarsFile(root, pb.Path, vf); len(vars) > 0 {
				add("vars_file", vf, vars)
			}
		}

		// role vars/main.yml sit near the top of Ansible's precedence — above
		// play vars and vars_files — so they win and are added last.
		for _, rn := range roleNames {
			if r := roleByName[rn]; r != nil {
				add("role_vars", rn, r.Vars)
			}
		}

		// include_vars from the play's tasks (following inlined import_tasks), in
		// task order — they sit even higher in Ansible's precedence, and a later
		// include_vars overrides an earlier one for the same key.
		for _, iv := range collectIncludeVars(play) {
			if vars := scanner.LoadVarsFile(filepath.Join(root, iv)); len(vars) > 0 {
				add("include_vars", filepath.Base(iv), vars)
			}
		}

		if theHost != nil {
			pv.Vars["inventory_hostname"] = theHost.Name
		}
		expandNestedVars(pv.Vars)
		out.Plays = append(out.Plays, pv)
	}
	out.Known = knownVarNames(res, root, pb)
	// flag vault-encrypted vars (before any redaction) so the UI can offer to
	// decrypt them with a vault password in a plan.
	seen := map[string]bool{}
	for _, pv := range out.Plays {
		for k, v := range pv.Vars {
			if isVaultValue(v) && !seen[k] {
				seen[k] = true
				out.VaultVars = append(out.VaultVars, k)
			}
		}
	}
	sort.Strings(out.VaultVars)
	return out, nil
}

// expandNestedVars resolves {{ var }} references *inside* resolved values
// against the full map, so a value like "{{ monitoring_dir }}/alloy" becomes
// the composed path (e.g. "{{ ansible_user_dir }}/monitoring/alloy") — the
// effective value, not the literal as-written. References Pine can't resolve
// (facts, runtime vars) are left intact. Only pv.Vars is rewritten; the lineage
// chain keeps each layer's value as it was authored.
func expandNestedVars(vars map[string]any) {
	for k, v := range vars {
		s, ok := v.(string)
		if !ok || !strings.Contains(s, "{{") {
			continue
		}
		for range 10 { // chase chains; capped against cycles
			out, _, _ := scanner.Interpolate(s, vars)
			if out == s {
				break
			}
			s = out
		}
		vars[k] = s
	}
}

// knownVarNames collects every variable name defined anywhere Pine scans — so
// the UI can distinguish "not in this scope" from "defined nowhere at all"
// (including no host var). Magic/fact vars are intentionally excluded; the UI
// recognizes those separately.
func knownVarNames(res *model.ScanResult, root string, pb *model.Playbook) []string {
	set := map[string]bool{}
	addKeys := func(m map[string]any) {
		for k := range m {
			set[k] = true
		}
	}
	for i := range res.Roles {
		addKeys(res.Roles[i].Defaults)
		addKeys(res.Roles[i].Vars)
	}
	for i := range res.Inventories {
		for _, g := range res.Inventories[i].Groups {
			addKeys(g.Vars)
		}
		for _, h := range res.Inventories[i].Hosts {
			addKeys(h.Vars)
		}
	}
	for pi := range pb.Plays {
		addKeys(pb.Plays[pi].Vars)
		for _, pr := range pb.Plays[pi].VarsPrompt {
			if pr.Name != "" {
				set[pr.Name] = true
			}
		}
		for _, vf := range pb.Plays[pi].VarsFiles {
			addKeys(loadPlayVarsFile(root, pb.Path, vf))
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Redact masks secret values in the resolved vars and their chains, reusing the
// same heuristic as lineage so the playbook visualization never renders a
// password or vault blob inline. Structure (provenance) is preserved.
func (r *ResolveResult) Redact() {
	for pi := range r.Plays {
		pv := &r.Plays[pi]
		for k, v := range pv.Vars {
			if sensitiveValue(secretKeyRe.MatchString(k), v) {
				pv.Vars[k] = RedactedMark
			}
		}
		for k := range pv.Lineage {
			keySecret := secretKeyRe.MatchString(k)
			chain := pv.Lineage[k]
			for j := range chain {
				if sensitiveValue(keySecret, chain[j].Value) {
					chain[j].Value = RedactedMark
				}
			}
		}
	}
}

func groupVarsOf(inv *model.Inventory) map[string]map[string]any {
	gv := map[string]map[string]any{}
	if inv == nil {
		return gv
	}
	for _, g := range inv.Groups {
		gv[g.Name] = g.Vars
	}
	return gv
}

var jinjaRefRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)`)

// playbookRefs collects the base variable names a playbook's tasks reference in
// {{ … }} (names, args, loop expressions), so "varies" can be scoped to the
// variables this playbook actually uses.
func playbookRefs(pb *model.Playbook) map[string]bool {
	set := map[string]bool{}
	add := func(s string) {
		for _, m := range jinjaRefRe.FindAllStringSubmatch(s, -1) {
			if m[1] != "item" {
				set[m[1]] = true
			}
		}
	}
	var walk func(tasks []model.Task)
	walk = func(tasks []model.Task) {
		for i := range tasks {
			t := &tasks[i]
			add(t.Name)
			add(t.Args)
			add(t.LoopExpr)
			walk(t.Block)
			walk(t.Rescue)
			walk(t.Always)
			walk(t.Imported)
		}
	}
	for pi := range pb.Plays {
		p := &pb.Plays[pi]
		walk(p.PreTasks)
		walk(p.Tasks)
		walk(p.PostTasks)
		walk(p.Handlers)
	}
	return set
}

// relevantVarNames is the set of variables that matter to a playbook: the ones
// it references in {{ … }}, plus the "constant" layers it resolves from (role
// defaults & vars, group_vars/all, vars_files, play vars & prompts). A host
// "varies" when its own host/group vars touch one of these — meaning resolving
// as that host changes a value the playbook uses (e.g. a region group that
// overrides the registry or a port).
func relevantVarNames(res *model.ScanResult, root string, pb *model.Playbook) map[string]bool {
	set := playbookRefs(pb)
	add := func(m map[string]any) {
		for k := range m {
			set[k] = true
		}
	}
	for i := range res.Roles {
		add(res.Roles[i].Defaults)
		add(res.Roles[i].Vars)
	}
	for i := range res.Inventories {
		for _, g := range res.Inventories[i].Groups {
			if g.Name == "all" {
				add(g.Vars)
			}
		}
	}
	for pi := range pb.Plays {
		add(pb.Plays[pi].Vars)
		for _, pr := range pb.Plays[pi].VarsPrompt {
			if pr.Name != "" {
				set[pr.Name] = true
			}
		}
		for _, vf := range pb.Plays[pi].VarsFiles {
			add(loadPlayVarsFile(root, pb.Path, vf))
		}
	}
	return set
}

// invHostsList builds the "resolve as" picker. A host is flagged Varies when its
// host_vars or a non-"all" group it belongs to defines one of the variables the
// playbook uses (see relevantVarNames) — i.e. resolving as that host would
// actually change a value this playbook resolves.
func invHostsList(res *model.ScanResult, refs map[string]bool, hostPatterns []string) []InvHosts {
	out := make([]InvHosts, 0, len(res.Inventories))
	for i := range res.Inventories {
		iv := &res.Inventories[i]
		groupVaries := map[string]bool{}
		for _, g := range iv.Groups {
			if g.Name == "all" {
				continue
			}
			for k := range g.Vars {
				if refs[k] {
					groupVaries[g.Name] = true
					break
				}
			}
		}
		// hosts the playbook's plays actually target (its hosts: pattern)
		targeted := map[string]bool{}
		for _, pat := range hostPatterns {
			for _, hn := range scanner.MatchHosts(pat, iv) {
				targeted[hn] = true
			}
		}
		hosts := make([]HostInfo, 0, len(iv.Hosts))
		for _, h := range iv.Hosts {
			varies := false
			for k := range h.Vars {
				if refs[k] {
					varies = true
					break
				}
			}
			if !varies {
				for _, gn := range h.Groups {
					if groupVaries[gn] {
						varies = true
						break
					}
				}
			}
			hosts = append(hosts, HostInfo{Name: h.Name, Varies: varies, Targeted: targeted[h.Name]})
		}
		// targeted hosts first, then alphabetical
		sort.Slice(hosts, func(a, b int) bool {
			if hosts[a].Targeted != hosts[b].Targeted {
				return hosts[a].Targeted
			}
			return hosts[a].Name < hosts[b].Name
		})
		out = append(out, InvHosts{Name: iv.Name, Hosts: hosts})
	}
	return out
}

func loadPlayVarsFile(root, pbPath, vf string) map[string]any {
	pbDir := filepath.Join(root, filepath.Dir(pbPath))
	// Resolve the common templated prefixes statically (Ansible expands these
	// from the playbook's location), so e.g. "{{ playbook_dir }}/vars/x.yml"
	// still loads. Anything else templated we can't resolve.
	for _, pd := range []string{"{{ playbook_dir }}", "{{playbook_dir}}"} {
		vf = strings.ReplaceAll(vf, pd, pbDir)
	}
	vf = strings.TrimPrefix(vf, "./")
	if strings.Contains(vf, "{{") {
		return nil
	}
	cands := []string{vf, filepath.Join(pbDir, vf), filepath.Join(root, vf)}
	for _, cand := range cands {
		if vars := scanner.LoadVarsFile(cand); vars != nil {
			return vars
		}
	}
	return nil
}

func playLabel(play *model.Play, idx int) string {
	if play.Name != "" {
		return play.Name
	}
	return fmt.Sprintf("play %d", idx+1)
}

// collectIncludeVars returns the repo-relative include_vars files a play loads,
// in task order (following blocks and inlined import_tasks) — so the resolver
// applies them like Ansible does.
func collectIncludeVars(play *model.Play) []string {
	var out []string
	var walk func(tasks []model.Task)
	walk = func(tasks []model.Task) {
		for i := range tasks {
			t := &tasks[i]
			if t.IncludeVars != "" {
				out = append(out, t.IncludeVars)
			}
			walk(t.Block)
			walk(t.Rescue)
			walk(t.Always)
			walk(t.Imported)
		}
	}
	walk(play.PreTasks)
	walk(play.Tasks)
	walk(play.PostTasks)
	return out
}

// ResolveLineage answers "what are this playbook's effective variables for one
// host?" — the full playbook resolution (group/host vars, role defaults & vars,
// vars_files, play vars, vars_prompt AND include_vars / import_tasks) flattened
// into the same per-host shape as Lineage, with provenance per variable.
func ResolveLineage(res *model.ScanResult, root, playbook, inventory, host string) (*LineageResult, error) {
	rr, err := Resolve(res, root, playbook, inventory, host)
	if err != nil {
		return nil, err
	}
	inv := rr.Inventory
	if inv == "" {
		inv = inventory
	}
	merged := map[string]VarLineage{}
	for _, pv := range rr.Plays {
		for k, v := range pv.Vars {
			merged[k] = VarLineage{Key: k, Value: v, Chain: pv.Lineage[k]}
		}
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := &LineageResult{Host: host, Inventory: inv, Vars: make([]VarLineage, 0, len(keys))}
	for _, k := range keys {
		out.Vars = append(out.Vars, merged[k])
	}
	return out, nil
}

// ResolveLineageAll runs ResolveLineage for every host of an inventory.
func ResolveLineageAll(res *model.ScanResult, root, playbook, inventory string) ([]*LineageResult, error) {
	inv := pickInventory(res, inventory)
	if inv == nil {
		return nil, fmt.Errorf("inventory not found: %s", inventory)
	}
	out := make([]*LineageResult, 0, len(inv.Hosts))
	for i := range inv.Hosts {
		l, err := ResolveLineage(res, root, playbook, inventory, inv.Hosts[i].Name)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

// collectRoleRefs appends role names referenced by include_role/import_role
// tasks (recursing into blocks and inlined import_tasks) to dst.
func collectRoleRefs(dst *[]string, tasks []model.Task) {
	for i := range tasks {
		t := &tasks[i]
		if t.RoleRef != "" {
			*dst = append(*dst, t.RoleRef)
		}
		collectRoleRefs(dst, t.Block)
		collectRoleRefs(dst, t.Rescue)
		collectRoleRefs(dst, t.Always)
		collectRoleRefs(dst, t.Imported)
	}
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func sortedMapKeys(m map[string]any) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
