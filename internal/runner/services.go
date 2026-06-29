package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jgsqware/pine/internal/model"
)

// ServicesJobName labels service-status jobs in the job list.
const ServicesJobName = "[service status]"

// ServiceCell is one service's status on one host in the report matrix.
// State is the honest tri-state running | stopped | unknown.
type ServiceCell struct {
	State  string `json:"state"`
	Unit   string `json:"unit,omitempty"`
	Status string `json:"status,omitempty"` // enabled | disabled | unknown
}

// ServiceSummary aggregates the report.
type ServiceSummary struct {
	Watched     int    `json:"watched"`      // distinct watched services
	Hosts       int    `json:"hosts"`        // hosts declaring at least one service
	Running     int    `json:"running"`      // running watched cells
	Down        int    `json:"down"`         // stopped watched cells
	HostsDown   int    `json:"hosts_down"`   // hosts with >=1 stopped service
	LastChecked string `json:"last_checked,omitempty"`
}

// ServiceReport is the repo-level service-status heatmap data: rows are the
// watched services (declared via the `services:` inventory var), columns are
// hosts, and cells carry each service's state on each host.
type ServiceReport struct {
	Inventory string                            `json:"inventory,omitempty"`
	Services  []string                          `json:"services"`
	Hosts     []string                          `json:"hosts"`
	Cells     map[string]map[string]ServiceCell `json:"cells"`
	Summary   ServiceSummary                    `json:"summary"`
	JobID     string                            `json:"job_id,omitempty"`
	Simulated bool                              `json:"simulated"`
}

// pickServiceInventory chooses the inventory to report on: the requested one,
// else the first that actually declares any `services:` (so the page works
// regardless of inventory ordering), else the first inventory.
func pickServiceInventory(res *model.ScanResult, requested string) *model.Inventory {
	if requested != "" {
		return pickInventory(res, requested)
	}
	for i := range res.Inventories {
		inv := &res.Inventories[i]
		gv := map[string]map[string]any{}
		for _, g := range inv.Groups {
			gv[g.Name] = g.Vars
		}
		for _, h := range inv.Hosts {
			if len(declaredServices(h, gv)) > 0 {
				return inv
			}
		}
	}
	if len(res.Inventories) > 0 {
		return &res.Inventories[0]
	}
	return nil
}

// CheckServices launches a job that harvests the status of every host's
// declared services (ansible -m service_facts, or a simulated equivalent).
func (m *Manager) CheckServices(repoID, inventory string) (model.Job, error) {
	return m.StartJob(model.Job{RepoID: repoID, Playbook: ServicesJobName, Inventory: inventory})
}

// runServices executes a service-status job.
func (m *Manager) runServices(ctx context.Context, job *model.Job, r *run) (failed bool) {
	res, err := m.Scan(job.RepoID)
	if err != nil {
		r.publish("ERROR: scan failed: " + err.Error())
		return true
	}
	inv := pickServiceInventory(res, job.Inventory)
	if inv == nil {
		r.publish("ERROR: no inventory found")
		return true
	}
	groupVars := map[string]map[string]any{}
	for _, g := range inv.Groups {
		groupVars[g.Name] = g.Vars
	}

	if !job.Simulated {
		return m.checkServicesReal(ctx, job, r, inv, groupVars)
	}

	// simulated: synthesize from each host's declared services so the page works
	// without ansible. Marked estimated via the job's Simulated flag.
	r.publish("$ ansible all -i " + inv.Path + " -m service_facts --tree <svc>")
	r.publish("[pine] ansible not found - synthesizing service status from inventory (estimated)")
	r.publish("")
	n := 0
	for _, h := range inv.Hosts {
		if ctx.Err() != nil {
			return false
		}
		decl := declaredServices(h, groupVars)
		if len(decl) == 0 {
			continue
		}
		svcs := make([]model.ServiceState, 0, len(decl))
		running := 0
		for _, name := range decl {
			state := model.ServiceRunning
			if n%4 == 3 { // sprinkle a few stopped services for a realistic demo
				state = model.ServiceStopped
			}
			n++
			if state == model.ServiceRunning {
				running++
			}
			svcs = append(svcs, model.ServiceState{
				Name: name, Unit: canonUnit(name), State: state, Status: "enabled",
			})
		}
		if err := m.Store.SaveHostServices(job.RepoID, h.Name, svcs); err != nil {
			r.publish("failed: [" + h.Name + "] " + err.Error())
			failed = true
			continue
		}
		r.publish(fmt.Sprintf("ok: [%s] %d/%d running", h.Name, running, len(svcs)))
		job.Summary.OK++
	}
	r.publish("")
	r.publish(fmt.Sprintf("[pine] service status stored for %d host(s)", job.Summary.OK))
	return failed
}

