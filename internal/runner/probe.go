package runner

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/jgsqware/pine/internal/ansible"
	"github.com/jgsqware/pine/internal/model"
)

// A Probe is a vetted, read-only observation Pine can run against hosts so an
// operator can answer "what is this box doing?" without opening an SSH session.
//
// The module and its arguments are compile-time constants. A request names a
// probe by ID and never carries a command string, so "this cannot modify a
// host" is a property of this catalog rather than a guess about text somebody
// typed. That distinction is the whole design:
//
//   - Filtering a shell string is unenforceable: `uptime; rm -rf /`, `$(...)`,
//     backticks and `&&` all reach /bin/sh through `-m shell`.
//   - Allowlisting the binary and running argv (no shell) closes that, but the
//     binary itself is still an exec vector — `find -exec`, `awk 'BEGIN{system()}'`,
//     `tar --to-command`, `sed -i`. See GTFOBins.
//   - Even a truly read-only command need not be safe here: `cat /etc/shadow`
//     copies host secrets into Pine's job log.
//
// So: no free-form command crosses the wire. Adding a probe is a code change,
// reviewed once, and deliberately excludes arbitrary file reads and log tailing.
type Probe struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Desc   string `json:"description"`
	Module string `json:"module"`
	Args   string `json:"args,omitempty"`

	// Sim is the canned per-host output used when ansible is unavailable.
	Sim string `json:"-"`
}

// probes is the catalog. Every entry must be read-only on the target host and
// must not stream secrets back into the job log.
//
// Note on `command` probes: ansible's command module always reports CHANGED,
// because it cannot know whether the binary it ran mutated anything. That is a
// property of the module, not evidence that the probe wrote to the host.
var probes = []Probe{
	{
		ID: "ping", Title: "Ping", Module: "ping",
		Desc: "Check Pine can reach the host over SSH and run Python on it.",
		Sim:  `{"changed": false, "ping": "pong"}`,
	},
	{
		ID: "uptime", Title: "Uptime & load", Module: "command", Args: "uptime",
		Desc: "How long the host has been up, and its 1/5/15-minute load average.",
		Sim:  ` 09:41:02 up 12 days,  3:17,  1 user,  load average: 0.08, 0.12, 0.09`,
	},
	{
		ID: "disk", Title: "Disk usage", Module: "setup", Args: "filter=ansible_mounts",
		Desc: "Mounted filesystems with their size and available space.",
		Sim:  `{"ansible_facts": {"ansible_mounts": [{"mount": "/", "size_total": 42010000000, "size_available": 28104000000}]}}`,
	},
	{
		ID: "memory", Title: "Memory", Module: "setup", Args: "filter=ansible_memory_mb",
		Desc: "Total, used and free memory, plus swap.",
		Sim:  `{"ansible_facts": {"ansible_memory_mb": {"real": {"total": 3936, "used": 1204, "free": 2732}}}}`,
	},
	{
		ID: "packages", Title: "Installed packages", Module: "package_facts",
		Desc: "Every package the host's package manager reports as installed.",
		Sim:  `{"ansible_facts": {"packages": {"openssh-server": [{"version": "1:9.6p1-3"}]}}}`,
	},
	{
		ID: "services", Title: "Service status", Module: "service_facts",
		Desc: "State and enablement of every init-managed service on the host.",
		Sim:  `{"ansible_facts": {"services": {"nginx.service": {"state": "running", "status": "enabled"}}}}`,
	},
	{
		ID: "users", Title: "Local users", Module: "getent", Args: "database=passwd",
		Desc: "Local user accounts from /etc/passwd (names, UIDs, shells — never hashes).",
		Sim:  `{"ansible_facts": {"getent_passwd": {"root": ["x", "0", "0", "root", "/root", "/bin/bash"]}}}`,
	},
	{
		ID: "ports", Title: "Listening ports", Module: "command", Args: "ss -tuln",
		Desc: "TCP/UDP sockets in LISTEN state. Omits the PID column so no privilege escalation is needed.",
		Sim: "Netid State  Recv-Q Send-Q Local Address:Port Peer Address:Port\n" +
			"tcp   LISTEN 0      4096         0.0.0.0:22        0.0.0.0:*",
	},
}

// Probes returns the probe catalog.
func Probes() []Probe {
	out := make([]Probe, len(probes))
	copy(out, probes)
	return out
}

// ProbeByID looks a probe up by its ID.
func ProbeByID(id string) (Probe, bool) {
	for _, p := range probes {
		if p.ID == id {
			return p, true
		}
	}
	return Probe{}, false
}

// ProbeJobLabel names a probe job in the job list, matching the bracketed
// convention of FactsJobName and ServicesJobName.
func ProbeJobLabel(id string) string { return "[probe: " + id + "]" }

