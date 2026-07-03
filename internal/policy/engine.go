package policy

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jgsqware/pine/internal/plan"
)

// Violation is one failed assertion. Hosts lists the in-scope hosts it affects
// (empty for plan-level rules like max_blast_radius_pct).
type Violation struct {
	Policy      string   `json:"policy"`
	Severity    Severity `json:"severity"`
	Description string   `json:"description,omitempty"`
	Play        string   `json:"play,omitempty"`
	Task        string   `json:"task,omitempty"`
	Module      string   `json:"module,omitempty"`
	Hosts       []string `json:"hosts,omitempty"`
	Detail      string   `json:"detail"`
}

// Options carries the context the plan alone does not: host→group membership
// (for `groups:` matching), an optional git-diff blast radius and the total
// inventory host count (the denominator for max_blast_radius_pct).
type Options struct {
	HostGroups map[string][]string // host name -> inventory groups
	Impact     *plan.ImpactResult  // optional; blast-radius numerator when set
	TotalHosts int                 // inventory size; blast-radius denominator
}

// Evaluate runs every policy against the plan and returns the violations, sorted
// error-first then by policy id. It never gates on its own — the caller decides
// (any error-severity violation should fail CI); see HasError.
func Evaluate(policies []Policy, res *plan.Result, opts Options) []Violation {
	var out []Violation
	for _, p := range policies {
		out = append(out, evalPolicy(p, res, opts)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == SeverityError // errors first
		}
		return out[i].Policy < out[j].Policy
	})
	return out
}

// HasError reports whether any violation is error-severity (the CI gate).
func HasError(vs []Violation) bool {
	for _, v := range vs {
		if v.Severity == SeverityError {
			return true
		}
	}
	return false
}

func evalPolicy(p Policy, res *plan.Result, opts Options) []Violation {
	sev := p.Severity
	if sev == "" {
		sev = SeverityError
	}
	// An inventory glob gates the whole policy.
	if p.Match.Inventory != "" && !globMatch(p.Match.Inventory, res.Inventory) {
		return nil
	}
	if p.Assert.MaxBlastRadiusPct > 0 {
		return evalBlastRadius(p, sev, res, opts)
	}

	re := p.nameRE
	if re == nil && p.Match.TaskNameRegex != "" {
		re, _ = regexp.Compile(p.Match.TaskNameRegex) // Parse validated; tolerate hand-built policies
	}

	var out []Violation
	for _, pp := range res.Plays {
		if pp.Import != "" {
			continue
		}
		if p.Match.Become != nil && *p.Match.Become != pp.Become {
			continue
		}
		for _, tp := range pp.Tasks {
			if !moduleMatches(p.Match.Module, tp.Module) {
				continue
			}
			if re != nil && !re.MatchString(tp.Name) {
				continue
			}
			if len(p.Match.Tags) > 0 && !hasAnyTag(tp.Tags, p.Match.Tags) {
				continue
			}
			hosts := matchedHosts(p.Match, tp, opts.HostGroups)
			if len(hosts) == 0 {
				continue
			}
			for _, d := range assertTask(p.Assert, tp) {
				out = append(out, Violation{
					Policy: p.ID, Severity: sev, Description: p.Description,
					Play: pp.Name, Task: taskLabel(pp, tp), Module: tp.Module,
					Hosts: hosts, Detail: d,
				})
			}
		}
	}
	return out
}

