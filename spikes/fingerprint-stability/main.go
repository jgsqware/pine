// Spike: how stable — and how blind — is the schedule-gating plan
// fingerprint under realistic repo mutations?
//
// Feeds pillar 2 of docs/design/state-machine-counter-analysis.md.
// plan.Fingerprint hashes: play name/hosts/batch count, task
// role/rawname/module, per-host verdict status. It does NOT hash module
// args, templated values, loop items or connection details. This spike
// applies benign and behaviour-changing mutations to a copy of
// examples/demo-infra and reports, for each, whether a gated schedule
// would block (fingerprint changed) — and whether that is the *right*
// outcome.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
	"github.com/jgsqware/pine/internal/scanner"
)

const inventory = "inventories/production/hosts.ini"

type scenario struct {
	name     string // short id
	playbook string // playbook whose fingerprint is gated
	profile  string // fact profile ("" = ubuntu-24.04 default below)
	file     string // file to mutate (relative), "" = no mutation
	old, new string // string replacement
	// expectation
	behaviourChanges bool   // does the mutation change what apply would do?
	comment          string // one-line analysis
}

var scenarios = []scenario{
	{name: "comment-only", playbook: "backup.yml",
		file: "backup.yml", old: "---", new: "---\n# audit annotation, zero behaviour change",
		behaviourChanges: false, comment: "cosmetic edit"},
	{name: "task-rename", playbook: "monitoring.yml",
		file: "monitoring.yml",
		old:  "name: Pull the Grafana Alloy agent image from the local registry",
		new:  "name: Pull the Grafana Alloy agent image (local registry)",
		behaviourChanges: false, comment: "pure rename, same module+args"},
	{name: "arg-value-change", playbook: "monitoring.yml",
		file: "monitoring.yml", old: `alloy_version: "1.5.1"`, new: `alloy_version: "9.9.9"`,
		behaviourChanges: true, comment: "image tag changes: apply deploys a different version"},
	{name: "host-ip-change", playbook: "webservers.yml",
		file: inventory, old: "web01 ansible_host=10.0.2.11", new: "web01 ansible_host=10.9.9.99",
		behaviourChanges: true, comment: "same play targets a different machine"},
	{name: "when-var-flip", playbook: "backup.yml",
		file: "inventories/production/group_vars/all.yml",
		old:  "backup_use_cron: false", new: "backup_use_cron: true",
		behaviourChanges: true, comment: "condition flips: different tasks run"},
	{name: "add-host", playbook: "webservers.yml",
		file: inventory, old: "web03 ansible_host=10.0.2.13",
		new:  "web03 ansible_host=10.0.2.13\nweb04 ansible_host=10.0.2.14",
		behaviourChanges: true, comment: "blast radius grows by one host"},
	{name: "fact-profile-swap", playbook: "webservers.yml", profile: "rhel-9",
		behaviourChanges: true, comment: "os_family conditionals resolve differently"},
}

func fingerprint(root, playbook, profile string) (string, error) {
	res, err := scanner.Scan(root)
	if err != nil {
		return "", err
	}
	if profile == "" {
		profile = "ubuntu-24.04"
	}
	out, err := plan.Compute(res, root, model.Repo{}, plan.Request{
		Playbook: playbook, Inventory: inventory, FactProfile: profile,
	})
	if err != nil {
		return "", err
	}
	return plan.Fingerprint(out), nil
}

func copyRepo(src string) (string, error) {
	dst, err := os.MkdirTemp("", "fp-spike-")
	if err != nil {
		return "", err
	}
	return filepath.Join(dst, "repo"), exec.Command("cp", "-R", src, filepath.Join(dst, "repo")).Run()
}

func main() {
	src := "examples/demo-infra"
	if len(os.Args) > 1 {
		src = os.Args[1]
	}
	fmt.Printf("%-18s %-15s %-9s %-9s %-14s %s\n",
		"scenario", "playbook", "fp", "behaviour", "verdict", "analysis")
	falseBlocks, falsePasses := 0, 0
	for _, sc := range scenarios {
		work, err := copyRepo(src)
		if err != nil {
			fmt.Println(sc.name, "copy failed:", err)
			continue
		}
		base, err := fingerprint(work, sc.playbook, "")
		if err != nil {
			fmt.Println(sc.name, "baseline failed:", err)
			continue
		}
		if sc.file != "" {
			p := filepath.Join(work, sc.file)
			data, err := os.ReadFile(p)
			if err != nil || !strings.Contains(string(data), sc.old) {
				fmt.Printf("%-18s MUTATION TARGET NOT FOUND (%s)\n", sc.name, sc.file)
				continue
			}
			os.WriteFile(p, []byte(strings.Replace(string(data), sc.old, sc.new, 1)), 0o644)
		}
		mutated, err := fingerprint(work, sc.playbook, sc.profile)
		if err != nil {
			fmt.Println(sc.name, "mutated plan failed:", err)
			continue
		}
		changed := mutated != base
		verdict := "correct"
		switch {
		case changed && !sc.behaviourChanges:
			verdict = "FALSE BLOCK"
			falseBlocks++
		case !changed && sc.behaviourChanges:
			verdict = "FALSE PASS"
			falsePasses++
		}
		fp, bh := "same", "same"
		if changed {
			fp = "CHANGED"
		}
		if sc.behaviourChanges {
			bh = "CHANGES"
		}
		fmt.Printf("%-18s %-15s %-9s %-9s %-14s %s\n", sc.name, sc.playbook, fp, bh, verdict, sc.comment)
		os.RemoveAll(filepath.Dir(work))
	}
	fmt.Printf("\nfalse blocks (benign edit blocks the schedule): %d\n", falseBlocks)
	fmt.Printf("false passes (behaviour changed, gate stays approved): %d\n", falsePasses)
}
