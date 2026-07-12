# Counter-analysis — how far can Pine push a Terraform-like state machine on top of Ansible?

Status: theoretical pass done; empirical spikes pending (see "Open questions").

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

## Open questions → empirical spikes

1. **How often does `--check` lie in the wild?** Measure module-level check
   reliability across ansible-nas, ansible-for-devops, debops.
   (`spikes/checkmode-liars/`)
2. **How stable is the plan fingerprint in practice?** Quantify
   false-block (benign refactor) and false-pass (fact change) rates on the
   demo repo and real repos. (`spikes/fingerprint-stability/`)
3. **Can an observed-state file + drift-driven loop work end to end?**
   Prototype the host × task state and the certified-only reconcile loop;
   find where it breaks. (`spikes/state-file/`)

## Provisional conclusion

Full Terraform-like is **not reachable**: pillar 1 is impossible in the
authoritative sense, pillar 2 caps at a labeled prediction, pillar 3 is
safe only behind eligibility gates. What *is* reachable is a coherent,
honest sub-model — observed state, confidence-labeled plans, tripwire
gating, certified reconciliation — which is more than any existing Ansible
control plane offers. The empirical spikes decide how much of pillars 2–3
survives contact with real repos; final verdict and ROADMAP impact land in
this document after the spikes run.
