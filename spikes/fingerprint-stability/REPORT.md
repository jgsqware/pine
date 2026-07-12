# Spike report — fingerprint stability between approval and run

Feeds pillar 2 of `docs/design/state-machine-counter-analysis.md`.
Method: copy `examples/demo-infra`, apply one realistic mutation per
scenario, recompute `plan.Fingerprint`, compare against the approved
baseline (fact profile ubuntu-24.04, production inventory).

## Results

| Scenario | Mutation | Behaviour | Fingerprint | Verdict |
|---|---|---|---|---|
| comment-only | YAML comment added | same | same | ✅ correct |
| task-rename | task `name:` reworded | same | changed | ⚠️ **FALSE BLOCK** |
| arg-value-change | `alloy_version 1.5.1 → 9.9.9` | **deploys another version** | same | 🔴 **FALSE PASS** |
| host-ip-change | `web01` IP → different machine | **targets another machine** | same | 🔴 **FALSE PASS** |
| when-var-flip | `backup_use_cron: false → true` | different tasks run | changed | ✅ correct |
| add-host | `web04` added to inventory | blast radius +1 | changed | ✅ correct |
| fact-profile-swap | ubuntu-24.04 → rhel-9 | conditionals flip | changed | ✅ correct |

**4/7 correct · 1 false block (fail-closed, annoying) · 2 false passes
(fail-open, dangerous).**

## Analysis

`plan.Fingerprint` hashes play name/hosts/batches, task
role/raw-name/module, and per-host verdict status
(`internal/plan/plan.go:683`). It captures *plan structure*, not *plan
content*:

- **False passes are structural, not edge cases.** Any change confined to
  module arguments, templated values, loop items, file/template content or
  connection vars (`ansible_host`) leaves the fingerprint intact. A gated
  schedule approved on `v1.5.1` will happily apply `v9.9.9` — the exact
  time-of-check/time-of-use gap predicted by the counter-analysis, now
  demonstrated. The gate fails **open** on the changes operators most often
  make (bump a version, repoint a host).
- **The false block is the mirror image**: hashing the raw task name means
  a comment-level rename blocks a schedule that would do byte-identical
  work, training operators to re-approve reflexively — which erodes the
  gate's value as a control.

## Consequence for Pine

The fingerprint should hash **resolved module arguments (canonical digest)
and per-host connection identity** in addition to structure — closing both
false passes — and should *drop* the raw task name in favour of
(role, module, args-digest), removing the rename false block. File/template
*content* referenced by tasks (`template:`, `copy: src=`) remains a known,
documented blind spot unless content digests are added; that is the
follow-up decision for the ROADMAP.
