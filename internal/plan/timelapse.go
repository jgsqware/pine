package plan

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jgsqware/pine/internal/scanner"
)

// TimelapseFrame is the inventory topology at one commit.
type TimelapseFrame struct {
	Commit   string            `json:"commit"`
	Date     string            `json:"date"`
	Message  string            `json:"message"`
	Hosts    int               `json:"hosts"`
	Groups   int               `json:"groups"`
	Topology *scanner.Topology `json:"topology"`
}

// TimelapseResult is the animated history of an inventory.
type TimelapseResult struct {
	Frames []TimelapseFrame `json:"frames"`
}

func gitOut(root string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return string(out), nil
}

// Timelapse replays the repo's git history and rebuilds the inventory
// topology at each commit (oldest first, consecutive duplicates dropped).
func Timelapse(root, inventory string, limit int) (*TimelapseResult, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	prefix, err := gitOut(root, "rev-parse", "--show-prefix")
	if err != nil {
		return nil, err
	}
	prefix = strings.TrimSpace(prefix)

	logOut, err := gitOut(root, "log", "--format=%H%x09%ct%x09%s", "-n", strconv.Itoa(limit), "--", ".")
	if err != nil {
		return nil, err
	}
	type commitInfo struct{ hash, date, msg string }
	var commits []commitInfo
	for _, line := range strings.Split(strings.TrimSpace(logOut), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		ts, _ := strconv.ParseInt(parts[1], 10, 64)
		commits = append(commits, commitInfo{
			hash: parts[0],
			date: time.Unix(ts, 0).UTC().Format(time.RFC3339),
			msg:  parts[2],
		})
	}
	// oldest first
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}

	out := &TimelapseResult{Frames: []TimelapseFrame{}}
	lastSig := ""
	for _, ci := range commits {
		frame, sig, err := frameAt(root, prefix, ci.hash, inventory)
		if err != nil || frame == nil {
			continue
		}
		if sig == lastSig {
			continue // inventory unchanged in this commit
		}
		lastSig = sig
		frame.Commit = ci.hash[:7]
		frame.Date = ci.date
		frame.Message = ci.msg
		out.Frames = append(out.Frames, *frame)
	}
	if len(out.Frames) == 0 {
		return nil, fmt.Errorf("no inventory found in the last %d commits", limit)
	}
	return out, nil
}

// frameAt materializes the inventory-relevant files of one commit into a
// temp dir, scans it, and builds the topology frame.
func frameAt(root, prefix, commit, inventory string) (*TimelapseFrame, string, error) {
	lsOut, err := gitOut(root, "ls-tree", "-r", "--name-only", commit, "--", ".")
	if err != nil {
		return nil, "", err
	}
	tmp, err := os.MkdirTemp("", "pine-tl-*")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(tmp)

	dumped := 0
	for _, full := range strings.Split(strings.TrimSpace(lsOut), "\n") {
		rel := strings.TrimPrefix(full, prefix)
		if rel == "" || !inventoryRelevant(rel) {
			continue
		}
		content, err := gitOut(root, "show", commit+":./"+rel)
		if err != nil {
			continue
		}
		dst := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			continue
		}
		if os.WriteFile(dst, []byte(content), 0o644) == nil {
			dumped++
		}
	}
	if dumped == 0 {
		return nil, "", nil
	}
	res, err := scanner.Scan(tmp)
	if err != nil {
		return nil, "", err
	}
	inv := pickInventory(res, inventory)
	if inv == nil {
		return nil, "", nil
	}
	topo := scanner.BuildTopology(inv)
	// signature: sorted node ids + member links
	var sig strings.Builder
	for _, n := range topo.Nodes {
		sig.WriteString(n.ID + ";")
	}
	for _, l := range topo.Links {
		sig.WriteString(l.Source + ">" + l.Target + ";")
	}
	return &TimelapseFrame{
		Hosts:    len(inv.Hosts),
		Groups:   len(inv.Groups),
		Topology: topo,
	}, sig.String(), nil
}

// inventoryRelevant keeps only the files that shape an inventory.
func inventoryRelevant(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, p := range parts[:max(0, len(parts)-1)] {
		switch p {
		case "inventories", "inventory", "environments", "group_vars", "host_vars":
			return true
		case "roles", "templates", "files", "tasks", "handlers", ".git", "node_modules":
			return false
		}
	}
	base := strings.ToLower(filepath.Base(rel))
	return strings.Contains(base, "hosts") || strings.Contains(base, "inventor")
}
