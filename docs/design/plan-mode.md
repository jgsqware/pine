# Plan mode — "terraform plan" for Ansible

## Problem

Pine's static analysis treats anything it cannot resolve as `false`,
silently. Conditions depending on runtime facts (`ansible_distribution`,
gathered facts, registered vars) make the constructed-inventory emulation
and any task-level prediction lie by omission. Users want to know what a
playbook *would* do — which hosts, which tasks run or skip, which handlers
fire — **before** applying it, optionally feeding in assumed variables.

## Core principle: three-valued logic

The expression evaluator moves from boolean to tri-state:

- `true` / `false` — statically resolved, trustworthy
- **`unknown`** — the expression depends on variables that are not
  available, and the evaluator reports *which ones*

Uncertainty becomes visible and actionable instead of being silently
collapsed to `false`. The UI shows unknown verdicts in amber with the list
of missing variables and lets the user provide values to resolve them.
A plan must never present a guess as a certainty.

Three-valued connectives: `false and X = false`, `true or X = true`,
everything else involving unknown stays unknown; `x is defined` and
`default()` resolve unknowns by design.

## Variable resolution (estimated mode)

Per host, merged in order (later wins), a simplified model of ansible
precedence:

1. role `defaults/main.yml` (roles of the play)
2. inventory group vars: `all`, then parents before children
3. inventory host vars
4. magic vars: `inventory_hostname`, `group_names`, `groups`
5. fact profile (built-in presets: ubuntu-24.04, debian-12, rhel-9, …)
6. play `vars` and `vars_files`
7. user-supplied vars (`vars`, `host_vars` in the request) — like `-e`

## Sources of truth, by increasing precision

1. **Static engine (Go, always available)** — `mode: "estimated"`.
2. **Fact profiles** — presets injecting `ansible_facts` +
   `ansible_os_family`-style aliases so OS conditionals resolve.
3. **Harvested facts (phase 3)** — parse `setup`/Gathering Facts output of
   real jobs into a per-host fact store; later plans use real facts.
4. **Exact mode (phase 4)** — when `ansible-playbook` is installed:
   `--check --diff` with a JSON callback, rendered in the same UI, labeled
   `mode: "exact"`.

## Plan output

For each play (imports followed, `serial` batches shown): per task ×
host verdict `run` / `skip` (+ reason: the false `when`) / `unknown`
(+ missing vars), templated names/args where resolvable, loop sizes when
the list resolves, handlers that would be notified, `--check` notes for
command-ish modules, and a global summary with aggregated missing vars.

## API

- `POST /api/plans` — body `{repo_id, playbook, inventory, limit, tags,
  check, vars, host_vars, fact_profile}` → Plan JSON (synchronous, not
  persisted; "Apply" reuses the same parameters to create a job).
- `POST /api/repos/{id}/inventory-preview` — recompute constructed groups
  with supplied vars → what-if inventory + per-group unknowns.
- `GET /api/fact-profiles` — available presets.

## Surfaces

- Web: "Plan" next to every "Run", vars editor (JSON or key=value lines),
  fact-profile select, plan view with per-task verdict bars and a missing
  vars panel that re-plans live; topology what-if panel.
- TUI: `p` on a playbook → plan rendering.
- CLI: `pine plan PATH PLAYBOOK [-i inv] [-e k=v] [--profile id]`.

## Phasing

1. Tri-state evaluator + missing-var tracking + inventory preview.
2. Playbook plan engine + web/TUI/CLI surfaces.  ← this change
3. Fact harvesting from real runs.
4. Exact mode via ansible + apply-with-comparison.

## Honest limits

Full Jinja parity is out of scope by construction. The supported subset
(interpolation, comparisons, in, and/or/not, is defined, default/lower/
upper, brackets and dotted lookups) covers common infra conditionals;
anything else degrades to `unknown`, never to a wrong answer. Registered
variables and `set_fact` chains are unknown by nature in estimated mode;
exact mode is the answer there.