// matchedHosts returns the in-scope hosts for a task: those it would run (or
// might run — skipped verdicts are governance-irrelevant), narrowed by the
// host/group match fields.
func matchedHosts(m Match, tp plan.TaskPlan, hostGroups map[string][]string) []string {
	var hosts []string
	for h, v := range tp.Hosts {
		if v.Status == plan.StatusSkip {
			continue
		}
		if m.Hosts != "" && !globMatch(m.Hosts, h) {
			continue
		}
		if len(m.Groups) > 0 && !hostInAnyGroup(hostGroups[h], m.Groups) {
			continue
		}
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts
}

// assertTask checks the task-level assertions and returns a detail string per
// failure.
func assertTask(a Assert, tp plan.TaskPlan) []string {
	var details []string
	if a.Forbid {
		details = append(details, "task is forbidden by policy")
	}
	if a.RequireTag != "" && !hasTag(tp.Tags, a.RequireTag) {
		details = append(details, fmt.Sprintf("missing required tag %q", a.RequireTag))
	}
	if len(a.ForbidArgs) > 0 {
		args := parseArgs(firstNonEmpty(tp.Args, tp.RawArgs))
		for k, want := range a.ForbidArgs {
			got, ok := args[k]
			if !ok {
				continue
			}
			if want == "*" || strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want)) {
				details = append(details, fmt.Sprintf("forbidden arg %s: %s", k, got))
			}
		}
	}
	sort.Strings(details)
	return details
}

// evalBlastRadius checks max_blast_radius_pct once for the plan. The numerator
// is the git-diff impact host count when provided, else the number of hosts the
// plan would touch; the denominator is the inventory size.
func evalBlastRadius(p Policy, sev Severity, res *plan.Result, opts Options) []Violation {
	total := opts.TotalHosts
	if total <= 0 {
		total = res.Summary.Hosts
	}
	if total <= 0 {
		return nil
	}
	var touched int
	source := "plan"
	if opts.Impact != nil {
		touched = opts.Impact.Summary.HostsTotal
		source = "changed files"
	} else {
		touched = planTouchedHosts(res)
	}
	pct := touched * 100 / total
	if pct <= p.Assert.MaxBlastRadiusPct {
		return nil
	}
	return []Violation{{
		Policy: p.ID, Severity: sev, Description: p.Description,
		Detail: fmt.Sprintf("blast radius %d%% (%d/%d hosts via %s) exceeds max %d%%",
			pct, touched, total, source, p.Assert.MaxBlastRadiusPct),
	}}
}

// planTouchedHosts counts distinct hosts with at least one non-skip verdict.
func planTouchedHosts(res *plan.Result) int {
	seen := map[string]bool{}
	for _, pp := range res.Plays {
		for _, tp := range pp.Tasks {
			for h, v := range tp.Hosts {
				if v.Status != plan.StatusSkip {
					seen[h] = true
				}
			}
		}
	}
	return len(seen)
}

// --- helpers ---

func taskLabel(pp plan.PlayPlan, tp plan.TaskPlan) string {
	if tp.Role != "" {
		return tp.Role + " : " + tp.Name
	}
	return tp.Name
}

// moduleMatches reports whether task's module matches any wanted module,
// comparing on the short (final) segment so "apt" matches "ansible.builtin.apt".
func moduleMatches(want []string, module string) bool {
	if len(want) == 0 {
		return true
	}
	m := shortModule(module)
	for _, w := range want {
		if shortModule(w) == m {
			return true
		}
	}
	return false
}

func shortModule(m string) string {
	if i := strings.LastIndex(m, "."); i >= 0 {
		return m[i+1:]
	}
	return m
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func hasAnyTag(tags, want []string) bool {
	for _, w := range want {
		if hasTag(tags, w) {
			return true
		}
	}
	return false
}

func hostInAnyGroup(groups, want []string) bool {
	for _, g := range groups {
		for _, w := range want {
			if g == w {
				return true
			}
		}
	}
	return false
}

// parseArgs turns the plan's rendered module-args summary ("state: latest,
// name: nginx") back into a key→value map. Best-effort: it only understands the
// `key: value, ...` form the scanner emits for mapping args.
func parseArgs(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	for _, part := range strings.Split(s, ", ") {
		k, v, ok := strings.Cut(part, ": ")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// globMatch matches a shell-style glob (* and ?) anywhere in s, treating "/" as
// an ordinary character (unlike path.Match) so "*production*" matches an
// inventory path like "inventories/production/hosts.ini".
func globMatch(pattern, s string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(s)
}
