package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
)

// identRe matches a bare variable/identifier token.
var identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// handleHover answers textDocument/hover: it finds the variable token under the
// cursor and renders its precedence lineage (where it is defined, which value
// wins, at which level) as Markdown, reusing plan.Resolve / plan.Lineage.
func (s *server) handleHover(m *message) {
	var p hoverParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		_ = s.conn.respond(m.ID, nil)
		return
	}
	res, root, text := s.snapshot(p.TextDocument.URI)
	if res == nil || text == "" {
		_ = s.conn.respond(m.ID, nil)
		return
	}
	word, wr, ok := wordAt(text, p.Position)
	if !ok {
		_ = s.conn.respond(m.ID, nil)
		return
	}
	md, ok := s.lineageMarkdown(res, root, p.TextDocument.URI, word)
	if !ok {
		_ = s.conn.respond(m.ID, nil)
		return
	}
	_ = s.conn.respond(m.ID, hoverResult{
		Contents: markupContent{Kind: "markdown", Value: md},
		Range:    &wr,
	})
}

// wordAt returns the identifier token under the cursor, its document range and
// whether one was found. It handles both `{{ var }}` references and bare YAML
// keys/usages by scanning the identifiers on the cursor's line and picking the
// one whose span contains the cursor (or ends exactly at it).
func wordAt(text string, pos position) (string, rng, bool) {
	lines := strings.Split(text, "\n")
	if pos.Line < 0 || pos.Line >= len(lines) {
		return "", rng{}, false
	}
	line := lines[pos.Line]
	for _, loc := range identRe.FindAllStringIndex(line, -1) {
		start, end := loc[0], loc[1]
		if pos.Character >= start && pos.Character <= end {
			return line[start:end], rng{
				Start: position{Line: pos.Line, Character: start},
				End:   position{Line: pos.Line, Character: end},
			}, true
		}
	}
	return "", rng{}, false
}

// magicVars are variables Ansible provides at runtime (facts, connection and
// inventory magic vars). They are never "defined nowhere"; hover labels them as
// runtime-resolved rather than showing a precedence chain.
var magicVars = map[string]bool{
	"inventory_hostname": true, "inventory_hostname_short": true,
	"hostvars": true, "groups": true, "group_names": true,
	"ansible_play_hosts": true, "ansible_play_batch": true,
	"play_hosts": true, "inventory_dir": true, "playbook_dir": true,
	"omit": true, "item": true, "ansible_host": true, "ansible_user": true,
}

// isRuntimeVar reports whether a name is resolved by Ansible at run time
// (facts, magic vars) and so is legitimately absent from static scopes.
func isRuntimeVar(name string) bool {
	return magicVars[name] ||
		strings.HasPrefix(name, "ansible_") ||
		strings.HasPrefix(name, "hostvars")
}

// lineageMarkdown builds the hover body for varName. Resolution order:
//  1. If the document is a playbook, resolve against a representative targeted
//     host (full precedence chain via plan.ResolveLineage).
//  2. Otherwise (or if the var is not in that play's scope), search every
//     scanned scope repo-wide for definitions of the name.
//  3. Runtime/magic vars get a short note instead of a chain.
//
// Returns ok=false when the token is not a variable Pine can say anything
// useful about (avoids noisy tooltips on random words).
func (s *server) lineageMarkdown(res *model.ScanResult, root, uri, varName string) (string, bool) {
	// 1. Playbook-scoped lineage against a representative host.
	if pbRel := playbookRel(res, root, uri); pbRel != "" {
		if inv, host, ok := pickTargetedHost(res, root, pbRel); ok {
			if lin, err := plan.ResolveLineage(res, root, pbRel, inv, host); err == nil {
				if vl, found := findVar(lin.Vars, varName); found {
					return renderChain(res, root, varName, vl, fmt.Sprintf("playbook `%s`, resolved as host `%s`", pbRel, host)), true
				}
			}
		}
		// Fall back to host-agnostic ("from my machine") resolution.
		if rr, err := plan.Resolve(res, root, pbRel, "", ""); err == nil {
			for _, pv := range rr.Plays {
				if v, present := pv.Vars[varName]; present {
					vl := plan.VarLineage{Key: varName, Value: v, Chain: pv.Lineage[varName]}
					return renderChain(res, root, varName, vl, fmt.Sprintf("playbook `%s`, host-agnostic (from-machine) scope", pbRel)), true
				}
			}
		}
	}

	// 2. Repo-wide definition search (works for group_vars/host_vars/role files).
	if defs := repoWideDefs(res, varName); len(defs) > 0 {
		return renderDefs(varName, defs), true
	}

	// 3. Runtime/magic.
	if isRuntimeVar(varName) {
		return fmt.Sprintf("### `%s`\n\n_Runtime variable_ — resolved by Ansible at run time (fact, connection or inventory magic var). Pine does not track a static precedence chain for it.", varName), true
	}
	return "", false
}

