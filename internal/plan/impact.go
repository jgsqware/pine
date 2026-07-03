package plan

import (
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/scanner"
)

// ImpactedPlaybook is a playbook reached from a changed file.
type ImpactedPlaybook struct {
	Path string `json:"path"`
	Via  string `json:"via"` // "direct", "role nginx", "group web", ...
}

// ImpactEntry maps one changed file to what it touches.
type ImpactEntry struct {
	File      string             `json:"file"`
	Kind      string             `json:"kind"`
	Roles     []string           `json:"roles"`
	Playbooks []ImpactedPlaybook `json:"playbooks"`
	Handlers  []string           `json:"handlers"`
}

// ImpactedHost is a host reached by at least one impacted playbook.
type ImpactedHost struct {
	Name      string   `json:"name"`
	Inventory string   `json:"inventory"`
	Via       []string `json:"via"`
}

// ImpactSummary aggregates the blast radius.
type ImpactSummary struct {
	Files            int            `json:"files"`
	Roles            int            `json:"roles"`
	Playbooks        int            `json:"playbooks"`
	Handlers         []string       `json:"handlers"`
	HostsTotal       int            `json:"hosts_total"`
	HostsByInventory map[string]int `json:"hosts_by_inventory"`
}

// ImpactResult is the blast radius of a git diff.
type ImpactResult struct {
	Base         string         `json:"base"`
	Head         string         `json:"head"`
	ChangedFiles []string       `json:"changed_files"`
	Entries      []ImpactEntry  `json:"entries"`
	Summary      ImpactSummary  `json:"summary"`
	Hosts        []ImpactedHost `json:"hosts"`
}

// changedFiles asks git for the files differing between base and head;
// empty refs mean "uncommitted changes vs HEAD" (worktree mode).
func changedFiles(root, base, head string) ([]string, string, string, error) {
	args := []string{"-C", root, "diff", "--name-only", "--relative"}
	labelBase, labelHead := base, head
	switch {
	case base == "" && head == "":
		args = append(args, "HEAD")
		labelBase, labelHead = "HEAD", "worktree"
	case head == "":
		args = append(args, base)
		labelHead = "worktree"
	default:
		args = append(args, base, head)
	}
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, "", "", fmt.Errorf("git diff failed: %s", msg)
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	sort.Strings(files)
	return files, labelBase, labelHead, nil
}

