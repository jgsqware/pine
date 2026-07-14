package runner

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jgsqware/pine/internal/model"
)

// The catalog is the security boundary: every probe must be read-only, so no
// entry may use a module that takes a caller-supplied command, and none may
// smuggle privilege escalation through its fixed args.
func TestCatalogIsReadOnly(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Probes() {
		if p.ID == "" || p.Title == "" || p.Desc == "" || p.Module == "" {
			t.Errorf("probe %q: incomplete catalog entry", p.ID)
		}
		if seen[p.ID] {
			t.Errorf("probe %q: duplicate ID", p.ID)
		}
		seen[p.ID] = true

		if p.Module == "shell" || p.Module == "raw" || p.Module == "script" {
			t.Errorf("probe %q: module %q reaches a shell on the host", p.ID, p.Module)
		}
		for _, bad := range []string{"become", "sudo", "rm ", ">", "|", ";", "&&", "$("} {
			if strings.Contains(p.Args, bad) {
				t.Errorf("probe %q: args %q contain %q", p.ID, p.Args, bad)
			}
		}
		if p.Sim == "" {
			t.Errorf("probe %q: no simulated output, `pine probe` breaks without ansible", p.ID)
		}
	}
}

func TestProbeByID(t *testing.T) {
	if _, ok := ProbeByID("uptime"); !ok {
		t.Fatal("uptime probe missing from catalog")
	}
	if _, ok := ProbeByID("rm -rf /"); ok {
		t.Fatal("ProbeByID accepted a command string as an ID")
	}
	if _, ok := ProbeByID(""); ok {
		t.Fatal("ProbeByID accepted an empty ID")
	}
}