// findVar returns the VarLineage for key, if present.
func findVar(vars []plan.VarLineage, key string) (plan.VarLineage, bool) {
	for _, v := range vars {
		if v.Key == key {
			return v, true
		}
	}
	return plan.VarLineage{}, false
}

// playbookRel returns the repo-relative path of uri if it is a scanned
// playbook, else "".
func playbookRel(res *model.ScanResult, root, uri string) string {
	rel := relPath(root, uri)
	if rel == "" {
		return ""
	}
	for i := range res.Playbooks {
		if res.Playbooks[i].Path == rel {
			return rel
		}
	}
	return ""
}

// relPath converts a document URI to a path relative to root.
func relPath(root, uri string) string {
	p := uriToPath(uri)
	if p == "" || root == "" {
		return ""
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return rel
}

// pickTargetedHost returns an inventory + host that a playbook's plays actually
// target, so the hover shows a real precedence chain. It reuses Resolve's
// per-inventory host list (Targeted flag).
func pickTargetedHost(res *model.ScanResult, root, pbRel string) (inv, host string, ok bool) {
	rr, err := plan.Resolve(res, root, pbRel, "", "")
	if err != nil {
		return "", "", false
	}
	for _, iv := range rr.Inventories {
		for _, h := range iv.Hosts {
			if h.Targeted {
				return iv.Name, h.Name, true
			}
		}
	}
	// No targeted host (e.g. pattern doesn't match): fall back to any host.
	for _, iv := range rr.Inventories {
		if len(iv.Hosts) > 0 {
			return iv.Name, iv.Hosts[0].Name, true
		}
	}
	return "", "", false
}

// renderChain formats a variable's precedence chain as Markdown: each layer in
// increasing precedence, the winning (effective) value highlighted, with a
// best-effort file:line for every layer.
func renderChain(res *model.ScanResult, root, varName string, vl plan.VarLineage, context string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### `%s`\n\n", varName)
	fmt.Fprintf(&b, "**Effective value:** `%s`\n\n", valStr(vl.Value))
	fmt.Fprintf(&b, "_Scope: %s_\n\n", context)
	if len(vl.Chain) == 0 {
		return b.String()
	}
	b.WriteString("**Precedence chain** (low → high, last wins):\n\n")
	for i, e := range vl.Chain {
		marker := ""
		if i == len(vl.Chain)-1 {
			marker = " ✅ **effective**"
		}
		loc := locateDef(res, root, e.Scope, e.Name, varName)
		if loc != "" {
			loc = " — " + loc
		}
		fmt.Fprintf(&b, "%d. **%s** `%s` = `%s`%s%s\n", i+1, e.Scope, e.Name, valStr(e.Value), loc, marker)
	}
	return b.String()
}

// def is one repo-wide definition of a variable.
type def struct {
	scope string
	name  string
	value any
}

// repoWideDefs collects every scanned scope that defines varName (role
// defaults/vars, group_vars, host_vars), lowest to highest precedence.
func repoWideDefs(res *model.ScanResult, varName string) []def {
	var out []def
	// group_vars / host_vars
	for i := range res.Inventories {
		iv := &res.Inventories[i]
		for _, g := range iv.Groups {
			if v, ok := g.Vars[varName]; ok {
				out = append(out, def{"group_vars", g.Name + " (" + iv.Name + ")", v})
			}
		}
		for _, h := range iv.Hosts {
			if v, ok := h.Vars[varName]; ok {
				out = append(out, def{"host_vars", h.Name + " (" + iv.Name + ")", v})
			}
		}
	}
	// role defaults / vars
	for i := range res.Roles {
		r := &res.Roles[i]
		if v, ok := r.Defaults[varName]; ok {
			out = append(out, def{"role_default", r.Name, v})
		}
		if v, ok := r.Vars[varName]; ok {
			out = append(out, def{"role_vars", r.Name, v})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return precRank(out[i].scope) < precRank(out[j].scope) })
	return out
}

// precRank orders scopes low → high precedence for display.
func precRank(scope string) int {
	switch scope {
	case "role_default":
		return 0
	case "group_vars":
		return 1
	case "host_vars":
		return 2
	case "role_vars":
		return 3
	}
	return 4
}

// renderDefs formats a repo-wide definition list when no single-host resolution
// applies (e.g. hovering a key in a group_vars file).
func renderDefs(varName string, defs []def) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### `%s`\n\n", varName)
	fmt.Fprintf(&b, "**Effective value:** `%s`\n\n", valStr(defs[len(defs)-1].value))
	b.WriteString("_Scope: repository-wide (no single target host)_\n\n")
	b.WriteString("**Defined in** (low → high precedence):\n\n")
	for i, d := range defs {
		marker := ""
		if i == len(defs)-1 {
			marker = " ✅ **wins**"
		}
		fmt.Fprintf(&b, "%d. **%s** `%s` = `%s`%s\n", i+1, d.scope, d.name, valStr(d.value), marker)
	}
	return b.String()
}

