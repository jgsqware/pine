# Counter-analysis — how far can Pine push a Terraform-like state machine on top of Ansible?

Status: **concluded** — theoretical pass + three empirical spikes
(`spikes/checkmode-liars`, `spikes/fingerprint-stability`,
`spikes/state-file`). Final verdict at the end of this document.

## Scope

The target model under scrutiny is **full Terraform-like**, i.e. three pillars:

1. **Per-resource state file** — a durable, authoritative record of what has
   been applied, addressable per resource.
2. **plan == apply guarantee** — what the plan shows is exactly what apply
   does, byte for byte, or apply refuses.
3. **Continuous reconcile loop** — a controller detects drift and
   re-converges automatically, not on a timer.

The question is not "is Pine useful" (it is) but: **which of these pillars
can honestly be built on Ansible's execution model, and where does the
abstraction break?**

## What Pine has today (baseline)

| Pillar | Closest Pine primitive | Where |
|---|---|---|
| State file | Job records + harvested facts (`setup` output per host) | `internal/runner/jobs.go`, `internal/runner/facts.go` |
| plan == apply | Three-valued plan (`run/skip/unknown`), `estimated` vs `exact` modes, plan fingerprint + gated schedules | `internal/plan/plan.go`, `internal/plan/exact.go`, `internal/runner/scheduler.go` |
| Reconcile loop | Drift heatmap from latest `--check` jobs, manual `DriftCheck`, time-driven schedules | `internal/runner/drift.go`, `internal/runner/scheduler.go` |

Boot-time reconciliation exists but only for Pine's *own* state (interrupted
jobs are marked failed at startup, `internal/runner/manager.go`), not for
managed-host state.

## Pillar 1 — per-resource state file

**Verdict: structurally impossible in Terraform's sense. An *observed-state
cache* is achievable, and must be labeled as such.**

Terraform's state works because every provider returns a full, addressable
resource object (`id` + attributes) on create/read/update, and the state
file is *authoritative*: the tool owns the resource lifecycle.

Ansible offers none of that:

- Modules return an ad-hoc result dict (`changed`, module-specific keys).
  There is no resource identity, no schema, no `Read()` contract. Two tasks
  touching the same file are two unrelated log lines, not one resource.
- Nothing prevents out-of-band changes *or other playbooks/roles* from
  touching the same thing. A state file that claims authority would lie the
  moment a human SSHes in — and unlike Terraform there is no `refresh` that
  can rebuild reality from provider APIs.
- Task ↔ real-world mapping is many-to-many: one `template` task can render
  N files across M hosts depending on loops and facts resolved at runtime.

What *is* buildable: a **host × task observed-state cache** derived from run
logs and harvested facts — "last time this task ran on this host: status,
when, from which commit". That supports drift display, run diffing and
staleness warnings. It can never support `destroy`, import, or "this
resource is managed by pine" claims. Under the honest-engine rule, every
surface built on it must say **observed**, never **desired achieved**.

## Pillar 2 — plan == apply guarantee

**Verdict: not guaranteeable. Achievable-with-compromise: a bounded,
labeled approximation.**

Terraform can promise plan == apply because plan and apply evaluate the same
graph against the same state snapshot, and apply fails on unexpected diffs.
Ansible breaks this in four independent ways:

1. **Check mode is advisory.** Modules may not support `--check` (skipped
   entirely), may lie (`command`/`shell` report changed or skip), or may be
   forced through with `check_mode: false`. The drift heuristic in
   `internal/runner/drift.go` ("changed under check ⇒ reality diverged")
   inherits every one of these lies.
2. **Runtime-only data.** `register` + `set_fact` chains, lookups, and
   facts gathered mid-play are unknowable before execution. The tri-state
   evaluator already degrades these to `unknown` honestly
   (`docs/design/plan-mode.md`), but `unknown` is precisely the admission
   that plan ≠ apply for those tasks.
3. **Time-of-check / time-of-use.** The gated-schedule fingerprint
   (`internal/runner/scheduler.go`) hashes the *plan structure* (plays,
   hosts, task names/modules, per-host verdicts). Facts can change between
   approval and execution without changing the fingerprint: the approved
   plan runs, but does something else. Conversely, an inconsequential
   refactor (task rename) changes the fingerprint and blocks a schedule
   that would have done exactly the same thing. Both false-negative and
   false-positive gating exist by construction.
4. **No apply-time diff enforcement.** Ansible has no "abort if the change
   set differs from the plan" primitive. Pine can *compare* after the fact
   (run diff, `internal/runner/diff.go`) but cannot *prevent*.

