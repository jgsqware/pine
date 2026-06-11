package plan

// FactProfile is a built-in preset of common ansible facts, letting plans
// resolve OS-dependent conditionals without running ansible.
type FactProfile struct {
	ID    string         `json:"id"`
	Label string         `json:"label"`
	Facts map[string]any `json:"-"`
}

// osFacts builds the usual fact set for one OS, both the modern
// ansible_facts.* namespace and the legacy ansible_* top-level aliases.
func osFacts(family, distribution, version, pkgMgr, serviceMgr string) map[string]any {
	facts := map[string]any{
		"os_family":            family,
		"distribution":         distribution,
		"distribution_version": version,
		"pkg_mgr":              pkgMgr,
		"service_mgr":          serviceMgr,
		"system":               "Linux",
	}
	return map[string]any{
		"ansible_facts":                facts,
		"ansible_os_family":            family,
		"ansible_distribution":         distribution,
		"ansible_distribution_version": version,
		"ansible_pkg_mgr":              pkgMgr,
		"ansible_service_mgr":          serviceMgr,
		"ansible_system":               "Linux",
	}
}

// Profiles lists the built-in fact profiles.
var Profiles = []FactProfile{
	{ID: "ubuntu-24.04", Label: "Ubuntu 24.04 LTS", Facts: osFacts("Debian", "Ubuntu", "24.04", "apt", "systemd")},
	{ID: "ubuntu-22.04", Label: "Ubuntu 22.04 LTS", Facts: osFacts("Debian", "Ubuntu", "22.04", "apt", "systemd")},
	{ID: "debian-12", Label: "Debian 12 (Bookworm)", Facts: osFacts("Debian", "Debian", "12", "apt", "systemd")},
	{ID: "rhel-9", Label: "RHEL / Rocky / Alma 9", Facts: osFacts("RedHat", "RedHat", "9.4", "dnf", "systemd")},
	{ID: "fedora-40", Label: "Fedora 40", Facts: osFacts("RedHat", "Fedora", "40", "dnf", "systemd")},
	{ID: "alpine-3.20", Label: "Alpine 3.20", Facts: osFacts("Alpine", "Alpine", "3.20", "apk", "openrc")},
}

// ProfileByID returns the profile facts, or nil for "" / unknown ids.
func ProfileByID(id string) map[string]any {
	for _, p := range Profiles {
		if p.ID == id {
			return p.Facts
		}
	}
	return nil
}
