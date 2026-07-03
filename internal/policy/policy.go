// Package policy is Pine's policy-as-code engine: a small, declarative set of
// governance rules (YAML) evaluated against an estimated plan (plan.Result) —
// the OPA/Sentinel of Ansible. Rules match tasks/hosts (module, tags, inventory
// group, ...) and assert constraints (forbid a module, require an approval tag,
// forbid dangerous args like state: latest, cap the blast radius). It is meant
// to run as a CI gate: `error` violations fail the build, `warning`s are only
// reported.
package policy

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Severity gates behaviour: error violations fail the CI gate, warnings do not.
type Severity string

// Severity levels.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Match narrows which task/host verdicts a policy applies to. An empty Match
// field is a wildcard; all set fields must hold (logical AND) for a task to be
// in scope. Host-scoped fields (Hosts, Groups) further narrow to specific hosts.
type Match struct {
	// Inventory is a glob on the plan's inventory (name or path), e.g.
	// "*production*". When set and it does not match, the whole policy is skipped.
	Inventory string `yaml:"inventory,omitempty"`
	// Hosts is a glob on the host name, e.g. "web*".
	Hosts string `yaml:"hosts,omitempty"`
	// Groups: the host must belong to at least one of these inventory groups.
	Groups []string `yaml:"groups,omitempty"`
	// Module: the task's module (matched on the short name, so "apt" matches
	// "ansible.builtin.apt"). Any listed module matches (logical OR).
	Module []string `yaml:"module,omitempty"`
	// Tags: the task must carry at least one of these tags to be in scope.
	Tags []string `yaml:"tags,omitempty"`
	// TaskNameRegex: the (resolved) task name must match this regexp.
	TaskNameRegex string `yaml:"task_name_regex,omitempty"`
	// Become: match only plays whose privilege escalation matches this value
	// (become: true is play-level in Ansible).
	Become *bool `yaml:"become,omitempty"`
}

// Assert is the constraint a matched task/host must satisfy. Exactly one kind of
// assertion is expected per policy; when several are set they are all checked.
type Assert struct {
	// Forbid: any matched task/host is a violation (a blanket ban).
	Forbid bool `yaml:"forbid,omitempty"`
	// RequireTag: the matched task must carry this tag (e.g. "approved").
	RequireTag string `yaml:"require_tag,omitempty"`
	// ForbidArgs: the matched task must not set these module args to these
	// values (e.g. {state: latest}). A value of "*" forbids the key with any
	// value.
	ForbidArgs map[string]string `yaml:"forbid_args,omitempty"`
	// MaxBlastRadiusPct: the plan (or the git-diff impact, when provided) must
	// touch no more than N% of the inventory's hosts. This is a plan-level
	// assertion evaluated once, not per task.
	MaxBlastRadiusPct int `yaml:"max_blast_radius_pct,omitempty"`
}

// Policy is one governance rule.
type Policy struct {
	ID          string   `yaml:"id"`
	Description string   `yaml:"description,omitempty"`
	Severity    Severity `yaml:"severity,omitempty"` // defaults to error
	Match       Match    `yaml:"match,omitempty"`
	Assert      Assert   `yaml:"assert"`

	nameRE *regexp.Regexp // compiled TaskNameRegex (Load only)
}

// file is the on-disk shape: a top-level `policies:` list.
type file struct {
	Policies []Policy `yaml:"policies"`
}

// Load reads and validates a policy file. It defaults severity to error,
// lower-cases severities and compiles task_name_regex up front so an invalid
// rule fails loudly instead of silently at evaluation time.
func Load(path string) ([]Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse loads policies from YAML bytes (Load without the file read; handy for
// tests).
func Parse(data []byte) ([]Policy, error) {
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse policies: %w", err)
	}
	for i := range f.Policies {
		p := &f.Policies[i]
		if p.ID == "" {
			return nil, fmt.Errorf("policy #%d: missing id", i+1)
		}
		switch p.Severity {
		case "":
			p.Severity = SeverityError
		case SeverityError, SeverityWarning:
		default:
			p.Severity = Severity(strings.ToLower(string(p.Severity)))
			if p.Severity != SeverityError && p.Severity != SeverityWarning {
				return nil, fmt.Errorf("policy %s: invalid severity %q (want error|warning)", p.ID, p.Severity)
			}
		}
		if p.Match.TaskNameRegex != "" {
			re, err := regexp.Compile(p.Match.TaskNameRegex)
			if err != nil {
				return nil, fmt.Errorf("policy %s: bad task_name_regex: %w", p.ID, err)
			}
			p.nameRE = re
		}
	}
	return f.Policies, nil
}
