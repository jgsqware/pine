package scanner

import (
	"path/filepath"
	"strings"

	"github.com/jgsqware/pine/internal/model"
)

// MatchHosts resolves an Ansible host pattern to the list of matching hosts
// in the inventory.  The Ansible semantics implemented here are:
//
//   - plain segments (union): group name, host name, wildcards, "all"/"*"
//   - "&group" (intersection): retain only hosts that are also in the group
//   - "!group" (exclusion): remove hosts that are in the group
//
// Processing follows Ansible's three-pass approach over colon/comma-separated
// segments: unions are accumulated first, then "&" intersections are applied,
// then "!" exclusions are removed.
//
// Unsupported constructs ("~regex", "[n:m]" ranges) are silently skipped —
// use HasUnsupportedPattern to detect them before calling this function.
func MatchHosts(pattern string, inv *model.Inventory) []string {
	if pattern == "" {
		return nil
	}

	segments := strings.FieldsFunc(pattern, func(r rune) bool { return r == ':' || r == ',' })

	// expandTerm resolves a plain term (no leading ! or &) to a set of host
	// names.  Unsupported terms (regex, range) expand to an empty set.
	expandTerm := func(term string) map[string]bool {
		term = strings.TrimSpace(term)
		set := map[string]bool{}
		if term == "" {
			return set
		}
		// Unsupported: regex (~) or range ([…]) – skip silently.
		if strings.HasPrefix(term, "~") || (strings.Contains(term, "[") && strings.Contains(term, "]")) {
			return set
		}
		if term == "all" || term == "*" {
			for _, h := range inv.Hosts {
				set[h.Name] = true
			}
			return set
		}
		// Try group membership first.
		matched := false
		for _, h := range inv.Hosts {
			for _, g := range h.Groups {
				if g == term {
					set[h.Name] = true
					matched = true
				}
			}
		}
		if !matched {
			// Fall back to host name / wildcard match.
			for _, h := range inv.Hosts {
				if h.Name == term || wildcardMatch(term, h.Name) {
					set[h.Name] = true
				}
			}
		}
		return set
	}

	// Pass 1: union – accumulate all plain (non-prefixed) segments.
	union := map[string]bool{}
	hasUnion := false
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" || strings.HasPrefix(seg, "!") || strings.HasPrefix(seg, "&") {
			continue
		}
		hasUnion = true
		for h := range expandTerm(seg) {
			union[h] = true
		}
	}
	if !hasUnion {
		return nil
	}

	// Pass 2: intersection – "&group" terms narrow the union set.
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if !strings.HasPrefix(seg, "&") {
			continue
		}
		inter := expandTerm(strings.TrimPrefix(seg, "&"))
		for h := range union {
			if !inter[h] {
				delete(union, h)
			}
		}
	}

	// Pass 3: exclusion – "!group" terms are removed from the result.
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if !strings.HasPrefix(seg, "!") {
			continue
		}
		for h := range expandTerm(strings.TrimPrefix(seg, "!")) {
			delete(union, h)
		}
	}

	// Return in a stable order that matches inv.Hosts.
	var out []string
	for _, h := range inv.Hosts {
		if union[h.Name] {
			out = append(out, h.Name)
		}
	}
	return out
}

// HasUnsupportedPattern reports whether pattern contains constructs that
// MatchHosts cannot evaluate: "~regex" terms or "[n:m]" range suffixes.
// The plan engine uses this to emit "unknown" verdicts rather than
// presenting a firm (and potentially wrong) result.
func HasUnsupportedPattern(pattern string) bool {
	// Range notation "[n:m]" or "[n]" contains a colon, so it cannot be
	// detected reliably after splitting on ':'.  Check the raw pattern first:
	// any '[' … ']' pair anywhere in the string indicates a range suffix.
	if strings.Contains(pattern, "[") && strings.Contains(pattern, "]") {
		return true
	}
	for _, seg := range strings.FieldsFunc(pattern, func(r rune) bool { return r == ':' || r == ',' }) {
		seg = strings.TrimSpace(seg)
		// Strip any leading operator so we check the term itself.
		seg = strings.TrimPrefix(seg, "!")
		seg = strings.TrimPrefix(seg, "&")
		if strings.HasPrefix(seg, "~") {
			return true
		}
	}
	return false
}

func wildcardMatch(pattern, s string) bool {
	ok, err := filepath.Match(pattern, s)
	return err == nil && ok
}
