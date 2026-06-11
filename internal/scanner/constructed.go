package scanner

import (
	"sort"
	"strings"

	"github.com/jgsqware/pine/internal/model"
)

// parsePluginFile reports whether file is an inventory *plugin* config
// (any `plugin:` key). The second return is non-nil only for the
// constructed plugin, the one Pine can emulate.
func parsePluginFile(file string) (isPlugin bool, cfg *model.ConstructedRule) {
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
	cfg = &model.ConstructedRule{Groups: map[string]string{}}
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
			kg := model.KeyedGroup{
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
func applyConstructed(inv *model.Inventory, cfg *model.ConstructedRule) {
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

// KeyedGroupName exposes keyed-group naming for what-if previews.
func KeyedGroupName(prefix, sep, value string) string {
	return sanitizeGroupName(prefix, sep, value)
}
