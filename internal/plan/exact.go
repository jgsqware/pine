package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jgsqware/pine/internal/ansible"
	"github.com/jgsqware/pine/internal/model"
)

// ansibleJSON mirrors the relevant parts of ansible's `json` stdout
// callback output.
type ansibleJSON struct {
	Plays []struct {
		Play struct {
			Name string `json:"name"`
		} `json:"play"`
		Tasks []struct {
			Task struct {
				Name string `json:"name"`
			} `json:"task"`
			Hosts map[string]struct {
				Changed bool   `json:"changed"`
				Skipped bool   `json:"skipped"`
				Failed  bool   `json:"failed"`
				Msg     any    `json:"msg"`
				Action  string `json:"action"`
			} `json:"hosts"`
		} `json:"tasks"`
	} `json:"plays"`
}

// ComputeExact runs `ansible-playbook --check` with the JSON callback and
// renders the result in the plan format, labeled mode: "exact".
func ComputeExact(root string, repo model.Repo, req Request) (*Result, error) {
	bin, ok := ansible.LookPath("ansible-playbook")
	if !ok {
		return nil, fmt.Errorf("ansible-playbook not found on this host - exact mode needs ansible installed (use estimated mode instead)")
	}
	execCtx := ansible.Resolve(root, req.Playbook, req.Inventory)
	args := []string{execCtx.Playbook, "--check"}
	if execCtx.Inventory != "" {
		args = append(args, "-i", execCtx.Inventory)
	}
	if req.Limit != "" {
		args = append(args, "--limit", req.Limit)
	}
	if req.Tags != "" {
		args = append(args, "--tags", req.Tags)
	}
	for k, v := range req.Vars {
		data, _ := json.Marshal(map[string]any{k: v})
		args = append(args, "-e", string(data))
	}
	if req.VaultPassword != "" {
		if pwf, err := os.CreateTemp("", "pine-vault-pw-*"); err == nil {
			_, _ = pwf.WriteString(req.VaultPassword)
			pwf.Close()
			defer os.Remove(pwf.Name())
			args = append(args, "--vault-password-file", pwf.Name())
		}
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = execCtx.Dir
	cmd.Env = append(ansible.Env(),
		"ANSIBLE_STDOUT_CALLBACK=json", "ANSIBLE_NOCOLOR=1", "ANSIBLE_FORCE_COLOR=0")
	switch repo.HostKeyChecking {
	case "disabled":
		cmd.Env = append(cmd.Env, "ANSIBLE_HOST_KEY_CHECKING=False")
	case "accept-new":
		cmd.Env = append(cmd.Env, "ANSIBLE_SSH_EXTRA_ARGS=-o StrictHostKeyChecking=accept-new")
	}
	out, runErr := cmd.Output() // check failures still produce JSON
	res, err := parseAnsibleJSON(out)
	if err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("ansible-playbook --check failed: %v", runErr)
		}
		return nil, err
	}
	res.RepoID, res.RepoName = repo.ID, repo.Name
	res.Playbook, res.Inventory = req.Playbook, req.Inventory
	res.Check = true
	return res, nil
}

// parseAnsibleJSON converts the json-callback document into a plan Result.
func parseAnsibleJSON(data []byte) (*Result, error) {
	start := strings.Index(string(data), "{")
	if start < 0 {
		return nil, fmt.Errorf("no JSON in ansible output")
	}
	var doc ansibleJSON
	if err := json.Unmarshal(data[start:], &doc); err != nil {
		return nil, fmt.Errorf("cannot parse ansible JSON callback output: %w", err)
	}
	out := &Result{Mode: "exact", Plays: []PlayPlan{}}
	hosts := map[string]bool{}
	for _, p := range doc.Plays {
		pp := PlayPlan{Name: p.Play.Name, MatchedHosts: []string{}, Batches: [][]string{}, Tasks: []TaskPlan{}}
		seen := map[string]bool{}
		for _, t := range p.Tasks {
			tp := TaskPlan{
				Name: t.Task.Name, Section: "tasks",
				Hosts: map[string]HostVerdict{},
			}
			for hn, hr := range t.Hosts {
				hosts[hn] = true
				if !seen[hn] {
					seen[hn] = true
					pp.MatchedHosts = append(pp.MatchedHosts, hn)
				}
				if tp.Module == "" && hr.Action != "" {
					tp.Module = hr.Action
				}
				v := HostVerdict{Status: StatusRun, Reason: "no change"}
				switch {
				case hr.Failed:
					v = HostVerdict{Status: StatusUnknown, Reason: "failed: " + toMsg(hr.Msg)}
				case hr.Skipped:
					v = HostVerdict{Status: StatusSkip, Reason: "skipped"}
				case hr.Changed:
					v = HostVerdict{Status: StatusRun, Reason: "would change"}
				}
				tp.Hosts[hn] = v
				switch v.Status {
				case StatusRun:
					tp.Counts.Run++
				case StatusSkip:
					tp.Counts.Skip++
				default:
					tp.Counts.Unknown++
				}
			}
			out.Summary.Tasks++
			out.Summary.Run += tp.Counts.Run
			out.Summary.Skip += tp.Counts.Skip
			out.Summary.Unknown += tp.Counts.Unknown
			pp.Tasks = append(pp.Tasks, tp)
		}
		if len(pp.MatchedHosts) > 0 {
			pp.Batches = append(pp.Batches, pp.MatchedHosts)
		}
		out.Plays = append(out.Plays, pp)
	}
	out.Summary.Hosts = len(hosts)
	out.Summary.MissingVars = []MissingVar{}
	return out, nil
}

func toMsg(v any) string {
	if s, ok := v.(string); ok {
		if len(s) > 120 {
			return s[:120] + "…"
		}
		return s
	}
	return ""
}