// checkServicesReal shells out to ansible -m service_facts and records, for
// every host, the state of each service it declares.
func (m *Manager) checkServicesReal(ctx context.Context, job *model.Job, r *run, inv *model.Inventory, groupVars map[string]map[string]any) (failed bool) {
	repo, err := m.Store.GetRepo(job.RepoID)
	if err != nil {
		r.publish("ERROR: " + err.Error())
		return true
	}
	tree, err := os.MkdirTemp("", "pine-svc-*")
	if err != nil {
		r.publish("ERROR: " + err.Error())
		return true
	}
	defer os.RemoveAll(tree)

	args := []string{"all", "-m", "service_facts", "--tree", tree}
	if job.Inventory != "" {
		args = append(args, "-i", job.Inventory)
	}
	cmd := exec.CommandContext(ctx, "ansible", args...)
	cmd.Dir = m.Store.RepoWorkdir(&repo)
	cmd.Env = append(os.Environ(), "ANSIBLE_NOCOLOR=1")
	out, _ := cmd.CombinedOutput()
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		r.publish(line)
	}

	declByHost := map[string][]string{}
	for _, h := range inv.Hosts {
		if d := declaredServices(h, groupVars); len(d) > 0 {
			declByHost[h.Name] = d
		}
	}

	entries, err := os.ReadDir(tree)
	if err != nil || len(entries) == 0 {
		r.publish("ERROR: no service facts gathered")
		return true
	}
	for _, e := range entries {
		host := e.Name()
		decl := declByHost[host]
		if len(decl) == 0 {
			continue // only record hosts that declare services
		}
		data, err := os.ReadFile(filepath.Join(tree, host))
		if err != nil {
			continue
		}
		var doc struct {
			AnsibleFacts struct {
				Services map[string]struct {
					Name   string `json:"name"`
					State  string `json:"state"`
					Status string `json:"status"`
				} `json:"services"`
			} `json:"ansible_facts"`
		}
		if json.Unmarshal(data, &doc) != nil {
			continue
		}
		// index reported units by canonical name (teamcity-agent.service -> teamcity-agent)
		idx := map[string]model.ServiceState{}
		for unit, sv := range doc.AnsibleFacts.Services {
			st := model.ServiceState{Unit: unit, State: normState(sv.State), Status: normStatus(sv.Status)}
			if sv.Name != "" {
				st.Unit = sv.Name
			}
			idx[canonService(unit)] = st
			if sv.Name != "" {
				idx[canonService(sv.Name)] = st
			}
		}
		svcs := make([]model.ServiceState, 0, len(decl))
		for _, name := range decl {
			// a declared service that service_facts did not report is not running
			cell := model.ServiceState{Name: name, Unit: canonUnit(name), State: model.ServiceStopped, Status: "unknown"}
			if got, ok := idx[canonService(name)]; ok {
				cell.State, cell.Status, cell.Unit = got.State, got.Status, got.Unit
			}
			svcs = append(svcs, cell)
		}
		if m.Store.SaveHostServices(job.RepoID, host, svcs) == nil {
			job.Summary.OK++
		}
	}
	r.publish(fmt.Sprintf("[pine] service status stored for %d host(s)", job.Summary.OK))
	return job.Summary.OK == 0
}

