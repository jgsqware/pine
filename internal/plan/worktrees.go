package plan

import (
	"path/filepath"
	"strings"
)

// Worktree is one git working tree attached to a repository: the main
// checkout, or a linked worktree created with `git worktree add`.
type Worktree struct {
	Path           string `json:"path"`
	Name           string `json:"name"`             // basename of Path, for display
	Head           string `json:"head,omitempty"`   // commit the worktree points at
	Branch         string `json:"branch,omitempty"` // short branch name, empty when detached/bare
	Detached       bool   `json:"detached"`
	Bare           bool   `json:"bare"`
	Locked         bool   `json:"locked"`
	LockReason     string `json:"lock_reason,omitempty"`
	Prunable       bool   `json:"prunable"`
	PrunableReason string `json:"prunable_reason,omitempty"`
	Main           bool   `json:"main"` // the primary worktree (first in the list)
}

// WorktreeResult lists the git worktrees attached to a repository.
type WorktreeResult struct {
	Root      string     `json:"root"`
	IsGit     bool       `json:"is_git"`
	Worktrees []Worktree `json:"worktrees"`
}

// Worktrees lists the git worktrees attached to the repository rooted at
// root by parsing `git worktree list --porcelain`.
//
// When root is not a git repository it returns IsGit=false with an empty
// list and no error, so callers (web / CLI / TUI) can honestly render
// "not a git repository" instead of surfacing a failure — Pine's path-based
// repos may be plain directories, and URL repos aren't cloned until synced.
func Worktrees(root string) (*WorktreeResult, error) {
	out := &WorktreeResult{Root: root, Worktrees: []Worktree{}}
	if _, err := gitOut(root, "rev-parse", "--git-dir"); err != nil {
		return out, nil // not a git repo: honest empty result, not an error
	}
	out.IsGit = true
	porcelain, err := gitOut(root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	out.Worktrees = parseWorktrees(porcelain)
	if len(out.Worktrees) > 0 {
		out.Worktrees[0].Main = true // the primary checkout is always listed first
	}
	return out, nil
}

// parseWorktrees turns `git worktree list --porcelain` output into structs.
// Records are separated by blank lines and start with a "worktree <path>"
// line, followed by attribute lines: HEAD <sha>, branch <ref>, detached,
// bare, locked [reason], prunable [reason].
func parseWorktrees(porcelain string) []Worktree {
	var list []Worktree
	var cur *Worktree
	flush := func() {
		if cur != nil {
			list = append(list, *cur)
			cur = nil
		}
	}
	for _, raw := range strings.Split(porcelain, "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			cur = &Worktree{Path: val, Name: filepath.Base(val)}
		case "HEAD":
			if cur != nil {
				cur.Head = val
			}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "locked":
			if cur != nil {
				cur.Locked = true
				cur.LockReason = val
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
				cur.PrunableReason = val
			}
		}
	}
	flush()
	return list
}
