// Inventory tab: an expandable group tree with selectable host leaves,
// live substring filtering, and one-key SSH into a host using its
// resolved Ansible connection variables.
package tui

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jgsqware/pine/internal/model"
)

type invKind int

const (
	rowInv   invKind = iota // an inventory header
	rowGroup                // a group node (expandable)
	rowHost                 // a host leaf (selectable / ssh-able)
)

// invRow is one visible line in the inventory tree.
type invRow struct {
	kind  invKind
	depth int
	key   string // stable identity for collapse state
	inv   *model.Inventory
	group *model.Group
	host  *model.Host
	open  bool // expanded (inv/group only)
	kids  bool // has any children (inv/group only)
}

// invTree flattens every inventory into the list of currently visible rows,
// honouring collapse state and the active filter. When a filter is set, only
// matching hosts and their ancestor groups appear, force-expanded.
func (a *app) invTree() []invRow {
	var out []invRow
	if a.scan == nil {
		return out
	}
	filter := strings.ToLower(strings.TrimSpace(a.invFilter))

	for i := range a.scan.Inventories {
		inv := &a.scan.Inventories[i]
		gByName := map[string]*model.Group{}
		for j := range inv.Groups {
			gByName[inv.Groups[j].Name] = &inv.Groups[j]
		}
		hByName := map[string]*model.Host{}
		for j := range inv.Hosts {
			hByName[inv.Hosts[j].Name] = &inv.Hosts[j]
		}
		childSet := map[string]bool{}
		for j := range inv.Groups {
			for _, c := range inv.Groups[j].Children {
				childSet[c] = true
			}
		}

		// Build the body (groups + ungrouped hosts) before the header so we
		// know whether the inventory has anything to show under a filter.
		var body []invRow
		var tops []string
		for j := range inv.Groups {
			if name := inv.Groups[j].Name; !childSet[name] {
				tops = append(tops, name)
			}
		}
		sort.Strings(tops)
		invKey := inv.Name
		invOpen := !a.collapsed[invKey] || filter != ""
		for _, gn := range tops {
			body = append(body, a.groupRows(inv, gByName, hByName, gn, 1, invKey, map[string]bool{}, filter)...)
		}
		// hosts that belong to no group
		var ungrouped []string
		for j := range inv.Hosts {
			if len(inv.Hosts[j].Groups) == 0 {
				ungrouped = append(ungrouped, inv.Hosts[j].Name)
			}
		}
		sort.Strings(ungrouped)
		for _, hn := range ungrouped {
			h := hByName[hn]
			if !hostMatches(h, filter) {
				continue
			}
			body = append(body, invRow{kind: rowHost, depth: 1, key: invKey + "\x00" + hn, inv: inv, host: h})
		}

		if filter != "" && len(body) == 0 {
			continue // nothing in this inventory matched
		}
		out = append(out, invRow{kind: rowInv, depth: 0, key: invKey, inv: inv, open: invOpen, kids: len(body) > 0})
		if invOpen {
			out = append(out, body...)
		}
	}
	return out
}

// groupRows renders a group subtree (the group row plus, when expanded, its
// child groups and direct hosts). visited guards against cyclic child links;
// a group reachable from several parents is rendered under each.
func (a *app) groupRows(inv *model.Inventory, gByName map[string]*model.Group, hByName map[string]*model.Host, name string, depth int, parentKey string, visited map[string]bool, filter string) []invRow {
	g := gByName[name]
	if g == nil || visited[name] {
		return nil
	}
	visited[name] = true
	defer delete(visited, name)

	key := parentKey + "/" + name
	open := !a.collapsed[key] || filter != ""

	var body []invRow
	children := append([]string(nil), g.Children...)
	sort.Strings(children)
	for _, c := range children {
		body = append(body, a.groupRows(inv, gByName, hByName, c, depth+1, key, visited, filter)...)
	}
	hosts := append([]string(nil), g.Hosts...)
	sort.Strings(hosts)
	for _, hn := range hosts {
		h := hByName[hn]
		if h == nil {
			h = &model.Host{Name: hn}
		}
		if !hostMatches(h, filter) {
			continue
		}
		body = append(body, invRow{kind: rowHost, depth: depth + 1, key: key + "\x00" + hn, inv: inv, host: h})
	}

	if filter != "" && len(body) == 0 {
		return nil // pruned: no descendant host matched
	}
	row := invRow{kind: rowGroup, depth: depth, key: key, inv: inv, group: g, open: open, kids: len(g.Children) > 0 || len(g.Hosts) > 0}
	rows := []invRow{row}
	if open {
		rows = append(rows, body...)
	}
	return rows
}

// invRowString renders a tree row to a display line.
func (a *app) invRowString(r invRow) string {
	indent := strings.Repeat("  ", r.depth)
	switch r.kind {
	case rowInv:
		return fmt.Sprintf("%s %s %s", expandMarker(r.open, true),
			sTitle.Render(r.inv.Name),
			sDim.Render(fmt.Sprintf("(%s, %d hosts)", r.inv.Format, len(r.inv.Hosts))))
	case rowGroup:
		meta := groupMeta(r.group)
		return fmt.Sprintf("%s%s %s  %s", indent, expandMarker(r.open, r.kids),
			sCyan.Render(r.group.Name), sDim.Render(meta))
	case rowHost:
		addr := hostAddr(r.host)
		return fmt.Sprintf("%s  %-22s %s", indent, r.host.Name, sDim.Render(addr))
	}
	return ""
}

