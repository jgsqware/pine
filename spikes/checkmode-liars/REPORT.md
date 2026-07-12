# Spike report — how often does `--check` lie on real repos?

Feeds pillar 2 of `docs/design/state-machine-counter-analysis.md`.

## Method (and its limits)

Static classification of every task in three real-world repos (shallow
clones, 2026-07-12) against a conservative knowledge base of module
check-mode behaviour. **No dynamic runs**: no ansible install and no target
hosts in this environment, so this measures the *structural* ceiling of
check-mode reliability, not observed lies. Loops are not expanded (a task
counts once, not once per item). Block-level `check_mode`/`changed_when`
are inherited by children. 9 files in ansible-nas failed YAML parsing
(Jinja-heavy) and are excluded — counted, not hidden.

Classes:

- **honest** — module predicts changes under `--check`
- **blind** — module is skipped under `--check` (`command`, `shell`, `uri`,
  `fetch`, …): drift on these tasks is invisible to the drift heatmap
- **forced** — `check_mode: false`: the task **runs for real during a
  check** — a "drift scan" with side effects
- **overridden** — `changed_when` present: verdict is author-defined
  (often *fixes* a blind `command`, sometimes hides a real change)
- **unknown** — module absent from the knowledge base; reported, never
  guessed

## Results

| Repo | Tasks | 🟢 honest | 🕶 blind | 🔥 forced | ✍ overridden | ❓ unknown |
|---|---:|---:|---:|---:|---:|---:|
| ansible-nas | 428 | **426 (100%)** | 0 | 0 | 2 | 0 |
| ansible-for-devops | 269 | **212 (79%)** | 27 (10%) | 0 | 8 (3%) | 22 (8%) |
| debops | 3642 | **3180 (87%)** | 70 (2%) | **79 (2%)** | 241 (7%) | 72 (2%) |

Top blind modules: `command` (56 across repos), `shell`, `uri`, `fetch`,
`script`, `synchronize`, `reboot`. Top unknowns are niche
(`virt_pool`, `lvol`, `openssl_*`, `composer`, `haproxy`).

## Findings

1. **Check mode is ~80–100% structurally honest on real repos** — much
   better than the worst-case narrative. A drift heatmap built on `--check`
   is *mostly* right.
2. **The blind spot is real but bounded (0–10%)** and concentrated in
   `command`/`shell`. These are precisely the tasks most likely to be
   non-idempotent — the blind spot overlaps the danger zone.
3. **`check_mode: false` is the sleeper finding**: debops forces 79 tasks
   (2%) to execute during checks. A pine "drift scan" against a
   debops-style repo **mutates the hosts it scans**. Any reconcile-loop or
   drift feature must surface these tasks before the first check run.
4. **`changed_when` (7% in debops) cuts both ways** — usually it makes
   `command` honest, but pine cannot statically verify the expression.
   Tri-state applies: these verdicts are author-trust, not engine-truth.

## Consequence for Pine

Per-task check-reliability classification is cheap (this spike is ~200
lines) and directly actionable: the plan and drift views can badge each
task 🟢/🕶/🔥/✍ and compute a **per-playbook drift-trust score**. The
pillar-3 reconcile loop should require: 0 forced tasks, blind tasks
excluded from the trigger set.