// The host pattern is the only caller-controlled value that reaches argv. It is
// ansible's first positional argument, so a leading dash would be parsed as a
// flag (e.g. --become) rather than a host pattern.
func TestValidHostPattern(t *testing.T) {
	valid := []string{
		"all", "web01", "web:db", "web:!web01", "web:&staging",
		"web[0:5]", "web*", "~webserver", "a.b.c", "web01,db01",
	}
	for _, s := range valid {
		if !validHostPattern(s) {
			t.Errorf("validHostPattern(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"", "--become", "-b", "-i/etc/passwd",
		"web01; rm -rf /", "web01 && id", "$(id)", "`id`",
		"web01 --become", "web01\nall", "web01|id",
		strings.Repeat("a", 300),
	}
	for _, s := range invalid {
		if validHostPattern(s) {
			t.Errorf("validHostPattern(%q) = true, want false", s)
		}
	}
}

// probeArgs must place the pattern first and always disable become.
func TestProbeArgs(t *testing.T) {
	p, _ := ProbeByID("ports")
	got := probeArgs(p, "web:!web01", "inventories/prod")
	want := []string{
		"web:!web01", "-m", "command", "-a", "ss -tuln", "-i", "inventories/prod",
		"-e", `{"ansible_become": false}`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("probeArgs = %q, want %q", got, want)
	}

	ping, _ := ProbeByID("ping")
	got = probeArgs(ping, "all", "")
	if !slices.Equal(got, []string{"all", "-m", "ping", "-e", `{"ansible_become": false}`}) {
		t.Fatalf("probeArgs(ping) = %q", got)
	}
}

// A repo's ansible.cfg (`become = True`) or an inventory var (`ansible_become:
// true`) must not be able to run a probe under sudo. Only the -e override wins.
func TestProbesNeverEscalate(t *testing.T) {
	for _, p := range Probes() {
		args := probeArgs(p, "all", "inv")
		for _, a := range args {
			if a == "-b" || a == "--become" {
				t.Errorf("probe %q passes %s", p.ID, a)
			}
		}
		if !slices.Contains(args, `{"ansible_become": false}`) {
			t.Errorf("probe %q does not force become off: %q", p.ID, args)
		}
	}
}

// The `$ ansible ...` line in a job log must be pasteable and mean the same
// thing: `-a ss -tuln` would read as two flags, not one quoted argument.
func TestQuoteArgs(t *testing.T) {
	p, _ := ProbeByID("ports")
	got := quoteArgs(probeArgs(p, "web:!web01", ""))
	want := `'web:!web01' -m command -a 'ss -tuln' -e '{"ansible_become": false}'`
	if got != want {
		t.Fatalf("quoteArgs =\n  %s\nwant\n  %s", got, want)
	}
	if got := quoteArgs([]string{"it's"}); got != `'it'\''s'` {
		t.Fatalf("quoteArgs single quote = %s", got)
	}
}

func TestCountProbeLine(t *testing.T) {
	var sum model.JobSummary
	lines := []string{
		"web01 | SUCCESS => {\"ping\": \"pong\"}",
		"web02 | CHANGED | rc=0 >>",
		"web03 | UNREACHABLE! => {\"msg\": \"timed out\"}",
		"web04 | FAILED | rc=1 >>",
		"  some ordinary output line",
	}
	for _, l := range lines {
		countProbeLine(l, &sum)
	}
	// CHANGED counts as OK: the command module cannot report anything else.
	if sum.OK != 2 || sum.Unreachable != 1 || sum.Failed != 1 {
		t.Fatalf("summary = %+v", sum)
	}
}

// End-to-end through the job manager, on the simulated path so it needs no
// hosts: a probe job must run, tally its hosts and land on success.
func TestRunProbeSimulated(t *testing.T) {
	t.Setenv("PINE_SIMULATE", "1")
	m, _ := newTestManager(t)

	job, err := m.RunProbe("r_test", "uptime", "", "")
	if err != nil {
		t.Fatal(err)
	}
	final := waitJob(t, m, job.ID)
	if final.Status != model.JobSuccess {
		t.Fatalf("status = %s", final.Status)
	}
	if final.Probe != "uptime" || final.Playbook != "[probe: uptime]" {
		t.Errorf("probe = %q, playbook = %q", final.Probe, final.Playbook)
	}
	if final.Summary.OK == 0 {
		t.Errorf("summary = %+v, want at least one host", final.Summary)
	}
	log, err := os.ReadFile(m.Store.JobLogPath(job.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "load average") {
		t.Errorf("simulated log missing probe output:\n%s", log)
	}
}

// RunProbe must reject before it ever creates a job.
func TestRunProbeRejects(t *testing.T) {
	m, _ := newTestManager(t)
	for _, tc := range []struct{ probe, limit string }{
		{"shell", "all"},
		{"uptime; rm -rf /", ""},
		{"uptime", "--become"},
		{"uptime", "all; id"},
	} {
		if _, err := m.RunProbe("r_test", tc.probe, "", tc.limit); err == nil {
			t.Errorf("RunProbe(%q, limit=%q) = nil error, want rejection", tc.probe, tc.limit)
		}
	}
	if jobs := m.Store.ListJobs(); len(jobs) != 0 {
		t.Errorf("rejected probes created %d job(s)", len(jobs))
	}
}

// The simulated path must honour the host pattern rather than listing every
// host, and must admit when it cannot resolve one faithfully.
func TestSimMatchHosts(t *testing.T) {
	inv := &model.Inventory{Hosts: []model.Host{
		{Name: "web01", Groups: []string{"web", "prod"}},
		{Name: "web02", Groups: []string{"web", "prod"}},
		{Name: "db01", Groups: []string{"db", "prod"}},
	}}
	names := func(hs []model.Host) []string {
		out := make([]string, len(hs))
		for i, h := range hs {
			out[i] = h.Name
		}
		return out
	}
	for _, tc := range []struct {
		pattern string
		want    []string
		exact   bool
	}{
		{"all", []string{"web01", "web02", "db01"}, true},
		{"web*", []string{"web01", "web02"}, true},
		{"web", []string{"web01", "web02"}, true}, // group name
		{"db01", []string{"db01"}, true},
		{"web01,db01", []string{"web01", "db01"}, true},
		{"prod", []string{"web01", "web02", "db01"}, true},
		{"nope", nil, true},
		{"web:!web01", []string{"web01", "web02"}, false},        // exclusion not emulated
		{"all:!db01", []string{"web01", "web02", "db01"}, false}, // "all" as one term of many
		{"*", []string{"web01", "web02", "db01"}, true},
	} {
		got, exact := simMatchHosts(inv, tc.pattern)
		if !slices.Equal(names(got), tc.want) {
			t.Errorf("simMatchHosts(%q) = %v, want %v", tc.pattern, names(got), tc.want)
		}
		if exact != tc.exact {
			t.Errorf("simMatchHosts(%q) exact = %v, want %v", tc.pattern, exact, tc.exact)
		}
	}
}

func TestProbeJobLabel(t *testing.T) {
	if got := ProbeJobLabel("uptime"); got != "[probe: uptime]" {
		t.Fatalf("ProbeJobLabel = %q", got)
	}
}
