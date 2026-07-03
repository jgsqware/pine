package plan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/scanner"
)

// alloyScan mirrors the reported case: an image string built from a
// group_vars/all constant and a role default.
func alloyScan() *model.ScanResult {
	return &model.ScanResult{
		Playbooks: []model.Playbook{{
			Path: "alloy.yml",
			Plays: []model.Play{{
				Name:  "Deploy Alloy",
				Hosts: "monitoring",
				Roles: []string{"alloy"},
				Vars:  map[string]any{"alloy_replicas": 2},
			}},
		}},
		Roles: []model.Role{{
			Name:     "alloy",
			Defaults: map[string]any{"alloy_version": "1.5.0"},
		}},
		Inventories: []model.Inventory{{
			Name: "inventories/prod",
			Groups: []model.Group{
				{Name: "all", Vars: map[string]any{
					"DOCKER_LOCAL_REGISTRY": "registry.local",
					"vault_alloy_token":     "s3cr3t-value",
				}},
				{Name: "monitoring", Hosts: []string{"mon1"}, Vars: map[string]any{"alloy_version": "1.6.0"}},
			},
			Hosts: []model.Host{{Name: "mon1", Groups: []string{"all", "monitoring"}}},
		}},
	}
}

func TestResolveMachineScope(t *testing.T) {
	res := alloyScan()
	out, err := Resolve(res, "", "alloy.yml", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Mode != "machine" {
		t.Fatalf("mode = %q, want machine", out.Mode)
	}
	if len(out.Plays) != 1 {
		t.Fatalf("plays = %d", len(out.Plays))
	}
	vars := out.Plays[0].Vars
	if vars["DOCKER_LOCAL_REGISTRY"] != "registry.local" {
		t.Errorf("DOCKER_LOCAL_REGISTRY = %v (from group_vars/all)", vars["DOCKER_LOCAL_REGISTRY"])
	}
	// machine scope sees only group_vars/all, not the monitoring group, so the
	// role default wins for alloy_version.
	if vars["alloy_version"] != "1.5.0" {
		t.Errorf("alloy_version = %v, want role default 1.5.0", vars["alloy_version"])
	}
	// the actual image string interpolates cleanly
	img, known, missing := scanner.Interpolate("{{ DOCKER_LOCAL_REGISTRY }}/grafana/alloy:{{ alloy_version }}", vars)
	if !known || img != "registry.local/grafana/alloy:1.5.0" {
		t.Errorf("interpolated = %q known=%v missing=%v", img, known, missing)
	}
	// Redact (applied by the server before sending) masks secret values
	out.Redact()
	if out.Plays[0].Vars["vault_alloy_token"] != RedactedMark {
		t.Errorf("secret not redacted: %v", out.Plays[0].Vars["vault_alloy_token"])
	}
}

func TestResolveHostScope(t *testing.T) {
	res := alloyScan()
	out, err := Resolve(res, "", "alloy.yml", "inventories/prod", "mon1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Mode != "host" || out.Host != "mon1" {
		t.Fatalf("scope = %s/%s, want host/mon1", out.Mode, out.Host)
	}
	vars := out.Plays[0].Vars
	// host scope sees the monitoring group, which overrides the role default
	if vars["alloy_version"] != "1.6.0" {
		t.Errorf("alloy_version = %v, want group override 1.6.0", vars["alloy_version"])
	}
	// lineage records both the role default and the group override, in order
	chain := out.Plays[0].Lineage["alloy_version"]
	if len(chain) != 2 || chain[0].Scope != "role_default" || chain[len(chain)-1].Scope != "group" {
		t.Errorf("alloy_version lineage = %+v", chain)
	}
}

// The reported case: alloy_dir lives in the role's vars/main.yml, and the role
// is pulled in via include_role (not listed under `roles:`). Both were blind
// spots — the resolver only used role defaults and the `roles:` list.
func TestResolveRoleVarsAndIncludeRole(t *testing.T) {
	res := &model.ScanResult{
		Playbooks: []model.Playbook{{
			Path: "alloy.yml",
			Plays: []model.Play{{
				Name:  "Deploy Alloy",
				Hosts: "monitoring",
				Tasks: []model.Task{
					{Name: "Run the alloy role", Module: "ansible.builtin.include_role", RoleRef: "alloy"},
				},
			}},
		}},
		Roles: []model.Role{{
			Name:     "alloy",
			Defaults: map[string]any{"alloy_version": "1.5.0"},
			Vars:     map[string]any{"alloy_dir": "/opt/alloy", "alloy_version": "1.9.9"},
		}},
	}
	out, err := Resolve(res, "", "alloy.yml", "", "")
	if err != nil {
		t.Fatal(err)
	}
	vars := out.Plays[0].Vars
	if vars["alloy_dir"] != "/opt/alloy" {
		t.Errorf("alloy_dir = %v, want /opt/alloy (role vars/main.yml via include_role)", vars["alloy_dir"])
	}
	// role vars outrank role defaults
	if vars["alloy_version"] != "1.9.9" {
		t.Errorf("alloy_version = %v, want role-vars 1.9.9 over default 1.5.0", vars["alloy_version"])
	}
	chain := out.Plays[0].Lineage["alloy_version"]
	if len(chain) != 2 || chain[0].Scope != "role_default" || chain[1].Scope != "role_vars" {
		t.Errorf("alloy_version lineage = %+v, want role_default then role_vars", chain)
	}
}

// vars_prompt variables are defined (asked interactively); their default —
// resolved against the rest — must show, not be flagged undefined.
func TestResolveVarsPrompt(t *testing.T) {
	res := &model.ScanResult{
		Playbooks: []model.Playbook{{
			Path: "p.yml",
			Plays: []model.Play{{
				Name: "p", Hosts: "all",
				Vars: map[string]any{"ver_default": "9.9"},
				VarsPrompt: []model.PromptVar{
					{Name: "ver", Default: "{{ ver_default }}"},
					{Name: "token"},
				},
			}},
		}},
	}
	out, err := Resolve(res, "", "p.yml", "", "")
	if err != nil {
		t.Fatal(err)
	}
	v := out.Plays[0].Vars
	if v["ver"] != "9.9" {
		t.Errorf("ver = %v, want 9.9 (vars_prompt default interpolated)", v["ver"])
	}
	if v["token"] != "(prompted)" {
		t.Errorf("token = %v, want (prompted)", v["token"])
	}
	if !contains(out.Known, "ver") || !contains(out.Known, "token") {
		t.Errorf("vars_prompt names should be in Known: %v", out.Known)
	}
}

// Nested var references are expanded against the resolved map; an unresolvable
// fact in the chain is left intact.
func TestResolveNestedExpansion(t *testing.T) {
	res := &model.ScanResult{
		Playbooks: []model.Playbook{{
			Path: "p.yml",
			Plays: []model.Play{{
				Name: "p", Hosts: "all",
				Vars: map[string]any{
					"monitoring_dir": "{{ ansible_user_dir }}/monitoring",
					"alloy_dir":      "{{ monitoring_dir }}/alloy",
					"alloy_conf":     "{{ alloy_dir }}/config.alloy",
				},
			}},
		}},
	}
	out, err := Resolve(res, "", "p.yml", "", "")
	if err != nil {
		t.Fatal(err)
	}
	v := out.Plays[0].Vars
	if v["alloy_dir"] != "{{ ansible_user_dir }}/monitoring/alloy" {
		t.Errorf("alloy_dir = %v", v["alloy_dir"])
	}
	if v["alloy_conf"] != "{{ ansible_user_dir }}/monitoring/alloy/config.alloy" {
		t.Errorf("alloy_conf = %v", v["alloy_conf"])
	}
	// the lineage keeps the value as authored (not expanded)
	chain := out.Plays[0].Lineage["alloy_dir"]
	if len(chain) == 0 || chain[len(chain)-1].Value != "{{ monitoring_dir }}/alloy" {
		t.Errorf("alloy_dir lineage should keep the authored value, got %+v", chain)
	}
}

// A host is flagged "varies" when its group/host vars override a variable the
// playbook uses (here mon1's monitoring group overrides the alloy role default).
func TestResolveHostVaries(t *testing.T) {
	res := alloyScan()
	out, err := Resolve(res, "", "alloy.yml", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var got *HostInfo
	for i := range out.Inventories {
		for j := range out.Inventories[i].Hosts {
			if out.Inventories[i].Hosts[j].Name == "mon1" {
				got = &out.Inventories[i].Hosts[j]
			}
		}
	}
	if got == nil {
		t.Fatal("mon1 not in the picker")
	}
	if !got.Varies {
		t.Errorf("mon1 should vary (monitoring group overrides alloy_version)")
	}
}

// A playbook that loads per-service config via import_tasks → include_vars (the
// landbased-map case): the include_vars values must appear in the effective
// vars, with {{ }} resolved against group/play vars, and provenance recorded.
func TestResolveIncludeVars(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("inv/group_vars/all.yml", "base_domain: acme.example\nbetregister_db_server: 10.1.1.107\n")
	write("inv/hosts", "[rs]\nh1\n")
	write("svc/admin/deploy-test.yaml", "- hosts: all\n  vars:\n    prefix: PT_TEST\n  tasks:\n    - import_tasks: deploy.yaml\n")
	write("svc/admin/deploy.yaml", "- include_vars: dedicated.yaml\n")
	write("svc/admin/vars/dedicated.yaml", "IIS_WebSite_Name: Admin\nKeycloak_URL: \"https://keycloak.{{ base_domain }}\"\nproject: \"{{ prefix }}_Admin\"\n")

	res, err := scanner.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	lin, err := ResolveLineage(res, root, "svc/admin/deploy-test.yaml", "inv/hosts", "h1")
	if err != nil {
		t.Fatal(err)
	}
	vals := map[string]any{}
	chains := map[string][]LineageEntry{}
	for _, v := range lin.Vars {
		vals[v.Key] = v.Value
		chains[v.Key] = v.Chain
	}
	if vals["IIS_WebSite_Name"] != "Admin" {
		t.Errorf("IIS_WebSite_Name = %v (from include_vars dedicated.yaml)", vals["IIS_WebSite_Name"])
	}
	if vals["Keycloak_URL"] != "https://keycloak.acme.example" {
		t.Errorf("Keycloak_URL = %v, want {{ base_domain }} resolved", vals["Keycloak_URL"])
	}
	if vals["project"] != "PT_TEST_Admin" {
		t.Errorf("project = %v, want {{ prefix }} (play var) resolved", vals["project"])
	}
	if vals["betregister_db_server"] != "10.1.1.107" {
		t.Errorf("group_vars lost: %v", vals["betregister_db_server"])
	}
	ch := chains["IIS_WebSite_Name"]
	if len(ch) == 0 || ch[len(ch)-1].Scope != "include_vars" {
		t.Errorf("IIS_WebSite_Name provenance = %+v, want include_vars scope", ch)
	}
}

// Ansible precedence: play vars_files (level 14) outrank play vars (level 12).
// Both variable engines had it inverted (play vars overwrote vars_files). When
// the same key is set in BOTH play vars and a vars_files file, the vars_files
// value must win, and the lineage must record play_vars first (lower) then
// vars_file last (higher).
func TestResolveVarsFilesBeatPlayVars(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "extra.yml"), []byte("shared: from_vars_file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := &model.ScanResult{
		Playbooks: []model.Playbook{{
			Path: "p.yml",
			Plays: []model.Play{{
				Name: "p", Hosts: "all",
				Vars:      map[string]any{"shared": "from_play_vars"},
				VarsFiles: []string{"extra.yml"},
			}},
		}},
	}
	out, err := Resolve(res, root, "p.yml", "", "")
	if err != nil {
		t.Fatal(err)
	}
	v := out.Plays[0].Vars
	if v["shared"] != "from_vars_file" {
		t.Errorf("shared = %v, want from_vars_file (vars_files outrank play vars)", v["shared"])
	}
	chain := out.Plays[0].Lineage["shared"]
	if len(chain) != 2 || chain[0].Scope != "play_vars" || chain[len(chain)-1].Scope != "vars_file" {
		t.Errorf("shared lineage = %+v, want play_vars then vars_file", chain)
	}
}

// Same precedence rule in the per-host effective() engine: playFileVars
// (vars_files, level 14) must beat play.Vars (level 12).
func TestEffectiveVarsFilesBeatPlayVars(t *testing.T) {
	r := newVarResolver(nil, nil, nil, nil)
	play := &model.Play{Vars: map[string]any{"shared": "from_play_vars"}}
	playFileVars := []map[string]any{{"shared": "from_vars_file"}}
	eff := r.effective(nil, play, nil, playFileVars, nil)
	if eff["shared"] != "from_vars_file" {
		t.Errorf("shared = %v, want from_vars_file (vars_files outrank play vars)", eff["shared"])
	}
}

func TestResolveDemoPlaybook(t *testing.T) {
	res, root := demoScan(t)
	out, err := Resolve(res, root, "webservers.yml", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Plays) == 0 {
		t.Fatal("no plays resolved")
	}
	// healthcheck_path is a play var → resolvable host-agnostically
	if out.Plays[0].Vars["healthcheck_path"] != "/healthz" {
		t.Errorf("healthcheck_path = %v", out.Plays[0].Vars["healthcheck_path"])
	}
	if len(out.Inventories) == 0 {
		t.Error("expected inventories for the host picker")
	}
}
