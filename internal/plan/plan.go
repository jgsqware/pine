// Package plan implements Pine's estimated execution plans: a static,
// three-valued prediction of what a playbook would do, computed from the
// scanned repository and user-supplied variables, without running ansible.
package plan

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/scanner"
)

// Request describes one plan computation.
type Request struct {
	RepoID      string                    `json:"repo_id"`
	Playbook    string                    `json:"playbook"`
	Inventory   string                    `json:"inventory"`
	Limit       string                    `json:"limit"`
	Tags        string                    `json:"tags"`
	Check       bool                      `json:"check"`
	Vars        map[string]any            `json:"vars"`
	HostVars    map[string]map[string]any `json:"host_vars"`
	FactProfile string                    `json:"fact_profile"`
}

// Verdict statuses.
const (
	StatusRun     = "run"
	StatusSkip    = "skip"
	StatusUnknown = "unknown"
)

// HostVerdict is the predicted outcome of one task on one host.
type HostVerdict struct {
	Status  string   `json:"status"`
	Reason  string   `json:"reason,omitempty"`
	Missing []string `json:"missing,omitempty"`
}

// Counts aggregates verdicts for one task.
type Counts struct {
	Run     int `json:"run"`
	Skip    int `json:"skip"`
	Unknown int `json:"unknown"`
}

// TaskPlan is the prediction for one task across the play's hosts.
type TaskPlan struct {
	Name      string                 `json:"name"`
	RawName   string                 `json:"raw_name,omitempty"`
	Module    string                 `json:"module"`
	Role      string                 `json:"role,omitempty"`
	Section   string                 `json:"section"`
	When      string                 `json:"when,omitempty"`
	Tags      []string               `json:"tags,omitempty"`
	LoopItems int                    `json:"loop_items"`
	Notify    []string               `json:"notify,omitempty"`
	CheckNote string                 `json:"check_note,omitempty"`
	Counts    Counts                 `json:"counts"`
	Hosts     map[string]HostVerdict `json:"hosts"`
}

// HandlerPlan is a handler that may fire as a result of the plan.
type HandlerPlan struct {
	Name        string   `json:"name"`
	Module      string   `json:"module"`
	TriggeredBy []string `json:"triggered_by"`
	Hosts       []string `json:"hosts"`
	Uncertain   bool     `json:"uncertain"`
}

// PlayPlan is the prediction for one play.
type PlayPlan struct {
	Name         string        `json:"name"`
	Hosts        string        `json:"hosts"`
	Import       string        `json:"import,omitempty"`
	MatchedHosts []string      `json:"matched_hosts"`
	Batches      [][]string    `json:"batches"`
	Tasks        []TaskPlan    `json:"tasks"`
	Handlers     []HandlerPlan `json:"handlers,omitempty"`
}

// MissingVar aggregates how many verdicts a missing variable blocks.
type MissingVar struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Summary is the plan's headline numbers.
type Summary struct {
	Hosts       int          `json:"hosts"`
	Tasks       int          `json:"tasks"`
	Run         int          `json:"run"`
	Skip        int          `json:"skip"`
	Unknown     int          `json:"unknown"`
	MissingVars []MissingVar `json:"missing_vars"`
}

// Result is a computed plan.
type Result struct {
	Mode        string     `json:"mode"`
	RepoID      string     `json:"repo_id"`
	RepoName    string     `json:"repo_name"`
	Playbook    string     `json:"playbook"`
	Inventory   string     `json:"inventory"`
	FactProfile string     `json:"fact_profile,omitempty"`
	Check       bool       `json:"check"`
	Summary     Summary    `json:"summary"`
	Plays       []PlayPlan `json:"plays"`
}

// checkModeModules never run under --check.
func checkNote(check bool, module string) string {
	if !check {
		return ""
	}
	m := strings.TrimPrefix(module, "ansible.builtin.")
	switch m {
	case "command", "shell", "raw", "script":
		return "command/shell tasks do not run with --check"
	}
	return ""
}

