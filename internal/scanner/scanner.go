// Package scanner walks an Ansible repository and extracts playbooks,
// roles and inventories without requiring ansible to be installed.
package scanner

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/jgsqware/pine/internal/model"
)

// Scan inspects the repository rooted at path and returns everything found.
// Optional scanPaths restrict playbook discovery to specific directories,
// files or globs (relative to root). It parses every file from scratch; for
// incremental re-scans that reuse unchanged parse results, use ScanWithCache.
func Scan(root string, scanPaths ...string) (*model.ScanResult, error) {
	return ScanWithCache(root, nil, scanPaths...)
}

// ScanWithCache is Scan with an optional incremental parse cache that survives
// between calls. A nil cache reproduces Scan's behavior exactly (parse
// everything). With a cache, playbook files and role directories whose
// mtime/size are unchanged since the previous scan reuse their cached parse
// result instead of being re-parsed; paths absent from this scan are purged.
func ScanWithCache(root string, cache *ScanCache, scanPaths ...string) (*model.ScanResult, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if cache != nil {
		// Serialize the whole scan on this cache and reset seen-tracking, so
		// vanished paths can be purged once all phases complete.
		cache.beginScan()
		defer cache.endScan()
	}
	plugin := detectPlugin(root)
	// The three phases are independent (disjoint result slices, read-only
	// filesystem access), so run them concurrently. Each writes only its own
	// field, so no locking is needed.
	res := &model.ScanResult{}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); res.Playbooks = scanPlaybooks(root, plugin, scanPaths, cache) }()
	go func() { defer wg.Done(); res.Roles = scanRoles(root, plugin, cache) }()
	go func() { defer wg.Done(); res.Inventories = scanInventories(root) }()
	wg.Wait()
	return res, nil
}

// Summarize computes the headline counts for a scan result.
func Summarize(res *model.ScanResult) model.RepoSummary {
	s := model.RepoSummary{
		Playbooks:   len(res.Playbooks),
		Roles:       len(res.Roles),
		Inventories: len(res.Inventories),
	}
	hosts := map[string]bool{}
	groups := map[string]bool{}
	for _, inv := range res.Inventories {
		for _, h := range inv.Hosts {
			hosts[h.Name] = true
		}
		for _, g := range inv.Groups {
			groups[g.Name] = true
		}
	}
	s.Hosts = len(hosts)
	s.Groups = len(groups)
	return s
}

// yamlFiles returns the .yml/.yaml entries directly inside dir.
func yamlFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml") {
			out = append(out, filepath.Join(dir, name))
		}
	}
	sort.Strings(out)
	return out
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// listRel returns file names (relative) under dir, non-recursive fallback to recursive walk.
func listRel(dir string) []string {
	var out []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

// toStrSlice normalizes a YAML scalar-or-list value into a string slice.
func toStrSlice(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// toStr renders a YAML scalar-or-list value as a single display string.
func toStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, toStr(e))
		}
		return strings.Join(parts, " and ")
	}
	return ""
}
