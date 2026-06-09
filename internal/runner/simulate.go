package runner

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand"
	"strings"
	"time"

	"github.com/jgsqware/pine/internal/model"
)

// simulate produces a realistic ansible-playbook transcript from scanned
// data. Used when ansible isn't installed (demo mode, ephemeral envs).
func (m *Manager) simulate(ctx context.Context, job *model.Job, r *run) (failed bool) {
	res, err := m.Scan(job.RepoID)
	if err != nil {
		r.publish("ERROR: scan failed: " + err.Error())
		return true
	}

	args := []string{job.Playbook}
	if job.Inventory != "" {
		args = append(args, "-i", job.Inventory)
	}
	if job.Limit != "" {
		args = append(args, "--limit", job.Limit)
	}
	if job.Tags != "" {
		args = append(args, "--tags", job.Tags)
	}
	if job.Check {
		args = append(args, "--check")
	}
	r.publish("$ ansible-playbook " + strings.Join(args, " "))
	r.publish("[pine] ansible-playbook not found on this host - running in simulation mode")
	r.publish("")

	inv := pickInventory(res, job.Inventory)
	roleTasks := map[string][]model.Task{}
	roleHandlers := map[string][]model.Task{}
	for _, role := range res.Roles {
		roleTasks[role.Name] = role.Tasks
		roleHandlers[role.Name] = role.Handlers
	}

	// deterministic per-job randomness so reruns look organic but stable
	h := fnv.New64a()
	h.Write([]byte(job.ID))
	rng := rand.New(rand.NewSource(int64(h.Sum64())))

	sim := &simState{
		mgr: m, job: job, run: r, rng: rng, ctx: ctx,
		roleTasks: roleTasks, roleHandlers: roleHandlers,
		perHost: map[string]*model.JobSummary{},
	}

	pb := findPlaybook(res, job.Playbook)
	if pb == nil {
		r.publish("ERROR! the playbook: " + job.Playbook + " could not be found")
		return true
	}
	if !sim.playbook(res, pb, inv, 0) {
		return ctx.Err() == nil && sim.failed
	}

	// PLAY RECAP
	r.publish("")
	r.publish("PLAY RECAP " + strings.Repeat("*", 69))
	for _, host := range sim.hostOrder {
		s := sim.perHost[host]
		r.publish(fmt.Sprintf("%-26s : ok=%-4d changed=%-4d unreachable=%-4d failed=%-4d skipped=%-4d rescued=0    ignored=0",
			host, s.OK, s.Changed, s.Unreachable, s.Failed, s.Skipped))
		job.Summary.OK += s.OK
		job.Summary.Changed += s.Changed
		job.Summary.Failed += s.Failed
		job.Summary.Skipped += s.Skipped
		job.Summary.Unreachable += s.Unreachable
	}
	return sim.failed
}

type simState struct {
	mgr          *Manager
	job          *model.Job
	run          *run
	rng          *rand.Rand
	ctx          context.Context
	roleTasks    map[string][]model.Task
	roleHandlers map[string][]model.Task
	perHost      map[string]*model.JobSummary
	hostOrder    []string
	failed       bool
}

func (s *simState) canceled() bool { return s.ctx.Err() != nil }

func (s *simState) sleep(ms int) {
	select {
	case <-s.ctx.Done():
	case <-time.After(time.Duration(ms) * time.Millisecond):
	}
}

func (s *simState) host(name string) *model.JobSummary {
	if h, ok := s.perHost[name]; ok {
		return h
	}
	h := &model.JobSummary{}
	s.perHost[name] = h
	s.hostOrder = append(s.hostOrder, name)
	return h
}

func pickInventory(res *model.ScanResult, requested string) *model.Inventory {
	for i := range res.Inventories {
		inv := &res.Inventories[i]
		if requested == "" || inv.Path == requested || inv.Name == requested ||
			strings.Contains(requested, inv.Name) {
			return inv
		}
	}
	if len(res.Inventories) > 0 {
		return &res.Inventories[0]
	}
	return nil
}

func findPlaybook(res *model.ScanResult, path string) *model.Playbook {
	for i := range res.Playbooks {
		if res.Playbooks[i].Path == path || res.Playbooks[i].Name == path {
			return &res.Playbooks[i]
		}
	}
	return nil
}

// playbook simulates all plays; returns false when canceled. depth guards
// against import_playbook cycles.
func (s *simState) playbook(res *model.ScanResult, pb *model.Playbook, inv *model.Inventory, depth int) bool {
	if depth > 5 {
		return true
	}
	for _, play := range pb.Plays {
		if s.canceled() {
			return false
		}
		if play.Import != "" {
			if imported := findPlaybook(res, play.Import); imported != nil {
				if !s.playbook(res, imported, inv, depth+1) {
					return false
				}
			}
			continue
		}
		s.play(play, inv)
	}
	return true
}

