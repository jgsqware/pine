package scanner

import (
	"path/filepath"
	"strings"

	"github.com/jgsqware/pine/internal/model"
)

// MatchHosts returns the hosts an ansible host pattern would target in
// this inventory (groups, host names, * wildcards, ":"/"," unions; "!"/"&"
// terms are ignored conservatively).
func MatchHosts(pattern string, inv *model.Inventory) []string {
	if pattern == "" {
		return nil
	}
	var out []string
	add := func(h string) {
		for _, e := range out {
			if e == h {
				return
			}
		}
		out = append(out, h)
	}
	for _, part := range strings.FieldsFunc(pattern, func(r rune) bool { return r == ':' || r == ',' }) {
		part = strings.TrimSpace(part)
		if part == "" || strings.HasPrefix(part, "!") || strings.HasPrefix(part, "&") {
			continue
		}
		if part == "all" || part == "*" {
			for _, h := range inv.Hosts {
				add(h.Name)
			}
			continue
		}
		matched := false
		for _, h := range inv.Hosts {
			for _, g := range h.Groups {
				if g == part {
					add(h.Name)
					matched = true
				}
			}
		}
		if !matched {
			for _, h := range inv.Hosts {
				if h.Name == part || wildcardMatch(part, h.Name) {
					add(h.Name)
				}
			}
		}
	}
	return out
}

func wildcardMatch(pattern, s string) bool {
	ok, err := filepath.Match(pattern, s)
	return err == nil && ok
}
