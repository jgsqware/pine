package scanner

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Minimal Jinja expression evaluator with three-valued logic.
//
// Supported: 'lit' in (var | default([])), ==/!=/</<=/>/>=, and/or/not,
// parentheses, bare truthiness, var is (not) defined, dotted and ['key']
// lookups, list literals, default/lower/upper filters, {{ }} interpolation.
//
// Anything unresolved evaluates to *unknown* (the undefined sentinel) and
// the variables that failed to resolve are reported, so callers can either
// degrade safely (constructed plugin: unknown -> not a member) or surface
// the uncertainty (plan mode: amber verdict + missing vars).
// ---------------------------------------------------------------------------

type undefinedT struct{}

var undefined = undefinedT{}

func isUndef(v any) bool {
	_, ok := v.(undefinedT)
	return ok
}

// Tri is a three-valued verdict.
type Tri int

const (
	False Tri = iota
	True
	Unknown
)

func (t Tri) String() string {
	switch t {
	case True:
		return "true"
	case False:
		return "false"
	}
	return "unknown"
}

// EvalCondition evaluates a Jinja-ish condition against vars and returns a
// tri-state verdict plus the variables whose absence made it unknown.
func EvalCondition(expr string, vars map[string]any) (Tri, []string) {
	v, missing := evalWithMissing(expr, vars)
	if isUndef(v) {
		return Unknown, missing
	}
	if truthy(v) {
		return True, nil
	}
	return False, nil
}

// evalExpr keeps the historical boolean-ish entry point used by the
// constructed plugin: unknown collapses to falsy via truthy().
func evalExpr(expr string, vars map[string]any) any {
	v, _ := evalWithMissing(expr, vars)
	return v
}

func evalWithMissing(expr string, vars map[string]any) (any, []string) {
	toks := tokenize(expr)
	if toks == nil {
		return undefined, nil
	}
	p := &exprParser{toks: toks, vars: vars, missing: map[string]bool{}}
	v := p.parseOr()
	if p.pos < len(p.toks) { // trailing garbage: be safe, not strict
		return undefined, sortedKeys(p.missing)
	}
	if isUndef(v) {
		return undefined, sortedKeys(p.missing)
	}
	return v, nil
}

// Interpolate renders {{ expr }} segments inside s. known is false when at
// least one segment could not be resolved (its raw text is kept in place);
// missing lists the variables that prevented resolution.
func Interpolate(s string, vars map[string]any) (out string, known bool, missing []string) {
	if !strings.Contains(s, "{{") {
		return s, true, nil
	}
	known = true
	missSet := map[string]bool{}
	var b strings.Builder
	rest := s
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			b.WriteString(rest)
			break
		}
		end := strings.Index(rest[start:], "}}")
		if end < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:start])
		inner := rest[start+2 : start+end]
		v, miss := evalWithMissing(strings.TrimSpace(inner), vars)
		if isUndef(v) {
			known = false
			for _, m := range miss {
				missSet[m] = true
			}
			b.WriteString(rest[start : start+end+2]) // keep raw {{ ... }}
		} else {
			b.WriteString(toStr(v))
		}
		rest = rest[start+end+2:]
	}
	return b.String(), known, sortedKeys(missSet)
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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

// --- three-valued connectives ---

func triAnd(a, b any) any {
	if !isUndef(a) && !truthy(a) {
		return false
	}
	if !isUndef(b) && !truthy(b) {
		return false
	}
	if isUndef(a) || isUndef(b) {
		return undefined
	}
	return true
}

func triOr(a, b any) any {
	if !isUndef(a) && truthy(a) {
		return true
	}
	if !isUndef(b) && truthy(b) {
		return true
	}
	if isUndef(a) || isUndef(b) {
		return undefined
	}
	return false
}

func triNot(a any) any {
	if isUndef(a) {
		return undefined
	}
	return !truthy(a)
}

// --- tokenizer ---

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
		case c == '<' || c == '>':
			op := string(c)
			if i+1 < len(s) && s[i+1] == '=' {
				op += "="
				i++
			}
			toks = append(toks, token{"sym", op})
			i++
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

// --- parser ---

