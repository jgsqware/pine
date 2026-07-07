package main

import (
	"regexp"
	"strings"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
)

// publishDiagnostics computes and sends diagnostics for one open document:
//   - warning: a variable referenced in {{ … }} that is defined in no scanned
//     scope ("defined nowhere") — playbook files only, where refs are known.
//   - hint:    a variable defined in this file that the hygiene engine reports
//     as never used anywhere in the repo.
//   - info:    the file belongs to a role no playbook/role/include ever
//     references (dead role).
//
// Positions are derived from the raw document text (exact for var refs and key
// definitions); the dead-role note lands on line 0 since a role has no single
// line. Diagnostics use plan.Hygiene and plan.Resolve so they mirror Pine's
// engine exactly.
func (s *server) publishDiagnostics(uri string) {
	res, root, text := s.snapshot(uri)
	if res == nil || text == "" {
		return
	}
	var diags []diagnostic
	lines := strings.Split(text, "\n")

	// --- unused vars (hint) + defined-nowhere (warning) ---
	hyg := plan.Hygiene(res, root)
	unused := map[string]bool{}
	for _, uv := range hyg.UnusedVars {
		unused[uv.Key] = true
	}
	for li, line := range lines {
		if loc := keyDefIndex(line); loc != nil {
			key := line[loc[0]:loc[1]]
			if unused[key] {
				diags = append(diags, diagnostic{
					Range:    spanRange(li, loc[0], loc[1]),
					Severity: severityHint,
					Source:   "pine",
					Message:  "'" + key + "' is defined here but never referenced anywhere Pine scanned (playbooks, roles, templates). Dead variable — safe to remove, or it is consumed only at runtime.",
				})
			}
		}
	}

	// defined-nowhere applies to playbooks, where the resolver knows the full
	// set of defined names (Known) and the doc supplies the {{ refs }}.
	if pbRel := playbookRel(res, root, uri); pbRel != "" {
		if rr, err := plan.Resolve(res, root, pbRel, "", ""); err == nil {
			known := map[string]bool{}
			for _, k := range rr.Known {
				known[k] = true
			}
			local := localRuntimeNames(lines) // register / set_fact / loop_var
			for li, line := range lines {
				for _, ref := range jinjaRefs(line) {
					name := ref.name
					if known[name] || isRuntimeVar(name) || local[name] {
						continue
					}
					diags = append(diags, diagnostic{
						Range:    spanRange(li, ref.start, ref.end),
						Severity: severityWarning,
						Source:   "pine",
						Message:  "'" + name + "' is referenced but defined in no scanned scope (role defaults/vars, group_vars, host_vars, play vars, vars_files). If it is set at runtime (register/set_fact/facts), this is expected.",
					})
				}
			}
		}
	}

	// --- dead role (info), when the open file lives under an unused role ---
	for _, ur := range hyg.UnusedRoles {
		if roleName := roleOfFile(res, root, uri); roleName == ur.Name {
			diags = append(diags, diagnostic{
				Range:    spanRange(0, 0, 0),
				Severity: severityInfo,
				Source:   "pine",
				Message:  "role '" + ur.Name + "' is " + ur.Reason + " — it appears to be dead code.",
			})
			break
		}
	}

	if diags == nil {
		diags = []diagnostic{}
	}
	_ = s.conn.notify("textDocument/publishDiagnostics", publishDiagnosticsParams{
		URI: uri, Diagnostics: diags,
	})
}

// spanRange builds a single-line range.
func spanRange(line, start, end int) rng {
	return rng{
		Start: position{Line: line, Character: start},
		End:   position{Line: line, Character: end},
	}
}

var keyDefRe = regexp.MustCompile(`^(\s*)([A-Za-z_][A-Za-z0-9_]*)\s*:`)

// keyDefIndex returns the [start,end] byte span of a top-of-line YAML key
// definition (`key:`), or nil. Leading `- ` list markers are not treated as key
// defs (those are list items, not mappings).
func keyDefIndex(line string) []int {
	m := keyDefRe.FindStringSubmatchIndex(line)
	if m == nil {
		return nil
	}
	// group 2 is the key
	return []int{m[4], m[5]}
}

// jinjaRef is one `{{ … }}` reference: its base variable name and column span.
type jinjaRef struct {
	name  string
	start int
	end   int
}

var jinjaSpanRe = regexp.MustCompile(`\{\{(.*?)\}\}`)

// jinjaRefs extracts the base variable of every `{{ … }}` expression on a line,
// with the column span of that identifier. Only the first identifier of each
// expression is taken (the referenced variable), so filter names after `|` and
// nested lookups are ignored — matching how plan resolves refs.
func jinjaRefs(line string) []jinjaRef {
	var out []jinjaRef
	for _, m := range jinjaSpanRe.FindAllStringSubmatchIndex(line, -1) {
		inner := line[m[2]:m[3]]
		loc := identRe.FindStringIndex(inner)
		if loc == nil {
			continue
		}
		name := inner[loc[0]:loc[1]]
		// Skip pure Jinja keywords that can lead an expression.
		switch name {
		case "not", "true", "false", "none", "lookup", "query", "range":
			continue
		}
		start := m[2] + loc[0]
		out = append(out, jinjaRef{name: name, start: start, end: start + len(name)})
	}
	return out
}

var (
	registerRe = regexp.MustCompile(`^\s*register:\s*([A-Za-z_][A-Za-z0-9_]*)`)
	loopVarRe  = regexp.MustCompile(`^\s*loop_var:\s*([A-Za-z_][A-Za-z0-9_]*)`)
	setFactRe  = regexp.MustCompile(`^(\s*)(?:ansible\.builtin\.)?set_fact:\s*$`)
)

// localRuntimeNames collects variable names a document defines at runtime —
// register targets, loop_var names, and keys set under a `set_fact:` mapping —
// so they are not mistaken for "defined nowhere". This is a textual heuristic
// (no YAML AST); inline set_fact and templated names may be missed.
func localRuntimeNames(lines []string) map[string]bool {
	out := map[string]bool{}
	inSetFact := -1 // indent of the set_fact: key, or -1 when outside one
	for _, line := range lines {
		if m := registerRe.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
		if m := loopVarRe.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
		if m := setFactRe.FindStringSubmatchIndex(line); m != nil {
			inSetFact = m[3] - m[2] // indent width
			continue
		}
		if inSetFact >= 0 {
			indent := len(line) - len(strings.TrimLeft(line, " "))
			if strings.TrimSpace(line) == "" {
				continue
			}
			if indent <= inSetFact {
				inSetFact = -1 // dedented out of the mapping
			} else if kd := keyDefIndex(line); kd != nil {
				out[line[kd[0]:kd[1]]] = true
			}
		}
	}
	return out
}

// roleOfFile returns the role name a document belongs to (its path contains
// roles/<name>/…), or "".
func roleOfFile(res *model.ScanResult, root, uri string) string {
	rel := relPath(root, uri)
	if rel == "" {
		return ""
	}
	parts := strings.Split(rel, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "roles" {
			return parts[i+1]
		}
	}
	return ""
}
