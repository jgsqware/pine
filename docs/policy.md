# Policy-as-code

Pine's policy engine is the OPA/Sentinel of Ansible: a small, declarative set of
governance rules (YAML) evaluated against the **estimated plan** (`plan.Result` —
task × host × verdict), not against source text. Because it runs on the plan, a
rule sees what a run *would actually do* on which hosts, with variables resolved.

Run it as a CI gate:

```bash
pine policy check PATH --policies FILE [-i INV] [--playbook PB] [--limit L] [--tags T] [-e k=v] [--json]
```

- Computes a plan per playbook in `PATH` (or just `--playbook PB`).
- Evaluates every rule against each plan.
- Prints the violations grouped by playbook.
- **Exit 1** when any `error`-severity rule fires; **exit 0** otherwise.
  `warning`-severity violations are reported but never gate.

## Rule format

A policy file is a top-level `policies:` list. Each policy has:

| field | meaning |
|-------|---------|
| `id` | unique identifier (required) |
| `description` | human-readable rationale (optional) |
| `severity` | `error` (gates CI) or `warning` (reported only). Default: `error` |
| `match` | which task/host verdicts the rule applies to (all set fields must hold) |
| `assert` | the constraint the matched task/host must satisfy |

### `match` — narrow the scope

| field | meaning |
|-------|---------|
| `inventory` | glob on the plan's inventory name/path, e.g. `"*production*"`. If set and it doesn't match, the whole policy is skipped |
| `hosts` | glob on the host name, e.g. `"web*"` |
| `groups` | the host must belong to at least one of these inventory groups |
| `module` | task module, matched on the short name (`apt` matches `ansible.builtin.apt`). Any listed module matches |
| `tags` | the task must carry at least one of these tags to be in scope |
| `task_name_regex` | the resolved task name must match this regexp |
| `become` | `true`/`false` — match only plays whose privilege escalation matches (Ansible `become:` is play-level) |

An empty `match` matches every task on every host.

### `assert` — the constraint

| field | meaning |
|-------|---------|
| `forbid: true` | any matched task/host is a violation (a blanket ban) |
| `require_tag: NAME` | the matched task must carry tag `NAME` (e.g. `approved`) |
| `forbid_args: {k: v}` | the matched task must not set module arg `k` to `v`. Use `"*"` to forbid the key with any value |
| `max_blast_radius_pct: N` | plan-level: the run (or the git-diff impact, when supplied) must touch ≤ N% of the inventory's hosts |

`forbid_args` reads the plan's resolved module-arg summary, so a value coming
from a variable is checked as it would actually render.

## Examples

### 1. No `state: latest` in production

Floating packages to "newest" is non-deterministic and defeats reproducible
deploys. Gate it, but only on the production inventory:

```yaml
policies:
  - id: no-state-latest-in-prod
    description: "state: latest is forbidden on production hosts (pin versions)"
    severity: error
    match:
      inventory: "*production*"
      module: [apt, yum, dnf, package, pip, npm]
    assert:
      forbid_args:
        state: latest
```

### 2. Privilege escalation requires an `approved` tag

Any play that runs with `become: true` must be signed off by tagging its tasks
`approved`, so a change-approver has explicitly reviewed the escalation:

```yaml
policies:
  - id: become-requires-approved-tag
    description: "privilege escalation (become) requires an 'approved' tag"
    severity: error
    match:
      become: true
    assert:
      require_tag: approved
```

### 3. Blast radius ≤ 30%

Nudge risky changes toward serial batches / smaller `--limit`s by capping how
much of the fleet one run may touch:

```yaml
policies:
  - id: blast-radius-max-30pct
    description: "a run must not touch more than 30% of the inventory hosts"
    severity: warning
    match:
      inventory: "*production*"
    assert:
      max_blast_radius_pct: 30
```

## In CI

```yaml
# .github/workflows/governance.yml (excerpt)
- run: pine policy check . --policies policies.yml -i production
  # non-zero exit fails the job on any error-severity violation
```

The shipped demo carries two ready examples:
`examples/demo-infra/policies.yml` (strict — gates on unapproved `become`) and
`examples/demo-infra/policies-baseline.yml` (relaxed — passes clean).
