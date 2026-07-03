package plan

import (
	"fmt"
	"sort"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/scanner"
)

// PreviewRequest asks for a what-if recomputation of an inventory's
// constructed groups with user-supplied variables.
type PreviewRequest struct {
	Inventory   string                    `json:"inventory"`
	Vars        map[string]any            `json:"vars"`
	HostVars    map[string]map[string]any `json:"host_vars"`
	FactProfile string                    `json:"fact_profile"`
}

// PreviewResult carries the recomputed inventory, its topology graph and
// the memberships that stay unknown (group -> host -> missing vars).
type PreviewResult struct {
	Inventory     model.Inventory                `json:"inventory"`
	Topology      *scanner.Topology              `json:"topology"`
	UnknownGroups map[string]map[string][]string `json:"unknown_groups"`
}

// PreviewInventory strips the statically-computed constructed groups,
// overlays the supplied vars and re-evaluates the constructed rules with
// three-valued logic.
func PreviewInventory(res *model.ScanResult, req PreviewRequest) (*PreviewResult, error) {
	src := pickInventory(res, req.Inventory)
	if src == nil {
		return nil, fmt.Errorf("inventory not found: %s", req.Inventory)
	}
	inv := stripConstructed(*src)
	resolver := newVarResolver(&inv, ProfileByID(req.FactProfile), req.Vars, req.HostVars)

	unknown := map[string]map[string][]string{}
	membership := map[string][]string{}

	for hi := range inv.Hosts {
		h := &inv.Hosts[hi]
		eff := resolver.effective(h, nil, nil, nil, nil)
		for _, rule := range inv.ConstructedRules {
			var names []string
			for n := range rule.Groups {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, gname := range names {
				verdict, missing := scanner.EvalCondition(rule.Groups[gname], eff)
				switch verdict {
				case scanner.True:
					membership[gname] = append(membership[gname], h.Name)
				case scanner.Unknown:
					if unknown[gname] == nil {
						unknown[gname] = map[string][]string{}
					}
					unknown[gname][h.Name] = missing
				}
			}
			for _, kg := range rule.KeyedGroups {
				v, ok := eff[kg.Key]
				if !ok || v == nil {
					continue
				}
				vals, isList := v.([]any)
				if !isList {
					vals = []any{v}
				}
				sep := kg.Separator
				if sep == "" {
					sep = "_"
				}
				for _, val := range vals {
					if gname := scanner.KeyedGroupName(kg.Prefix, sep, fmt.Sprintf("%v", val)); gname != "" {
						membership[gname] = append(membership[gname], h.Name)
					}
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
		inv.Groups = append(inv.Groups, model.Group{Name: gname, Hosts: hosts, Constructed: true})
		for hi := range inv.Hosts {
			h := &inv.Hosts[hi]
			for _, hn := range hosts {
				if h.Name == hn {
					h.Groups = append(h.Groups, gname)
					sort.Strings(h.Groups)
				}
			}
		}
	}
	sort.Slice(inv.Groups, func(i, j int) bool { return inv.Groups[i].Name < inv.Groups[j].Name })

	return &PreviewResult{
		Inventory:     inv,
		Topology:      scanner.BuildTopology(&inv),
		UnknownGroups: unknown,
	}, nil
}

// stripConstructed deep-copies an inventory without its generated groups.
func stripConstructed(src model.Inventory) model.Inventory {
	out := src
	constructed := map[string]bool{}
	out.Groups = nil
	for _, g := range src.Groups {
		if g.Constructed {
			constructed[g.Name] = true
			continue
		}
		cp := g
		cp.Hosts = append([]string{}, g.Hosts...)
		cp.Children = append([]string{}, g.Children...)
		out.Groups = append(out.Groups, cp)
	}
	out.Hosts = nil
	for _, h := range src.Hosts {
		cp := h
		cp.Groups = nil
		for _, g := range h.Groups {
			if !constructed[g] {
				cp.Groups = append(cp.Groups, g)
			}
		}
		out.Hosts = append(out.Hosts, cp)
	}
	return out
}
