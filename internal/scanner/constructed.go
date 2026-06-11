package scanner

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jgsqware/pine/internal/model"
)

// constructedConfig is a parsed ansible.builtin.constructed inventory
// plugin file (e.g. inventory/99-constructed.yml): groups derived from
// Jinja conditions over host vars, plus keyed_groups.
type constructedConfig struct {
	Groups      map[string]string
	KeyedGroups []keyedGroup
}

type keyedGroup struct {
	Key       string
	Prefix    string
	Separator string
}

// parsePluginFile reports whether file is an inventory *plugin* config
// (any `plugin:` key). The second return is non-nil only for the
// constructed plugin, the one Pine can emulate.
func parsePluginFile(file string) (isPlugin bool, cfg *constructedConfig) {
	doc := parseVarsFile(file)
	if doc == nil {
		return false, nil
	}
	plugin, _ := doc["plugin"].(string)
	if plugin == "" {
		return false, nil
	}
	if plugin != "constructed" && plugin != "ansible.builtin.constructed" {
		return true, nil
	}
	cfg = &constructedConfig{Groups: map[string]string{}}
	if groups, ok := doc["groups"].(map[string]any); ok {
		for name, expr := range groups {
			if s, ok := expr.(string); ok {
				cfg.Groups[name] = s
			}
		}
	}
	if kgs, ok := doc["keyed_groups"].([]any); ok {
		for _, e := range kgs {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			kg := keyedGroup{
				Key:       toStr(m["key"]),
				Prefix:    toStr(m["prefix"]),
				Separator: "_",
			}
			if sep, ok := m["separator"].(string); ok {
				kg.Separator = sep
			}
			if kg.Key != "" {
				cfg.KeyedGroups = append(cfg.KeyedGroups, kg)
			}
		}
	}
	return true, cfg
}

// applyConstructed evaluates the plugin config against every host of inv
// (using its merged group+host vars) and adds the generated groups, the
// same way `ansible-inventory -i dir/ --graph` would show them.
func applyConstructed(inv *model.Inventory, cfg *constructedConfig) {
	groupVars := map[string]map[string]any{}
	for _, g := range inv.Groups {
		groupVars[g.Name] = g.Vars
	}

	membership := map[string][]string{} // generated group -> hosts
	for hi := range inv.Hosts {
		h := &inv.Hosts[hi]
		eff := map[string]any{}
		for _, gn := range h.Groups {
			for k, v := range groupVars[gn] {
				eff[k] = v
			}
		}
		for k, v := range h.Vars {
			eff[k] = v
		}

		var names []string
		for n := range cfg.Groups {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, gname := range names {
			if truthy(evalExpr(cfg.Groups[gname], eff)) {
				membership[gname] = append(membership[gname], h.Name)
			}
		}
		for _, kg := range cfg.KeyedGroups {
			v, ok := lookupVar(eff, kg.Key)
			if !ok || v == nil {
				continue
			}
			vals, isList := v.([]any)
			if !isList {
				vals = []any{v}
			}
			for _, val := range vals {
				gname := sanitizeGroupName(kg.Prefix, kg.Separator, toStr(val))
				if gname != "" {
					membership[gname] = append(membership[gname], h.Name)
				}
			}
		}
	}

	var gnames []string
	for n := range membership {
		gnames = append(gnames, n)
	}
	sort.Strings(gnames)
	for _, gname := range gnames {
		hosts := membership[gname]
		merged := false
		for gi := range inv.Groups {
			if inv.Groups[gi].Name == gname {
				for _, h := range hosts {
					if !contains(inv.Groups[gi].Hosts, h) {
						inv.Groups[gi].Hosts = append(inv.Groups[gi].Hosts, h)
					}
				}
				merged = true
				break
			}
		}
		if !merged {
			inv.Groups = append(inv.Groups, model.Group{
				Name: gname, Hosts: hosts, Constructed: true,
			})
		}
		for hi := range inv.Hosts {
			if contains(hosts, inv.Hosts[hi].Name) && !contains(inv.Hosts[hi].Groups, gname) {
				inv.Hosts[hi].Groups = append(inv.Hosts[hi].Groups, gname)
				sort.Strings(inv.Hosts[hi].Groups)
			}
		}
	}
	sort.Slice(inv.Groups, func(i, j int) bool { return inv.Groups[i].Name < inv.Groups[j].Name })
}

// sanitizeGroupName builds a keyed-group name the way ansible does:
// prefix + separator + value, invalid chars replaced by underscores.
func sanitizeGroupName(prefix, sep, value string) string {
	clean := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
				b.WriteRune(r)
			default:
				b.WriteByte('_')
			}
		}
		return b.String()
	}
	v := clean(value)
	if v == "" {
		return ""
	}
	if prefix == "" {
		return v
	}
	return clean(prefix) + sep + v
}

