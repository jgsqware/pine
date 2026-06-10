package scanner

import (
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

// demoPath resolves examples/demo-infra relative to this source file.
func demoPath(t *testing.T) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve source path")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "examples", "demo-infra")
}

func TestScanDemoInfra(t *testing.T) {
	res, err := Scan(demoPath(t))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	if got := len(res.Playbooks); got != 10 {
		t.Errorf("playbooks = %d, want 10", got)
	}
	if got := len(res.Roles); got != 12 {
		t.Errorf("roles = %d, want 12", got)
	}
	if got := len(res.Inventories); got != 2 {
		t.Errorf("inventories = %d, want 2", got)
	}

	// site.yml must resolve every import_playbook stage
	var foundSite bool
	for _, pb := range res.Playbooks {
		if pb.Path != "site.yml" {
			continue
		}
		foundSite = true
		if len(pb.Plays) != 8 {
			t.Errorf("site.yml plays = %d, want 8", len(pb.Plays))
		}
		for _, p := range pb.Plays {
			if p.Import == "" {
				t.Errorf("site.yml play %q: expected import_playbook entry", p.Name)
			}
		}
	}
	if !foundSite {
		t.Error("site.yml not detected as playbook")
	}

	// rolling-update must keep serial and loop/tags metadata
	for _, pb := range res.Playbooks {
		if pb.Path != "rolling-update.yml" {
			continue
		}
		play := pb.Plays[0]
		if play.Serial != "1" {
			t.Errorf("rolling-update serial = %q, want 1", play.Serial)
		}
		if len(play.PreTasks) == 0 || !play.PreTasks[0].Loop {
			t.Error("rolling-update pre_task drain should be a loop")
		}
	}

	// production inventory: groups, children, transitive memberships, vars
	for _, inv := range res.Inventories {
		if inv.Name != "production" {
			continue
		}
		if inv.Format != "ini" {
			t.Errorf("production format = %q, want ini", inv.Format)
		}
		if got := len(inv.Hosts); got != 11 {
			t.Errorf("production hosts = %d, want 11", got)
		}
		var web01 *struct{ groups []string }
		for _, h := range inv.Hosts {
			if h.Name == "web01" {
				web01 = &struct{ groups []string }{h.Groups}
				if h.Vars["ansible_host"] == nil {
					t.Error("web01 missing ansible_host var")
				}
			}
		}
		if web01 == nil {
			t.Fatal("web01 not found")
		}
		want := map[string]bool{"web": false, "frontend": false, "acme": false}
		for _, g := range web01.groups {
			if _, ok := want[g]; ok {
				want[g] = true
			}
		}
		for g, ok := range want {
			if !ok {
				t.Errorf("web01 missing transitive group %s (got %v)", g, web01.groups)
			}
		}
	}

	// role dependencies from meta/main.yml
	for _, r := range res.Roles {
		if r.Name == "docker_apps" {
			if len(r.Dependencies) == 0 || r.Dependencies[0] != "docker" {
				t.Errorf("docker_apps deps = %v, want [docker]", r.Dependencies)
			}
		}
	}

	// topology graph
	for _, inv := range res.Inventories {
		if inv.Name == "production" {
			topo := BuildTopology(&inv)
			if len(topo.Nodes) < 15 || len(topo.Links) < 15 {
				t.Errorf("topology too small: %d nodes %d links", len(topo.Nodes), len(topo.Links))
			}
		}
	}
}

func TestSummarizeArgs(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"banktransfer_documents_env_vars.yml", "banktransfer_documents_env_vars.yml"},
		{map[string]any{"msg": "{{ foo }}"}, "msg: {{ foo }}"},
		{map[string]any{"file": "x.yml", "name": "v"}, "file: x.yml, name: v"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := summarizeArgs(c.in); got != c.want {
			t.Errorf("summarizeArgs(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseTaskCapturesArgsAndPrompt(t *testing.T) {
	doc := `
- name: Prompted play
  hosts: private_runners
  vars_prompt:
    - name: config_var_name
      prompt: Which config?
      default: central
      private: no
  tasks:
    - include_vars: "config_vars/{{ config_var_name }}.yml"
    - name: Include documents vars
      include_vars: banktransfer_documents_env_vars.yml
`
	var entries []map[string]any
	if err := yaml.Unmarshal([]byte(doc), &entries); err != nil {
		t.Fatal(err)
	}
	play := parsePlay(entries[0])

	if len(play.VarsPrompt) != 1 || play.VarsPrompt[0].Name != "config_var_name" ||
		play.VarsPrompt[0].Default != "central" {
		t.Fatalf("vars_prompt not captured: %+v", play.VarsPrompt)
	}
	if play.Tasks[0].Args != "config_vars/{{ config_var_name }}.yml" {
		t.Errorf("task0 args = %q", play.Tasks[0].Args)
	}
	if play.Tasks[1].Args != "banktransfer_documents_env_vars.yml" {
		t.Errorf("task1 args = %q", play.Tasks[1].Args)
	}
	if play.Tasks[0].IncludePath != "config_vars/{{ config_var_name }}.yml" {
		t.Errorf("task0 include_path = %q", play.Tasks[0].IncludePath)
	}
	if play.Tasks[1].IncludePath != "banktransfer_documents_env_vars.yml" {
		t.Errorf("task1 include_path = %q", play.Tasks[1].IncludePath)
	}
}

func TestIncludePath(t *testing.T) {
	cases := []struct {
		module string
		value  any
		want   string
	}{
		{"include_vars", "foo.yml", "foo.yml"},
		{"ansible.builtin.include_tasks", "sub/tasks.yml", "sub/tasks.yml"},
		{"include_vars", map[string]any{"file": "x.yml", "name": "v"}, "x.yml"},
		{"include_role", "myrole", ""}, // role name, not a file
		{"debug", map[string]any{"msg": "hi"}, ""},
	}
	for _, c := range cases {
		if got := includePath(c.module, c.value); got != c.want {
			t.Errorf("includePath(%q, %v) = %q, want %q", c.module, c.value, got, c.want)
		}
	}
}

func TestExpandRange(t *testing.T) {
	got := expandRange("web[01:03]")
	if len(got) != 3 || got[0] != "web01" || got[2] != "web03" {
		t.Errorf("expandRange = %v", got)
	}
}
