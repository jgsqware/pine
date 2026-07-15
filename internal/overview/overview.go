// Package overview composes a new user's orientation view of an Ansible repo
// from data Pine already scanned: playbooks grouped into tiers with their
// resolved target hosts, a role catalog with usage cross-references, entry
// points, and an honest "what you can / can't do" caution list derived from the
// hygiene report. It generates no prose and makes no guesses — every field is a
// projection of the scan result or the hygiene findings, so the Guide page it
// backs never presents an inference as a fact.
package overview

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jgsqware/pine/internal/claudecode"
	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
	"github.com/jgsqware/pine/internal/scanner"
)

// Overview is the composed orientation view of one repository.
type Overview struct {
	Summary     model.RepoSummary      `json:"summary"`
	Tiers       []Tier                 `json:"tiers"`
	Roles       []RoleInfo             `json:"roles"`
	Inventories []InventoryInfo        `json:"inventories"`
	EntryPoints []EntryPoint           `json:"entry_points"`
	Cautions    []Caution              `json:"cautions"`
	ClaudeCode  claudecode.Capability  `json:"claude_code"`
}

// Tier groups playbooks by their top directory segment (playbooks/main →
// "main"). Repos without that layout collapse to a single "playbooks" tier.
type Tier struct {
	Name      string         `json:"name"`
	Playbooks []TierPlaybook `json:"playbooks"`
}