// hostPatternRe guards the one piece of a probe invocation the caller controls.
// The pattern is passed as argv (so there is no shell to inject into), but it
// is ansible's *first positional argument*: a value like "--become" would be
// consumed as a flag instead. Hence a strict allowlist that cannot start with
// a dash. The body covers real ansible patterns: web:db, web:!web01,
// web:&staging, web[0:5], web*.
var hostPatternRe = regexp.MustCompile(`^[A-Za-z0-9_*~][A-Za-z0-9_.:,!&*\[\]+-]{0,255}$`)

// validHostPattern reports whether s is safe to pass as ansible's host pattern.
func validHostPattern(s string) bool { return hostPatternRe.MatchString(s) }

// RunProbe launches a read-only probe against the hosts matched by limit
// (default "all"). Unknown probe IDs and unsafe host patterns are rejected
// before any process is spawned.
func (m *Manager) RunProbe(repoID, probeID, inventory, limit string) (model.Job, error) {
	p, ok := ProbeByID(probeID)
	if !ok {
		return model.Job{}, fmt.Errorf("unknown probe %q", probeID)
	}
	if limit != "" && !validHostPattern(limit) {
		return model.Job{}, fmt.Errorf("invalid host pattern %q", limit)
	}
	if strings.HasPrefix(inventory, "-") {
		return model.Job{}, fmt.Errorf("invalid inventory %q", inventory)
	}
	return m.StartJob(model.Job{
		RepoID:    repoID,
		Playbook:  ProbeJobLabel(p.ID),
		Probe:     p.ID,
		Inventory: inventory,
		Limit:     limit,
	})
}

// adhocRe matches ansible's ad-hoc per-host status lines, e.g.
// "web01 | SUCCESS | rc=0 >>" or "web02 | UNREACHABLE! => {...}".
var adhocRe = regexp.MustCompile(`^(\S+) \| (SUCCESS|CHANGED|FAILED|UNREACHABLE|SKIPPED)`)

// noBecome forces privilege escalation off. Simply not passing -b is not
// enough: a repo's ansible.cfg (`become = True`) or an inventory var
// (`ansible_become: true`) would otherwise silently run every probe under
// sudo. Extra vars outrank both, so this is the one reliable override — and it
// is why no probe in the catalog needs root.
var noBecome = []string{"-e", `{"ansible_become": false}`}

// quoteArgs renders argv the way a shell would accept it, so the command
// echoed into the job log is the command that actually ran. A plain join would
// print `-a ss -tuln` and `-e {"ansible_become": false}`, both of which mean
// something different when pasted into a terminal.
func quoteArgs(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n\"'{}$&|;<>()*?!`\\[]") {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			out[i] = a
		}
	}
	return strings.Join(out, " ")
}

// probeArgs builds the argv for a probe. Only pattern and inventory come from
// the caller, and both are validated in RunProbe.
func probeArgs(p Probe, pattern, inventory string) []string {
	args := []string{pattern, "-m", p.Module}
	if p.Args != "" {
		args = append(args, "-a", p.Args)
	}
	if inventory != "" {
		args = append(args, "-i", inventory)
	}
	return append(args, noBecome...)
}

// runProbe executes a probe job.
func (m *Manager) runProbe(ctx context.Context, job *model.Job, r *run) (failed bool) {
	p, ok := ProbeByID(job.Probe)
	if !ok {
		r.publish("ERROR: unknown probe " + job.Probe)
		return true
	}
	pattern := job.Limit
	if pattern == "" {
		pattern = "all"
	}
	if !validHostPattern(pattern) {
		r.publish("ERROR: invalid host pattern " + pattern)
		return true
	}
	if job.Simulated {
		return m.probeSim(ctx, job, r, p, probeArgs(p, pattern, job.Inventory))
	}
	return m.probeReal(ctx, job, r, p, pattern)
}

// simMatchHosts resolves an ansible host pattern against the inventory well
// enough to simulate it. exact reports whether the resolution is faithful: set
// operators (!exclude, &intersect) are not emulated, and saying so beats
// silently listing hosts the real run would have skipped.
func simMatchHosts(inv *model.Inventory, pattern string) (hosts []model.Host, exact bool) {
	exact = true
	var terms []string
	for _, t := range strings.FieldsFunc(pattern, func(r rune) bool { return r == ',' || r == ':' }) {
		if strings.HasPrefix(t, "!") || strings.HasPrefix(t, "&") || strings.HasPrefix(t, "~") {
			exact = false // a set operator or regex we do not emulate
			continue
		}
		terms = append(terms, t)
	}
	// "all" and "*" are ansible's catch-all groups, and may appear as one term
	// of a larger pattern such as "all:!web01".
	if slices.ContainsFunc(terms, func(t string) bool { return t == "all" || t == "*" }) {
		return inv.Hosts, exact
	}
	seen := map[string]bool{}
	for _, h := range inv.Hosts {
		for _, t := range terms {
			if matchTerm(t, h.Name) || slices.ContainsFunc(h.Groups, func(g string) bool { return matchTerm(t, g) }) {
				if !seen[h.Name] {
					seen[h.Name] = true
					hosts = append(hosts, h)
				}
				break
			}
		}
	}
	return hosts, exact
}

