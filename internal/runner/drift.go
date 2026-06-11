package runner

import (
	"fmt"
	"sort"

	"github.com/jgsqware/pine/internal/model"
)

// DriftHost is the drift observed on one host by one check run.
type DriftHost struct {
	Changed int      `json:"changed"`
	Failed  int      `json:"failed"`
	Tasks   []string `json:"tasks"`
}

// DriftPlaybook is the latest check-mode result of one playbook.
type DriftPlaybook struct {
	Playbook string               `json:"playbook"`
	JobID    string               `json:"job_id"`
	Finished string               `json:"finished"`
	Hosts    map[string]DriftHost `json:"hosts"`
}

// DriftSummary aggregates the report.
type DriftSummary struct {
	CheckedPlaybooks int    `json:"checked_playbooks"`
	HostsWithDrift   int    `json:"hosts_with_drift"`
	TotalChanged     int    `json:"total_changed"`
	LastChecked      string `json:"last_checked,omitempty"`
}

// DriftReport is the repo-level drift heatmap data.
type DriftReport struct {
	Playbooks []DriftPlaybook `json:"playbooks"`
	Hosts     []string        `json:"hosts"`
	Summary   DriftSummary    `json:"summary"`
}

// Drift builds the report from the most recent terminal --check job of
// each playbook: a task reporting "changed" under --check means reality
// diverges from the repo.
func (m *Manager) Drift(repoID string) (*DriftReport, error) {
	out := &DriftReport{Playbooks: []DriftPlaybook{}, Hosts: []string{}}
	latest := map[string]model.Job{}
	for _, j := range m.Store.ListJobs() { // newest first
		if j.RepoID != repoID || !j.Check || !j.Terminal() || j.Playbook == FactsJobName {
			continue
		}
		if _, seen := latest[j.Playbook]; !seen {
			latest[j.Playbook] = j
		}
	}

	var names []string
	for n := range latest {
		names = append(names, n)
	}
	sort.Strings(names)

	hostSet := map[string]bool{}
	drifted := map[string]bool{}
	for _, name := range names {
		j := latest[name]
		_, statuses, err := parseJobLog(m.Store.JobLogPath(j.ID))
		if err != nil {
			continue
		}
		dp := DriftPlaybook{Playbook: name, JobID: j.ID, Finished: j.Finished, Hosts: map[string]DriftHost{}}
		for task, hosts := range statuses {
			for host, st := range hosts {
				hostSet[host] = true
				dh := dp.Hosts[host]
				if dh.Tasks == nil {
					dh.Tasks = []string{}
				}
				switch st {
				case "changed":
					dh.Changed++
					dh.Tasks = append(dh.Tasks, task)
					drifted[host] = true
					out.Summary.TotalChanged++
				case "failed":
					dh.Failed++
					dh.Tasks = append(dh.Tasks, task+" (failed)")
				}
				dp.Hosts[host] = dh
			}
		}
		for h := range dp.Hosts {
			sort.Strings(dp.Hosts[h].Tasks)
		}
		if j.Finished > out.Summary.LastChecked {
			out.Summary.LastChecked = j.Finished
		}
		out.Playbooks = append(out.Playbooks, dp)
	}

	for h := range hostSet {
		out.Hosts = append(out.Hosts, h)
	}
	sort.Strings(out.Hosts)
	out.Summary.CheckedPlaybooks = len(out.Playbooks)
	out.Summary.HostsWithDrift = len(drifted)
	return out, nil
}

// DriftCheck launches --check jobs for the given playbooks (all scanned
// playbooks when the list is empty), against the given inventory (the
// first one when empty).
func (m *Manager) DriftCheck(repoID string, playbooks []string, inventory string) ([]model.Job, error) {
	res, err := m.Scan(repoID)
	if err != nil {
		return nil, err
	}
	if len(playbooks) == 0 {
		for _, pb := range res.Playbooks {
			playbooks = append(playbooks, pb.Path)
		}
	}
	if inventory == "" && len(res.Inventories) > 0 {
		inventory = res.Inventories[0].Path
	}
	if len(playbooks) == 0 {
		return nil, fmt.Errorf("no playbooks to check")
	}
	jobs := make([]model.Job, 0, len(playbooks))
	for _, pb := range playbooks {
		j, err := m.StartJob(model.Job{RepoID: repoID, Playbook: pb, Inventory: inventory, Check: true})
		if err != nil {
			return jobs, err
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}
