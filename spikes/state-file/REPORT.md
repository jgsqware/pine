# Spike report — observed-state file + drift-driven reconcile loop

Feeds pillars 1 and 3 of `docs/design/state-machine-counter-analysis.md`.
Method: run the guarded loop against a deterministic simulated world with
adversarial task semantics (non-idempotence, check liars, blind and
`check_mode: false` tasks) and scripted out-of-band drift. See
`state.sample.json` for the state-file shape.

## Scenario outcomes

| Scenario | Adversarial trait | Outcome |
|---|---|---|
| S1 idempotent + drift | none | ✅ certifies (2×0-changed), detects injected drift, reconciles, converges |
| S2 non-idempotent | task always changes | ✅ never certifies — but ⚠️ bootstrap kept applying (see finding 2) |
| S3 check-liar | always `changed` under check | ✅ reliability filter blocks the false trigger; no apply storm |
| S4 blind drift | drift on a check-skipped task | 🔴 drift survives **unreconciled and invisible**; staleness age is the only signal |
| S5 `check_mode: false` | check mutates hosts | ✅ playbook refused before the first scan |

## Findings

1. **The guarded loop works.** Certification (2 consecutive zero-changed
   applies) + check-reliability filtering + oscillation breaker +
   forced-task refusal are each individually necessary — every scenario
   removes exactly one guardrail's justification and fails without it.
2. **The bootstrap is itself a hazard** (unplanned finding): to certify, the
   loop must *apply*, and S2 shows it re-applying a non-idempotent playbook
   every cycle while "waiting" for certification. Design consequence:
   certification must only be counted on **user-initiated** runs; the loop
   must never apply an uncertified playbook.
3. **The blind spot is permanent, not fixable** (S4): drift on
   check-skipped tasks is invisible by construction. The only honest
   surface is staleness — "this task was last observed N days ago" — which
   the observed-state file makes cheap to compute. Pair with the
   checkmode-liars badge: blind-task density per playbook = drift-trust
   score.
4. **The state file earns its keep without claiming authority.** Host ×
   task observations (status, observed-at, run id) are enough for drift
   display, staleness alarms and reconcile bookkeeping. Nothing in the
   loop required desired-state authority, resource identity, or `destroy`
   — confirming pillar 1's verdict: *observed cache yes, Terraform state
   no*. The `semantics` field in the file is load-bearing: it is the
   honesty label.

## Consequence for Pine

A production version is a modest feature, not a research project: persist
observations from existing job logs (`parseJobLog` already extracts
per-task × host statuses), add per-playbook certification tracking, and a
drift-driven schedule type gated on: certified ∧ zero forced tasks ∧
trigger counted on check-honest tasks only ∧ oscillation breaker. The
blind-spot staleness alarm falls out of the state file for free.