// Impact computes the blast radius of the diff base..head in the repo at
// root: changed files -> roles -> playbooks -> hosts (+ handlers).
func Impact(res *model.ScanResult, root, base, head string) (*ImpactResult, error) {
	files, labelBase, labelHead, err := changedFiles(root, base, head)
	if err != nil {
		return nil, err
	}
	out := &ImpactResult{
		Base: labelBase, Head: labelHead,
		ChangedFiles: files,
		Entries:      []ImpactEntry{},
		Hosts:        []ImpactedHost{},
		Summary: ImpactSummary{
			Handlers:         []string{},
			HostsByInventory: map[string]int{},
		},
	}
	if len(files) == 0 {
		return out, nil
	}

	ix := newImpactIndex(res)
	allRoles := map[string]bool{}
	allPlaybooks := map[string]bool{}
	allHandlers := map[string]bool{}
	hostVia := map[string]map[string]bool{} // inv/host -> playbooks

	for _, f := range files {
		e := ImpactEntry{File: f, Roles: []string{}, Playbooks: []ImpactedPlaybook{}, Handlers: []string{}}
		kind, role, group, host := classify(res, f)
		e.Kind = kind

		pbVia := map[string]string{}
		switch {
		case role != "":
			// the changed role plus every role that (transitively) depends on it
			users := ix.roleUsers(role)
			e.Roles = users
			for _, r := range users {
				allRoles[r] = true
				for _, pb := range ix.playbooksOfRole[r] {
					via := "role " + role
					if r != role {
						via = fmt.Sprintf("role %s (via %s)", role, r)
					}
					if _, seen := pbVia[pb]; !seen {
						pbVia[pb] = via
					}
				}
			}
			for _, h := range ix.roleHandlers[role] {
				e.Handlers = append(e.Handlers, h)
				allHandlers[h] = true
			}
		case kind == "playbook":
			pbVia[f] = "direct"
			for _, parent := range ix.importers[f] {
				pbVia[parent] = "imported by " + path.Base(f)
			}
		case group != "":
			for pb, via := range ix.playbooksOfGroup(group) {
				pbVia[pb] = via
			}
		case host != "":
			for pb, via := range ix.playbooksOfHost(host) {
				pbVia[pb] = via
			}
		case kind == "inventory":
			for _, pb := range res.Playbooks {
				pbVia[pb.Path] = "uses this inventory"
			}
		}

		var pbs []string
		for pb := range pbVia {
			pbs = append(pbs, pb)
		}
		sort.Strings(pbs)
		for _, pb := range pbs {
			e.Playbooks = append(e.Playbooks, ImpactedPlaybook{Path: pb, Via: pbVia[pb]})
			allPlaybooks[pb] = true
			for key := range ix.hostsOfPlaybook(pb) {
				if hostVia[key] == nil {
					hostVia[key] = map[string]bool{}
				}
				hostVia[key][pb] = true
			}
		}
		sort.Strings(e.Handlers)
		out.Entries = append(out.Entries, e)
	}

	out.Summary.Files = len(files)
	out.Summary.Roles = len(allRoles)
	out.Summary.Playbooks = len(allPlaybooks)
	for h := range allHandlers {
		out.Summary.Handlers = append(out.Summary.Handlers, h)
	}
	sort.Strings(out.Summary.Handlers)

	var hostKeys []string
	for k := range hostVia {
		hostKeys = append(hostKeys, k)
	}
	sort.Strings(hostKeys)
	for _, key := range hostKeys {
		inv, name, _ := strings.Cut(key, "/")
		var via []string
		for pb := range hostVia[key] {
			via = append(via, pb)
		}
		sort.Strings(via)
		out.Hosts = append(out.Hosts, ImpactedHost{Name: name, Inventory: inv, Via: via})
		out.Summary.HostsByInventory[inv]++
		out.Summary.HostsTotal++
	}
	return out, nil
}

// classify decides what a changed file is (role part, playbook, vars, ...).
func classify(res *model.ScanResult, f string) (kind, role, group, host string) {
	parts := strings.Split(f, "/")
	for i := 0; i < len(parts)-1; i++ {
		switch parts[i] {
		case "roles", "generic_roles":
			if i+1 < len(parts)-0 && i+2 <= len(parts) {
				role = parts[i+1]
				sub := "other"
				if i+2 < len(parts) {
					sub = parts[i+2]
				}
				switch sub {
				case "templates":
					kind = "role_template"
				case "tasks":
					kind = "role_task"
				case "defaults":
					kind = "role_defaults"
				case "vars":
					kind = "role_vars"
				case "handlers":
					kind = "role_handler"
				case "files":
					kind = "role_file"
				case "meta":
					kind = "role_meta"
				default:
					kind = "role_task"
				}
				return kind, role, "", ""
			}
		case "group_vars":
			if i+1 < len(parts) {
				g := strings.TrimSuffix(strings.TrimSuffix(parts[i+1], ".yml"), ".yaml")
				return "group_vars", "", g, ""
			}
		case "host_vars":
			if i+1 < len(parts) {
				h := strings.TrimSuffix(strings.TrimSuffix(parts[i+1], ".yml"), ".yaml")
				return "host_vars", "", "", h
			}
		}
	}
	for _, pb := range res.Playbooks {
		if pb.Path == f {
			return "playbook", "", "", ""
		}
	}
	for _, inv := range res.Inventories {
		if strings.HasPrefix(f, inv.Path+"/") || f == inv.Path {
			return "inventory", "", "", ""
		}
	}
	return "other", "", "", ""
}

// impactIndex precomputes reverse lookups over the scan result.
type impactIndex struct {
	res             *model.ScanResult
	playbooksOfRole map[string][]string // role -> playbook paths (direct or include_role)
	dependents      map[string][]string // role -> roles depending on it
	roleHandlers    map[string][]string
	importers       map[string][]string // playbook -> playbooks importing it
}