// locateDef best-effort resolves a lineage layer to a `relpath:line` string by
// mapping the scope+name to a likely file and grepping for the key. Returns ""
// when the file or key line cannot be found (positions are approximate).
func locateDef(res *model.ScanResult, root, scope, name, key string) string {
	var candidates []string
	switch scope {
	case "role_default", "role_vars":
		sub := "defaults"
		if scope == "role_vars" {
			sub = "vars"
		}
		for i := range res.Roles {
			if res.Roles[i].Name == name {
				candidates = append(candidates,
					filepath.Join(res.Roles[i].Path, sub, "main.yml"),
					filepath.Join(res.Roles[i].Path, sub, "main.yaml"))
			}
		}
	case "group", "group_vars":
		gname := name
		if i := strings.Index(gname, " ("); i >= 0 {
			gname = gname[:i]
		}
		for i := range res.Inventories {
			d := filepath.Dir(res.Inventories[i].Path)
			candidates = append(candidates,
				filepath.Join(d, "group_vars", gname+".yml"),
				filepath.Join(d, "group_vars", gname+".yaml"))
		}
	case "host", "host_vars":
		hname := name
		if i := strings.Index(hname, " ("); i >= 0 {
			hname = hname[:i]
		}
		for i := range res.Inventories {
			d := filepath.Dir(res.Inventories[i].Path)
			candidates = append(candidates,
				filepath.Join(d, "host_vars", hname+".yml"),
				filepath.Join(d, "host_vars", hname+".yaml"))
		}
	}
	for _, c := range candidates {
		abs := c
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, c)
		}
		if line := grepKeyLine(abs, key); line > 0 {
			rel, err := filepath.Rel(root, abs)
			if err != nil {
				rel = c
			}
			return fmt.Sprintf("`%s:%d`", rel, line)
		}
	}
	return ""
}

// keyLineRe builds a matcher for a YAML key definition at the start of a line.
func keyLineRe(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `\s*:`)
}

// grepKeyLine returns the 1-based line where key is defined in file, or 0.
func grepKeyLine(file, key string) int {
	data, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	re := keyLineRe(key)
	for i, line := range strings.Split(string(data), "\n") {
		if re.MatchString(line) {
			return i + 1
		}
	}
	return 0
}

// valStr renders a resolved value compactly for a tooltip, truncating long
// collections/strings.
func valStr(v any) string {
	if v == nil {
		return "null"
	}
	s := fmt.Sprintf("%v", v)
	s = strings.ReplaceAll(s, "`", "'")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 120 {
		s = s[:117] + "…"
	}
	return s
}