func expandMarker(open, hasKids bool) string {
	if !hasKids {
		return " "
	}
	if open {
		return "▾"
	}
	return "▸"
}

func groupMeta(g *model.Group) string {
	var parts []string
	if n := len(g.Hosts); n > 0 {
		parts = append(parts, fmt.Sprintf("%d hosts", n))
	}
	if n := len(g.Children); n > 0 {
		parts = append(parts, fmt.Sprintf("%d sub", n))
	}
	return strings.Join(parts, ", ")
}

// hostMatches reports whether a host satisfies the (already lowercased) filter,
// matching against its name, address or any group membership.
func hostMatches(h *model.Host, filter string) bool {
	if filter == "" {
		return true
	}
	if h == nil {
		return false
	}
	if strings.Contains(strings.ToLower(h.Name), filter) {
		return true
	}
	if a := strings.ToLower(hostAddr(h)); a != "" && strings.Contains(a, filter) {
		return true
	}
	for _, g := range h.Groups {
		if strings.Contains(strings.ToLower(g), filter) {
			return true
		}
	}
	return false
}

func hostAddr(h *model.Host) string {
	if h == nil {
		return ""
	}
	if v, ok := h.Vars["ansible_host"]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// clampCursor keeps the active tab's cursor within the current row count.
func (a *app) clampCursor() {
	n := a.listLen()
	if c := a.cursor[a.tab]; c >= n {
		a.cursor[a.tab] = n - 1
	}
	if a.cursor[a.tab] < 0 {
		a.cursor[a.tab] = 0
	}
}

// sshSelected suspends the TUI and opens an interactive ssh session to the
// host under the cursor, building the command from its resolved vars.
func (a *app) sshSelected() (tea.Model, tea.Cmd) {
	tree := a.invTree()
	i := a.cursor[a.tab]
	if i < 0 || i >= len(tree) || tree[i].kind != rowHost {
		a.status = "select a host to ssh into"
		return a, nil
	}
	r := tree[i]
	args := sshArgs(r.inv, r.host)
	a.status = "ssh " + strings.Join(args, " ")
	cmd := exec.Command("ssh", args...)
	return a, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return sshDoneMsg{host: r.host.Name, err: err}
	})
}

// resolveHostVars merges the vars of every group a host belongs to (least
// specific) with the host's own vars (most specific, wins on conflict).
func resolveHostVars(inv *model.Inventory, h *model.Host) map[string]any {
	out := map[string]any{}
	if inv != nil {
		gByName := map[string]*model.Group{}
		for i := range inv.Groups {
			gByName[inv.Groups[i].Name] = &inv.Groups[i]
		}
		// Every host implicitly belongs to "all"; apply it first (lowest
		// precedence) since it isn't listed in the host's explicit groups.
		if g := gByName["all"]; g != nil {
			for k, v := range g.Vars {
				out[k] = v
			}
		}
		for _, gn := range h.Groups {
			if g := gByName[gn]; g != nil {
				for k, v := range g.Vars {
					out[k] = v
				}
			}
		}
	}
	for k, v := range h.Vars {
		out[k] = v
	}
	return out
}

// sshArgs builds the argument list for `ssh` from a host's Ansible connection
// variables, falling back to the inventory hostname when no address is set.
func sshArgs(inv *model.Inventory, h *model.Host) []string {
	vars := resolveHostVars(inv, h)
	addr := varStr(vars["ansible_host"])
	if addr == "" {
		addr = h.Name
	}
	user := varStr(vars["ansible_user"])
	port := varStr(vars["ansible_port"])
	key := varStr(vars["ansible_ssh_private_key_file"])

	var args []string
	if port != "" {
		args = append(args, "-p", port)
	}
	if key != "" {
		args = append(args, "-i", key)
	}
	if extra := varStr(vars["ansible_ssh_common_args"]); extra != "" {
		args = append(args, strings.Fields(extra)...)
	}
	dest := addr
	if user != "" {
		dest = user + "@" + addr
	}
	return append(args, dest)
}

func varStr(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

// renderHostDetail shows a host's resolved connection target and variables.
func renderHostDetail(inv *model.Inventory, h *model.Host) string {
	vars := resolveHostVars(inv, h)
	args := sshArgs(inv, h)

	var b strings.Builder
	b.WriteString(sTitle.Render("host "+h.Name) + sDim.Render("  inventory: "+inv.Name) + "\n\n")
	b.WriteString(sDim.Render("ssh: ") + sLogPlay.Render("ssh "+strings.Join(args, " ")) + sDim.Render("   (press s)") + "\n\n")
	if len(h.Groups) > 0 {
		b.WriteString(sDim.Render("groups: ") + sCyan.Render(strings.Join(h.Groups, ", ")) + "\n\n")
	}
	if len(vars) > 0 {
		b.WriteString(sDim.Render("resolved vars:") + "\n")
		keys := make([]string, 0, len(vars))
		for k := range vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(fmt.Sprintf("  %s: %v\n", sCyan.Render(k), vars[k]))
		}
	}
	return b.String()
}
