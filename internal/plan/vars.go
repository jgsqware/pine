package plan

import (
	"sort"
	"strings"

	"github.com/jgsqware/pine/internal/model"
)

// varResolver precomputes per-inventory structures so per-host effective
// vars can be assembled quickly, following a simplified version of
// ansible's precedence:
//
//	role defaults < group vars (all, parents before children) < host vars
//	< magic vars < fact profile < play vars/vars_files < user vars
type varResolver struct {
	inv       *model.Inventory
	groupVars map[string]map[string]any
	depth     map[string]int // distance from a root group; parents merge first
	groupsMap map[string][]string
	profile   map[string]any
	userVars  map[string]any
	hostVars  map[string]map[string]any // user-supplied per-host overrides
	hostFacts map[string]map[string]any // harvested facts per host
}

func newVarResolver(inv *model.Inventory, profile, userVars map[string]any, hostVars map[string]map[string]any) *varResolver {
	return newVarResolverWithFacts(inv, profile, userVars, hostVars, nil)
}

func newVarResolverWithFacts(inv *model.Inventory, profile, userVars map[string]any, hostVars, hostFacts map[string]map[string]any) *varResolver {
	r := &varResolver{
		inv:       inv,
		groupVars: map[string]map[string]any{},
		depth:     map[string]int{},
		groupsMap: map[string][]string{},
		profile:   profile,
		userVars:  userVars,
		hostVars:  hostVars,
		hostFacts: hostFacts,
	}
	if inv == nil {
		return r
	}
	parentOf := map[string][]string{}
	for _, g := range inv.Groups {
		r.groupVars[g.Name] = g.Vars
		r.groupsMap[g.Name] = g.Hosts
		for _, c := range g.Children {
			parentOf[c] = append(parentOf[c], g.Name)
		}
	}
	// depth = longest chain of parents; deeper (more specific) merges later
	var depthOf func(name string, seen map[string]bool) int
	depthOf = func(name string, seen map[string]bool) int {
		if seen[name] {
			return 0
		}
		seen[name] = true
		d := 0
		for _, p := range parentOf[name] {
			if pd := depthOf(p, seen) + 1; pd > d {
				d = pd
			}
		}
		return d
	}
	for _, g := range inv.Groups {
		r.depth[g.Name] = depthOf(g.Name, map[string]bool{})
	}
	return r
}

// setVar assigns key=val into eff. Dotted keys ("ansible_facts.os_family",
// as reported in missing-vars lists) are expanded into nested maps with a
// deep merge, so providing them resolves the corresponding lookups.
func setVar(eff map[string]any, key string, val any) {
	parts := strings.Split(key, ".")
	cur := eff
	for i, part := range parts {
		if i == len(parts)-1 {
			cur[part] = val
			return
		}
		// copy-on-write: never mutate maps owned by the inventory/profile
		cp := map[string]any{}
		if prev, ok := cur[part].(map[string]any); ok {
			for k, v := range prev {
				cp[k] = v
			}
		}
		cur[part] = cp
		cur = cp
	}
}

// effective computes the merged vars visible to one host within one play.
func (r *varResolver) effective(host *model.Host, play *model.Play, roleDefaults []map[string]any, playFileVars []map[string]any) map[string]any {
	eff := map[string]any{}
	merge := func(m map[string]any) {
		for k, v := range m {
			eff[k] = v
		}
	}
	// user-supplied vars may use dotted keys; expand + deep-merge them
	mergeUser := func(m map[string]any) {
		for k, v := range m {
			setVar(eff, k, v)
		}
	}

	for _, d := range roleDefaults {
		merge(d)
	}

	if host != nil {
		// "all" vars first, then groups ordered parents-before-children
		merge(r.groupVars["all"])
		groups := append([]string{}, host.Groups...)
		sort.SliceStable(groups, func(i, j int) bool { return r.depth[groups[i]] < r.depth[groups[j]] })
		for _, g := range groups {
			merge(r.groupVars[g])
		}
		merge(host.Vars)

		// magic vars
		eff["inventory_hostname"] = host.Name
		gn := make([]any, 0, len(host.Groups))
		for _, g := range host.Groups {
			gn = append(gn, g)
		}
		eff["group_names"] = gn
		allGroups := map[string]any{}
		for name, hosts := range r.groupsMap {
			lst := make([]any, 0, len(hosts))
			for _, h := range hosts {
				lst = append(lst, h)
			}
			allGroups[name] = lst
		}
		eff["groups"] = allGroups
	}

	merge(r.profile)

	// harvested facts (real data beats the synthetic profile)
	if host != nil && r.hostFacts != nil {
		if facts := r.hostFacts[host.Name]; len(facts) > 0 {
			af := map[string]any{}
			if prev, ok := eff["ansible_facts"].(map[string]any); ok {
				for k, v := range prev {
					af[k] = v
				}
			}
			for k, v := range facts {
				af[k] = v
				eff["ansible_"+k] = v
			}
			eff["ansible_facts"] = af
		}
	}

	if play != nil {
		for _, fv := range playFileVars {
			merge(fv)
		}
		merge(play.Vars)
	}

	mergeUser(r.userVars)
	if host != nil && r.hostVars != nil {
		mergeUser(r.hostVars[host.Name])
	}
	return eff
}
