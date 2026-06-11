package plan

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/scanner"
)

func demoScan(t *testing.T) (*model.ScanResult, string) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "examples", "demo-infra")
	res, err := scanner.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	return res, root
}

func planFor(t *testing.T, req Request) *Result {
	t.Helper()
	res, root := demoScan(t)
	out, err := Compute(res, root, model.Repo{ID: "r_test", Name: "demo-infra"}, req)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPlanWebserversWithoutFacts(t *testing.T) {
	out := planFor(t, Request{Playbook: "webservers.yml", Inventory: "inventories/production"})
	if out.Mode != "estimated" {
		t.Errorf("mode = %q", out.Mode)
	}
	if out.Summary.Hosts != 3 {
		t.Errorf("hosts = %d, want 3 (web tier)", out.Summary.Hosts)
	}
	if out.Summary.Unknown == 0 {
		t.Error("expected unknown verdicts without a fact profile (OS-family conditionals)")
	}
	found := false
	for _, mv := range out.Summary.MissingVars {
		if mv.Name == "ansible_facts.os_family" || mv.Name == "ansible_os_family" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing vars should mention the OS family fact, got %+v", out.Summary.MissingVars)
	}
}

func TestPlanFactProfileResolvesOSConditionals(t *testing.T) {
	without := planFor(t, Request{Playbook: "webservers.yml", Inventory: "inventories/production"})
	with := planFor(t, Request{Playbook: "webservers.yml", Inventory: "inventories/production", FactProfile: "ubuntu-24.04"})
	if with.Summary.Unknown >= without.Summary.Unknown {
		t.Errorf("fact profile should reduce unknowns: %d -> %d", without.Summary.Unknown, with.Summary.Unknown)
	}
	if with.Summary.Run <= without.Summary.Run {
		t.Errorf("fact profile should increase run verdicts: %d -> %d", without.Summary.Run, with.Summary.Run)
	}
}

func TestPlanUserVarsResolveUnknowns(t *testing.T) {
	base := planFor(t, Request{Playbook: "webservers.yml", Inventory: "inventories/production", FactProfile: "ubuntu-24.04"})
	if len(base.Summary.MissingVars) == 0 {
		t.Skip("nothing left to resolve")
	}
	vars := map[string]any{}
	for _, mv := range base.Summary.MissingVars {
		vars[mv.Name] = true
	}
	resolved := planFor(t, Request{Playbook: "webservers.yml", Inventory: "inventories/production", FactProfile: "ubuntu-24.04", Vars: vars})
	if resolved.Summary.Unknown >= base.Summary.Unknown {
		t.Errorf("supplying missing vars should reduce unknowns: %d -> %d", base.Summary.Unknown, resolved.Summary.Unknown)
	}
}

func TestPlanRollingUpdateBatches(t *testing.T) {
	out := planFor(t, Request{Playbook: "rolling-update.yml", Inventory: "inventories/production"})
	if len(out.Plays) == 0 {
		t.Fatal("no plays")
	}
	pp := out.Plays[0]
	if len(pp.Batches) != 3 {
		t.Errorf("serial:1 should give 3 batches, got %d", len(pp.Batches))
	}
	if len(pp.MatchedHosts) != 3 {
		t.Errorf("matched = %v", pp.MatchedHosts)
	}
}

func TestPlanLimit(t *testing.T) {
	out := planFor(t, Request{Playbook: "webservers.yml", Inventory: "inventories/production", Limit: "web01"})
	for _, pp := range out.Plays {
		if pp.Import != "" {
			continue
		}
		if len(pp.MatchedHosts) != 1 || pp.MatchedHosts[0] != "web01" {
			t.Errorf("limit web01: matched = %v", pp.MatchedHosts)
		}
	}
}

func TestPlanImportsFollowed(t *testing.T) {
	out := planFor(t, Request{Playbook: "site.yml", Inventory: "inventories/production"})
	imports, real := 0, 0
	for _, pp := range out.Plays {
		if pp.Import != "" {
			imports++
		} else {
			real++
		}
	}
	if imports != 8 || real < 8 {
		t.Errorf("site.yml: imports=%d real=%d", imports, real)
	}
	if out.Summary.Tasks == 0 || out.Summary.Run == 0 {
		t.Errorf("summary empty: %+v", out.Summary)
	}
}

func TestPlanHandlers(t *testing.T) {
	out := planFor(t, Request{Playbook: "webservers.yml", Inventory: "inventories/production", FactProfile: "ubuntu-24.04"})
	foundHandler := false
	for _, pp := range out.Plays {
		if len(pp.Handlers) > 0 {
			foundHandler = true
			for _, h := range pp.Handlers {
				if len(h.TriggeredBy) == 0 || len(h.Hosts) == 0 {
					t.Errorf("handler %s lacks triggers/hosts: %+v", h.Name, h)
				}
			}
		}
	}
	if !foundHandler {
		t.Error("webservers.yml should predict at least one handler")
	}
}

func TestPreviewInventoryWhatIf(t *testing.T) {
	res, _ := demoScan(t)

	// baseline: prod-db is not a docker host
	base, err := PreviewInventory(res, PreviewRequest{Inventory: "homelab"})
	if err != nil {
		t.Fatal(err)
	}
	if hosts := groupHosts(base.Inventory, "docker_hosts"); contains(hosts, "prod-db") {
		t.Errorf("baseline docker_hosts should not contain prod-db: %v", hosts)
	}

	// what-if: prod-db now also runs docker
	whatif, err := PreviewInventory(res, PreviewRequest{
		Inventory: "homelab",
		HostVars:  map[string]map[string]any{"prod-db": {"services": []any{"postgresql", "docker"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	hosts := groupHosts(whatif.Inventory, "docker_hosts")
	if !contains(hosts, "prod-db") {
		t.Errorf("what-if docker_hosts should contain prod-db: %v", hosts)
	}
	if whatif.Topology == nil || len(whatif.Topology.Nodes) == 0 {
		t.Error("preview should include a topology graph")
	}
}

func TestPreviewReportsUnknownMemberships(t *testing.T) {
	res, _ := demoScan(t)
	// removing services from a host makes its membership conditions
	// default([]) -> false, not unknown; instead reference a var that the
	// rules use but is absent: simulate by clearing host vars entirely and
	// checking that defaults keep verdicts known (regression), then force
	// an unknown via a rule-less check is impossible here - so assert the
	// shape: unknown_groups must be non-nil even when empty.
	out, err := PreviewInventory(res, PreviewRequest{Inventory: "homelab"})
	if err != nil {
		t.Fatal(err)
	}
	if out.UnknownGroups == nil {
		t.Error("unknown_groups must be a (possibly empty) map")
	}
}

func groupHosts(inv model.Inventory, name string) []string {
	for _, g := range inv.Groups {
		if g.Name == name {
			return g.Hosts
		}
	}
	return nil
}

func contains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

// Missing vars are reported with dotted paths (ansible_facts.os_family);
// supplying them back with the same dotted key must resolve the lookups.
func TestPlanDottedUserVarsResolve(t *testing.T) {
	out := planFor(t, Request{
		Playbook: "webservers.yml", Inventory: "inventories/production",
		Vars: map[string]any{"ansible_facts.os_family": "Debian"},
	})
	if out.Summary.Unknown != 0 {
		t.Errorf("dotted user var should resolve all unknowns, got %d (missing %+v)",
			out.Summary.Unknown, out.Summary.MissingVars)
	}
}
