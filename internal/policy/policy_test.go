package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
	"github.com/jgsqware/pine/internal/scanner"
)

// task builds a TaskPlan running on the given hosts.
func task(name, module, args string, tags []string, hosts ...string) plan.TaskPlan {
	tp := plan.TaskPlan{Name: name, Module: module, Args: args, Tags: tags, Hosts: map[string]plan.HostVerdict{}}
	for _, h := range hosts {
		tp.Hosts[h] = plan.HostVerdict{Status: plan.StatusRun}
	}
	return tp
}

// result wraps one play into a plan.Result.
func result(inv string, become bool, totalHosts int, tasks ...plan.TaskPlan) *plan.Result {
	return &plan.Result{
		Inventory: inv,
		Summary:   plan.Summary{Hosts: totalHosts},
		Plays:     []plan.PlayPlan{{Name: "play", Become: become, Tasks: tasks}},
	}
}

func TestEvaluate(t *testing.T) {
	noLatest := `policies:
  - id: no-state-latest
    severity: error
    match: {inventory: "*production*", module: [apt, package]}
    assert: {forbid_args: {state: latest}}`

	becomeApproved := `policies:
  - id: become-approved
    severity: error
    match: {become: true}
    assert: {require_tag: approved}`

	warnForbid := `policies:
  - id: warn-shell
    severity: warning
    match: {module: [shell]}
    assert: {forbid: true}`

	groupWeb := `policies:
  - id: web-only
    severity: error
    match: {groups: [web]}
    assert: {forbid: true}`

	blast := `policies:
  - id: blast30
    severity: error
    match: {inventory: "*production*"}
    assert: {max_blast_radius_pct: 30}`

	cases := []struct {
		name      string
		yaml      string
		res       *plan.Result
		opts      Options
		wantErrs  int  // number of error-severity violations
		wantWarns int  // number of warning-severity violations
		wantGate  bool // HasError
		wantHosts []string
	}{
		{
			name:     "no-state-latest fails on offending plan",
			yaml:     noLatest,
			res:      result("inventories/production/hosts.ini", false, 3, task("Install nginx", "ansible.builtin.apt", "name: nginx, state: latest", nil, "web01")),
			wantErrs: 1, wantGate: true,
		},
		{
			name:     "no-state-latest passes on clean plan",
			yaml:     noLatest,
			res:      result("inventories/production/hosts.ini", false, 3, task("Install nginx", "ansible.builtin.apt", "name: nginx, state: present", nil, "web01")),
			wantErrs: 0, wantGate: false,
		},
		{
			name:     "no-state-latest skipped on non-production inventory",
			yaml:     noLatest,
			res:      result("inventories/staging/hosts.yml", false, 3, task("Install nginx", "ansible.builtin.apt", "name: nginx, state: latest", nil, "web01")),
			wantErrs: 0, wantGate: false,
		},
		{
			name:     "become without approved tag violates",
			yaml:     becomeApproved,
			res:      result("inv", true, 2, task("Harden sshd", "ansible.builtin.lineinfile", "", nil, "web01")),
			wantErrs: 1, wantGate: true,
		},
		{
			name:     "become with approved tag passes",
			yaml:     becomeApproved,
			res:      result("inv", true, 2, task("Harden sshd", "ansible.builtin.lineinfile", "", []string{"approved"}, "web01")),
			wantErrs: 0, wantGate: false,
		},
		{
			name:      "warning severity does not gate",
			yaml:      warnForbid,
			res:       result("inv", false, 2, task("Run migration", "ansible.builtin.shell", "flask db upgrade", nil, "web01")),
			wantWarns: 1, wantGate: false,
		},
		{
			name:     "group matching narrows to inventory group members",
			yaml:     groupWeb,
			res:      result("inv", false, 2, task("Deploy", "ansible.builtin.copy", "", nil, "web01", "db01")),
			opts:     Options{HostGroups: map[string][]string{"web01": {"web"}, "db01": {"db"}}},
			wantErrs: 1, wantGate: true, wantHosts: []string{"web01"},
		},
		{
			name:     "blast radius over threshold violates",
			yaml:     blast,
			res:      result("inventories/production/x", false, 10, task("t", "ansible.builtin.copy", "", nil, "h1", "h2", "h3", "h4", "h5")),
			opts:     Options{TotalHosts: 10},
			wantErrs: 1, wantGate: true,
		},
		{
			name:     "blast radius under threshold passes",
			yaml:     blast,
			res:      result("inventories/production/x", false, 10, task("t", "ansible.builtin.copy", "", nil, "h1", "h2")),
			opts:     Options{TotalHosts: 10},
			wantErrs: 0, wantGate: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			policies, err := Parse([]byte(c.yaml))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			vs := Evaluate(policies, c.res, c.opts)
			var errs, warns int
			for _, v := range vs {
				switch v.Severity {
				case SeverityError:
					errs++
				case SeverityWarning:
					warns++
				}
			}
			if errs != c.wantErrs {
				t.Errorf("errors = %d, want %d (%+v)", errs, c.wantErrs, vs)
			}
			if warns != c.wantWarns {
				t.Errorf("warnings = %d, want %d (%+v)", warns, c.wantWarns, vs)
			}
			if got := HasError(vs); got != c.wantGate {
				t.Errorf("HasError = %v, want %v", got, c.wantGate)
			}
			if c.wantHosts != nil {
				if len(vs) != 1 {
					t.Fatalf("want 1 violation for host check, got %d", len(vs))
				}
				if got := vs[0].Hosts; !equalStrings(got, c.wantHosts) {
					t.Errorf("hosts = %v, want %v", got, c.wantHosts)
				}
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseDefaultsAndValidation(t *testing.T) {
	ps, err := Parse([]byte("policies:\n  - id: x\n    assert: {forbid: true}"))
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].Severity != SeverityError {
		t.Errorf("default severity = %q, want error", ps[0].Severity)
	}
	if _, err := Parse([]byte("policies:\n  - assert: {forbid: true}")); err == nil {
		t.Error("expected error for missing id")
	}
	if _, err := Parse([]byte("policies:\n  - id: x\n    match: {task_name_regex: \"([\"}")); err == nil {
		t.Error("expected error for bad regex")
	}
}

// TestIntegrationDemoInfra evaluates the shipped example policies against the
// demo repo: the strict set gates (become without approval), the baseline set
// is clean.
func TestIntegrationDemoInfra(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "demo-infra")
	if _, err := os.Stat(root); err != nil {
		t.Skip("demo-infra not present")
	}
	res, err := scanner.Scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	abs, _ := filepath.Abs(root)

	eval := func(policyFile string) []Violation {
		policies, err := Load(filepath.Join(root, policyFile))
		if err != nil {
			t.Fatalf("load %s: %v", policyFile, err)
		}
		var all []Violation
		for _, pb := range res.Playbooks {
			out, err := plan.Compute(res, abs, model.Repo{ID: "t", Name: "demo"}, plan.Request{Playbook: pb.Path, Inventory: "production"})
			if err != nil {
				continue
			}
			all = append(all, Evaluate(policies, out, Options{TotalHosts: out.Summary.Hosts})...)
		}
		return all
	}

	if vs := eval("policies.yml"); !HasError(vs) {
		t.Errorf("strict policies.yml should gate the demo, got no error violations")
	}
	if vs := eval("policies-baseline.yml"); HasError(vs) {
		t.Errorf("baseline policies should pass the demo, got error violations: %+v", vs)
	}
}
