package scanner

import "github.com/jgsqware/pine/internal/model"

// TopoNode is a vertex of the inventory topology graph.
type TopoNode struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Type        string `json:"type"` // group | host
	Group       string `json:"group,omitempty"`
	Size        int    `json:"size,omitempty"`
	Constructed bool   `json:"constructed,omitempty"`
}

// TopoLink is an edge: group->group ("child") or group->host ("member").
type TopoLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
}

// Topology is the graph representation of one inventory.
type Topology struct {
	Nodes []TopoNode `json:"nodes"`
	Links []TopoLink `json:"links"`
}

// BuildTopology converts an inventory into a graph for visualization.
func BuildTopology(inv *model.Inventory) *Topology {
	t := &Topology{Nodes: []TopoNode{}, Links: []TopoLink{}}

	// transitive host count per group
	hostCount := map[string]int{}
	children := map[string][]string{}
	direct := map[string][]string{}
	for _, g := range inv.Groups {
		children[g.Name] = g.Children
		direct[g.Name] = g.Hosts
	}
	var count func(g string, seen map[string]bool) int
	count = func(g string, seen map[string]bool) int {
		if seen[g] {
			return 0
		}
		seen[g] = true
		n := len(direct[g])
		for _, c := range children[g] {
			n += count(c, seen)
		}
		return n
	}
	for _, g := range inv.Groups {
		hostCount[g.Name] = count(g.Name, map[string]bool{})
	}

	for _, g := range inv.Groups {
		if g.Name == "all" {
			continue
		}
		t.Nodes = append(t.Nodes, TopoNode{
			ID: "g:" + g.Name, Label: g.Name, Type: "group", Size: hostCount[g.Name],
			Constructed: g.Constructed,
		})
		for _, c := range g.Children {
			if c == "all" {
				continue
			}
			t.Links = append(t.Links, TopoLink{Source: "g:" + g.Name, Target: "g:" + c, Type: "child"})
		}
		for _, h := range g.Hosts {
			t.Links = append(t.Links, TopoLink{Source: "g:" + g.Name, Target: "h:" + h, Type: "member"})
		}
	}
	for _, h := range inv.Hosts {
		primary := ""
		// pick the most specific group: one that lists the host directly
		for _, g := range inv.Groups {
			if contains(g.Hosts, h.Name) {
				primary = g.Name
				break
			}
		}
		t.Nodes = append(t.Nodes, TopoNode{ID: "h:" + h.Name, Label: h.Name, Type: "host", Group: primary})
	}
	return t
}
