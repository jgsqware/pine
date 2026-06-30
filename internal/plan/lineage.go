package plan

import (
	"fmt"
	"sort"
	"strings"

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

// RedactedMark replaces secret material when a LineageResult is redacted.
const RedactedMark = "***REDACTED***"

// Redact masks sensitive material in place: vault-encrypted blobs and
// plaintext scalars under a password-like variable name. The chain
// structure — scopes, layer names, precedence order — is preserved so
// "where does this come from?" still answers, while the secret value never
// leaves Pine. It reuses the hygiene secret heuristic (secretKeyRe +
// looksLikeSecretValue) so toggles like server_tokens: "off" and numeric
// policy knobs are not mistaken for secrets. Defense-in-depth: callers may
// keep masking on their side too.
func (r *LineageResult) Redact() {
	for i := range r.Vars {
		v := &r.Vars[i]
		keySecret := secretKeyRe.MatchString(v.Key)
		if sensitiveValue(keySecret, v.Value) {
			v.Value = RedactedMark
		}
		for j := range v.Chain {
			if sensitiveValue(keySecret, v.Chain[j].Value) {
				v.Chain[j].Value = RedactedMark
			}
		}
	}
}

// sensitiveValue reports whether v should be masked: any ansible-vault blob,
// or — when the variable name is password-like — a plaintext scalar that
// still reads as a secret (so on/off toggles and numbers stay visible).
func sensitiveValue(keySecret bool, v any) bool {
	if isVaultValue(v) {
		return true
	}
	if !keySecret {
		return false
	}
	s, ok := v.(string)
	return ok && looksLikeSecretValue(s)
}

// isVaultValue reports whether v is an ansible-vault encrypted scalar.
func isVaultValue(v any) bool {
	s, ok := v.(string)
	return ok && strings.Contains(s, "$ANSIBLE_VAULT")
}

// LineageAll resolves the variable lineage for every host of an inventory in a
// single scan pass (the scan is done once by the caller). Useful for tools that
// want every target's effective config at once instead of one process per host.
func LineageAll(res *model.ScanResult, inventory string) ([]*LineageResult, error) {
	inv := pickInventory(res, inventory)
	if inv == nil {
		return nil, fmt.Errorf("inventory not found: %s", inventory)
	}
	out := make([]*LineageResult, 0, len(inv.Hosts))
	for i := range inv.Hosts {
		lin, err := Lineage(res, inventory, inv.Hosts[i].Name)
		if err != nil {
			return nil, err
		}
		out = append(out, lin)
	}
	return out, nil
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

	// role vars/main.yml sit high in Ansible's precedence (above host/group
	// vars), so they are added last — they win the chain.
	for _, rn := range roleNames {
		for i := range res.Roles {
			if res.Roles[i].Name == rn && len(res.Roles[i].Vars) > 0 {
				add("role_vars", rn, res.Roles[i].Vars)
			}
		}
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