The honest ceiling is: **plan is a prediction with a confidence label**
(`estimated` < `exact`), gating is a *change-detection tripwire* rather than
a guarantee, and post-apply comparison closes the loop by reporting where
the prediction was wrong. That is genuinely valuable — and it is not
Terraform's guarantee, and must never be presented as one.

## Pillar 3 — continuous reconcile loop

**Verdict: mechanically feasible, unsafe as a default. Feasible with
eligibility guardrails.**

The loop itself is trivial to build on existing primitives: periodic
`DriftCheck` → if drifted, trigger the apply job (drift-driven instead of
time-driven schedules). The danger is what the loop assumes:

- **Convergence assumes idempotence.** Kubernetes controllers converge
  because reconciliation is level-based against declared specs. An Ansible
  playbook with non-idempotent tasks (`command` without `creates`,
  notify-restart cascades, anything time-dependent) makes the loop
  oscillate: every check "detects drift", every apply "fixes" it, forever —
  each iteration restarting services in production.
- **Check-mode lies feed the trigger.** A module that always reports
  `changed` under `--check` turns the loop into a cron job with extra
  steps and a false sense of causality.
- **Blast radius is unbounded** unless policy caps it (the
  `max_blast_radius_pct` policy primitive already exists).

Guardrails that make it honest:

1. **Idempotence certification per playbook**: two consecutive real runs
   with zero `changed` ⇒ eligible for auto-reconcile; anything else is
   display-only drift. Certification is empirical, revocable, and stored.
2. **Check-reliability scoring per module/task** (see spike below): tasks
   using check-unreliable modules are excluded from the drift *trigger*.
3. Backoff + oscillation detection (same task drifting N cycles in a row ⇒
   stop and page a human), human gate on first auto-converge, blast-radius
   policy enforced on the reconcile plan.

## Comparison table

| Property | Terraform | Ansible (raw) | Pine's honest ceiling |
|---|---|---|---|
| Resource identity | provider schema, `id` | none | host × task observation |
| State authority | state file, refresh from API | none | observed cache + staleness label |
| plan fidelity | plan == apply enforced | `--check` advisory | tri-state prediction + `estimated`/`exact` label + post-apply diff |
| Change gating | plan diff | none | structural fingerprint (tripwire, both false +/− possible) |
| Reconciliation | apply converges by contract | idempotence by convention only | drift-driven loop for *certified* playbooks only |
| Destroy/import | yes | no | out of scope, permanently |

## Empirical results (three spikes, 2026-07-12)

1. **`--check` honesty in the wild** (`spikes/checkmode-liars/REPORT.md`,
   4,339 tasks across ansible-nas, ansible-for-devops, debops): check mode
   is **79–100% structurally honest**. The blind spot (0–10%) concentrates
   in `command`/`shell` — exactly the tasks most likely to be
   non-idempotent. Sleeper finding: debops sets `check_mode: false` on 79
   tasks, so a "drift scan" against it **mutates the hosts it scans**.
2. **Fingerprint stability** (`spikes/fingerprint-stability/REPORT.md`,
   7 mutations on the demo repo): the gate is **fail-open on the most
   common operator changes**. A version bump in module args and a host-IP
   repoint both pass the approved fingerprint unchanged (2 false passes);
   a pure task rename blocks it (1 false block). Cause: the fingerprint
   hashes plan *structure*, not resolved args or connection identity.
3. **Observed state + guarded reconcile loop** (`spikes/state-file/REPORT.md`,
   adversarial simulation): the loop is safe iff **all four guardrails**
   hold — idempotence certification on user-initiated runs only,
   check-reliability filtering of the trigger, oscillation breaker,
   refusal of `check_mode: false` playbooks. Drift on check-blind tasks is
   **permanently invisible**; observation staleness is the only honest
   signal. Nothing required desired-state authority.

## Final verdict

**Full Terraform-like: no — and the spikes say the gap is narrower and
different than feared.**

| Pillar | Verdict | Empirical nuance |
|---|---|---|
| State file | ❌ authoritative state impossible | ✅ observed host × task cache works and suffices for the loop |
| plan == apply | ❌ not guaranteeable | ✅ ~80–100% structural check honesty; 🔴 gate currently fail-open on args/connection changes — fixable |
| Reconcile loop | ⚠️ unsafe as default | ✅ safe behind the four guardrails, all cheap to implement |

The honest ceiling is real and worth building: **fingerprint v2**
(resolved-args digest + connection identity, raw name dropped),
**check-reliability badges** with a per-playbook drift-trust score, an
**observed-state file** with staleness alarms, and **drift-driven
schedules for certified playbooks**. Terraform-style destroy/import/state
authority is abandoned permanently, with this document as the record of
why. ROADMAP.md carries the actionable items.