type exprParser struct {
	toks    []token
	vars    map[string]any
	pos     int
	missing map[string]bool
	pending string // most recent failed lookup, not yet committed
	curPath string // dotted origin of the most recent value, for ['k'] paths
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

// takePending returns and clears the pending missing-var name.
func (p *exprParser) takePending() string {
	v := p.pending
	p.pending = ""
	return v
}

func (p *exprParser) commit(name string) {
	if name != "" {
		p.missing[name] = true
	}
}

func (p *exprParser) parseOr() any {
	v := p.parseAnd()
	for p.accept("ident", "or") {
		v = triOr(v, p.parseAnd())
	}
	return v
}

func (p *exprParser) parseAnd() any {
	v := p.parseNot()
	for p.accept("ident", "and") {
		v = triAnd(v, p.parseNot())
	}
	return v
}

func (p *exprParser) parseNot() any {
	if p.accept("ident", "not") {
		return triNot(p.parseNot())
	}
	return p.parseCmp()
}

func (p *exprParser) parseCmp() any {
	left := p.parseValue()
	leftPending := p.takePending()

	binary := func(op func(a, b any) any) any {
		right := p.parseValue()
		rightPending := p.takePending()
		res := op(left, right)
		if isUndef(res) {
			p.commit(leftPending)
			p.commit(rightPending)
		}
		return res
	}

	switch {
	case p.accept("sym", "=="):
		return binary(cmpEq)
	case p.accept("sym", "!="):
		return binary(func(a, b any) any { return triNot(cmpEq(a, b)) })
	case p.accept("sym", "<"), p.accept("sym", "<="), p.accept("sym", ">"), p.accept("sym", ">="):
		op := p.toks[p.pos-1].val
		return binary(func(a, b any) any { return cmpRel(op, a, b) })
	case p.accept("ident", "in"):
		return binary(inVals)
	case p.peek().val == "not" && p.pos+1 < len(p.toks) && p.toks[p.pos+1].val == "in":
		p.pos += 2
		return binary(func(a, b any) any { return triNot(inVals(a, b)) })
	case p.accept("ident", "is"):
		neg := p.accept("ident", "not")
		// "is defined"/"is undefined" intentionally test absence: a failed
		// lookup here is the point, not a missing input to report.
		if p.accept("ident", "defined") {
			return neg == isUndef(left)
		}
		if p.accept("ident", "undefined") {
			return neg != isUndef(left)
		}
		p.commit(leftPending)
		return undefined
	}
	if isUndef(left) {
		p.commit(leftPending)
	}
	return left
}

// parseValue parses a literal, variable, list or parenthesized expression,
// then applies bracket lookups and trailing | filters.
func (p *exprParser) parseValue() any {
	var v any = undefined
	p.curPath = ""
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
			p.takePending()
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
			p.curPath = t.val
			if got, ok := lookupVar(p.vars, t.val); ok {
				v = got
			} else {
				v = undefined
				p.pending = t.val
			}
		}
	default:
		return undefined
	}

	// bracket lookups: var['key'] / var[0], chainable with dots already
	// folded into the ident token
	for p.peek().kind == "sym" && p.peek().val == "[" {
		basePending := p.takePending()
		basePath := p.curPath
		p.pos++
		key := p.parseValue()
		p.takePending() // key's own resolution issues are not reported
		if !p.accept("sym", "]") {
			return undefined
		}
		ks, keyIsStr := key.(string)
		if keyIsStr {
			p.curPath = joinPath(basePath, ks)
		} else {
			p.curPath = basePath
		}
		switch m := v.(type) {
		case map[string]any:
			if keyIsStr {
				if got, ok := m[ks]; ok {
					v = got
				} else {
					v = undefined
					p.pending = p.curPath
				}
			} else {
				v = undefined
			}
		case []any:
			if kf, ok := key.(float64); ok && int(kf) >= 0 && int(kf) < len(m) {
				v = m[int(kf)]
			} else {
				v = undefined
			}
		case undefinedT:
			// extend the pending path so ansible_facts['os_family'] reports
			// the full key when ansible_facts itself is absent
			if keyIsStr {
				p.pending = p.curPath
			} else {
				p.pending = basePending
			}
		default:
			v = undefined
		}
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
				p.takePending()
				p.accept("sym", ",")
			}
		}
		switch name.val {
		case "default", "d":
			if isUndef(v) || v == nil {
				if len(args) > 0 {
					v = args[0]
				} else {
					v = ""
				}
				p.pending = "" // defaulted: nothing is missing
			}
		case "lower":
			if s, ok := v.(string); ok {
				v = strings.ToLower(s)
			}
		case "upper":
			if s, ok := v.(string); ok {
				v = strings.ToUpper(s)
			}
		case "int":
			switch s := v.(type) {
			case string:
				if f, err := strconv.ParseFloat(s, 64); err == nil {
					v = f
				}
			}
		case "length", "count":
			switch s := v.(type) {
			case []any:
				v = float64(len(s))
			case string:
				v = float64(len(s))
			case map[string]any:
				v = float64(len(s))
			}
		case "bool", "list", "unique", "trim", "string":
			// shape-preserving enough for plan-level checks
		default:
			// unknown filter: pass the value through unchanged
		}
	}
	return v
}

// joinPath builds a dotted missing-var path from a base and a key.
func joinPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

func cmpEq(a, b any) any {
	if isUndef(a) || isUndef(b) {
		return undefined
	}
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func cmpRel(op string, a, b any) any {
	if isUndef(a) || isUndef(b) {
		return undefined
	}
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if aok && bok {
		switch op {
		case "<":
			return af < bf
		case "<=":
			return af <= bf
		case ">":
			return af > bf
		case ">=":
			return af >= bf
		}
	}
	as, bs := fmt.Sprintf("%v", a), fmt.Sprintf("%v", b)
	switch op {
	case "<":
		return as < bs
	case "<=":
		return as <= bs
	case ">":
		return as > bs
	case ">=":
		return as >= bs
	}
	return undefined
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return 0, false
}

func inVals(needle, hay any) any {
	if isUndef(needle) {
		return undefined
	}
	switch h := hay.(type) {
	case []any:
		for _, e := range h {
			if eq, _ := cmpEq(needle, e).(bool); eq {
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

// equalVals keeps the historical helper used by constructed.go.
func equalVals(a, b any) bool {
	eq, _ := cmpEq(a, b).(bool)
	return eq
}

// EvalValue evaluates an expression to a value. ok is false (with the
// missing variables listed) when it could not be resolved.
func EvalValue(expr string, vars map[string]any) (v any, missing []string, ok bool) {
	v, missing = evalWithMissing(expr, vars)
	return v, missing, !isUndef(v)
}
