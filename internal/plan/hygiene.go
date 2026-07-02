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

// Smell is a task-level anti-pattern, grouped by rule across the whole repo so
// a repo with 400 unnamed tasks yields one finding with a count, not 400 rows.
type Smell struct {
	Rule     string `json:"rule"`     // stable id, e.g. "unnamed-task"
	Severity string `json:"severity"` // medium | low
	Detail   string `json:"detail"`   // what & why
	Where    string `json:"where"`    // first occurrence
	Count    int    `json:"count"`    // total occurrences of this rule
}

// HygieneResult is the dead-code + secrets report of one repository.
type HygieneResult struct {
	Score              int                 `json:"score"`
	UnusedRoles        []UnusedRole        `json:"unused_roles"`
	UnnotifiedHandlers []UnnotifiedHandler `json:"unnotified_handlers"`
	UnusedVars         []UnusedVar         `json:"unused_vars"`
	UntargetedHosts    []UntargetedHost    `json:"untargeted_hosts"`
	SecretFindings     []SecretFinding     `json:"secret_findings"`
	Smells             []Smell             `json:"smells"`
	VaultFiles         int                 `json:"vault_files"`
}

// secretKeyRe flags variable names that hold secrets: the usual password/token/
// key/credential suffixes, plus the ansible-vault naming convention (a
// `vault_`-prefixed variable is a secret by definition — a plaintext value under
// one is itself a hygiene smell).
var secretKeyRe = regexp.MustCompile(`(?i)(^vault_|(^|_)(pass(word|wd|phrase)?|secret|token|api_?key|access_?key|private_?key|credentials?)s?$)`)

// server_tokens is the apache/nginx version-disclosure directive, not a
// credential, despite ending in "tokens"
var notSecretKeyRe = regexp.MustCompile(`(?i)server_+tokens?$`)

// IsSecretKey reports whether a variable name looks like it holds a secret
// (password/token/key/credential suffix, or the vault_ convention), excluding
// known false positives like server_tokens. Callers use it to avoid persisting
// or displaying secret-looking values.
func IsSecretKey(key string) bool {
	return secretKeyRe.MatchString(key) && !notSecretKeyRe.MatchString(key)
}

