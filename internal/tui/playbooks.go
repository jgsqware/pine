// Playbooks tab: a directory treeview over the scanned playbook paths, with
// collapsible folders, a substring filter and per-leaf flow navigation.
package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type pbKind int

const (
	pbDir  pbKind = iota // a directory node (collapsible)
	pbLeaf               // a playbook leaf
)

// pbRow is one visible line of the playbook tree.
type pbRow struct {
	kind  pbKind
	depth int
	key   string // stable identity for collapse state
	name  string // path segment to display
	open  bool   // expanded (dir only)
	kids  bool   // has children (dir only)
	pbIX  int    // index into scan.Playbooks (leaf only)
}

// pbnode is the intermediate directory tree built from playbook paths.
type pbnode struct {
	children map[string]*pbnode
	leafIX   int // >= 0 when this node is a playbook leaf
}

func newPBNode() *pbnode { return &pbnode{children: map[string]*pbnode{}, leafIX: -1} }

func (a *app) pbTreeRoot() *pbnode {
	root := newPBNode()
	if a.scan == nil {
		return root
	}
	for i := range a.scan.Playbooks {
		parts := strings.Split(filepath.ToSlash(a.scan.Playbooks[i].Path), "/")
		n := root
		for j, part := range parts {
			if part == "" {
				continue
			}
			child, ok := n.children[part]
			if !ok {
				child = newPBNode()
				n.children[part] = child
			}
			if j == len(parts)-1 {
				child.leafIX = i
			}
			n = child
		}
	}
	return root
}

// pbTree flattens the directory tree into the currently visible rows, honouring
// collapse state and the active filter (matches force folders open).
func (a *app) pbTree() []pbRow {
	if a.scan == nil {
		return nil
	}
	return a.pbRows(a.pbTreeRoot(), 0, "pb:", strings.ToLower(strings.TrimSpace(a.pbFilter)))
}

func (a *app) pbRows(n *pbnode, depth int, prefix, filter string) []pbRow {
	var dirs, leaves []string
	for name, c := range n.children {
		if len(c.children) > 0 {
			dirs = append(dirs, name)
		} else {
			leaves = append(leaves, name)
		}
	}
	sort.Strings(dirs)
	sort.Strings(leaves)

	var out []pbRow
	for _, name := range dirs {
		c := n.children[name]
		key := prefix + "/" + name
		open := !a.collapsed[key] || filter != ""
		body := a.pbRows(c, depth+1, key, filter)
		if filter != "" && len(body) == 0 {
			continue // no descendant matched the filter
		}
		out = append(out, pbRow{kind: pbDir, depth: depth, key: key, name: name, open: open, kids: len(c.children) > 0})
		if open {
			out = append(out, body...)
		}
	}
	for _, name := range leaves {
		c := n.children[name]
		if filter != "" {
			p := filepath.ToSlash(a.scan.Playbooks[c.leafIX].Path)
			if !strings.Contains(strings.ToLower(p), filter) {
				continue
			}
		}
		out = append(out, pbRow{kind: pbLeaf, depth: depth, key: prefix + "/" + name, name: name, pbIX: c.leafIX})
	}
	return out
}

// pbSelected returns the row under the cursor on the playbooks tab.
func (a *app) pbSelected() (pbRow, bool) {
	tree := a.pbTree()
	if i := a.cursor[tabPlaybooks]; i >= 0 && i < len(tree) {
		return tree[i], true
	}
	return pbRow{}, false
}

func (a *app) pbRowString(r pbRow) string {
	indent := strings.Repeat("  ", r.depth)
	switch r.kind {
	case pbDir:
		return fmt.Sprintf("%s%s %s", indent, expandMarker(r.open, r.kids), sCyan.Render(r.name+"/"))
	case pbLeaf:
		p := a.scan.Playbooks[r.pbIX]
		return fmt.Sprintf("%s  %s %s", indent, r.name, sFaint.Render(fmt.Sprintf("· %d plays", len(p.Plays))))
	}
	return ""
}

// pbDirDetail summarises a folder in the preview pane.
func (a *app) pbDirDetail(r pbRow) string {
	dir := strings.TrimPrefix(r.key, "pb:/")
	var rels []string
	for i := range a.scan.Playbooks {
		sp := filepath.ToSlash(a.scan.Playbooks[i].Path)
		if strings.HasPrefix(sp, dir+"/") {
			rels = append(rels, strings.TrimPrefix(sp, dir+"/"))
		}
	}
	sort.Strings(rels)

	var b strings.Builder
	b.WriteString(sCyan.Render("folder") + "  " + dir + "/\n")
	b.WriteString(sFaint.Render(fmt.Sprintf("%d playbooks below", len(rels))) + "\n\n")
	const max = 30
	for i, rel := range rels {
		if i >= max {
			b.WriteString(sFaint.Render(fmt.Sprintf("… %d more", len(rels)-max)))
			break
		}
		b.WriteString("  " + rel + "\n")
	}
	return b.String()
}