func (s *simState) play(play model.Play, inv *model.Inventory) {
	title := play.Name
	if title == "" {
		title = play.Hosts
	}
	banner := fmt.Sprintf("PLAY [%s] ", title)
	s.run.publish(banner + strings.Repeat("*", max(8, 80-len(banner))))
	s.run.publish("")

	hosts := []string{"localhost"}
	if inv != nil {
		if got := matchHosts(play.Hosts, inv); len(got) > 0 {
			hosts = got
		}
	}
	if s.job.Limit != "" && inv != nil {
		if got := matchHosts(s.job.Limit, inv); len(got) > 0 {
			limited := hosts[:0:0]
			for _, h := range hosts {
				for _, l := range got {
					if h == l {
						limited = append(limited, h)
					}
				}
			}
			if len(limited) > 0 {
				hosts = limited
			}
		}
	}

	// batches for serial: N
	batches := [][]string{hosts}
	if play.Serial != "" {
		if n := atoiSafe(play.Serial); n > 0 && n < len(hosts) {
			batches = nil
			for i := 0; i < len(hosts); i += n {
				end := min(i+n, len(hosts))
				batches = append(batches, hosts[i:end])
			}
		}
	}

	notified := map[string]bool{}
	for _, batch := range batches {
		if s.canceled() {
			return
		}
		s.task(model.Task{Name: "Gathering Facts", Module: "gather_facts"}, batch, "", notified, true)
		s.taskList(play.PreTasks, batch, "", notified)
		for _, role := range play.Roles {
			s.taskList(s.roleTasks[role], batch, role, notified)
		}
		s.taskList(play.Tasks, batch, "", notified)
		s.taskList(play.PostTasks, batch, "", notified)

		// flush handlers
		if len(notified) > 0 {
			all := append([]model.Task{}, play.Handlers...)
			for _, role := range play.Roles {
				all = append(all, s.roleHandlers[role]...)
			}
			for _, hd := range all {
				if notified[hd.Name] {
					s.run.publish(fmt.Sprintf("RUNNING HANDLER [%s] %s", hd.Name, strings.Repeat("*", max(8, 61-len(hd.Name)))))
					for _, host := range batch {
						s.host(host).Changed++
						s.host(host).OK++
						s.run.publish(fmt.Sprintf("changed: [%s]", host))
					}
					s.run.publish("")
				}
			}
			notified = map[string]bool{}
		}
	}
}

func (s *simState) taskList(tasks []model.Task, hosts []string, role string, notified map[string]bool) {
	for _, t := range tasks {
		if s.canceled() {
			return
		}
		if t.Module == "block" {
			s.taskList(t.Block, hosts, role, notified)
			s.taskList(t.Always, hosts, role, notified)
			continue
		}
		// --tags filtering (simple: skip tasks lacking a requested tag)
		if s.job.Tags != "" && !hasAnyTag(t.Tags, s.job.Tags) {
			continue
		}
		s.task(t, hosts, role, notified, false)
	}
}

func hasAnyTag(tags []string, requested string) bool {
	for _, want := range strings.Split(requested, ",") {
		want = strings.TrimSpace(want)
		if want == "all" {
			return true
		}
		for _, t := range tags {
			if t == want || t == "always" {
				return true
			}
		}
	}
	return false
}

func (s *simState) task(t model.Task, hosts []string, role string, notified map[string]bool, factsOK bool) {
	label := t.Name
	if role != "" {
		label = role + " : " + t.Name
	}
	banner := fmt.Sprintf("TASK [%s] ", label)
	s.run.publish(banner + strings.Repeat("*", max(8, 80-len(banner))))

	loopItems := 1
	if t.Loop {
		loopItems = 2 + s.rng.Intn(3)
	}
	for _, host := range hosts {
		sum := s.host(host)
		for i := 0; i < loopItems; i++ {
			suffix := ""
			if t.Loop {
				suffix = fmt.Sprintf(" => (item=item%d)", i+1)
			}
			roll := s.rng.Float64()
			switch {
			case factsOK:
				sum.OK++
				s.run.publish(fmt.Sprintf("ok: [%s]", host))
			case t.When != "" && roll < 0.22:
				sum.Skipped++
				s.run.publish(fmt.Sprintf("skipping: [%s]%s", host, suffix))
			case changeable(t.Module) && roll < 0.45 && !s.job.Check:
				sum.Changed++
				sum.OK++
				s.run.publish(fmt.Sprintf("changed: [%s]%s", host, suffix))
				for _, n := range t.Notify {
					notified[n] = true
				}
			case changeable(t.Module) && s.job.Check && roll < 0.45:
				sum.Changed++
				sum.OK++
				s.run.publish(fmt.Sprintf("changed: [%s]%s  (check mode)", host, suffix))
			default:
				sum.OK++
				s.run.publish(fmt.Sprintf("ok: [%s]%s", host, suffix))
			}
			if factsOK {
				break
			}
		}
	}
	s.run.publish("")
	s.sleep(60 + s.rng.Intn(140))
}

// changeable: modules that plausibly report "changed"
func changeable(module string) bool {
	for _, frag := range []string{
		"copy", "template", "file", "lineinfile", "blockinfile", "apt", "dnf",
		"yum", "package", "pip", "service", "systemd", "user", "group", "git",
		"docker", "compose", "command", "shell", "unarchive", "sysctl", "cron",
		"firewalld", "ufw", "postgresql", "mysql", "uri", "authorized_key",
	} {
		if strings.Contains(module, frag) {
			return true
		}
	}
	return false
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