// TierPlaybook is one playbook as shown in the structure map.
type TierPlaybook struct {
	Path        string   `json:"path"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Hosts       string   `json:"hosts"`                   // the play hosts: pattern(s), joined
	TargetHosts []string `json:"target_hosts,omitempty"`  // concrete hosts matched in an inventory
	Roles       []string `json:"roles,omitempty"`         // roles applied (roles: + include_role/import_role)
	Tags        []string `json:"tags,omitempty"`
	NeedsInput  bool     `json:"needs_input,omitempty"`   // has a vars_prompt
	HasSerial   bool     `json:"has_serial,omitempty"`    // rolling (serial:) play
	Become      bool     `json:"become,omitempty"`        // escalates privilege
}

// RoleInfo is one role plus how it is used across the repo.
type RoleInfo struct {
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Dependencies  []string `json:"dependencies,omitempty"`
	TasksCount    int      `json:"tasks_count"`
	HandlersCount int      `json:"handlers_count,omitempty"`
	UsedBy        []string `json:"used_by,omitempty"` // playbook paths that apply this role
	Unused        bool     `json:"unused,omitempty"`  // hygiene flagged it as never referenced
}

// InventoryInfo is a compact inventory summary for the Guide.
type InventoryInfo struct {
	Name             string `json:"name"`
	Path             string `json:"path"`
	Format           string `json:"format"`
	Hosts            int    `json:"hosts"`
	Groups           int    `json:"groups"`
	ConstructedGroup int    `json:"constructed_groups,omitempty"`
}

// EntryPoint is a repo-root file a new user would start from.
type EntryPoint struct {
	Kind string `json:"kind"` // run-script | make | site | requirements | docs | config
	Path string `json:"path"`
	Hint string `json:"hint"`
}

// Caution is one "what you can / can't do" note, derived from hygiene + scan.
type Caution struct {
	Kind     string `json:"kind"`     // needs-input | vault | plaintext-secret | unused-role | untargeted-host | rolling
	Severity string `json:"severity"` // high | medium | info
	Subject  string `json:"subject"`  // the playbook/role/host it concerns
	Detail   string `json:"detail"`
}

// Compose builds the Overview from an already-scanned repo. root is the repo
// working copy (used for hygiene cross-referencing and entry-point detection).
func Compose(res *model.ScanResult, root string) Overview {
	hy := plan.Hygiene(res, root)
	return ComposeWith(res, root, hy, claudecode.Detect())
}

// ComposeWith is Compose with the hygiene report and Claude Code capability
// injected — used by tests and by callers that already have them.
func ComposeWith(res *model.ScanResult, root string, hy *plan.HygieneResult, cc claudecode.Capability) Overview {
	ov := Overview{
		Summary:     scanner.Summarize(res),
		Tiers:       buildTiers(res),
		Roles:       buildRoles(res, hy),
		Inventories: buildInventories(res),
		EntryPoints: detectEntryPoints(root),
		Cautions:    buildCautions(res, hy),
		ClaudeCode:  cc,
	}
	return ov
}

// tierOf returns the grouping label for a playbook path: the directory segment
// under a top-level "playbooks" dir when present (playbooks/main/x.yml →
// "main"), otherwise the immediate parent dir, otherwise "playbooks".
func tierOf(path string) string {
	path = filepath.ToSlash(path)
	parts := strings.Split(path, "/")
	if len(parts) >= 3 && parts[0] == "playbooks" {
		return parts[1]
	}
	if len(parts) >= 2 {
		if parts[0] == "playbooks" {
			return "playbooks"
		}
		return parts[len(parts)-2]
	}
	return "playbooks"
}

func buildTiers(res *model.ScanResult) []Tier {
	byTier := map[string][]TierPlaybook{}
	var order []string
	for _, pb := range res.Playbooks {
		t := tierOf(pb.Path)
		if _, seen := byTier[t]; !seen {
			order = append(order, t)
		}
		byTier[t] = append(byTier[t], playbookInfo(pb, res))
	}
	sort.Strings(order)
	tiers := make([]Tier, 0, len(order))
	for _, name := range order {
		pbs := byTier[name]
		sort.Slice(pbs, func(i, j int) bool { return pbs[i].Path < pbs[j].Path })
		tiers = append(tiers, Tier{Name: name, Playbooks: pbs})
	}
	return tiers
}

func playbookInfo(pb model.Playbook, res *model.ScanResult) TierPlaybook {
	tp := TierPlaybook{Path: pb.Path, Name: pb.Name, Description: pb.Description}
	hostPatterns := map[string]bool{}
	roles := map[string]bool{}
	tags := map[string]bool{}
	targets := map[string]bool{}
	for _, play := range pb.Plays {
		if play.Hosts != "" {
			hostPatterns[play.Hosts] = true
		}
		if play.Become {
			tp.Become = true
		}
		if play.Serial != "" {
			tp.HasSerial = true
		}
		if len(play.VarsPrompt) > 0 {
			tp.NeedsInput = true
		}
		for _, r := range play.Roles {
			roles[r] = true
		}
		for _, t := range play.Tags {
			tags[t] = true
		}
		collectRoleRefs(play, roles)
		// resolve concrete target hosts against any inventory that matches
		for i := range res.Inventories {
			for _, h := range scanner.MatchHosts(play.Hosts, &res.Inventories[i]) {
				targets[h] = true
			}
		}
	}
	tp.Hosts = strings.Join(sortedKeys(hostPatterns), ", ")
	tp.Roles = sortedKeys(roles)
	tp.Tags = sortedKeys(tags)
	tp.TargetHosts = sortedKeys(targets)
	return tp
}

// collectRoleRefs adds role names pulled in via include_role/import_role tasks
// (Task.RoleRef), recursing through blocks and statically-imported tasks.
func collectRoleRefs(play model.Play, into map[string]bool) {
	var walk func(t model.Task)
	walk = func(t model.Task) {
		if t.RoleRef != "" {
			into[t.RoleRef] = true
		}
		for _, sub := range [][]model.Task{t.Block, t.Rescue, t.Always, t.Imported} {
			for _, st := range sub {
				walk(st)
			}
		}
	}
	for _, group := range [][]model.Task{play.PreTasks, play.Tasks, play.PostTasks, play.Handlers} {
		for _, t := range group {
			walk(t)
		}
	}
}

func buildRoles(res *model.ScanResult, hy *plan.HygieneResult) []RoleInfo {
	// used-by: which playbooks reference each role.
	usedBy := map[string][]string{}
	for _, pb := range res.Playbooks {
		refs := map[string]bool{}
		for _, play := range pb.Plays {
			for _, r := range play.Roles {
				refs[r] = true
			}
			collectRoleRefs(play, refs)
		}
		for r := range refs {
			usedBy[r] = append(usedBy[r], pb.Path)
		}
	}
	unused := map[string]bool{}
	if hy != nil {
		for _, u := range hy.UnusedRoles {
			unused[u.Name] = true
		}
	}
	out := make([]RoleInfo, 0, len(res.Roles))
	for _, r := range res.Roles {
		ub := usedBy[r.Name]
		sort.Strings(ub)
		out = append(out, RoleInfo{
			Name:          r.Name,
			Description:   r.Description,
			Dependencies:  r.Dependencies,
			TasksCount:    r.TasksCount,
			HandlersCount: len(r.Handlers),
			UsedBy:        ub,
			Unused:        unused[r.Name],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func buildInventories(res *model.ScanResult) []InventoryInfo {
	out := make([]InventoryInfo, 0, len(res.Inventories))
	for _, inv := range res.Inventories {
		constructed := 0
		for _, g := range inv.Groups {
			if g.Constructed {
				constructed++
			}
		}
		out = append(out, InventoryInfo{
			Name:             inv.Name,
			Path:             inv.Path,
			Format:           inv.Format,
			Hosts:            len(inv.Hosts),
			Groups:           len(inv.Groups),
			ConstructedGroup: constructed,
		})
	}
	return out
}

// entryPointCandidates maps a repo-root filename to its kind + hint. Order is
// the display order.
var entryPointCandidates = []struct {
	file, kind, hint string
	dir              bool
}{
	{"run.sh", "run-script", "interactive playbook runner — start here", false},
	{"Makefile", "make", "make targets wrap common commands", false},
	{"site.yml", "site", "umbrella playbook (site-wide converge)", false},
	{"site.yaml", "site", "umbrella playbook (site-wide converge)", false},
	{"requirements.yml", "requirements", "Galaxy roles/collections to install first", false},
	{"ansible.cfg", "config", "Ansible config (inventory, roles_path, plugins)", false},
	{"mkdocs.yml", "docs", "documentation site (mkdocs serve)", false},
	{"docs", "docs", "repo documentation", true},
	{"README.md", "docs", "primary README", false},
}

func detectEntryPoints(root string) []EntryPoint {
	if root == "" {
		return nil
	}
	var out []EntryPoint
	seenDocs := false
	for _, c := range entryPointCandidates {
		fi, err := os.Stat(filepath.Join(root, c.file))
		if err != nil || fi.IsDir() != c.dir {
			continue
		}
		if c.kind == "docs" {
			if seenDocs {
				continue // one docs pointer is enough
			}
			seenDocs = true
		}
		out = append(out, EntryPoint{Kind: c.kind, Path: c.file, Hint: c.hint})
	}
	return out
}

func buildCautions(res *model.ScanResult, hy *plan.HygieneResult) []Caution {
	var out []Caution
	// playbooks that need input (vars_prompt) before they can run unattended.
	for _, pb := range res.Playbooks {
		var prompts []string
		serial := false
		for _, play := range pb.Plays {
			for _, vp := range play.VarsPrompt {
				prompts = append(prompts, vp.Name)
			}
			if play.Serial != "" {
				serial = true
			}
		}
		if len(prompts) > 0 {
			out = append(out, Caution{
				Kind: "needs-input", Severity: "info", Subject: pb.Path,
				Detail: "prompts for " + strings.Join(prompts, ", ") + " — supply via -e or the run modal",
			})
		}
		if serial {
			out = append(out, Caution{
				Kind: "rolling", Severity: "info", Subject: pb.Path,
				Detail: "runs in serial batches (rolling update) — hosts are updated in waves",
			})
		}
	}
	if hy == nil {
		return out
	}
	if hy.VaultFiles > 0 {
		out = append(out, Caution{
			Kind: "vault", Severity: "medium", Subject: "vault",
			Detail: pluralN(hy.VaultFiles, "vault-encrypted file") + " — runs need the ansible-vault password",
		})
	}
	for _, s := range hy.SecretFindings {
		if s.Severity == "high" {
			out = append(out, Caution{
				Kind: "plaintext-secret", Severity: "high", Subject: s.File,
				Detail: "plaintext secret `" + s.Key + "` — should be vaulted before sharing",
			})
		}
	}
	for _, u := range hy.UnusedRoles {
		out = append(out, Caution{
			Kind: "unused-role", Severity: "info", Subject: u.Name,
			Detail: "role is never applied by any playbook",
		})
	}
	for _, h := range hy.UntargetedHosts {
		out = append(out, Caution{
			Kind: "untargeted-host", Severity: "info", Subject: h.Name,
			Detail: "host in " + h.Inventory + " is targeted by no playbook",
		})
	}
	return out
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

func pluralN(n int, noun string) string {
	s := strconv.Itoa(n) + " " + noun
	if n != 1 {
		s += "s"
	}
	return s
}
