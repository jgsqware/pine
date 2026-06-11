package runner

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/jgsqware/pine/internal/model"
)

// HostChange is one host whose status differs between two runs.
type HostChange struct {
	Host string `json:"host"`
	A    string `json:"a"`
	B    string `json:"b"`
}

// TaskDiff is one task whose outcome differs between two runs.
type TaskDiff struct {
	Name    string       `json:"name"`
	OnlyIn  string       `json:"only_in"` // "" | "a" | "b"
	Changes []HostChange `json:"changes"`
}

// DiffSummary aggregates a job comparison.
type DiffSummary struct {
	Regressed    int `json:"regressed"` // -> failed, or changed where it was ok
	Improved     int `json:"improved"`  // failed -> ok/changed
	Changed      int `json:"changed"`   // any other transition
	NewTasks     int `json:"new_tasks"`
	RemovedTasks int `json:"removed_tasks"`
	Same         int `json:"same"`
}

// JobDiff compares two runs of the same playbook.
type JobDiff struct {
	A       model.Job   `json:"a"`
	B       model.Job   `json:"b"`
	Summary DiffSummary `json:"summary"`
	Tasks   []TaskDiff  `json:"tasks"`
}

var (
	taskBannerRe = regexp.MustCompile(`^(?:TASK|RUNNING HANDLER) \[(.+?)\] \**`)
	statusLineRe = regexp.MustCompile(`^(ok|changed|failed|fatal|skipping): \[([^\]\s]+)\]`)
)

// statusRank orders severities so repeated tasks (serial batches) keep the
// worst outcome per host.
func statusRank(s string) int {
	switch s {
	case "failed":
		return 3
	case "changed":
		return 2
	case "ok":
		return 1
	case "skipped":
		return 0
	}
	return -1
}

// parseJobLog extracts per task -> per host -> status from an ansible
// transcript, preserving first-seen task order.
func parseJobLog(path string) (order []string, statuses map[string]map[string]string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	statuses = map[string]map[string]string{}
	cur := ""
	for _, line := range strings.Split(string(data), "\n") {
		if m := taskBannerRe.FindStringSubmatch(line); m != nil {
			cur = m[1]
			if _, seen := statuses[cur]; !seen {
				statuses[cur] = map[string]string{}
				order = append(order, cur)
			}
			continue
		}
		if cur == "" {
			continue
		}
		if m := statusLineRe.FindStringSubmatch(line); m != nil {
			st, host := m[1], m[2]
			switch st {
			case "fatal":
				st = "failed"
			case "skipping":
				st = "skipped"
			}
			if statusRank(st) > statusRank(statuses[cur][host]) || statuses[cur][host] == "" {
				statuses[cur][host] = st
			}
		}
	}
	return order, statuses, nil
}

// DiffJobs compares job bID (the reference, "b") with job aID ("a", the
// older run). Both must be terminal runs of the same playbook and repo.
func (m *Manager) DiffJobs(bID, aID string) (*JobDiff, error) {
	a, err := m.Store.GetJob(aID)
	if err != nil {
		return nil, fmt.Errorf("job %s: %w", aID, err)
	}
	b, err := m.Store.GetJob(bID)
	if err != nil {
		return nil, fmt.Errorf("job %s: %w", bID, err)
	}
	if a.RepoID != b.RepoID || a.Playbook != b.Playbook {
		return nil, fmt.Errorf("jobs must run the same playbook on the same repo")
	}
	if !a.Terminal() || !b.Terminal() {
		return nil, fmt.Errorf("both jobs must be finished")
	}

	orderA, stA, err := parseJobLog(m.Store.JobLogPath(a.ID))
	if err != nil {
		return nil, err
	}
	orderB, stB, err := parseJobLog(m.Store.JobLogPath(b.ID))
	if err != nil {
		return nil, err
	}

	out := &JobDiff{A: a, B: b, Tasks: []TaskDiff{}}
	seen := map[string]bool{}
	consider := func(task string) {
		if seen[task] {
			return
		}
		seen[task] = true
		inA, inB := stA[task], stB[task]
		switch {
		case inA == nil:
			out.Summary.NewTasks++
			out.Tasks = append(out.Tasks, TaskDiff{Name: task, OnlyIn: "b", Changes: []HostChange{}})
			return
		case inB == nil:
			out.Summary.RemovedTasks++
			out.Tasks = append(out.Tasks, TaskDiff{Name: task, OnlyIn: "a", Changes: []HostChange{}})
			return
		}
		hosts := map[string]bool{}
		for h := range inA {
			hosts[h] = true
		}
		for h := range inB {
			hosts[h] = true
		}
		var changes []HostChange
		for h := range hosts {
			sa, sb := inA[h], inB[h]
			if sa == "" {
				sa = "missing"
			}
			if sb == "" {
				sb = "missing"
			}
			if sa == sb {
				continue
			}
			changes = append(changes, HostChange{Host: h, A: sa, B: sb})
			switch {
			case sb == "failed", sa == "ok" && sb == "changed":
				out.Summary.Regressed++
			case sa == "failed" && (sb == "ok" || sb == "changed"):
				out.Summary.Improved++
			default:
				out.Summary.Changed++
			}
		}
		if len(changes) == 0 {
			out.Summary.Same++
			return
		}
		// stable host order
		for i := 0; i < len(changes); i++ {
			for j := i + 1; j < len(changes); j++ {
				if changes[j].Host < changes[i].Host {
					changes[i], changes[j] = changes[j], changes[i]
				}
			}
		}
		out.Tasks = append(out.Tasks, TaskDiff{Name: task, Changes: changes})
	}
	for _, t := range orderB {
		consider(t)
	}
	for _, t := range orderA {
		consider(t)
	}
	return out, nil
}