// Compute builds an estimated plan from a scan result.
func Compute(res *model.ScanResult, root string, repo model.Repo, req Request) (*Result, error) {
	pb := findPlaybook(res, req.Playbook)
	if pb == nil {
		return nil, fmt.Errorf("playbook not found: %s", req.Playbook)
	}
	inv := pickInventory(res, req.Inventory)
	profile := ProfileByID(req.FactProfile)

	c := &computer{
		res:      res,
		root:     root,
		req:      req,
		inv:      inv,
		resolver: newVarResolver(inv, profile, req.Vars, req.HostVars),
		missing:  map[string]int{},
		roles:    map[string]*model.Role{},
	}
	for i := range res.Roles {
		c.roles[res.Roles[i].Name] = &res.Roles[i]
	}

	out := &Result{
		Mode:        "estimated",
		RepoID:      repo.ID,
		RepoName:    repo.Name,
		Playbook:    pb.Path,
		Inventory:   req.Inventory,
		FactProfile: req.FactProfile,
		Check:       req.Check,
	}
	if inv != nil && req.Inventory == "" {
		out.Inventory = inv.Path
	}

	c.playbook(pb, out, 0)

	hosts := map[string]bool{}
	for _, pp := range out.Plays {
		for _, h := range pp.MatchedHosts {
			hosts[h] = true
		}
		for _, tp := range pp.Tasks {
			out.Summary.Tasks++
			out.Summary.Run += tp.Counts.Run
			out.Summary.Skip += tp.Counts.Skip
			out.Summary.Unknown += tp.Counts.Unknown
		}
	}
	out.Summary.Hosts = len(hosts)
	for name, count := range c.missing {
		out.Summary.MissingVars = append(out.Summary.MissingVars, MissingVar{Name: name, Count: count})
	}
	sort.Slice(out.Summary.MissingVars, func(i, j int) bool {
		a, b := out.Summary.MissingVars[i], out.Summary.MissingVars[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Name < b.Name
	})
	if out.Summary.MissingVars == nil {
		out.Summary.MissingVars = []MissingVar{}
	}
	return out, nil
}

type computer struct {
	res      *model.ScanResult
	root     string
	req      Request
	inv      *model.Inventory
	resolver *varResolver
	missing  map[string]int
	roles    map[string]*model.Role
}

func (c *computer) playbook(pb *model.Playbook, out *Result, depth int) {
	if depth > 5 {
		return
	}
	for _, play := range pb.Plays {
		if play.Import != "" {
			out.Plays = append(out.Plays, PlayPlan{
				Name: play.Name, Hosts: play.Hosts, Import: play.Import,
				MatchedHosts: []string{}, Batches: [][]string{}, Tasks: []TaskPlan{},
			})
			if imported := findPlaybook(c.res, play.Import); imported != nil {
				c.playbook(imported, out, depth+1)
			}
			continue
		}
		out.Plays = append(out.Plays, c.play(pb, play))
	}
}

func (c *computer) play(pb *model.Playbook, play model.Play) PlayPlan {
	pp := PlayPlan{Name: play.Name, Hosts: play.Hosts, Tasks: []TaskPlan{}}

	// targeted hosts
	var hosts []string
	if c.inv != nil {
		hosts = scanner.MatchHosts(play.Hosts, c.inv)
		if c.req.Limit != "" {
			limited := scanner.MatchHosts(c.req.Limit, c.inv)
			lim := map[string]bool{}
			for _, h := range limited {
				lim[h] = true
			}
			var keep []string
			for _, h := range hosts {
				if lim[h] {
					keep = append(keep, h)
				}
			}
			hosts = keep
		}
	}
	if len(hosts) == 0 && (play.Hosts == "localhost" || play.Hosts == "127.0.0.1" || c.inv == nil) {
		hosts = []string{"localhost"}
	}
	pp.MatchedHosts = hosts
	if pp.MatchedHosts == nil {
		pp.MatchedHosts = []string{}
	}

	// serial batches
	pp.Batches = [][]string{}
	if n := atoiSafe(play.Serial); n > 0 && n < len(hosts) {
		for i := 0; i < len(hosts); i += n {
			pp.Batches = append(pp.Batches, hosts[i:min(i+n, len(hosts))])
		}
	} else if len(hosts) > 0 {
		pp.Batches = append(pp.Batches, hosts)
	}

	// per-host effective vars
	var roleDefaults []map[string]any
	for _, rn := range play.Roles {
		if r := c.roles[rn]; r != nil && r.Defaults != nil {
			roleDefaults = append(roleDefaults, r.Defaults)
		}
	}
	var fileVars []map[string]any
	for _, vf := range play.VarsFiles {
		for _, cand := range []string{
			filepath.Join(c.root, filepath.Dir(pb.Path), vf),
			filepath.Join(c.root, vf),
		} {
			if vars := scanner.LoadVarsFile(cand); vars != nil {
				fileVars = append(fileVars, vars)
				break
			}
		}
	}
	hostByName := map[string]*model.Host{}
	if c.inv != nil {
		for i := range c.inv.Hosts {
			hostByName[c.inv.Hosts[i].Name] = &c.inv.Hosts[i]
		}
	}
	eff := map[string]map[string]any{}
	for _, h := range hosts {
		eff[h] = c.resolver.effective(hostByName[h], &play, roleDefaults, fileVars)
	}

	// tasks in execution order
	add := func(section, role string, tasks []model.Task) {
		c.flatten(&pp, play, section, role, "", tasks, hosts, eff, false)
	}
	add("pre_tasks", "", play.PreTasks)
	for _, rn := range play.Roles {
		if r := c.roles[rn]; r != nil {
			add("roles", rn, r.Tasks)
		}
	}
	add("tasks", "", play.Tasks)
	add("post_tasks", "", play.PostTasks)

	// handlers
	handlers := append([]model.Task{}, play.Handlers...)
	for _, rn := range play.Roles {
		if r := c.roles[rn]; r != nil {
			handlers = append(handlers, r.Handlers...)
		}
	}
	pp.Handlers = c.handlers(handlers, pp.Tasks)
	return pp
}

// flatten walks tasks (recursing into blocks, inheriting their when) and
// appends a TaskPlan per leaf task.
func (c *computer) flatten(pp *PlayPlan, play model.Play, section, role, inheritedWhen string, tasks []model.Task, hosts []string, eff map[string]map[string]any, rescue bool) {
	for _, t := range tasks {
		when := combineWhen(inheritedWhen, t.When)
		if t.Module == "block" {
			c.flatten(pp, play, section, role, when, t.Block, hosts, eff, rescue)
			c.flatten(pp, play, section, role, when, t.Rescue, hosts, eff, true)
			c.flatten(pp, play, section, role, when, t.Always, hosts, eff, rescue)
			continue
		}
		pp.Tasks = append(pp.Tasks, c.task(play, section, role, when, t, hosts, eff, rescue))
	}
}

func combineWhen(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return "(" + a + ") and (" + b + ")"
}

func (c *computer) task(play model.Play, section, role, when string, t model.Task, hosts []string, eff map[string]map[string]any, rescue bool) TaskPlan {
	tp := TaskPlan{
		RawName: t.Name,
		Name:    t.Name,
		Module:  t.Module,
		Role:    role,
		Section: section,
		When:    when,
		Tags:    t.Tags,
		Notify:  t.Notify,
		Hosts:   map[string]HostVerdict{},
	}
	tp.CheckNote = checkNote(c.req.Check, t.Module)
	if rescue {
		tp.CheckNote = strings.TrimSpace(tp.CheckNote + " rescue: runs only when the block fails")
	}

	// representative vars for templating (first host)
	var repVars map[string]any
	if len(hosts) > 0 {
		repVars = eff[hosts[0]]
	} else {
		repVars = c.resolver.effective(nil, &play, nil, nil)
	}
	if name, known, _ := scanner.Interpolate(t.Name, repVars); known {
		tp.Name = name
	}

	// loop size
	switch {
	case !t.Loop:
		tp.LoopItems = 0
	case t.LoopItems > 0:
		tp.LoopItems = t.LoopItems
	case t.LoopExpr != "":
		tp.LoopItems = -1
		expr := strings.TrimSpace(t.LoopExpr)
		expr = strings.TrimPrefix(expr, "{{")
		expr = strings.TrimSuffix(expr, "}}")
		if v, _, ok := scanner.EvalValue(strings.TrimSpace(expr), repVars); ok {
			if list, isList := v.([]any); isList {
				tp.LoopItems = len(list)
			}
		}
	default:
		tp.LoopItems = -1
	}

	// tags filter
	tagsExcluded := c.req.Tags != "" && !hasAnyTag(append(append([]string{}, t.Tags...), play.Tags...), c.req.Tags)

	for _, h := range hosts {
		v := HostVerdict{Status: StatusRun}
		switch {
		case rescue:
			v = HostVerdict{Status: StatusSkip, Reason: "rescue path: runs only when the block fails"}
		case tagsExcluded:
			v = HostVerdict{Status: StatusSkip, Reason: fmt.Sprintf("--tags %s does not match", c.req.Tags)}
		case when != "":
			verdict, missing := scanner.EvalCondition(when, eff[h])
			switch verdict {
			case scanner.True:
				v = HostVerdict{Status: StatusRun}
			case scanner.False:
				v = HostVerdict{Status: StatusSkip, Reason: fmt.Sprintf("when: %s → false", when)}
			default:
				v = HostVerdict{Status: StatusUnknown, Missing: missing}
				for _, m := range missing {
					c.missing[m]++
				}
			}
		}
		tp.Hosts[h] = v
		switch v.Status {
		case StatusRun:
			tp.Counts.Run++
		case StatusSkip:
			tp.Counts.Skip++
		default:
			tp.Counts.Unknown++
		}
	}
	return tp
}

// handlers computes which handlers may fire, based on the verdicts of the
// tasks that notify them.
func (c *computer) handlers(handlers []model.Task, tasks []TaskPlan) []HandlerPlan {
	type agg struct {
		by      map[string]bool
		hosts   map[string]bool
		certain bool // at least one notifying task has a run verdict
	}
	notified := map[string]*agg{}
	for _, tp := range tasks {
		if len(tp.Notify) == 0 || (tp.Counts.Run == 0 && tp.Counts.Unknown == 0) {
			continue
		}
		for _, hn := range tp.Notify {
			a := notified[hn]
			if a == nil {
				a = &agg{by: map[string]bool{}, hosts: map[string]bool{}}
				notified[hn] = a
			}
			a.by[tp.Name] = true
			if tp.Counts.Run > 0 {
				a.certain = true
			}
			for h, v := range tp.Hosts {
				if v.Status != StatusSkip {
					a.hosts[h] = true
				}
			}
		}
	}
	var out []HandlerPlan
	for _, hd := range handlers {
		a := notified[hd.Name]
		if a == nil {
			continue
		}
		hp := HandlerPlan{Name: hd.Name, Module: hd.Module, Uncertain: !a.certain}
		for n := range a.by {
			hp.TriggeredBy = append(hp.TriggeredBy, n)
		}
		for h := range a.hosts {
			hp.Hosts = append(hp.Hosts, h)
		}
		sort.Strings(hp.TriggeredBy)
		sort.Strings(hp.Hosts)
		out = append(out, hp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// --- shared helpers (kept aligned with the simulator's behavior) ---

func findPlaybook(res *model.ScanResult, path string) *model.Playbook {
	for i := range res.Playbooks {
		if res.Playbooks[i].Path == path || res.Playbooks[i].Name == path {
			return &res.Playbooks[i]
		}
	}
	return nil
}

func pickInventory(res *model.ScanResult, requested string) *model.Inventory {
	for i := range res.Inventories {
		inv := &res.Inventories[i]
		if requested == "" || inv.Path == requested || inv.Name == requested ||
			strings.Contains(requested, inv.Name) {
			return inv
		}
	}
	if len(res.Inventories) > 0 {
		return &res.Inventories[0]
	}
	return nil
}

func hasAnyTag(tags []string, requested string) bool {
	for _, want := range strings.Split(requested, ",") {
		want = strings.TrimSpace(want)
		if want == "all" {
			return true
		}
		for _, t := range tags {
			if t == want || t == "always" {
				return true
			}
		}
	}
	return false
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
