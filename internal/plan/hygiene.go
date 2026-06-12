package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/scanner"
)

// Hygiene findings.
type UnusedRole struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}
type UnnotifiedHandler struct {
	Role   string `json:"role,omitempty"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}
type UnusedVar struct {
	Key       string `json:"key"`
	DefinedIn string `json:"defined_in"`
}
type UntargetedHost struct {
	Name      string `json:"name"`
	Inventory string `json:"inventory"`
	Reason    string `json:"reason"`
}
type SecretFinding struct {
	File     string `json:"file"`
	Key      string `json:"key"`
	Severity string `json:"severity"` // high | low
	Reason   string `json:"reason"`
	Hint     string `json:"hint"`
}

// HygieneResult is the dead-code + secrets report of one repository.
type HygieneResult struct {
	Score              int                 `json:"score"`
	UnusedRoles        []UnusedRole        `json:"unused_roles"`
	UnnotifiedHandlers []UnnotifiedHandler `json:"unnotified_handlers"`
	UnusedVars         []UnusedVar         `json:"unused_vars"`
	UntargetedHosts    []UntargetedHost    `json:"untargeted_hosts"`
	SecretFindings     []SecretFinding     `json:"secret_findings"`
	VaultFiles         int                 `json:"vault_files"`
}

var secretKeyRe = regexp.MustCompile(`(?i)(^|_)(pass(word|wd)?|secret|token|api_?key|access_?key|private_?key|credentials?)s?$`)

// server_tokens is the apache/nginx version-disclosure directive, not a
// credential, despite ending in "tokens"
var notSecretKeyRe = regexp.MustCompile(`(?i)server_+tokens?$`)

// Hygiene cross-references the scan result (plus the raw repo text for
// variable usage and vault detection) into a tidiness report.
func Hygiene(res *model.ScanResult, root string) *HygieneResult {
	out := &HygieneResult{
		UnusedRoles:        []UnusedRole{},
		UnnotifiedHandlers: []UnnotifiedHandler{},
		UnusedVars:         []UnusedVar{},
		UntargetedHosts:    []UntargetedHost{},
		SecretFindings:     []SecretFinding{},
	}

	// --- unused roles ---
	referenced := map[string]bool{}
	var markTask func(t model.Task)
	markTask = func(t model.Task) {
		m := strings.TrimPrefix(t.Module, "ansible.builtin.")
		if m == "include_role" || m == "import_role" {
			for i := range res.Roles {
				if strings.Contains(t.Args, res.Roles[i].Name) {
					referenced[res.Roles[i].Name] = true
				}
			}
		}
		for _, sub := range [][]model.Task{t.Block, t.Rescue, t.Always} {
			for _, st := range sub {
				markTask(st)
			}
		}
	}
	forEachPlayTask := func(fn func(t model.Task)) {
		for _, pb := range res.Playbooks {
			for _, play := range pb.Plays {
				for _, list := range [][]model.Task{play.PreTasks, play.Tasks, play.PostTasks, play.Handlers} {
					for _, t := range list {
						fn(t)
					}
				}
			}
		}
	}
	for _, pb := range res.Playbooks {
		for _, play := range pb.Plays {
			for _, rn := range play.Roles {
				referenced[rn] = true
			}
		}
	}
	forEachPlayTask(markTask)
	for _, r := range res.Roles {
		for _, t := range r.Tasks {
			markTask(t)
		}
	}
	// dependencies of referenced roles are referenced too (transitively)
	for changed := true; changed; {
		changed = false
		for _, r := range res.Roles {
			if !referenced[r.Name] {
				continue
			}
			for _, dep := range r.Dependencies {
				if !referenced[dep] {
					referenced[dep] = true
					changed = true
				}
			}
		}
	}
	for _, r := range res.Roles {
		if !referenced[r.Name] {
			out.UnusedRoles = append(out.UnusedRoles, UnusedRole{
				Name:   r.Name,
				Reason: "never referenced by any playbook, role dependency or include_role",
			})
		}
	}

	// --- unnotified handlers ---
	notified := map[string]bool{}
	collectNotify := func(t model.Task) {
		for _, n := range t.Notify {
			notified[n] = true
		}
	}
	forEachPlayTask(collectNotify)
	for _, r := range res.Roles {
		var walk func(ts []model.Task)
		walk = func(ts []model.Task) {
			for _, t := range ts {
				collectNotify(t)
				walk(t.Block)
				walk(t.Rescue)
				walk(t.Always)
			}
		}
		walk(r.Tasks)
	}
	// templated notifies ("Restart {{ item }}") can hit any handler of the
	// role at runtime: skip handler analysis for those roles entirely
	templatedNotify := map[string]bool{}
	for _, r := range res.Roles {
		var walk func(ts []model.Task)
		walk = func(ts []model.Task) {
			for _, t := range ts {
				for _, n := range t.Notify {
					if strings.Contains(n, "{{") {
						templatedNotify[r.Name] = true
					}
				}
				walk(t.Block)
				walk(t.Rescue)
				walk(t.Always)
			}
		}
		walk(r.Tasks)
	}
	checkHandler := func(role string, h model.Task) {
		if notified[h.Name] || (h.Listen != "" && notified[h.Listen]) {
			return
		}
		out.UnnotifiedHandlers = append(out.UnnotifiedHandlers, UnnotifiedHandler{
			Role: role, Name: h.Name,
			Reason: "no task notifies it (listen topics included)",
		})
	}
	for _, r := range res.Roles {
		if !referenced[r.Name] || templatedNotify[r.Name] {
			continue // unused role, or notifies resolved only at runtime
		}
		for _, h := range r.Handlers {
			checkHandler(r.Name, h)
		}
	}
	for _, pb := range res.Playbooks {
		for _, play := range pb.Plays {
			for _, h := range play.Handlers {
				checkHandler("", h)
			}
		}
	}

	// --- unused vars + secrets (shared candidate walk) ---
	type candidate struct {
		key       string
		value     any
		definedIn string
	}
	var candidates []candidate
	for _, inv := range res.Inventories {
		for _, g := range inv.Groups {
			for k, v := range g.Vars {
				candidates = append(candidates, candidate{k, v, fmt.Sprintf("group_vars/%s (%s)", g.Name, inv.Name)})
			}
		}
		for _, h := range inv.Hosts {
			for k, v := range h.Vars {
				candidates = append(candidates, candidate{k, v, fmt.Sprintf("host_vars/%s (%s)", h.Name, inv.Name)})
			}
		}
	}
	for _, r := range res.Roles {
		for k, v := range r.Defaults {
			candidates = append(candidates, candidate{k, v, "defaults (role " + r.Name + ")"})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].key != candidates[j].key {
			return candidates[i].key < candidates[j].key
		}
		return candidates[i].definedIn < candidates[j].definedIn
	})

	blob, vaultFiles := repoText(root)
	out.VaultFiles = vaultFiles
	seenUnused := map[string]bool{}
	seenSecret := map[string]bool{}
	for _, c := range candidates {
		// secrets: suspicious key with a plaintext scalar value
		if s, isStr := c.value.(string); isStr && secretKeyRe.MatchString(c.key) &&
			!notSecretKeyRe.MatchString(c.key) && !seenSecret[c.key+c.definedIn] {
			seenSecret[c.key+c.definedIn] = true
			if looksLikeSecretValue(s) {
				sev, reason := "high", "password-like key with a plaintext value"
				if strings.Contains(strings.ToUpper(s), "CHANGEME") {
					sev, reason = "low", "password-like key with a placeholder value"
				}
				out.SecretFindings = append(out.SecretFindings, SecretFinding{
					File: c.definedIn, Key: c.key, Severity: sev, Reason: reason,
					Hint: "encrypt with ansible-vault",
				})
			}
		}
		// unused: key never appears outside its own definition lines
		if seenUnused[c.key] {
			continue
		}
		seenUnused[c.key] = true
		if isMagicish(c.key) {
			continue
		}
		if !blob.usedOutsideDefinition(c.key) {
			out.UnusedVars = append(out.UnusedVars, UnusedVar{Key: c.key, DefinedIn: c.definedIn})
		}
	}

	// --- untargeted hosts ---
	if len(res.Playbooks) > 0 {
		for i := range res.Inventories {
			inv := &res.Inventories[i]
			targeted := map[string]bool{}
			for _, pb := range res.Playbooks {
				for _, play := range pb.Plays {
					if play.Import != "" {
						continue
					}
					for _, h := range scanner.MatchHosts(play.Hosts, inv) {
						targeted[h] = true
					}
				}
			}
			for _, h := range inv.Hosts {
				if !targeted[h.Name] {
					out.UntargetedHosts = append(out.UntargetedHosts, UntargetedHost{
						Name: h.Name, Inventory: inv.Name,
						Reason: "no playbook targets any of its groups",
					})
				}
			}
		}
	}

	// --- score ---
	score := 100
	score -= 5 * len(out.UnusedRoles)
	score -= 3 * len(out.UnnotifiedHandlers)
	score -= 1 * len(out.UnusedVars)
	score -= 3 * len(out.UntargetedHosts)
	for _, f := range out.SecretFindings {
		if f.Severity == "high" {
			score -= 10
		} else {
			score -= 2
		}
	}
	out.Score = max(0, score)
	return out
}

// looksLikeSecretValue filters out values that cannot be secrets even when
// the key name is password-like: templated/vaulted references, booleans
// (nginx_server_tokens: "off"), and trivially short toggles.
func looksLikeSecretValue(s string) bool {
	if s == "" || strings.Contains(s, "{{") || strings.HasPrefix(s, "$ANSIBLE_VAULT") {
		return false
	}
	switch strings.ToLower(s) {
	case "on", "off", "true", "false", "yes", "no", "none", "null",
		"enabled", "disabled", "present", "absent":
		return false
	}
	return len(s) >= 6
}

// ansible_* and connection vars look unused in repo text but drive runtime
var magicishPrefixes = []string{"ansible_", "vault_"}

func isMagicish(key string) bool {
	for _, p := range magicishPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// textIndex counts identifier tokens across the repo's text files:
// counts = every occurrence, defs = occurrences as a "key:" / "key ="
// definition. Built once, so unused-var checks are O(1) per key instead
// of rescanning every line (15s -> ms on debops-sized repos).
type textIndex struct {
	counts map[string]int
	defs   map[string]int
}

const maxHygieneFile = 256 * 1024

var textExts = map[string]bool{
	".yml": true, ".yaml": true, ".j2": true, ".cfg": true, ".conf": true,
	".ini": true, ".sh": true, ".service": true, ".timer": true, ".env": true,
	".json": true, ".toml": true,
}

// repoText indexes all small text files; also counts vault-encrypted files.
func repoText(root string) (*textIndex, int) {
	idx := &textIndex{counts: map[string]int{}, defs: map[string]int{}}
	vault := 0
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == ".venv" {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() > maxHygieneFile {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if !textExts[ext] && filepath.Base(p) != "hosts" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		s := string(data)
		if strings.Contains(s, "$ANSIBLE_VAULT") {
			vault++
		}
		for _, line := range strings.Split(s, "\n") {
			idx.indexLine(line)
		}
		return nil
	})
	return idx, vault
}

// indexLine tokenizes one line into identifiers and records whether the
// leading token is a definition ("key:" / "key =").
func (idx *textIndex) indexLine(line string) {
	first := true
	i := 0
	for i < len(line) {
		c := line[i]
		if isWordStart(c) {
			j := i + 1
			for j < len(line) && isWordChar(line[j]) {
				j++
			}
			tok := line[i:j]
			idx.counts[tok]++
			if first {
				// definition shape: optional spaces then ':' or '='
				k := j
				for k < len(line) && line[k] == ' ' {
					k++
				}
				if k < len(line) && (line[k] == ':' || line[k] == '=') {
					idx.defs[tok]++
				}
			}
			first = false
			i = j
			continue
		}
		if c != ' ' && c != '\t' && c != '-' {
			first = false
		}
		i++
	}
}

func isWordStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

func isWordChar(c byte) bool {
	return isWordStart(c) || c >= '0' && c <= '9'
}

// usedOutsideDefinition reports whether key appears anywhere beyond its
// own "key:" definition lines.
func (idx *textIndex) usedOutsideDefinition(key string) bool {
	return idx.counts[key] > idx.defs[key]
}
