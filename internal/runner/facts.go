package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jgsqware/pine/internal/ansible"
	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
)

// FactsJobName labels fact-gathering jobs in the job list.
const FactsJobName = "[gather facts]"

// GatherFacts launches a job that collects ansible facts for every host of
// the inventory (ansible -m setup --tree, or a simulated equivalent) and
// stores them for plan mode.
func (m *Manager) GatherFacts(repoID, inventory string) (model.Job, error) {
	return m.StartJob(model.Job{RepoID: repoID, Playbook: FactsJobName, Inventory: inventory})
}

// runGather executes a fact-gathering job.
func (m *Manager) runGather(ctx context.Context, job *model.Job, r *run) (failed bool) {
	res, err := m.Scan(job.RepoID)
	if err != nil {
		r.publish("ERROR: scan failed: " + err.Error())
		return true
	}
	inv := pickInventory(res, job.Inventory)
	if inv == nil {
		r.publish("ERROR: no inventory found")
		return true
	}

	if !job.Simulated {
		return m.gatherReal(ctx, job, r, inv)
	}

	// simulated: synthesize facts from a baseline profile + inventory vars,
	// so plans get per-host facts even without ansible installed
	r.publish("$ ansible all -i " + inv.Path + " -m setup --tree <facts>")
	r.publish("[pine] ansible not found - synthesizing facts from inventory data")
	r.publish("")
	base := plan.ProfileByID("ubuntu-24.04")
	for _, h := range inv.Hosts {
		if ctx.Err() != nil {
			return false
		}
		facts := map[string]any{}
		if af, ok := base["ansible_facts"].(map[string]any); ok {
			for k, v := range af {
				facts[k] = v
			}
		}
		facts["hostname"] = h.Name
		facts["fqdn"] = h.Name + ".acme.internal"
		if v, ok := h.Vars["ansible_host"]; ok {
			facts["default_ipv4_address"] = v
		}
		if err := m.Store.SaveHostFacts(job.RepoID, h.Name, facts); err != nil {
			r.publish("failed: [" + h.Name + "] " + err.Error())
			failed = true
			continue
		}
		r.publish("ok: [" + h.Name + "]")
		job.Summary.OK++
	}
	r.publish("")
	r.publish(fmt.Sprintf("[pine] facts stored for %d host(s)", job.Summary.OK))
	return failed
}

// gatherReal shells out to ansible -m setup --tree and imports the result.
func (m *Manager) gatherReal(ctx context.Context, job *model.Job, r *run, inv *model.Inventory) (failed bool) {
	repo, err := m.Store.GetRepo(job.RepoID)
	if err != nil {
		r.publish("ERROR: " + err.Error())
		return true
	}
	tree, err := os.MkdirTemp("", "pine-facts-*")
	if err != nil {
		r.publish("ERROR: " + err.Error())
		return true
	}
	defer os.RemoveAll(tree)

	execCtx := ansible.Resolve(m.Store.RepoWorkdir(&repo), "", job.Inventory)
	args := []string{"all", "-m", "setup", "--tree", tree}
	if execCtx.Inventory != "" {
		args = append(args, "-i", execCtx.Inventory)
	}
	cmd := exec.CommandContext(ctx, ansible.Bin("ansible"), args...)
	cmd.Dir = execCtx.Dir
	cmd.Env = append(ansible.Env(), "ANSIBLE_NOCOLOR=1")
	out, _ := cmd.CombinedOutput()
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		r.publish(line)
	}

	entries, err := os.ReadDir(tree)
	if err != nil || len(entries) == 0 {
		r.publish("ERROR: no facts gathered")
		return true
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(tree, e.Name()))
		if err != nil {
			continue
		}
		var doc struct {
			AnsibleFacts map[string]any `json:"ansible_facts"`
		}
		if json.Unmarshal(data, &doc) != nil || doc.AnsibleFacts == nil {
			continue
		}
		facts := map[string]any{}
		for k, v := range doc.AnsibleFacts {
			facts[strings.TrimPrefix(k, "ansible_")] = v
		}
		if m.Store.SaveHostFacts(job.RepoID, e.Name(), facts) == nil {
			job.Summary.OK++
		}
	}
	r.publish(fmt.Sprintf("[pine] facts stored for %d host(s)", job.Summary.OK))
	return job.Summary.OK == 0
}