// matchTerm matches one pattern term against a host or group name, honouring
// shell-style globs the way ansible does.
func matchTerm(term, name string) bool {
	if term == name {
		return true
	}
	ok, err := path.Match(term, name)
	return err == nil && ok
}

// probeSim replays canned probe output without contacting any host.
func (m *Manager) probeSim(ctx context.Context, job *model.Job, r *run, p Probe, args []string) (failed bool) {
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
	pattern := job.Limit
	if pattern == "" {
		pattern = "all"
	}
	r.publish("$ ansible " + quoteArgs(args))
	if ansible.Available("ansible") {
		r.publish("[pine] PINE_SIMULATE=1 - simulating probe output, no host is contacted")
	} else {
		r.publish("[pine] ansible not found - simulating probe output")
	}
	hosts, exact := simMatchHosts(inv, pattern)
	if !exact {
		r.publish("[pine] pattern uses set operators Pine does not emulate; " +
			"the real run may match fewer hosts than shown")
	}
	if len(hosts) == 0 {
		r.publish("[pine] no hosts matched " + pattern)
		return false
	}
	r.publish("")
	for _, h := range hosts {
		if ctx.Err() != nil {
			return false
		}
		// mirror ansible's two ad-hoc output shapes: `| CHANGED | rc=0 >>` with
		// raw stdout for the command module, `| SUCCESS => {json}` for the rest
		if p.Module == "command" {
			r.publish(fmt.Sprintf("%s | CHANGED | rc=0 >>", h.Name))
			for _, line := range strings.Split(p.Sim, "\n") {
				r.publish(line)
			}
		} else {
			r.publish(fmt.Sprintf("%s | SUCCESS => %s", h.Name, p.Sim))
		}
		r.publish("")
		job.Summary.OK++
	}
	r.publish(fmt.Sprintf("[pine] probe %s ran on %d host(s)", p.ID, job.Summary.OK))
	return false
}

// probeReal shells out to `ansible <pattern> -m <module> [-a <args>]`, streams
// the output, and tallies the per-host status lines.
func (m *Manager) probeReal(ctx context.Context, job *model.Job, r *run, p Probe, pattern string) (failed bool) {
	repo, err := m.Store.GetRepo(job.RepoID)
	if err != nil {
		r.publish("ERROR: " + err.Error())
		return true
	}
	execCtx := ansible.Resolve(m.Store.RepoWorkdir(&repo), "", job.Inventory)
	args := probeArgs(p, pattern, execCtx.Inventory)
	cmd := exec.CommandContext(ctx, ansible.Bin("ansible"), args...)
	cmd.Dir = execCtx.Dir
	cmd.Env = append(ansible.Env(), "ANSIBLE_FORCE_COLOR=0", "ANSIBLE_NOCOLOR=1")
	cmd.Env = append(cmd.Env, hostKeyCheckingEnv(repo.HostKeyChecking)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.publish("ERROR: " + err.Error())
		return true
	}
	cmd.Stderr = cmd.Stdout
	r.publish("$ ansible " + quoteArgs(args))
	if err := cmd.Start(); err != nil {
		r.publish("ERROR: " + err.Error())
		return true
	}
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scan.Scan() {
		line := scan.Text()
		r.publish(line)
		countProbeLine(line, &job.Summary)
	}
	_ = cmd.Wait() // a non-zero exit is already reflected in the per-host tallies
	return job.Summary.Failed > 0 || job.Summary.Unreachable > 0
}

// countProbeLine tallies one ad-hoc status line into the job summary.
func countProbeLine(line string, sum *model.JobSummary) bool {
	mm := adhocRe.FindStringSubmatch(line)
	if mm == nil {
		return false
	}
	switch mm[2] {
	case "SUCCESS":
		sum.OK++
	case "CHANGED":
		// the command module cannot report anything else; a probe never mutates
		sum.OK++
	case "FAILED":
		sum.Failed++
	case "UNREACHABLE":
		sum.Unreachable++
	case "SKIPPED":
		sum.Skipped++
	}
	return true
}
