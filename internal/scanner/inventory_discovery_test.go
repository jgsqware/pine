package scanner

import (
	"path/filepath"
	"testing"

	"github.com/jgsqware/pine/internal/model"
)

func findHost(hosts []model.Host, name string) *model.Host {
	for i := range hosts {
		if hosts[i].Name == name {
			return &hosts[i]
		}
	}
	return nil
}

func hasGroup(groups []model.Group, name string) bool {
	for _, g := range groups {
		if g.Name == name {
			return true
		}
	}
	return false
}

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

// From a user report: the inventory is a root-level file named `production`
// with NO extension whose content is YAML, pointed at by ansible.cfg's
// `inventory = ./production`. It must be discovered by content, named after
// its stem, while sibling non-inventory files (mkdocs.yml, README) are ignored.
func TestScanRootExtensionlessYAMLInventory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "production"), `
azure:
  hosts:
    we-d-ppvs-id-tc-agent-5:
      ansible_host: 10.170.135.4
  vars:
    platform: azure
windows_hosts:
  children:
    tc_agents:
`)
	// decoys that must not be mistaken for inventories
	writeFile(t, filepath.Join(root, "ansible.cfg"), "[defaults]\ninventory = ./production\n")
	writeFile(t, filepath.Join(root, "mkdocs.yml"), "site_name: Docs\nnav:\n  - Home: index.md\n")
	writeFile(t, filepath.Join(root, "README"), "adaptInsight AutoTest Infrastructure\n")

	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Inventories) != 1 {
		t.Fatalf("inventories = %d, want 1 (%+v)", len(res.Inventories), res.Inventories)
	}
	inv := res.Inventories[0]
	if inv.Name != "production" {
		t.Errorf("name = %q, want production", inv.Name)
	}
	if inv.Format != "yaml" {
		t.Errorf("format = %q, want yaml", inv.Format)
	}
	if findHost(inv.Hosts, "we-d-ppvs-id-tc-agent-5") == nil {
		t.Errorf("host we-d-ppvs-id-tc-agent-5 not found: %+v", inv.Hosts)
	}
	if !hasGroup(inv.Groups, "azure") || !hasGroup(inv.Groups, "windows_hosts") {
		t.Errorf("expected groups azure + windows_hosts, got %+v", inv.Groups)
	}
}

// An extensionless file that parses as a YAML map but has no inventory shape
// must NOT be picked up (guards against false positives now that extensionless
// files reach the content sniff).
func TestScanExtensionlessNonInventoryIgnored(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes"), "title: hello\nauthor: me\n")

	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Inventories) != 0 {
		t.Fatalf("inventories = %d, want 0 (%+v)", len(res.Inventories), res.Inventories)
	}
}

// The excluded-stem case must still be evaluated before the extensionless
// content sniff: a file literally named `ansible` is config, not an inventory,
// even if its content happens to look inventory-shaped.
func TestScanExtensionlessAnsibleStemExcluded(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ansible"), `
web:
  hosts:
    web01:
`)
	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Inventories) != 0 {
		t.Fatalf("inventories = %d, want 0 (%+v)", len(res.Inventories), res.Inventories)
	}
}

// Boundary of the content-sniff fix: an extensionless INI inventory with an
// arbitrary name is NOT discovered (INI never unmarshals into a YAML map), but
// the same INI content in an extensionless `hosts` file IS, via the stem rule.
// Catching arbitrarily-named extensionless INI would require honoring
// ansible.cfg's `inventory =` (a deferred follow-up).
func TestScanExtensionlessINIBoundary(t *testing.T) {
	iniBody := "[web]\nweb01\n"

	staging := t.TempDir()
	writeFile(t, filepath.Join(staging, "staging"), iniBody)
	res, err := Scan(staging)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Inventories) != 0 {
		t.Fatalf("arbitrarily-named extensionless INI: inventories = %d, want 0", len(res.Inventories))
	}

	hosts := t.TempDir()
	writeFile(t, filepath.Join(hosts, "hosts"), iniBody)
	res, err = Scan(hosts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Inventories) != 1 || res.Inventories[0].Format != "ini" {
		t.Fatalf("extensionless `hosts` INI: got %+v, want 1 ini inventory", res.Inventories)
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