// ---------------------------------------------------------------------------
// Minimal Jinja condition evaluator: enough for the expressions the
// constructed plugin typically uses over host vars.
//
// Supported: 'lit' in (var | default([])), var == 'x', !=, and/or/not,
// parentheses, bare truthiness, var is (not) defined, dotted lookups,
// the default() filter. Unknown constructs evaluate to undefined/false
// (the plugin's strict:false behavior).
// ---------------------------------------------------------------------------

type undefinedT struct{}

var undefined = undefinedT{}

// evalExpr evaluates a Jinja-ish condition against vars; errors and
// unsupported syntax yield undefined (falsy).
func evalExpr(expr string, vars map[string]any) any {
	p := &exprParser{toks: tokenize(expr), vars: vars}
	v := p.parseOr()
	if p.pos < len(p.toks) { // trailing garbage: be safe, not strict
		return undefined
	}
	return v
}

func truthy(v any) bool {
	switch t := v.(type) {
	case undefinedT, nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case int:
		return t != 0
	case float64:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

func lookupVar(vars map[string]any, path string) (any, bool) {
	cur := any(vars)
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

type token struct {
	kind string // ident str num sym
	val  string
}

func tokenize(s string) []token {
	var toks []token
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			i++
		case c == '\'' || c == '"':
			q := c
			j := i + 1
			for j < len(s) && s[j] != q {
				j++
			}
			if j >= len(s) {
				return nil
			}
			toks = append(toks, token{"str", s[i+1 : j]})
			i = j + 1
		case c == '(' || c == ')' || c == '|' || c == ',' || c == '[' || c == ']':
			toks = append(toks, token{"sym", string(c)})
			i++
		case c == '=' && i+1 < len(s) && s[i+1] == '=':
			toks = append(toks, token{"sym", "=="})
			i += 2
		case c == '!' && i+1 < len(s) && s[i+1] == '=':
			toks = append(toks, token{"sym", "!="})
			i += 2
		case c >= '0' && c <= '9':
			j := i
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			toks = append(toks, token{"num", s[i:j]})
			i = j
		case isIdentChar(c):
			j := i
			for j < len(s) && (isIdentChar(s[j]) || s[j] == '.' || s[j] >= '0' && s[j] <= '9') {
				j++
			}
			toks = append(toks, token{"ident", s[i:j]})
			i = j
		default:
			return nil // unsupported character
		}
	}
	return toks
}

func isIdentChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

type exprParser struct {
	toks []token
	vars map[string]any
	pos  int
}

func (p *exprParser) peek() token {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return token{}
}

func (p *exprParser) accept(kind, val string) bool {
	t := p.peek()
	if t.kind == kind && t.val == val {
		p.pos++
		return true
	}
	return false
}

func (p *exprParser) parseOr() any {
	v := p.parseAnd()
	for p.accept("ident", "or") {
		r := p.parseAnd()
		v = truthy(v) || truthy(r)
	}
	return v
}

func (p *exprParser) parseAnd() any {
	v := p.parseNot()
	for p.accept("ident", "and") {
		r := p.parseNot()
		v = truthy(v) && truthy(r)
	}
	return v
}

func (p *exprParser) parseNot() any {
	if p.accept("ident", "not") {
		return !truthy(p.parseNot())
	}
	return p.parseCmp()
}

func (p *exprParser) parseCmp() any {
	left := p.parseValue()
	switch {
	case p.accept("sym", "=="):
		return equalVals(left, p.parseValue())
	case p.accept("sym", "!="):
		return !equalVals(left, p.parseValue())
	case p.accept("ident", "in"):
		return inVals(left, p.parseValue())
	case p.peek().val == "not" && p.pos+1 < len(p.toks) && p.toks[p.pos+1].val == "in":
		p.pos += 2
		return !truthy(inVals(left, p.parseValue()))
	case p.accept("ident", "is"):
		neg := p.accept("ident", "not")
		if p.accept("ident", "defined") {
			_, isUndef := left.(undefinedT)
			return neg == isUndef
		}
		if p.accept("ident", "undefined") {
			_, isUndef := left.(undefinedT)
			return neg != isUndef
		}
		return undefined
	}
	return left
}

// parseValue parses a literal, variable, list or parenthesized expression,
// then applies any trailing | filters.
func (p *exprParser) parseValue() any {
	var v any = undefined
	t := p.peek()
	switch {
	case t.kind == "str":
		p.pos++
		v = t.val
	case t.kind == "num":
		p.pos++
		if f, err := strconv.ParseFloat(t.val, 64); err == nil {
			v = f
		}
	case t.kind == "sym" && t.val == "(":
		p.pos++
		v = p.parseOr()
		if !p.accept("sym", ")") {
			return undefined
		}
	case t.kind == "sym" && t.val == "[":
		p.pos++
		list := []any{}
		for !p.accept("sym", "]") {
			if p.pos >= len(p.toks) {
				return undefined
			}
			list = append(list, p.parseValue())
			p.accept("sym", ",")
		}
		v = list
	case t.kind == "ident":
		p.pos++
		switch t.val {
		case "true", "True":
			v = true
		case "false", "False":
			v = false
		case "none", "None", "null":
			v = nil
		default:
			if got, ok := lookupVar(p.vars, t.val); ok {
				v = got
			} else {
				v = undefined
			}
		}
	default:
		return undefined
	}

	for p.accept("sym", "|") {
		name := p.peek()
		if name.kind != "ident" {
			return undefined
		}
		p.pos++
		var args []any
		if p.accept("sym", "(") {
			for !p.accept("sym", ")") {
				if p.pos >= len(p.toks) {
					return undefined
				}
				args = append(args, p.parseValue())
				p.accept("sym", ",")
			}
		}
		switch name.val {
		case "default", "d":
			if _, isUndef := v.(undefinedT); isUndef || v == nil {
				if len(args) > 0 {
					v = args[0]
				} else {
					v = ""
				}
			}
		case "lower":
			if s, ok := v.(string); ok {
				v = strings.ToLower(s)
			}
		case "upper":
			if s, ok := v.(string); ok {
				v = strings.ToUpper(s)
			}
		case "list", "unique", "trim":
			// shape-preserving enough for membership checks
		default:
			// unknown filter: pass the value through unchanged
		}
	}
	return v
}

func equalVals(a, b any) bool {
	if _, ok := a.(undefinedT); ok {
		return false
	}
	if _, ok := b.(undefinedT); ok {
		return false
	}
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func inVals(needle, hay any) any {
	switch h := hay.(type) {
	case []any:
		for _, e := range h {
			if equalVals(needle, e) {
				return true
			}
		}
		return false
	case string:
		if s, ok := needle.(string); ok {
			return strings.Contains(h, s)
		}
	case undefinedT:
		return undefined
	}
	return false
}
