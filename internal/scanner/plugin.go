package scanner

import (
	"path/filepath"
)

// Plugin teaches the scanner where to find Ansible artifacts in a repository
// that does not follow the conventional layout. A plugin only *adds* load
// paths on top of the defaults (repo root, playbooks/, plays/, roles/) — it
// never removes them, so conventional repos keep scanning exactly as before.
type Plugin struct {
	// Name identifies the plugin, e.g. "gaming1".
	Name string
	// Detect reports whether this plugin recognizes the repository at root.
	Detect func(root string) bool
	// PlaybookDirs are directories searched recursively for playbooks, in
	// addition to the repo root and playbooks//plays/.
	PlaybookDirs []string
	// RoleDirs are parent directories whose immediate subdirectories are
	// roles, in addition to roles/.
	RoleDirs []string
}

// plugins is the ordered registry of layout plugins. The first whose Detect
// matches wins.
var plugins = []Plugin{gaming1Plugin}

// gaming1Plugin describes the Gaming1 "dockers" Ansible deployment platform
// (gitlab.gaming1.net/landbased-gaming/devops/dockers): an interactive
// menu.sh dispatcher over playbooks grouped into projects/, servers/ and
// tools/, with shared roles under generic_roles/ and a single inventory/.
var gaming1Plugin = Plugin{
	Name: "gaming1",
	Detect: func(root string) bool {
		return isFile(filepath.Join(root, "menu.sh")) &&
			isDir(filepath.Join(root, "projects")) &&
			isDir(filepath.Join(root, "inventory"))
	},
	PlaybookDirs: []string{"projects", "servers", "tools"},
	RoleDirs:     []string{"generic_roles"},
}

// detectPlugin returns the first registered plugin that recognizes root,
// or nil when the repository uses the conventional layout.
func detectPlugin(root string) *Plugin {
	for i := range plugins {
		if plugins[i].Detect(root) {
			return &plugins[i]
		}
	}
	return nil
}
