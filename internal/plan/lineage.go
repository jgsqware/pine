package plan

import (
	"fmt"
	"sort"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/scanner"
)

// LineageEntry is one layer of the precedence chain for a variable.
type LineageEntry struct {
	Scope string `json:"scope"` // role_default | group | host
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// VarLineage is the full precedence chain of one variable on one host.
// The last chain entry is the effective value.
type VarLineage struct {
	Key   string         `json:"key"`
	Value any            `json:"value"`
	Chain []LineageEntry `json:"chain"`
}

// LineageResult answers "where does each value come from?" for a host.
type LineageResult struct {
	Host      string       `json:"host"`
	Inventory string       `json:"inventory"`
	Vars      []VarLineage `json:"vars"`
}

// Lineage computes, for every variable visible to host, the ordered chain
// of layers that define it: role defaults (for roles of plays targeting
// this host), then group vars ("all" first, parents before children),
// then host vars. Magic vars are excluded.
func Lineage(res *model.ScanResult, inventory, hostName string) (*LineageResult, error) {
	inv := pickInventory(res, inventory)
	if inv == nil {
		return nil, fmt.Errorf("inventory not found: %s", inventory)
	}
	var host *model.Host
	for i := range inv.Hosts {
		if inv.Hosts[i].Name == hostName {
			host = &inv.Hosts[i]
		}
	}
	if host == nil {
		return nil, fmt.Errorf("host not found: %s", hostName)
	}

	chains := map[string][]LineageEntry{}
	add := func(scope, name string, vars map[string]any) {
		var keys []string
		for k := range vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			chains[k] = append(chains[k], LineageEntry{Scope: scope, Name: name, Value: vars[k]})
		}
	}

	// role defaults: only roles used by plays that actually target this host
	roleSeen := map[string]bool{}
	var roleNames []string
	for _, pb := range res.Playbooks {
		for _, play := range pb.Plays {
			if play.Import != "" {
				continue
			}
			targeted := false
			for _, h := range scanner.MatchHosts(play.Hosts, inv) {
				if h == hostName {
					targeted = true
				}
			}
			if !targeted {
				continue
			}
			for _, rn := range play.Roles {
				if !roleSeen[rn] {
					roleSeen[rn] = true
					roleNames = append(roleNames, rn)
				}
			}
		}
	}
	sort.Strings(roleNames)
	for _, rn := range roleNames {
		for i := range res.Roles {
			if res.Roles[i].Name == rn && len(res.Roles[i].Defaults) > 0 {
				add("role_default", rn, res.Roles[i].Defaults)
			}
		}
	}

	// groups: "all" first, then the host's groups, parents before children
	groupVars := map[string]map[string]any{}
	for _, g := range inv.Groups {
		groupVars[g.Name] = g.Vars
	}
	resolver := newVarResolver(inv, nil, nil, nil)
	if vars := groupVars["all"]; len(vars) > 0 {
		add("group", "all", vars)
	}
	groups := append([]string{}, host.Groups...)
	sort.SliceStable(groups, func(i, j int) bool { return resolver.depth[groups[i]] < resolver.depth[groups[j]] })
	for _, gn := range groups {
		if gn == "all" {
			continue
		}
		if vars := groupVars[gn]; len(vars) > 0 {
			add("group", gn, vars)
		}
	}

	if len(host.Vars) > 0 {
		add("host", host.Name, host.Vars)
	}

	out := &LineageResult{Host: hostName, Inventory: inv.Name, Vars: []VarLineage{}}
	var keys []string
	for k := range chains {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		chain := chains[k]
		out.Vars = append(out.Vars, VarLineage{
			Key:   k,
			Value: chain[len(chain)-1].Value,
			Chain: chain,
		})
	}
	return out, nil
}