func newImpactIndex(res *model.ScanResult) *impactIndex {
	ix := &impactIndex{
		res:             res,
		playbooksOfRole: map[string][]string{},
		dependents:      map[string][]string{},
		roleHandlers:    map[string][]string{},
		importers:       map[string][]string{},
	}
	addPB := func(role, pb string) {
		for _, e := range ix.playbooksOfRole[role] {
			if e == pb {
				return
			}
		}
		ix.playbooksOfRole[role] = append(ix.playbooksOfRole[role], pb)
	}
	var taskRoles func(t model.Task) []string
	taskRoles = func(t model.Task) []string {
		var out []string
		if t.RoleRef != "" {
			out = append(out, t.RoleRef)
		}
		for _, sub := range [][]model.Task{t.Block, t.Rescue, t.Always} {
			for _, st := range sub {
				out = append(out, taskRoles(st)...)
			}
		}
		return out
	}
	for _, pb := range res.Playbooks {
		for _, play := range pb.Plays {
			if play.Import != "" {
				ix.importers[play.Import] = append(ix.importers[play.Import], pb.Path)
				continue
			}
			for _, rn := range play.Roles {
				addPB(rn, pb.Path)
			}
			for _, list := range [][]model.Task{play.PreTasks, play.Tasks, play.PostTasks} {
				for _, t := range list {
					for _, rn := range taskRoles(t) {
						addPB(rn, pb.Path)
					}
				}
			}
		}
	}
	for _, r := range res.Roles {
		for _, dep := range r.Dependencies {
			ix.dependents[dep] = append(ix.dependents[dep], r.Name)
		}
		var names []string
		for _, h := range r.Handlers {
			names = append(names, h.Name)
		}
		ix.roleHandlers[r.Name] = names
	}
	return ix
}

// roleUsers returns role plus every role transitively depending on it.
func (ix *impactIndex) roleUsers(role string) []string {
	seen := map[string]bool{}
	var walk func(r string)
	walk = func(r string) {
		if seen[r] {
			return
		}
		seen[r] = true
		for _, d := range ix.dependents[r] {
			walk(d)
		}
	}
	walk(role)
	var out []string
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// hostsOfPlaybook returns "inventory/host" keys targeted by a playbook.
func (ix *impactIndex) hostsOfPlaybook(pbPath string) map[string]bool {
	out := map[string]bool{}
	var pb *model.Playbook
	for i := range ix.res.Playbooks {
		if ix.res.Playbooks[i].Path == pbPath {
			pb = &ix.res.Playbooks[i]
		}
	}
	if pb == nil {
		return out
	}
	for _, play := range pb.Plays {
		if play.Import != "" {
			continue
		}
		for i := range ix.res.Inventories {
			inv := &ix.res.Inventories[i]
			for _, h := range scanner.MatchHosts(play.Hosts, inv) {
				out[inv.Name+"/"+h] = true
			}
		}
	}
	return out
}

// playbooksOfGroup: playbooks whose plays target hosts of this group.
func (ix *impactIndex) playbooksOfGroup(group string) map[string]string {
	out := map[string]string{}
	for i := range ix.res.Inventories {
		inv := &ix.res.Inventories[i]
		members := map[string]bool{}
		for _, h := range inv.Hosts {
			for _, g := range h.Groups {
				if g == group {
					members[h.Name] = true
				}
			}
		}
		if len(members) == 0 && group != "all" {
			continue
		}
		for _, pb := range ix.res.Playbooks {
			for _, play := range pb.Plays {
				if play.Import != "" {
					continue
				}
				for _, h := range scanner.MatchHosts(play.Hosts, inv) {
					if group == "all" || members[h] {
						if _, seen := out[pb.Path]; !seen {
							out[pb.Path] = "group " + group
						}
					}
				}
			}
		}
	}
	return out
}

// playbooksOfHost: playbooks targeting this specific host.
func (ix *impactIndex) playbooksOfHost(host string) map[string]string {
	out := map[string]string{}
	for i := range ix.res.Inventories {
		inv := &ix.res.Inventories[i]
		for _, pb := range ix.res.Playbooks {
			for _, play := range pb.Plays {
				if play.Import != "" {
					continue
				}
				for _, h := range scanner.MatchHosts(play.Hosts, inv) {
					if h == host {
						if _, seen := out[pb.Path]; !seen {
							out[pb.Path] = "host " + host
						}
					}
				}
			}
		}
	}
	return out
}