// Hygiene cross-references the scan result (plus the raw repo text for
// variable usage and vault detection) into a tidiness report.
func Hygiene(res *model.ScanResult, root string) *HygieneResult {
	out := &HygieneResult{
		UnusedRoles:        []UnusedRole{},
		UnnotifiedHandlers: []UnnotifiedHandler{},
		UnusedVars:         []UnusedVar{},
		UntargetedHosts:    []UntargetedHost{},
		SecretFindings:     []SecretFinding{},
		Smells:             []Smell{},
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

	// --- task smells ---
	out.Smells = detectSmells(res)

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
	for _, sm := range out.Smells {
		// grouped: penalize the rule once (bounded) plus a little per occurrence
		pen := 1
		if sm.Severity == "medium" {
			pen = 3
		}
		score -= pen + min(sm.Count/10, 5)
	}
	out.Score = max(0, score)
	return out
}

// smellAcc accumulates occurrences of one rule while keeping the first location.
type smellAcc struct {
	severity string
	detail   string
	where    string
	count    int
}

// moduleFor maps a shell/command's leading executable to the native Ansible
// module that should replace it (the classic command-instead-of-module smell).
var moduleFor = map[string]string{
	"apt": "ansible.builtin.apt", "apt-get": "ansible.builtin.apt", "aptitude": "ansible.builtin.apt",
	"yum": "ansible.builtin.yum", "dnf": "ansible.builtin.dnf",
	"systemctl": "ansible.builtin.systemd", "service": "ansible.builtin.service",
	"pip": "ansible.builtin.pip", "pip3": "ansible.builtin.pip",
	"useradd": "ansible.builtin.user", "usermod": "ansible.builtin.user", "userdel": "ansible.builtin.user",
	"groupadd": "ansible.builtin.group", "groupdel": "ansible.builtin.group",
	"curl": "ansible.builtin.get_url", "wget": "ansible.builtin.get_url",
	"git": "ansible.builtin.git", "unzip": "ansible.builtin.unarchive",
	"mkdir": "ansible.builtin.file", "rm": "ansible.builtin.file", "chown": "ansible.builtin.file",
	"chmod": "ansible.builtin.file", "ln": "ansible.builtin.file",
}

// detectSmells walks every task in the repo (playbooks + roles, recursing into
// blocks) and groups task-level anti-patterns by rule.
func detectSmells(res *model.ScanResult) []Smell {
	acc := map[string]*smellAcc{}
	add := func(rule, severity, detail, where string) {
		a := acc[rule]
		if a == nil {
			a = &smellAcc{severity: severity, detail: detail, where: where}
			acc[rule] = a
		}
		a.count++
	}

	var visit func(t model.Task, where string)
	visit = func(t model.Task, where string) {
		if t.Module == "block" {
			for _, sub := range [][]model.Task{t.Block, t.Rescue, t.Always} {
				for _, st := range sub {
					visit(st, where)
				}
			}
			return
		}
		loc := where + " › " + taskLabel(t)
		mod := strings.TrimPrefix(t.Module, "ansible.builtin.")

		if t.Unnamed && mod != "meta" && mod != "debug" {
			add("unnamed-task", "low",
				"task has no name: — harder to read logs and to --start-at-task", loc)
		}
		if t.IgnoreErrors {
			add("ignore-errors", "medium",
				"ignore_errors: true hides real failures; prefer failed_when or a rescue block", loc)
		}
		if mod == "include" {
			add("deprecated-include", "medium",
				"bare `include:` is deprecated — use include_tasks / import_tasks", loc)
		}
		if strings.Contains(t.When, "{{") {
			add("jinja-in-when", "low",
				"when: wrapped in {{ }} — write a bare expression (when: my_var), Jinja is implicit", loc)
		}
		if mod == "command" || mod == "shell" || mod == "raw" {
			cmd := commandText(t.Args)
			if alt := moduleFor[firstWord(cmd)]; alt != "" {
				add("command-instead-of-module", "medium",
					"runs `"+firstWord(cmd)+"` via "+mod+" — use the "+alt+" module (idempotent, check-mode aware)", loc)
			} else if !t.HasChangedWhen && !strings.Contains(t.Args, "creates") && !strings.Contains(t.Args, "removes") {
				add("shell-without-changed-when", "low",
					mod+" always reports changed — set changed_when (or creates/removes) for honest idempotency", loc)
			}
		}
		if packageModule(mod) && strings.Contains(strings.ToLower(t.Args), "latest") {
			add("package-latest", "low",
				"state: latest makes runs non-reproducible — pin a version or use state: present", loc)
		}
	}

	for _, pb := range res.Playbooks {
		for _, play := range pb.Plays {
			for _, list := range [][]model.Task{play.PreTasks, play.Tasks, play.PostTasks, play.Handlers} {
				for _, t := range list {
					visit(t, pb.Path)
				}
			}
		}
	}
	for _, r := range res.Roles {
		for _, list := range [][]model.Task{r.Tasks, r.Handlers} {
			for _, t := range list {
				visit(t, "role "+r.Name)
			}
		}
	}

	out := make([]Smell, 0, len(acc))
	for rule, a := range acc {
		out = append(out, Smell{Rule: rule, Severity: a.severity, Detail: a.detail, Where: a.where, Count: a.count})
	}
	// stable order: medium before low, then by count desc, then rule name
	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == "medium"
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Rule < out[j].Rule
	})
	return out
}

// taskLabel renders a short, human location for a task.
func taskLabel(t model.Task) string {
	if !t.Unnamed && t.Name != "" {
		return t.Name
	}
	if t.Args != "" {
		a := t.Args
		if len(a) > 40 {
			a = a[:40] + "…"
		}
		return t.Module + ": " + a
	}
	return t.Module
}

// commandText pulls the command string out of a command/shell arg summary,
// which is either the raw command or a "cmd: …, creates: …" rendering.
func commandText(args string) string {
	if i := strings.Index(args, "cmd:"); i >= 0 {
		rest := args[i+4:]
		if c := strings.IndexByte(rest, ','); c >= 0 {
			rest = rest[:c]
		}
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(args)
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			return s[:i]
		}
	}
	return s
}

func packageModule(mod string) bool {
	switch mod {
	case "apt", "yum", "dnf", "package", "pip", "npm", "gem", "homebrew":
		return true
	}
	return false
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
