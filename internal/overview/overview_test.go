package overview

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jgsqware/pine/internal/claudecode"
	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
)

// sample builds a small in-memory scan result: two playbooks in a
// main/components tier layout, two roles (one unused), one inventory.
func sample() *model.ScanResult {
	return &model.ScanResult{
		Playbooks: []model.Playbook{
			{
				Path: "playbooks/main/site.yml", Name: "site", Description: "full converge",
				Plays: []model.Play{{
					Name: "all", Hosts: "web", Become: true, Serial: "1",
					Roles: []string{"nginx"}, Tags: []string{"deploy"},
					VarsPrompt: []model.PromptVar{{Name: "version"}},
				}},
			},
			{
				Path: "playbooks/components/nginx-only.yml", Name: "nginx-only",
				Plays: []model.Play{{Name: "web", Hosts: "web", Roles: []string{"nginx"}}},
			},
		},
		Roles: []model.Role{
			{Name: "nginx", Description: "web server", TasksCount: 3},
			{Name: "unusedrole", TasksCount: 1},
		},
		Inventories: []model.Inventory{{
			Name: "production", Path: "inventories/production", Format: "ini",
			Hosts:  []model.Host{{Name: "web01", Groups: []string{"web"}}, {Name: "web02", Groups: []string{"web"}}},
			Groups: []model.Group{{Name: "web", Hosts: []string{"web01", "web02"}}},
		}},
	}
}

func TestTiersAndTargets(t *testing.T) {
	res := sample()
	ov := ComposeWith(res, "", &plan.HygieneResult{}, claudecode.Capability{})

	if len(ov.Tiers) != 2 {
		t.Fatalf("want 2 tiers, got %d: %+v", len(ov.Tiers), ov.Tiers)
	}
	// tiers sorted alphabetically: components, main
	if ov.Tiers[0].Name != "components" || ov.Tiers[1].Name != "main" {
		t.Fatalf("unexpected tier order: %s, %s", ov.Tiers[0].Name, ov.Tiers[1].Name)
	}
	site := ov.Tiers[1].Playbooks[0]
	if site.Name != "site" {
		t.Fatalf("want site playbook, got %s", site.Name)
	}
	if len(site.TargetHosts) != 2 || site.TargetHosts[0] != "web01" {
		t.Fatalf("hosts pattern web should resolve to web01,web02; got %v", site.TargetHosts)
	}
	if !site.NeedsInput || !site.HasSerial || !site.Become {
		t.Fatalf("site flags wrong: needsInput=%v serial=%v become=%v", site.NeedsInput, site.HasSerial, site.Become)
	}
	if site.Description != "full converge" {
		t.Fatalf("description not carried: %q", site.Description)
	}
}

func TestRoleUsedByAndUnused(t *testing.T) {
	res := sample()
	hy := &plan.HygieneResult{UnusedRoles: []plan.UnusedRole{{Name: "unusedrole"}}}
	ov := ComposeWith(res, "", hy, claudecode.Capability{})

	byName := map[string]RoleInfo{}
	for _, r := range ov.Roles {
		byName[r.Name] = r
	}
	nginx := byName["nginx"]
	if len(nginx.UsedBy) != 2 {
		t.Fatalf("nginx should be used by 2 playbooks, got %v", nginx.UsedBy)
	}
	if nginx.Unused {
		t.Fatal("nginx wrongly marked unused")
	}
	if !byName["unusedrole"].Unused {
		t.Fatal("unusedrole should be flagged unused")
	}
}

func TestCautions(t *testing.T) {
	res := sample()
	hy := &plan.HygieneResult{
		VaultFiles:     2,
		SecretFindings: []plan.SecretFinding{{File: "group_vars/all.yml", Key: "db_password", Severity: "high"}},
		UnusedRoles:    []plan.UnusedRole{{Name: "unusedrole"}},
	}
	ov := ComposeWith(res, "", hy, claudecode.Capability{})

	kinds := map[string]bool{}
	for _, c := range ov.Cautions {
		kinds[c.Kind] = true
	}
	for _, want := range []string{"needs-input", "rolling", "vault", "plaintext-secret", "unused-role"} {
		if !kinds[want] {
			t.Errorf("missing caution kind %q; got %v", want, kinds)
		}
	}
}

func TestRoleRefsIncluded(t *testing.T) {
	res := &model.ScanResult{
		Playbooks: []model.Playbook{{
			Path: "site.yml", Name: "site",
			Plays: []model.Play{{
				Name:  "p", Hosts: "all",
				Tasks: []model.Task{{Module: "include_role", RoleRef: "common"}},
			}},
		}},
		Roles: []model.Role{{Name: "common", TasksCount: 1}},
	}
	ov := ComposeWith(res, "", &plan.HygieneResult{}, claudecode.Capability{})
	if got := ov.Tiers[0].Playbooks[0].Roles; len(got) != 1 || got[0] != "common" {
		t.Fatalf("include_role role not collected: %v", got)
	}
	if ov.Roles[0].UsedBy == nil {
		t.Fatal("role used-by via include_role not detected")
	}
}

func TestEntryPoints(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"run.sh", "ansible.cfg", "README.md"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	eps := detectEntryPoints(dir)
	kinds := map[string]string{}
	for _, e := range eps {
		kinds[e.kindKey()] = e.Path
	}
	if kinds["run-script"] != "run.sh" || kinds["config"] != "ansible.cfg" {
		t.Fatalf("entry points not detected: %+v", eps)
	}
}

// kindKey is a tiny helper so the test reads clearly.
func (e EntryPoint) kindKey() string { return e.Kind }