// ServiceStatus assembles the hosts × services matrix from the declared
// `services:` vars and the most recent harvested states.
func (m *Manager) ServiceStatus(repoID, inventory string) (*ServiceReport, error) {
	res, err := m.Scan(repoID)
	if err != nil {
		return nil, err
	}
	rep := &ServiceReport{Services: []string{}, Hosts: []string{}, Cells: map[string]map[string]ServiceCell{}}
	inv := pickServiceInventory(res, inventory)
	if inv == nil {
		return rep, nil
	}
	rep.Inventory = inv.Name
	groupVars := map[string]map[string]any{}
	for _, g := range inv.Groups {
		groupVars[g.Name] = g.Vars
	}

	// newest service-status job carries the check time / estimated flag / link
	for _, j := range m.Store.ListJobs() { // newest first
		if j.RepoID == repoID && j.Playbook == ServicesJobName && j.Terminal() {
			rep.JobID, rep.Simulated, rep.Summary.LastChecked = j.ID, j.Simulated, j.Finished
			break
		}
	}

	svcSet := map[string]bool{}
	hostSet := map[string]bool{}
	hostsDown := map[string]bool{}
	for _, h := range inv.Hosts {
		decl := declaredServices(h, groupVars)
		if len(decl) == 0 {
			continue
		}
		hostSet[h.Name] = true
		stored, _ := m.Store.HostServices(repoID, h.Name)
		byName := map[string]model.ServiceState{}
		for _, s := range stored {
			byName[s.Name] = s
		}
		for _, name := range decl {
			svcSet[name] = true
			cell := ServiceCell{State: model.ServiceUnknown}
			if s, ok := byName[name]; ok {
				cell.State, cell.Unit, cell.Status = s.State, s.Unit, s.Status
			}
			if rep.Cells[name] == nil {
				rep.Cells[name] = map[string]ServiceCell{}
			}
			rep.Cells[name][h.Name] = cell
			switch cell.State {
			case model.ServiceRunning:
				rep.Summary.Running++
			case model.ServiceStopped:
				rep.Summary.Down++
				hostsDown[h.Name] = true
			}
		}
	}
	for s := range svcSet {
		rep.Services = append(rep.Services, s)
	}
	for h := range hostSet {
		rep.Hosts = append(rep.Hosts, h)
	}
	sort.Strings(rep.Services)
	sort.Strings(rep.Hosts)
	rep.Summary.Watched = len(rep.Services)
	rep.Summary.Hosts = len(rep.Hosts)
	rep.Summary.HostsDown = len(hostsDown)
	return rep, nil
}

// declaredServices returns the services a host watches: its own `services:`
// var, else the first of its groups that declares one.
func declaredServices(h model.Host, groupVars map[string]map[string]any) []string {
	if v := coerceStrings(h.Vars["services"]); len(v) > 0 {
		return v
	}
	for _, g := range h.Groups {
		if gv := groupVars[g]; gv != nil {
			if v := coerceStrings(gv["services"]); len(v) > 0 {
				return v
			}
		}
	}
	return nil
}

func coerceStrings(v any) []string {
	switch t := v.(type) {
	case []any:
		out := []string{}
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		if t != "" {
			return []string{t}
		}
	}
	return nil
}

// canonService is the comparison key for matching a declared name to a reported
// systemd unit: lower-cased, without the .service suffix.
func canonService(n string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(n)), ".service")
}

// canonUnit renders a declared name as a systemd unit for display.
func canonUnit(n string) string {
	n = strings.TrimSpace(n)
	if strings.Contains(n, ".") {
		return n
	}
	return n + ".service"
}

func normState(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "running":
		return model.ServiceRunning
	case "stopped", "dead", "inactive", "failed", "exited":
		return model.ServiceStopped
	default:
		return model.ServiceUnknown
	}
}

func normStatus(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "":
		return "unknown"
	case strings.HasPrefix(s, "enabled"):
		return "enabled"
	case strings.HasPrefix(s, "disabled"):
		return "disabled"
	default:
		return s
	}
}
