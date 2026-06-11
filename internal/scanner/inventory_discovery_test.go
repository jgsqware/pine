package scanner

import (
	"path/filepath"
	"testing"
)

// Layout from a user report: inventories/ holds hosts.yml, group_vars/ and
// host_vars/ directly, with no per-environment subdirectory.
func TestScanFlatInventoriesDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "inventories/hosts.yml"), `
all:
  children:
    web:
      hosts:
        web01:
          ansible_host: 10.1.0.1
    db:
      hosts:
        db01:
          ansible_host: 10.1.0.9
`)
	writeFile(t, filepath.Join(root, "inventories/group_vars/web.yml"), "nginx_port: 8080\n")
	writeFile(t, filepath.Join(root, "inventories/host_vars/web01.yml"), "role_color: blue\n")

	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Inventories) != 1 {
		t.Fatalf("inventories = %d, want 1 (%+v)", len(res.Inventories), res.Inventories)
	}
	inv := res.Inventories[0]
	if inv.Name != "default" {
		t.Errorf("name = %q, want default", inv.Name)
	}
	if inv.Format != "yaml" || len(inv.Hosts) != 2 {
		t.Errorf("format=%q hosts=%d, want yaml/2", inv.Format, len(inv.Hosts))
	}
	for _, g := range inv.Groups {
		if g.Name == "web" && g.Vars["nginx_port"] != 8080 {
			t.Errorf("group_vars not merged: %v", g.Vars)
		}
	}
	for _, h := range inv.Hosts {
		if h.Name == "web01" && h.Vars["role_color"] != "blue" {
			t.Errorf("host_vars not merged: %v", h.Vars)
		}
	}
}

// File-per-environment layout: inventories/production.yaml + staging.yml,
// each its own inventory, named after the file stem; -i must point at the
// file since the directory holds several sources.
func TestScanFilePerEnvInventories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "inventories/production.yaml"), `
web:
  hosts:
    prod-web01:
`)
	writeFile(t, filepath.Join(root, "inventories/staging.yml"), `
web:
  hosts:
    stg-web01:
`)
	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Inventories) != 2 {
		t.Fatalf("inventories = %d, want 2 (%+v)", len(res.Inventories), res.Inventories)
	}
	byName := map[string]string{}
	for _, inv := range res.Inventories {
		byName[inv.Name] = inv.Path
	}
	if byName["production"] != "inventories/production.yaml" {
		t.Errorf("production path = %q", byName["production"])
	}
	if byName["staging"] != "inventories/staging.yml" {
		t.Errorf("staging path = %q", byName["staging"])
	}
}

// Inventory nested below the repo root (ansible/inventory/hosts, plain YAML
// without extension) must be discovered recursively and detected by content.
func TestScanNestedInventoryDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ansible/inventory/hosts"), `
all:
  children:
    runners:
      hosts:
        runner01:
`)
	writeFile(t, filepath.Join(root, "ansible/inventory/group_vars/runners.yml"), "speed: fast\n")

	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Inventories) != 1 {
		t.Fatalf("inventories = %d, want 1", len(res.Inventories))
	}
	inv := res.Inventories[0]
	if inv.Format != "yaml" || len(inv.Hosts) != 1 || inv.Hosts[0].Name != "runner01" {
		t.Errorf("unexpected inventory: %+v", inv)
	}
}

// A dir with group_vars/host_vars but no conventional name is still an
// inventory location; .yaml hosts files are equivalent to .yml.
func TestScanVarsDirSignalAndYamlExt(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "infra/hosts.yaml"), `
lb:
  hosts:
    lb01:
`)
	writeFile(t, filepath.Join(root, "infra/group_vars/lb.yml"), "vip: 10.0.0.100\n")

	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Inventories) != 1 {
		t.Fatalf("inventories = %d, want 1 (%+v)", len(res.Inventories), res.Inventories)
	}
	if res.Inventories[0].Name != "infra" {
		t.Errorf("name = %q, want infra", res.Inventories[0].Name)
	}
}
