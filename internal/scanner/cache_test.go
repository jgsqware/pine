package scanner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeRepo lays down a tiny but real Ansible repo (one playbook + one role)
// and returns its root. Timestamps are backdated so a later os.Chtimes with
// "now" reliably registers as a change even on coarse-grained filesystems.
func writeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	mkdir := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		return p
	}
	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		old := time.Now().Add(-time.Hour)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}

	write(filepath.Join(root, "site.yml"), `---
- hosts: all
  roles:
    - web
  tasks:
    - name: ping
      ansible.builtin.ping:
`)

	roleTasks := mkdir("roles", "web", "tasks")
	write(filepath.Join(roleTasks, "main.yml"), `---
- name: install nginx
  ansible.builtin.package:
    name: nginx
`)
	roleDefaults := mkdir("roles", "web", "defaults")
	write(filepath.Join(roleDefaults, "main.yml"), "---\nweb_port: 80\n")

	return root
}

// touch rewrites a file with new content and stamps it "now" so its mtime and
// size differ from the backdated original.
func touch(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("rewrite %s: %v", path, err)
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func TestScanCache_ResyncNoChangeReparsesNothing(t *testing.T) {
	root := writeRepo(t)
	cache := NewScanCache()

	res1, err := ScanWithCache(root, cache)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	first := cache.Parses()
	if first == 0 {
		t.Fatal("first scan should have parsed something (counter still 0)")
	}
	if len(res1.Playbooks) != 1 || len(res1.Roles) != 1 {
		t.Fatalf("first scan: playbooks=%d roles=%d, want 1/1", len(res1.Playbooks), len(res1.Roles))
	}

	// Re-scan with no filesystem change: every worklist item must hit.
	if _, err := ScanWithCache(root, cache); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if delta := cache.Parses() - first; delta != 0 {
		t.Errorf("re-sync without change re-parsed %d items, want 0", delta)
	}
}

func TestScanCache_ModifiedFileOnlyReparsesIt(t *testing.T) {
	root := writeRepo(t)
	cache := NewScanCache()

	if _, err := ScanWithCache(root, cache); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	before := cache.Parses()

	// Change only the playbook file.
	touch(t, filepath.Join(root, "site.yml"), `---
- hosts: web
  tasks:
    - name: ping again
      ansible.builtin.ping:
`)

	res, err := ScanWithCache(root, cache)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if delta := cache.Parses() - before; delta != 1 {
		t.Errorf("after modifying one file, re-parsed %d items, want exactly 1", delta)
	}
	// And the new content is reflected (not the stale cached play).
	if len(res.Playbooks) != 1 || res.Playbooks[0].Plays[0].Hosts != "web" {
		t.Errorf("modified playbook not reflected: %+v", res.Playbooks)
	}
}

func TestScanCache_ModifiedRoleFileReparsesRole(t *testing.T) {
	root := writeRepo(t)
	cache := NewScanCache()

	if _, err := ScanWithCache(root, cache); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	before := cache.Parses()

	// Change a file *inside* the role's tree (not the role dir itself).
	touch(t, filepath.Join(root, "roles", "web", "defaults", "main.yml"), "---\nweb_port: 8080\n")

	res, err := ScanWithCache(root, cache)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if delta := cache.Parses() - before; delta != 1 {
		t.Errorf("after modifying a role file, re-parsed %d items, want exactly 1 (the role)", delta)
	}
	if len(res.Roles) != 1 || res.Roles[0].Defaults["web_port"] != 8080 {
		t.Errorf("modified role defaults not reflected: %+v", res.Roles)
	}
}

func TestScanCache_DeletedFileIsPurged(t *testing.T) {
	root := writeRepo(t)
	cache := NewScanCache()

	if _, err := ScanWithCache(root, cache); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	pbPath := filepath.Join(root, "site.yml")
	if _, ok := cache.entries.Load(pbPath); !ok {
		t.Fatalf("playbook not cached after first scan")
	}

	// Delete the playbook and re-scan: its cache entry must be purged so a
	// stale result can never be served.
	if err := os.Remove(pbPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	res, err := ScanWithCache(root, cache)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if len(res.Playbooks) != 0 {
		t.Errorf("deleted playbook still returned: %+v", res.Playbooks)
	}
	if _, ok := cache.entries.Load(pbPath); ok {
		t.Errorf("deleted playbook still present in cache")
	}
}

func TestScanCache_NilCacheMatchesPlainScan(t *testing.T) {
	root := writeRepo(t)

	plain, err := Scan(root)
	if err != nil {
		t.Fatalf("plain scan: %v", err)
	}
	nilCache, err := ScanWithCache(root, nil)
	if err != nil {
		t.Fatalf("nil-cache scan: %v", err)
	}
	if len(plain.Playbooks) != len(nilCache.Playbooks) || len(plain.Roles) != len(nilCache.Roles) {
		t.Errorf("nil cache diverged from Scan: %d/%d vs %d/%d",
			len(plain.Playbooks), len(plain.Roles), len(nilCache.Playbooks), len(nilCache.Roles))
	}
}
